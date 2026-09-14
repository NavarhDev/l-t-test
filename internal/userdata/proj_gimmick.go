package userdata

import (
	"sort"
	"sync"

	"lunar-tear/server/internal/masterdata"
	"lunar-tear/server/internal/store"
	"lunar-tear/server/internal/utils"
)

var gimmickOrnamentRefs = sync.OnceValue(masterdata.LoadGimmickOrnamentRefs)
var gimmickSequenceChains = sync.OnceValue(masterdata.LoadGimmickSequenceChains)
var hiddenSequenceSet = sync.OnceValue(masterdata.LoadHiddenGimmickSequenceIDs)
var gimmickSequenceRanks = sync.OnceValue(masterdata.LoadGimmickSequenceRanks)
var birdGimmicks = sync.OnceValue(masterdata.LoadBirdGimmickIDs)

const birdDefaultBaseDatetime int64 = 1577836800000 // 2020-01-01 00:00:00 UTC in ms

func init() {
	register("IUserGimmick", func(user store.UserState) string {
		s, _ := utils.EncodeJSONMaps(sortedGimmickRecords(user)...)
		return s
	})
	register("IUserGimmickOrnamentProgress", func(user store.UserState) string {
		s, _ := utils.EncodeJSONMaps(sortedGimmickOrnamentProgressRecords(user)...)
		return s
	})
	register("IUserGimmickSequence", func(user store.UserState) string {
		s, _ := utils.EncodeJSONMaps(sortedGimmickSequenceRecords(user)...)
		return s
	})
	register("IUserGimmickUnlock", func(user store.UserState) string {
		s, _ := utils.EncodeJSONMaps(sortedGimmickUnlockRecords(user)...)
		return s
	})
}

func projectActiveChainOrnaments(
	user store.UserState,
	addKey func(seqKey store.GimmickSequenceKey, seqId int32, ref masterdata.GimmickOrnamentRef),
	sizeFn func() int,
	cap int,
) {
	refs := gimmickOrnamentRefs()
	chains := gimmickSequenceChains()
	hiddenSeq := hiddenSequenceSet()

	walkChain := func(seqKey store.GimmickSequenceKey) {
		chain := chains[seqKey.GimmickSequenceId]
		if len(chain) == 0 {
			chain = []int32{seqKey.GimmickSequenceId}
		}
		for _, seqId := range chain {
			for _, ref := range refs[seqId] {
				addKey(seqKey, seqId, ref)
			}
		}
	}

	var nonHidden []store.GimmickSequenceKey
	for seqKey := range user.Gimmick.Sequences {
		if hiddenSeq[seqKey.GimmickSequenceId] {
			walkChain(seqKey)
		} else {
			nonHidden = append(nonHidden, seqKey)
		}
	}
	for _, seqKey := range nonHidden {
		if sizeFn() >= cap {
			break
		}
		walkChain(seqKey)
	}
}

func sortedGimmickRecords(user store.UserState) []map[string]any {

	keySet := make(map[store.GimmickKey]struct{})
	// Real progress rows (genuine user data) — always kept.
	for key := range user.Gimmick.Progress {
		keySet[key] = struct{}{}
	}
	projectActiveChainOrnaments(user,
		func(seqKey store.GimmickSequenceKey, seqId int32, ref masterdata.GimmickOrnamentRef) {
			keySet[store.GimmickKey{
				GimmickSequenceScheduleId: seqKey.GimmickSequenceScheduleId,
				GimmickSequenceId:         seqId,
				GimmickId:                 ref.GimmickId,
			}] = struct{}{}
		},
		func() int { return len(keySet) },
		masterdata.MaxUserGimmickRows,
	)

	keys := make([]store.GimmickKey, 0, len(keySet))
	for key := range keySet {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return compareGimmickKey(keys[i], keys[j]) < 0
	})

	records := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		isGimmickCleared := false
		startDatetime := user.GameStartDatetime
		latestVersion := user.GameStartDatetime
		if row, ok := user.Gimmick.Progress[key]; ok {
			isGimmickCleared = row.IsGimmickCleared
			startDatetime = row.StartDatetime
			latestVersion = row.LatestVersion
		}
		records = append(records, map[string]any{
			"userId":                    user.UserId,
			"gimmickSequenceScheduleId": key.GimmickSequenceScheduleId,
			"gimmickSequenceId":         key.GimmickSequenceId,
			"gimmickId":                 key.GimmickId,
			"isGimmickCleared":          isGimmickCleared,
			"startDatetime":             startDatetime,
			"latestVersion":             latestVersion,
		})
	}
	return records
}

func sortedGimmickOrnamentProgressRecords(user store.UserState) []map[string]any {

	keySet := make(map[store.GimmickOrnamentKey]struct{})
	// Real progress rows (genuine user data) — always kept.
	for key := range user.Gimmick.OrnamentProgress {
		keySet[key] = struct{}{}
	}
	projectActiveChainOrnaments(user,
		func(seqKey store.GimmickSequenceKey, seqId int32, ref masterdata.GimmickOrnamentRef) {
			keySet[store.GimmickOrnamentKey{
				GimmickSequenceScheduleId: seqKey.GimmickSequenceScheduleId,
				GimmickSequenceId:         seqId,
				GimmickId:                 ref.GimmickId,
				GimmickOrnamentIndex:      ref.OrnamentIndex,
			}] = struct{}{}
		},
		func() int { return len(keySet) },
		masterdata.MaxUserGimmickRows,
	)

	keys := make([]store.GimmickOrnamentKey, 0, len(keySet))
	for key := range keySet {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return compareGimmickOrnamentKey(keys[i], keys[j]) < 0
	})

	birdG := birdGimmicks()
	records := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		progressValueBit := int32(0)
		baseDatetime := user.GameStartDatetime
		latestVersion := user.GameStartDatetime
		if row, ok := user.Gimmick.OrnamentProgress[key]; ok {
			progressValueBit = row.ProgressValueBit
			baseDatetime = row.BaseDatetime
			latestVersion = row.LatestVersion
		} else if birdG[key.GimmickId] {
			baseDatetime = birdDefaultBaseDatetime
		}
		records = append(records, map[string]any{
			"userId":                    user.UserId,
			"gimmickSequenceScheduleId": key.GimmickSequenceScheduleId,
			"gimmickSequenceId":         key.GimmickSequenceId,
			"gimmickId":                 key.GimmickId,
			"gimmickOrnamentIndex":      key.GimmickOrnamentIndex,
			"progressValueBit":          progressValueBit,
			"baseDatetime":              baseDatetime,
			"latestVersion":             latestVersion,
		})
	}
	return records
}

func sortedGimmickSequenceRecords(user store.UserState) []map[string]any {

	ranks := gimmickSequenceRanks()

	keys := make([]store.GimmickSequenceKey, 0, len(user.Gimmick.Sequences))
	for key := range user.Gimmick.Sequences {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		ri, rj := ranks[keys[i].GimmickSequenceId], ranks[keys[j].GimmickSequenceId]
		if ri != rj {
			return ri < rj
		}
		if keys[i].GimmickSequenceScheduleId != keys[j].GimmickSequenceScheduleId {
			return keys[i].GimmickSequenceScheduleId < keys[j].GimmickSequenceScheduleId
		}
		return keys[i].GimmickSequenceId < keys[j].GimmickSequenceId
	})
	if len(keys) > masterdata.MaxUserGimmickRows {
		keys = keys[:masterdata.MaxUserGimmickRows]
	}

	records := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		row := user.Gimmick.Sequences[key]
		records = append(records, map[string]any{
			"userId":                    user.UserId,
			"gimmickSequenceScheduleId": row.Key.GimmickSequenceScheduleId,
			"gimmickSequenceId":         row.Key.GimmickSequenceId,
			"isGimmickSequenceCleared":  row.IsGimmickSequenceCleared,
			"clearDatetime":             row.ClearDatetime,
			"latestVersion":             row.LatestVersion,
		})
	}
	return records
}

func sortedGimmickUnlockRecords(user store.UserState) []map[string]any {
	keys := make([]store.GimmickKey, 0, len(user.Gimmick.Unlocks))
	for key := range user.Gimmick.Unlocks {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return compareGimmickKey(keys[i], keys[j]) < 0
	})

	records := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		row := user.Gimmick.Unlocks[key]
		records = append(records, map[string]any{
			"userId":                    user.UserId,
			"gimmickSequenceScheduleId": row.Key.GimmickSequenceScheduleId,
			"gimmickSequenceId":         row.Key.GimmickSequenceId,
			"gimmickId":                 row.Key.GimmickId,
			"isUnlocked":                row.IsUnlocked,
			"latestVersion":             row.LatestVersion,
		})
	}
	return records
}
