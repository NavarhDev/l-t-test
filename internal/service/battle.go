package service

import (
	"context"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"
)

type BattleServiceServer struct {
	pb.UnimplementedBattleServiceServer
	users    store.UserRepository
	sessions store.SessionRepository
	holder   *runtime.Holder
}

func NewBattleServiceServer(users store.UserRepository, sessions store.SessionRepository, holder *runtime.Holder) *BattleServiceServer {
	if holder == nil {
		panic("runtime holder is required")
	}
	return &BattleServiceServer{users: users, sessions: sessions, holder: holder}
}

func (s *BattleServiceServer) StartWave(ctx context.Context, req *pb.StartWaveRequest) (*pb.StartWaveResponse, error) {
	// log.Printf("[BattleService] StartWave: userParty=%d npcParty=%d", len(req.UserPartyInitialInfoList), len(req.NpcPartyInitialInfoList))
	// currentUserId := CurrentUserId(ctx, s.users, s.sessions)
	// for _, party := range req.UserPartyInitialInfoList {
	// 	userId := party.UserId
	// 	if userId == 0 {
	// 		userId = currentUserId
	// 	}
	// 	// totalHp is computed by the client simulation, so it already includes
	// 	// quest deck bonuses ("Resonant") that the server-side estimate below
	// 	// does not fold into its TOTAL line.
	// 	log.Printf("[BattleDebug] client-reported party totalHp=%d (deckType=%d deckNumber=%d)", party.TotalHp, party.DeckType, party.UserDeckNumber)
	// 	user, err := s.users.LoadUser(userId)
	// 	if err != nil {
	// 		log.Printf("[BattleDebug] load user %d failed: %v", userId, err)
	// 		continue
	// 	}
	// 	logBattleDeckDebug(s.holder.Get(), user, model.DeckType(party.DeckType), int32(party.UserDeckNumber))
	// }
	return &pb.StartWaveResponse{}, nil
}

func (s *BattleServiceServer) FinishWave(ctx context.Context, req *pb.FinishWaveRequest) (*pb.FinishWaveResponse, error) {
	// log.Printf("[BattleService] FinishWave: battleBinary=%d userParty=%d npcParty=%d elapsedFrames=%d",
	// 	len(req.BattleBinary), len(req.UserPartyResultInfoList), len(req.NpcPartyResultInfoList), req.ElapsedFrameCount)
	userId := CurrentUserId(ctx, s.users, s.sessions)
	s.users.UpdateUser(userId, func(user *store.UserState) {
		user.Battle.IsActive = false
		user.Battle.FinishCount++
		user.Battle.LastFinishedAt = gametime.NowMillis()
		user.Battle.LastUserPartyCount = int32(len(req.UserPartyResultInfoList))
		user.Battle.LastNpcPartyCount = int32(len(req.NpcPartyResultInfoList))
		user.Battle.LastBattleBinarySize = int32(len(req.BattleBinary))
		user.Battle.LastElapsedFrameCount = req.ElapsedFrameCount

		// totalSkillUseInfoCount := 0
		// for _, party := range req.UserPartyResultInfoList {
		// 	totalSkillUseInfoCount += len(party.SkillUseInfo)
		// }
		// log.Printf("[BattleService] FinishWave: userParties=%d totalSkillUseInfo=%d", len(req.UserPartyResultInfoList), totalSkillUseInfoCount)

		// Count costume skill usages from SkillUseInfo. The client does not always populate
		// BattleDetail.playerCostumeActiveSkillUsedCount, so this per-skill breakdown is the
		// reliable source for Type 6/54 missions.
		costumeSkillDetailIds := s.holder.Get().Quest.CostumeSkillDetailIds
		costumeSkillUsesFromInfo := int32(0)
		for _, party := range req.UserPartyResultInfoList {
			for _, su := range party.SkillUseInfo {
				if costumeSkillDetailIds[su.SkillDetailId] {
					costumeSkillUsesFromInfo += su.UseCount
					// log.Printf("[BattleService] FinishWave: costume skill use detected skillDetailId=%d useCount=%d", su.SkillDetailId, su.UseCount)
				}
			}
		}

		if d := req.GetBattleDetail(); d != nil {
			// log.Printf("[BattleService] BattleDetail: deaths=%d costumeSkills=%d weaponSkills=%d companionSkills=%d crits=%d combo=%d comboMaxDmg=%d totalRecover=%d costumeBattleInfoCount=%d",
			// 	d.CharacterDeathCount, d.PlayerCostumeActiveSkillUsedCount, d.PlayerWeaponActiveSkillUsedCount,
			// 	d.PlayerCompanionSkillUsedCount, d.CriticalCount, d.ComboCount, d.ComboMaxDamage, d.TotalRecoverPoint, len(d.CostumeBattleInfo))
			// for i, ci := range d.CostumeBattleInfo {
			// 	if i >= 3 {
			// 		break
			// 	}
			// 	log.Printf("  [CostumeBattleInfo %d] alive=%v maxHp=%d remainingHp=%d deckCharNum=%d",
			// 		i, ci.IsAlive, ci.MaxHp, ci.RemainingHp, ci.DeckCharacterNumber)
			// }
			user.Battle.LastCharacterDeathCount = d.CharacterDeathCount
			// ACCUMULATE skill counts across waves (don't overwrite)
			// For costume skills prefer BattleDetail, but fall back to the SkillUseInfo
			// breakdown when the client sends 0 there despite skills being used.
			costumeSkillDelta := d.PlayerCostumeActiveSkillUsedCount
			if costumeSkillDelta == 0 && costumeSkillUsesFromInfo > 0 {
				costumeSkillDelta = costumeSkillUsesFromInfo
				// log.Printf("[BattleService] FinishWave: BattleDetail costumeSkills=0, using SkillUseInfo count=%d", costumeSkillUsesFromInfo)
			}
			user.Battle.LastCostumeSkillUsedCount += costumeSkillDelta
			user.Battle.LastWeaponSkillUsedCount += d.PlayerWeaponActiveSkillUsedCount
			user.Battle.LastCompanionSkillUsedCount += d.PlayerCompanionSkillUsedCount
			user.Battle.LastCriticalCount += d.CriticalCount
			// For combo count, take the maximum value received from client
			// This is the max combo chain achieved during this wave
			// log.Printf("[BattleService] FinishWave: combo from client - received=%d, current max=%d", d.ComboCount, user.Battle.LastComboCount)
			if d.ComboCount > user.Battle.LastComboCount {
				user.Battle.LastComboCount = d.ComboCount
				// log.Printf("[BattleService] FinishWave: combo updated to new max=%d", user.Battle.LastComboCount)
			}
			// log.Printf("[BattleService] FinishWave: combo state after update - LastComboCount=%d", user.Battle.LastComboCount)
			// ACCUMULATE recovery points
			user.Battle.LastTotalRecoverPoint += d.TotalRecoverPoint
			// For max damage, keep the maximum value seen
			if d.ComboMaxDamage > user.Battle.LastComboMaxDamage {
				user.Battle.LastComboMaxDamage = d.ComboMaxDamage
			}

			// Initialize all slots to alive (default), then overwrite with battle data
			aliveCount := 0
			partySize := int32(len(d.CostumeBattleInfo))

			// Get the deck to map DeckCharacterNumber to weapon IDs
			// Try to get from active quest if available, otherwise use default deck 1
			deckNumber := int32(1)
			// Try to find active quest
			for _, qstate := range user.Quests {
				if qstate.QuestStateType == 1 { // In progress
					deckNumber = qstate.UserDeckNumber
					if deckNumber == 0 {
						deckNumber = 1
					}
					break
				}
			}

			deckKey := store.DeckKey{DeckType: model.DeckTypeQuest, UserDeckNumber: deckNumber}
			deck, _ := user.Decks[deckKey]
			deckUuids := [3]string{deck.UserDeckCharacterUuid01, deck.UserDeckCharacterUuid02, deck.UserDeckCharacterUuid03}

			for i, ci := range d.CostumeBattleInfo {
				if i >= 3 {
					break
				}
				if ci.IsAlive {
					aliveCount++
				}
				if ci.MaxHp > 0 {
					user.Battle.LastCostumeHpPercent[i] = int32(ci.GetRemainingHp() * 100 / ci.MaxHp)
				} else {
					user.Battle.LastCostumeHpPercent[i] = 0
				}
				// Save character IDs from the battle
				user.Battle.LastPartyCharacterIds[i] = ci.DeckCharacterNumber

				// Save main weapon ID - map DeckCharacterNumber to UUID and get weapon
				if ci.DeckCharacterNumber >= 1 && ci.DeckCharacterNumber <= 3 {
					dcUuid := deckUuids[ci.DeckCharacterNumber-1]
					if dcUuid != "" {
						if dc, ok := user.DeckCharacters[dcUuid]; ok {
							if w, ok := user.Weapons[dc.MainUserWeaponUuid]; ok {
								user.Battle.LastMainWeaponIds[i] = w.WeaponId
							}
						}
					}
				}
			}
			user.Battle.LastCostumeAliveCount = int32(aliveCount)
			user.Battle.LastCostumePartySize = partySize
			// log.Printf("[BattleService] Saved: aliveCount=%d partySize=%d weaponSkills=%d costumeSkills=%d", aliveCount, partySize, user.Battle.LastWeaponSkillUsedCount, user.Battle.LastCostumeSkillUsedCount)
		} else {
			// log.Printf("[BattleService] FinishWave: BattleDetail is nil!")
			// No BattleDetail at all: still credit costume skill uses from SkillUseInfo
			if costumeSkillUsesFromInfo > 0 {
				user.Battle.LastCostumeSkillUsedCount += costumeSkillUsesFromInfo
				// log.Printf("[BattleService] FinishWave: credited %d costume skill uses from SkillUseInfo (no BattleDetail)", costumeSkillUsesFromInfo)
			}
		}
	})
	return &pb.FinishWaveResponse{}, nil
}
