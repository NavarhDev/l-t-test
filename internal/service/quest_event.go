package service

import (
	"context"
	"log"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/questflow"
	"lunar-tear/server/internal/store"

	emptypb "google.golang.org/protobuf/types/known/emptypb"
)

func (s *QuestServiceServer) StartEventQuest(ctx context.Context, req *pb.StartEventQuestRequest) (*pb.StartEventQuestResponse, error) {
	log.Printf("[QuestService] StartEventQuest: chapterId=%d questId=%d isBattleOnly=%v maxAutoOrbitCount=%d",
		req.EventQuestChapterId, req.QuestId, req.IsBattleOnly, req.MaxAutoOrbitCount)

	engine := s.holder.Get().QuestHandler
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()
	s.users.UpdateUser(userId, func(user *store.UserState) {
		engine.HandleEventQuestStart(user, req.EventQuestChapterId, req.QuestId, req.IsBattleOnly, req.UserDeckNumber, nowMillis)
		startAutoOrbit(user, model.QuestTypeEvent, req.EventQuestChapterId, req.QuestId, req.MaxAutoOrbitCount, nowMillis)
		// Track stamina usage for missions (even though stamina is free on this server)
		staminaCost := engine.EventQuestStaminaCost(req.EventQuestChapterId, req.QuestId, nowMillis)
		if staminaCost > 0 {
			ApplyMissionProgressEvent(user, s.holder.Get().Mission, MissionProgressEvent{ConditionType: missionConditionStaminaUsed, Delta: staminaCost}, nowMillis)
		}
	})

	drops := engine.BattleDropRewards(req.QuestId)
	pbDrops := make([]*pb.BattleDropReward, len(drops))
	for i, d := range drops {
		pbDrops[i] = &pb.BattleDropReward{
			QuestSceneId:         d.QuestSceneId,
			BattleDropCategoryId: d.BattleDropCategoryId,
			BattleDropEffectId:   1,
		}
	}

	return &pb.StartEventQuestResponse{
		BattleDropReward: pbDrops,
	}, nil
}

func (s *QuestServiceServer) FinishEventQuest(ctx context.Context, req *pb.FinishEventQuestRequest) (*pb.FinishEventQuestResponse, error) {
	log.Printf("[QuestService] FinishEventQuest: chapterId=%d questId=%d isRetired=%v isAnnihilated=%v isAutoOrbit=%v",
		req.EventQuestChapterId, req.QuestId, req.IsRetired, req.IsAnnihilated, req.IsAutoOrbit)

	nowMillis := gametime.NowMillis()
	engine := s.holder.Get().QuestHandler
	userId := CurrentUserId(ctx, s.users, s.sessions)
	var outcome questflow.FinishOutcome
	var endedDrops []store.AutoOrbitDropEntry
	var loopEnded bool
	s.users.UpdateUser(userId, func(user *store.UserState) {
		log.Printf("[QuestService] FinishEventQuest: MARKER_A - about to call HandleEventQuestFinish")
		outcome = engine.HandleEventQuestFinish(user, req.EventQuestChapterId, req.QuestId, req.IsRetired, req.IsAnnihilated, nowMillis)
		log.Printf("[QuestService] FinishEventQuest: MARKER_B - returned from HandleEventQuestFinish, user.Battle.LastComboCount=%d", user.Battle.LastComboCount)
		endedDrops, loopEnded = finishAutoOrbit(user, req.IsAutoOrbit, req.IsRetired, req.IsAnnihilated, model.QuestTypeEvent, req.EventQuestChapterId, req.QuestId, nowMillis, outcome.DropRewards)

		log.Printf("[QuestService] FinishEventQuest: LoadUser result - LastComboCount=%d, LastComboMaxDamage=%d (BEFORE any updates)", user.Battle.LastComboCount, user.Battle.LastComboMaxDamage)

		// Track quit/retire and party wipe for missions
		if req.IsRetired && !req.IsAnnihilated {
			ApplyMissionProgressEvent(user, s.holder.Get().Mission, MissionProgressEvent{ConditionType: missionConditionQuitBattle, Delta: 1}, nowMillis)
		}
		if req.IsAnnihilated {
			ApplyMissionProgressEvent(user, s.holder.Get().Mission, MissionProgressEvent{ConditionType: missionConditionPartyWipe, Delta: 1}, nowMillis)
		}

		log.Printf("[QuestService] FinishEventQuest: condition check - IsRetired=%v, IsAnnihilated=%v, will check missions=%v", req.IsRetired, req.IsAnnihilated, !req.IsRetired && !req.IsAnnihilated)
		if !req.IsRetired && !req.IsAnnihilated {
			log.Printf("[QuestService] FinishEventQuest: before mission check - LastComboCount=%d, LastComboMaxDamage=%d", user.Battle.LastComboCount, user.Battle.LastComboMaxDamage)
			ApplyQuestClearMissionProgress(user, s.holder.Get().Mission, model.QuestTypeEvent, req.QuestId, req.EventQuestChapterId, nowMillis)
			ApplyMissionProgressEvent(user, s.holder.Get().Mission, MissionProgressEvent{
				ConditionType: missionConditionPlayerLevel,
				CurrentValue:  user.Status.Level,
			}, nowMillis)

			// Update boss defeat missions (type 35 - for missions 220001-220022)
			if n := questBossCount(s.holder.Get().Quest, req.QuestId); n > 0 {
				log.Printf("[QuestService] EventQuest %d has %d boss(es) (BattleEnemyType=2)", req.QuestId, n)
				ApplyMissionProgressEvent(user, s.holder.Get().Mission, MissionProgressEvent{
					ConditionType: missionConditionBossDefeat,
					Delta:         n,
				}, nowMillis)
			}

			// Hidden-story quest-clear missions (category 7): character-in-
			// loadout counters, dungeon clears and Dark Memory quests.
			ApplyHiddenStoryQuestClearMissionProgress(user, s.holder.Get().Mission, s.holder.Get().Quest, req.QuestId, nowMillis)
		}
	})

	autoOrbitReward := emptyAutoOrbitReward()
	if loopEnded {
		autoOrbitReward.DropReward = autoOrbitDropsToProto(endedDrops)
	}

	return &pb.FinishEventQuestResponse{
		DropReward:                      toProtoRewards(outcome.DropRewards),
		FirstClearReward:                toProtoRewards(outcome.FirstClearRewards),
		MissionClearReward:              toProtoRewards(outcome.MissionClearRewards),
		MissionClearCompleteReward:      toProtoRewards(outcome.MissionClearCompleteRewards),
		AutoOrbitResult:                 []*pb.QuestReward{},
		IsBigWin:                        outcome.IsBigWin,
		BigWinClearedQuestMissionIdList: outcome.BigWinClearedQuestMissionIds,
		UserStatusCampaignReward:        []*pb.QuestReward{},
		AutoOrbitReward:                 autoOrbitReward,
	}, nil
}

func (s *QuestServiceServer) RestartEventQuest(ctx context.Context, req *pb.RestartEventQuestRequest) (*pb.RestartEventQuestResponse, error) {
	log.Printf("[QuestService] RestartEventQuest: chapterId=%d questId=%d", req.EventQuestChapterId, req.QuestId)

	engine := s.holder.Get().QuestHandler
	userId := CurrentUserId(ctx, s.users, s.sessions)
	s.users.UpdateUser(userId, func(user *store.UserState) {
		engine.HandleEventQuestRestart(user, req.EventQuestChapterId, req.QuestId, gametime.NowMillis())
	})

	return &pb.RestartEventQuestResponse{
		BattleDropReward: []*pb.BattleDropReward{},
	}, nil
}

func (s *QuestServiceServer) UpdateEventQuestSceneProgress(ctx context.Context, req *pb.UpdateEventQuestSceneProgressRequest) (*pb.UpdateEventQuestSceneProgressResponse, error) {
	log.Printf("[QuestService] UpdateEventQuestSceneProgress: questSceneId=%d", req.QuestSceneId)

	engine := s.holder.Get().QuestHandler
	userId := CurrentUserId(ctx, s.users, s.sessions)
	s.users.UpdateUser(userId, func(user *store.UserState) {
		engine.HandleEventQuestSceneProgress(user, req.QuestSceneId, gametime.NowMillis())
	})

	return &pb.UpdateEventQuestSceneProgressResponse{}, nil
}

const defaultGuerrillaFreeOpenMinutes = int32(60)

func (s *QuestServiceServer) StartGuerrillaFreeOpen(ctx context.Context, req *emptypb.Empty) (*pb.StartGuerrillaFreeOpenResponse, error) {
	log.Printf("[QuestService] StartGuerrillaFreeOpen")

	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()
	s.users.UpdateUser(userId, func(user *store.UserState) {
		user.GuerrillaFreeOpen.StartDatetime = nowMillis
		user.GuerrillaFreeOpen.OpenMinutes = defaultGuerrillaFreeOpenMinutes
		user.GuerrillaFreeOpen.DailyOpenedCount++
		user.GuerrillaFreeOpen.LatestVersion = nowMillis
	})

	return &pb.StartGuerrillaFreeOpenResponse{}, nil
}
