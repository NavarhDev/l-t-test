package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/masterdata"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"

	emptypb "google.golang.org/protobuf/types/known/emptypb"
)

type LoginBonusServiceServer struct {
	pb.UnimplementedLoginBonusServiceServer
	users    store.UserRepository
	sessions store.SessionRepository
	snaps    store.SnapshotRepository
	holder   *runtime.Holder
}

func NewLoginBonusServiceServer(users store.UserRepository, sessions store.SessionRepository, snaps store.SnapshotRepository, holder *runtime.Holder) *LoginBonusServiceServer {
	return &LoginBonusServiceServer{users: users, sessions: sessions, snaps: snaps, holder: holder}
}

func (s *LoginBonusServiceServer) ReceiveStamp(ctx context.Context, req *emptypb.Empty) (*pb.ReceiveStampResponse, error) {
	log.Printf("[LoginBonusService] ReceiveStamp")

	userId := CurrentUserId(ctx, s.users, s.sessions)
	catalog := s.holder.Get().LoginBonus

	var alreadyClaimed bool
	_, err := s.users.UpdateUser(userId, func(u *store.UserState) {
		// Convert any existing stamina to gold on first login bonus claim
		store.ConvertStaminaToGold(u)
		
		// Same-day protection INSIDE the update to prevent race conditions.
		// The client auto-calls ReceiveStamp after certain quest completions
		// (e.g. tutorial quests), so without this guard each call would
		// incorrectly advance the login-day counter.
		if u.LoginBonus.LatestRewardReceiveDatetime >= gametime.StartOfDayMillis() {
			alreadyClaimed = true
			return
		}

		nextPage, nextStamp, reward, err := resolveNextStamp(catalog, u.LoginBonus)
		if err != nil {
			log.Printf("[LoginBonusService] resolveNextStamp error: %v", err)
			alreadyClaimed = true
			return
		}

		log.Printf("[LoginBonusService] bonusId=%d page %d->%d stamp %d->%d",
			u.LoginBonus.LoginBonusId, u.LoginBonus.CurrentPageNumber, nextPage,
			u.LoginBonus.CurrentStampNumber, nextStamp)

		mainChapterCount := countCompletedChapters(*u)
		log.Printf("[LoginBonusService] Player chapter-difficulty score: %d", mainChapterCount)

		now := gametime.NowMillis()

		// 1. Original stamp reward * 10
		u.AddGift(store.NotReceivedGiftState{
			GiftCommon: store.GiftCommonState{
				PossessionType: reward.PossessionType,
				PossessionId:   reward.PossessionId,
				Count:          reward.Count * 10,
				GrantDatetime:  now,
			},
			ExpirationDatetime: now + int64(30*24*time.Hour/time.Millisecond),
			UserGiftUuid:       uuid.New().String(),
		})

		// 2. Daily rewards based on main quest progress
		/* dailyGifts := []store.GiftCommonState{
			{PossessionType: 11, PossessionId: 0, Count: 100 * mainChapterCount},  // Paid Gems
			{PossessionType: 6, PossessionId: 9001, Count: 5 * mainChapterCount}, // Mama Point
			{PossessionType: 6, PossessionId: 242, Count: 1 * mainChapterCount},   // Countdown Resurrected Event Medal
		} */

		// Apply all additional rewards
		/* for _, g := range dailyGifts {
			u.Gifts.NotReceived = append(u.Gifts.NotReceived, store.NotReceivedGiftState{
				GiftCommon: store.GiftCommonState{
					PossessionType: g.PossessionType,
					PossessionId:   g.PossessionId,
					Count:          g.Count,
					GrantDatetime:  now,
				},
				ExpirationDatetime: now + int64(30*24*time.Hour/time.Millisecond),
				UserGiftUuid:       uuid.New().String(),
			})
		} */

		u.LoginBonus.CurrentPageNumber = nextPage
		u.LoginBonus.CurrentStampNumber = nextStamp
		u.LoginBonus.LatestRewardReceiveDatetime = now
		u.LoginBonus.LatestVersion = now

		ApplyMissionProgressEvent(u, s.holder.Get().Mission, MissionProgressEvent{ConditionType: missionConditionLogin, Delta: 1}, now)
		ApplyMissionProgressEvent(u, s.holder.Get().Mission, MissionProgressEvent{ConditionType: missionConditionLoginTotalDays, Delta: 1}, now)

		// Claim BigHunt daily and weekly rewards on login
		ApplyBigHuntRewards(u, s.holder.Get(), now)

	})
	if err != nil {
		return nil, fmt.Errorf("update user: %w", err)
	}

	if alreadyClaimed {
		log.Printf("[LoginBonusService] already claimed today, skipping")
	} else {
		// Every claimed login-bonus day doubles as a simulated arena day:
		// 20 honest battles (outcomes by deck power, ±25% contested band),
		// arena missions and per-match/weekly rewards advance together with
		// the stamp. Guarded to run at most once per day and only once the
		// arena is open for the player (checked inside).
		RunPvpAutoProgress(s.users, s.snaps, s.holder, userId)
	}

	return &pb.ReceiveStampResponse{}, nil
}

func resolveNextStamp(catalog *masterdata.LoginBonusCatalog, lb store.UserLoginBonusState) (nextPage, nextStamp int32, reward masterdata.LoginBonusReward, err error) {
	bonusId := lb.LoginBonusId
	curPage := lb.CurrentPageNumber
	curStamp := lb.CurrentStampNumber

	nextPage = curPage
	nextStamp = curStamp + 1
	var ok bool
	reward, ok = catalog.LookupStampReward(bonusId, nextPage, nextStamp)
	if !ok {
		nextPage = curPage + 1
		nextStamp = 1
		total := catalog.TotalPageCount(bonusId)
		if total > 0 && nextPage > total {
			err = status.Errorf(codes.FailedPrecondition,
				"login bonus %d exhausted (page %d stamp %d is the last)",
				bonusId, curPage, curStamp)
			return
		}
		reward, ok = catalog.LookupStampReward(bonusId, nextPage, nextStamp)
		if !ok {
			err = status.Errorf(codes.FailedPrecondition,
				"no reward found for login bonus %d page %d stamp %d",
				bonusId, nextPage, nextStamp)
			return
		}
	}
	return
}

// Calculate completed chapters
var chapterFinalQuestIdsNormal = []int32{
	11, 21, 31, 41, 51, 61, 71, 81, 91, 101,
	111, 121, 314, 324, 334, 350, 360,
	370, 414, 424, 434, 450, 460, 470,
	507, 514, 521, 529, 536, 546, 550,
}

var chapterFinalQuestIdsHard = []int32{
	10004, 10014, 10024, 10034, 10044, 10054, 10064, 10074, 10084, 10094, 10104, 10108,
	10310, 10320, 10330, 10340, 10350, 10360,
	10410, 10420, 10430, 10440, 10450, 10460,
	10507, 10514, 10521, 10529, 10536, 10546,
}

var chapterFinalQuestIdsExHard = []int32{
	20004, 20014, 20024, 20034, 20044, 20054, 20064, 20074, 20084, 20094, 20104, 20108,
	20310, 20320, 20330, 20340, 20350, 20360,
	20410, 20420, 20430, 20440, 20450, 20460,
	20507, 20514, 20521, 20529, 20536, 20546,
}

func countClearedFrom(user store.UserState, questIds []int32) int32 {
	count := int32(0)
	for _, questId := range questIds {
		if q, ok := user.Quests[questId]; ok && q.ClearCount > 0 {
			count++
		}
	}
	return count
}

func countCompletedChapters(user store.UserState) int32 {
	return countClearedFrom(user, chapterFinalQuestIdsNormal) +
		countClearedFrom(user, chapterFinalQuestIdsHard) +
		countClearedFrom(user, chapterFinalQuestIdsExHard)
}
