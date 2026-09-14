package service

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/masterdata"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"
	"lunar-tear/server/internal/utils"
)

// battleDebugCatalog holds the extra masterdata tables needed to itemize
// character stats and abilities at battle start. Loaded lazily on the first
// StartWave because these tables are not part of the regular runtime catalogs.
type battleDebugCatalog struct {
	costumeBaseStatus  map[int32]masterdata.EntityMCostumeBaseStatus
	costumeStatusCalc  map[int32]masterdata.EntityMCostumeStatusCalculation
	costumeAbilities   map[int32][]masterdata.EntityMCostumeAbilityGroup      // key: CostumeAbilityGroupId
	costumeAbilityLvls map[int32][]masterdata.EntityMCostumeAbilityLevelGroup // key: CostumeAbilityLevelGroupId, sorted asc by lower limit
	awakenAbilities    map[int32]masterdata.EntityMCostumeAwakenAbility       // key: CostumeAwakenAbilityId

	weaponBaseStatus map[int32]masterdata.EntityMWeaponBaseStatus
	weaponStatusCalc map[int32]masterdata.EntityMWeaponStatusCalculation

	companionBaseStatus map[int32]masterdata.EntityMCompanionBaseStatus
	companionStatusCalc map[int32]masterdata.EntityMCompanionStatusCalculation
	companionAbilities  map[int32][]masterdata.EntityMCompanionAbilityGroup // key: CompanionAbilityGroupId
	companionAbilityLvl []masterdata.EntityMCompanionAbilityLevel           // sorted asc by CompanionLevelLowerLimit

	thoughts map[int32]masterdata.EntityMThought

	partsGroups     map[int32]masterdata.EntityMPartsGroup
	partsSeries     map[int32]masterdata.EntityMPartsSeries
	partsSeriesAbis map[int32][]masterdata.EntityMPartsSeriesBonusAbilityGroup // key: PartsSeriesBonusAbilityGroupId

	// Quest deck bonuses ("Resonant Characters/Weapons" of event quests).
	questBonuses              map[int32]masterdata.EntityMQuestBonus                      // key: QuestBonusId
	questBonusCharGroups      map[int32][]masterdata.EntityMQuestBonusCharacterGroup      // key: QuestBonusCharacterGroupId
	questBonusCostumeGroups   map[int32][]masterdata.EntityMQuestBonusCostumeGroup        // key: QuestBonusCostumeGroupId
	questBonusCostumeSettings map[int32][]masterdata.EntityMQuestBonusCostumeSettingGroup // key: QuestBonusCostumeSettingGroupId
	questBonusWeaponGroups    map[int32][]masterdata.EntityMQuestBonusWeaponGroup         // key: QuestBonusWeaponGroupId
	questBonusEffects         map[int32][]masterdata.EntityMQuestBonusEffectGroup         // key: QuestBonusEffectGroupId
	questBonusAbilities       map[int32][]masterdata.EntityMQuestBonusAbility             // key: QuestBonusEffectId
	questBonusDrops           map[int32][]masterdata.EntityMQuestBonusDropReward          // key: QuestBonusEffectId
	questBonusTerms           map[int32][]masterdata.EntityMQuestBonusTermGroup           // key: QuestBonusTermGroupId
	questBonusAllyChars       map[int32][]masterdata.EntityMQuestBonusAllyCharacter       // key: QuestBonusAllyCharacterId

	// Ability behaviour chain used to expand passive stat-up abilities.
	// The bin has no m_ability table: AbilityId doubles as AbilityLevelGroupId.
	abilityLevelTiers   map[int32][]masterdata.EntityMAbilityLevelGroup            // key: AbilityLevelGroupId, sorted asc by LevelLowerLimit
	abilityDetails      map[int32]masterdata.EntityMAbilityDetail                  // key: AbilityDetailId
	abilityBehGroups    map[int32][]masterdata.EntityMAbilityBehaviourGroup        // key: AbilityBehaviourGroupId
	abilityBehaviours   map[int32]masterdata.EntityMAbilityBehaviour               // key: AbilityBehaviourId
	abilityActionStatus map[int32][]masterdata.EntityMAbilityBehaviourActionStatus // key: AbilityBehaviourActionId
	abilityStatuses     map[int32]masterdata.EntityMAbilityStatus                  // key: AbilityStatusId

	// Stained glass ("remnant" important items) permanent stat bonuses.
	stainedGlassByItemId map[int32]masterdata.EntityMStainedGlass                      // key: ImportantItemId (type 5)
	stainedGlassTargets  map[int32][]masterdata.EntityMStainedGlassStatusUpTargetGroup // key: StainedGlassStatusUpTargetGroupId
	stainedGlassStatuses map[int32][]masterdata.EntityMStainedGlassStatusUpGroup       // key: StainedGlassStatusUpGroupId

	funcs *masterdata.FunctionResolver

	// Optional id->name maps loaded from lunar-base/data/names/*.json.
	// Empty when the directory is not available; log falls back to raw IDs.
	names map[string]map[int32]string
}

var (
	battleDebugOnce sync.Once
	battleDebug     *battleDebugCatalog
)

// Stained glass ("remnant") constants from m_important_item /
// m_stained_glass_status_up_target_group.
const (
	importantItemTypeStainedGlass            = 5 // m_important_item.ImportantItemType
	stainedGlassTargetTypeCharacter          = 1 // TargetValue = CharacterId
	stainedGlassTargetTypeSkillfulWeaponType = 2 // TargetValue = costume SkillfulWeaponType
)

func getBattleDebugCatalog() *battleDebugCatalog {
	battleDebugOnce.Do(func() {
		c, err := loadBattleDebugCatalog()
		if err != nil {
			log.Printf("[BattleDebug] disabled: %v", err)
			return
		}
		battleDebug = c
	})
	return battleDebug
}

func loadBattleDebugCatalog() (*battleDebugCatalog, error) {
	c := &battleDebugCatalog{
		costumeBaseStatus:   map[int32]masterdata.EntityMCostumeBaseStatus{},
		costumeStatusCalc:   map[int32]masterdata.EntityMCostumeStatusCalculation{},
		costumeAbilities:    map[int32][]masterdata.EntityMCostumeAbilityGroup{},
		costumeAbilityLvls:  map[int32][]masterdata.EntityMCostumeAbilityLevelGroup{},
		awakenAbilities:     map[int32]masterdata.EntityMCostumeAwakenAbility{},
		weaponBaseStatus:    map[int32]masterdata.EntityMWeaponBaseStatus{},
		weaponStatusCalc:    map[int32]masterdata.EntityMWeaponStatusCalculation{},
		companionBaseStatus: map[int32]masterdata.EntityMCompanionBaseStatus{},
		companionStatusCalc: map[int32]masterdata.EntityMCompanionStatusCalculation{},
		companionAbilities:  map[int32][]masterdata.EntityMCompanionAbilityGroup{},
		thoughts:            map[int32]masterdata.EntityMThought{},
		partsGroups:         map[int32]masterdata.EntityMPartsGroup{},
		partsSeries:         map[int32]masterdata.EntityMPartsSeries{},
		partsSeriesAbis:     map[int32][]masterdata.EntityMPartsSeriesBonusAbilityGroup{},

		questBonuses:              map[int32]masterdata.EntityMQuestBonus{},
		questBonusCharGroups:      map[int32][]masterdata.EntityMQuestBonusCharacterGroup{},
		questBonusCostumeGroups:   map[int32][]masterdata.EntityMQuestBonusCostumeGroup{},
		questBonusCostumeSettings: map[int32][]masterdata.EntityMQuestBonusCostumeSettingGroup{},
		questBonusWeaponGroups:    map[int32][]masterdata.EntityMQuestBonusWeaponGroup{},
		questBonusEffects:         map[int32][]masterdata.EntityMQuestBonusEffectGroup{},
		questBonusAbilities:       map[int32][]masterdata.EntityMQuestBonusAbility{},
		questBonusDrops:           map[int32][]masterdata.EntityMQuestBonusDropReward{},
		questBonusTerms:           map[int32][]masterdata.EntityMQuestBonusTermGroup{},
		questBonusAllyChars:       map[int32][]masterdata.EntityMQuestBonusAllyCharacter{},

		abilityLevelTiers:   map[int32][]masterdata.EntityMAbilityLevelGroup{},
		abilityDetails:      map[int32]masterdata.EntityMAbilityDetail{},
		abilityBehGroups:    map[int32][]masterdata.EntityMAbilityBehaviourGroup{},
		abilityBehaviours:   map[int32]masterdata.EntityMAbilityBehaviour{},
		abilityActionStatus: map[int32][]masterdata.EntityMAbilityBehaviourActionStatus{},
		abilityStatuses:     map[int32]masterdata.EntityMAbilityStatus{},

		stainedGlassByItemId: map[int32]masterdata.EntityMStainedGlass{},
		stainedGlassTargets:  map[int32][]masterdata.EntityMStainedGlassStatusUpTargetGroup{},
		stainedGlassStatuses: map[int32][]masterdata.EntityMStainedGlassStatusUpGroup{},

		names: map[string]map[int32]string{},
	}

	rows1, err := utils.ReadTable[masterdata.EntityMCostumeBaseStatus]("m_costume_base_status")
	if err != nil {
		return nil, err
	}
	for _, r := range rows1 {
		c.costumeBaseStatus[r.CostumeBaseStatusId] = r
	}
	rows2, err := utils.ReadTable[masterdata.EntityMCostumeStatusCalculation]("m_costume_status_calculation")
	if err != nil {
		return nil, err
	}
	for _, r := range rows2 {
		c.costumeStatusCalc[r.CostumeStatusCalculationId] = r
	}
	rows3, err := utils.ReadTable[masterdata.EntityMCostumeAbilityGroup]("m_costume_ability_group")
	if err != nil {
		return nil, err
	}
	for _, r := range rows3 {
		c.costumeAbilities[r.CostumeAbilityGroupId] = append(c.costumeAbilities[r.CostumeAbilityGroupId], r)
	}
	for gid := range c.costumeAbilities {
		rows := c.costumeAbilities[gid]
		sort.Slice(rows, func(i, j int) bool { return rows[i].SlotNumber < rows[j].SlotNumber })
	}
	rows4, err := utils.ReadTable[masterdata.EntityMCostumeAbilityLevelGroup]("m_costume_ability_level_group")
	if err != nil {
		return nil, err
	}
	for _, r := range rows4 {
		c.costumeAbilityLvls[r.CostumeAbilityLevelGroupId] = append(c.costumeAbilityLvls[r.CostumeAbilityLevelGroupId], r)
	}
	for gid := range c.costumeAbilityLvls {
		rows := c.costumeAbilityLvls[gid]
		sort.Slice(rows, func(i, j int) bool {
			return rows[i].CostumeLimitBreakCountLowerLimit < rows[j].CostumeLimitBreakCountLowerLimit
		})
	}
	rows5, err := utils.ReadTable[masterdata.EntityMCostumeAwakenAbility]("m_costume_awaken_ability")
	if err != nil {
		return nil, err
	}
	for _, r := range rows5 {
		c.awakenAbilities[r.CostumeAwakenAbilityId] = r
	}

	rows6, err := utils.ReadTable[masterdata.EntityMWeaponBaseStatus]("m_weapon_base_status")
	if err != nil {
		return nil, err
	}
	for _, r := range rows6 {
		c.weaponBaseStatus[r.WeaponBaseStatusId] = r
	}
	rows7, err := utils.ReadTable[masterdata.EntityMWeaponStatusCalculation]("m_weapon_status_calculation")
	if err != nil {
		return nil, err
	}
	for _, r := range rows7 {
		c.weaponStatusCalc[r.WeaponStatusCalculationId] = r
	}

	rows8, err := utils.ReadTable[masterdata.EntityMCompanionBaseStatus]("m_companion_base_status")
	if err != nil {
		return nil, err
	}
	for _, r := range rows8 {
		c.companionBaseStatus[r.CompanionBaseStatusId] = r
	}
	rows9, err := utils.ReadTable[masterdata.EntityMCompanionStatusCalculation]("m_companion_status_calculation")
	if err != nil {
		return nil, err
	}
	for _, r := range rows9 {
		c.companionStatusCalc[r.CompanionStatusCalculationId] = r
	}
	rows10, err := utils.ReadTable[masterdata.EntityMCompanionAbilityGroup]("m_companion_ability_group")
	if err != nil {
		return nil, err
	}
	for _, r := range rows10 {
		c.companionAbilities[r.CompanionAbilityGroupId] = append(c.companionAbilities[r.CompanionAbilityGroupId], r)
	}
	rows11, err := utils.ReadTable[masterdata.EntityMCompanionAbilityLevel]("m_companion_ability_level")
	if err != nil {
		return nil, err
	}
	c.companionAbilityLvl = rows11
	sort.Slice(c.companionAbilityLvl, func(i, j int) bool {
		return c.companionAbilityLvl[i].CompanionLevelLowerLimit < c.companionAbilityLvl[j].CompanionLevelLowerLimit
	})

	rows12, err := utils.ReadTable[masterdata.EntityMThought]("m_thought")
	if err != nil {
		return nil, err
	}
	for _, r := range rows12 {
		c.thoughts[r.ThoughtId] = r
	}

	rows13, err := utils.ReadTable[masterdata.EntityMPartsGroup]("m_parts_group")
	if err != nil {
		return nil, err
	}
	for _, r := range rows13 {
		c.partsGroups[r.PartsGroupId] = r
	}
	rows14, err := utils.ReadTable[masterdata.EntityMPartsSeries]("m_parts_series")
	if err != nil {
		return nil, err
	}
	for _, r := range rows14 {
		c.partsSeries[r.PartsSeriesId] = r
	}
	rows15, err := utils.ReadTable[masterdata.EntityMPartsSeriesBonusAbilityGroup]("m_parts_series_bonus_ability_group")
	if err != nil {
		return nil, err
	}
	for _, r := range rows15 {
		c.partsSeriesAbis[r.PartsSeriesBonusAbilityGroupId] = append(c.partsSeriesAbis[r.PartsSeriesBonusAbilityGroupId], r)
	}

	rows16, err := utils.ReadTable[masterdata.EntityMQuestBonus]("m_quest_bonus")
	if err != nil {
		return nil, err
	}
	for _, r := range rows16 {
		c.questBonuses[r.QuestBonusId] = r
	}
	rows17, err := utils.ReadTable[masterdata.EntityMQuestBonusCharacterGroup]("m_quest_bonus_character_group")
	if err != nil {
		return nil, err
	}
	for _, r := range rows17 {
		c.questBonusCharGroups[r.QuestBonusCharacterGroupId] = append(c.questBonusCharGroups[r.QuestBonusCharacterGroupId], r)
	}
	rows18, err := utils.ReadTable[masterdata.EntityMQuestBonusCostumeGroup]("m_quest_bonus_costume_group")
	if err != nil {
		return nil, err
	}
	for _, r := range rows18 {
		c.questBonusCostumeGroups[r.QuestBonusCostumeGroupId] = append(c.questBonusCostumeGroups[r.QuestBonusCostumeGroupId], r)
	}
	rows19, err := utils.ReadTable[masterdata.EntityMQuestBonusCostumeSettingGroup]("m_quest_bonus_costume_setting_group")
	if err != nil {
		return nil, err
	}
	for _, r := range rows19 {
		c.questBonusCostumeSettings[r.QuestBonusCostumeSettingGroupId] = append(c.questBonusCostumeSettings[r.QuestBonusCostumeSettingGroupId], r)
	}
	rows20, err := utils.ReadTable[masterdata.EntityMQuestBonusWeaponGroup]("m_quest_bonus_weapon_group")
	if err != nil {
		return nil, err
	}
	for _, r := range rows20 {
		c.questBonusWeaponGroups[r.QuestBonusWeaponGroupId] = append(c.questBonusWeaponGroups[r.QuestBonusWeaponGroupId], r)
	}
	rows21, err := utils.ReadTable[masterdata.EntityMQuestBonusEffectGroup]("m_quest_bonus_effect_group")
	if err != nil {
		return nil, err
	}
	for _, r := range rows21 {
		c.questBonusEffects[r.QuestBonusEffectGroupId] = append(c.questBonusEffects[r.QuestBonusEffectGroupId], r)
	}
	rows22, err := utils.ReadTable[masterdata.EntityMQuestBonusAbility]("m_quest_bonus_ability")
	if err != nil {
		return nil, err
	}
	for _, r := range rows22 {
		c.questBonusAbilities[r.QuestBonusEffectId] = append(c.questBonusAbilities[r.QuestBonusEffectId], r)
	}
	rows23, err := utils.ReadTable[masterdata.EntityMQuestBonusDropReward]("m_quest_bonus_drop_reward")
	if err != nil {
		return nil, err
	}
	for _, r := range rows23 {
		c.questBonusDrops[r.QuestBonusEffectId] = append(c.questBonusDrops[r.QuestBonusEffectId], r)
	}
	rows24, err := utils.ReadTable[masterdata.EntityMQuestBonusTermGroup]("m_quest_bonus_term_group")
	if err != nil {
		return nil, err
	}
	for _, r := range rows24 {
		c.questBonusTerms[r.QuestBonusTermGroupId] = append(c.questBonusTerms[r.QuestBonusTermGroupId], r)
	}
	rows24b, err := utils.ReadTable[masterdata.EntityMQuestBonusAllyCharacter]("m_quest_bonus_ally_character")
	if err != nil {
		return nil, err
	}
	for _, r := range rows24b {
		c.questBonusAllyChars[r.QuestBonusAllyCharacterId] = append(c.questBonusAllyChars[r.QuestBonusAllyCharacterId], r)
	}

	rows25, err := utils.ReadTable[masterdata.EntityMAbilityLevelGroup]("m_ability_level_group")
	if err != nil {
		return nil, err
	}
	for _, r := range rows25 {
		c.abilityLevelTiers[r.AbilityLevelGroupId] = append(c.abilityLevelTiers[r.AbilityLevelGroupId], r)
	}
	for gid := range c.abilityLevelTiers {
		rows := c.abilityLevelTiers[gid]
		sort.Slice(rows, func(i, j int) bool { return rows[i].LevelLowerLimit < rows[j].LevelLowerLimit })
	}
	rows26, err := utils.ReadTable[masterdata.EntityMAbilityDetail]("m_ability_detail")
	if err != nil {
		return nil, err
	}
	for _, r := range rows26 {
		c.abilityDetails[r.AbilityDetailId] = r
	}
	rows27, err := utils.ReadTable[masterdata.EntityMAbilityBehaviourGroup]("m_ability_behaviour_group")
	if err != nil {
		return nil, err
	}
	for _, r := range rows27 {
		c.abilityBehGroups[r.AbilityBehaviourGroupId] = append(c.abilityBehGroups[r.AbilityBehaviourGroupId], r)
	}
	rows28, err := utils.ReadTable[masterdata.EntityMAbilityBehaviour]("m_ability_behaviour")
	if err != nil {
		return nil, err
	}
	for _, r := range rows28 {
		c.abilityBehaviours[r.AbilityBehaviourId] = r
	}
	rows29, err := utils.ReadTable[masterdata.EntityMAbilityBehaviourActionStatus]("m_ability_behaviour_action_status")
	if err != nil {
		return nil, err
	}
	for _, r := range rows29 {
		c.abilityActionStatus[r.AbilityBehaviourActionId] = append(c.abilityActionStatus[r.AbilityBehaviourActionId], r)
	}
	rows30, err := utils.ReadTable[masterdata.EntityMAbilityStatus]("m_ability_status")
	if err != nil {
		return nil, err
	}
	for _, r := range rows30 {
		c.abilityStatuses[r.AbilityStatusId] = r
	}

	// Stained glass ("remnant") tables. Important items of type 5 reference a
	// stained glass row through ExternalReferenceId.
	rows31, err := utils.ReadTable[masterdata.EntityMStainedGlass]("m_stained_glass")
	if err != nil {
		return nil, err
	}
	glassById := map[int32]masterdata.EntityMStainedGlass{}
	for _, r := range rows31 {
		glassById[r.StainedGlassId] = r
	}
	rows32, err := utils.ReadTable[masterdata.EntityMStainedGlassStatusUpTargetGroup]("m_stained_glass_status_up_target_group")
	if err != nil {
		return nil, err
	}
	for _, r := range rows32 {
		c.stainedGlassTargets[r.StainedGlassStatusUpTargetGroupId] = append(c.stainedGlassTargets[r.StainedGlassStatusUpTargetGroupId], r)
	}
	rows33, err := utils.ReadTable[masterdata.EntityMStainedGlassStatusUpGroup]("m_stained_glass_status_up_group")
	if err != nil {
		return nil, err
	}
	for _, r := range rows33 {
		c.stainedGlassStatuses[r.StainedGlassStatusUpGroupId] = append(c.stainedGlassStatuses[r.StainedGlassStatusUpGroupId], r)
	}
	rows34, err := utils.ReadTable[masterdata.EntityMImportantItem]("m_important_item")
	if err != nil {
		return nil, err
	}
	for _, r := range rows34 {
		if r.ImportantItemType != importantItemTypeStainedGlass {
			continue
		}
		if g, ok := glassById[r.ExternalReferenceId]; ok {
			c.stainedGlassByItemId[r.ImportantItemId] = g
		}
	}

	c.funcs, err = masterdata.LoadFunctionResolver()
	if err != nil {
		return nil, err
	}

	c.loadNames()
	return c, nil
}

// loadNames pulls optional id->name maps from lunar-base/data/names. Missing
// files or directories are not an error: the log simply shows raw IDs.
func (c *battleDebugCatalog) loadNames() {
	var candidates []string
	if env := os.Getenv("LUNAR_BASE_NAMES_DIR"); env != "" {
		candidates = append(candidates, env)
	}
	candidates = append(candidates,
		filepath.Join("..", "lunar-base", "data", "names"),
		filepath.Join("..", "..", "lunar-base", "data", "names"),
	)
	dir := ""
	for _, cand := range candidates {
		if st, err := os.Stat(cand); err == nil && st.IsDir() {
			dir = cand
			break
		}
	}
	if dir == "" {
		log.Printf("[BattleDebug] names dir not found (set LUNAR_BASE_NAMES_DIR), logging raw ids")
		return
	}

	kinds := []string{
		"abilities", "skills", "weapon_skills", "costume_active_skills",
		"costumes", "weapons", "companions", "characters", "thoughts", "parts",
	}
	type nameRecord struct {
		Id   int32  `json:"id"`
		Name string `json:"name"`
	}
	type namesFile struct {
		Records []nameRecord `json:"records"`
	}
	total := 0
	for _, kind := range kinds {
		raw, err := os.ReadFile(filepath.Join(dir, kind+".json"))
		if err != nil {
			continue
		}
		var nf namesFile
		if err := json.Unmarshal(raw, &nf); err != nil {
			log.Printf("[BattleDebug] bad names file %s.json: %v", kind, err)
			continue
		}
		m := make(map[int32]string, len(nf.Records))
		for _, r := range nf.Records {
			if r.Name != "" {
				m[r.Id] = r.Name
			}
		}
		c.names[kind] = m
		total += len(m)
	}
	log.Printf("[BattleDebug] loaded %d names from %s", total, dir)
}

// n formats "id" or "id «Name»" when the name is known.
func (c *battleDebugCatalog) n(kind string, id int32) string {
	if m := c.names[kind]; m != nil {
		if s := m[id]; s != "" {
			return fmt.Sprintf("%d «%s»", id, s)
		}
	}
	return fmt.Sprintf("%d", id)
}

// statBlock keeps the six displayed stats. Crit values are permil (100 = 10%).
type statBlock struct {
	Hp, Attack, Vitality, Agility, CritRatio, CritAttack int64
}

func (s *statBlock) add(o statBlock) {
	s.Hp += o.Hp
	s.Attack += o.Attack
	s.Vitality += o.Vitality
	s.Agility += o.Agility
	s.CritRatio += o.CritRatio
	s.CritAttack += o.CritAttack
}

func (s statBlock) isZero() bool {
	return s.Hp == 0 && s.Attack == 0 && s.Vitality == 0 && s.Agility == 0 && s.CritRatio == 0 && s.CritAttack == 0
}

// String prints absolute stats; crit permil is rendered as percent.
func (s statBlock) String() string {
	return fmt.Sprintf("HP=%d ATK=%d DEF=%d AGI=%d CritRate=%.1f%% CritDmg=%.1f%%",
		s.Hp, s.Attack, s.Vitality, s.Agility, float64(s.CritRatio)/10, float64(s.CritAttack)/10)
}

// deltaString prints only non-zero fields as "+N" (or "+N.N%" for crits/permil).
func (s statBlock) deltaString(permil bool) string {
	var parts []string
	f := func(label string, v int64) {
		if v == 0 {
			return
		}
		if permil {
			parts = append(parts, fmt.Sprintf("%s%+.1f%%", label, float64(v)/10))
		} else {
			parts = append(parts, fmt.Sprintf("%s%+d", label, v))
		}
	}
	f("HP", s.Hp)
	f(" ATK", s.Attack)
	f(" DEF", s.Vitality)
	f(" AGI", s.Agility)
	if permil {
		f(" CritRate", s.CritRatio)
		f(" CritDmg", s.CritAttack)
	} else {
		// flat crit bonuses are stored in permil as well
		if s.CritRatio != 0 {
			parts = append(parts, fmt.Sprintf(" CritRate%+.1f%%", float64(s.CritRatio)/10))
		}
		if s.CritAttack != 0 {
			parts = append(parts, fmt.Sprintf(" CritDmg%+.1f%%", float64(s.CritAttack)/10))
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, "")
}

// addByKind routes a (StatusKindType, value) pair into the right stat field.
func (s *statBlock) addByKind(kind int32, value int64) {
	switch model.StatusKindType(kind) {
	case model.StatusKindTypeAgility:
		s.Agility += value
	case model.StatusKindTypeAttack:
		s.Attack += value
	case model.StatusKindTypeCriticalAttack:
		s.CritAttack += value
	case model.StatusKindTypeCriticalRatio:
		s.CritRatio += value
	case model.StatusKindTypeHp:
		s.Hp += value
	case model.StatusKindTypeVitality:
		s.Vitality += value
	}
}

func (c *battleDebugCatalog) eval(funcId int32, level int32) int64 {
	if f, ok := c.funcs.Resolve(funcId); ok {
		return int64(f.Evaluate(level))
	}
	return 0
}

func (c *battleDebugCatalog) costumeStats(cm masterdata.EntityMCostume, level int32) statBlock {
	var out statBlock
	if base, ok := c.costumeBaseStatus[cm.CostumeBaseStatusId]; ok {
		out.Hp += int64(base.Hp)
		out.Attack += int64(base.Attack)
		out.Vitality += int64(base.Vitality)
		out.Agility += int64(base.Agility)
		out.CritRatio += int64(base.CriticalRatioPermil)
		out.CritAttack += int64(base.CriticalAttackRatioPermil)
	}
	if calc, ok := c.costumeStatusCalc[cm.CostumeStatusCalculationId]; ok {
		out.Hp += c.eval(calc.HpNumericalFunctionId, level)
		out.Attack += c.eval(calc.AttackNumericalFunctionId, level)
		out.Vitality += c.eval(calc.VitalityNumericalFunctionId, level)
		out.Agility += c.eval(calc.AgilityNumericalFunctionId, level)
		out.CritRatio += c.eval(calc.CriticalRatioPermilNumericalFunctionId, level)
		out.CritAttack += c.eval(calc.CriticalAttackRatioPermilNumericalFunctionId, level)
	}
	return out
}

func (c *battleDebugCatalog) weaponStats(wm masterdata.EntityMWeapon, level int32) statBlock {
	var out statBlock
	if base, ok := c.weaponBaseStatus[wm.WeaponBaseStatusId]; ok {
		out.Hp += int64(base.Hp)
		out.Attack += int64(base.Attack)
		out.Vitality += int64(base.Vitality)
	}
	if calc, ok := c.weaponStatusCalc[wm.WeaponStatusCalculationId]; ok {
		out.Hp += c.eval(calc.HpNumericalFunctionId, level)
		out.Attack += c.eval(calc.AttackNumericalFunctionId, level)
		out.Vitality += c.eval(calc.VitalityNumericalFunctionId, level)
	}
	return out
}

func (c *battleDebugCatalog) companionStats(comp masterdata.EntityMCompanion, level int32) statBlock {
	var out statBlock
	if base, ok := c.companionBaseStatus[comp.CompanionBaseStatusId]; ok {
		out.Hp += int64(base.Hp)
		out.Attack += int64(base.Attack)
		out.Vitality += int64(base.Vitality)
	}
	if calc, ok := c.companionStatusCalc[comp.CompanionStatusCalculationId]; ok {
		out.Hp += c.eval(calc.HpNumericalFunctionId, level)
		out.Attack += c.eval(calc.AttackNumericalFunctionId, level)
		out.Vitality += c.eval(calc.VitalityNumericalFunctionId, level)
	}
	return out
}

// costumeAbilityLevel resolves the ability level from the limit-break count.
func (c *battleDebugCatalog) costumeAbilityLevel(levelGroupId, limitBreak int32) int32 {
	lvl := int32(1)
	for _, row := range c.costumeAbilityLvls[levelGroupId] {
		if row.CostumeLimitBreakCountLowerLimit <= limitBreak {
			lvl = row.AbilityLevel
		}
	}
	return lvl
}

func (c *battleDebugCatalog) companionAbilityLevel(companionLevel int32) int32 {
	lvl := int32(1)
	for _, row := range c.companionAbilityLvl {
		if row.CompanionLevelLowerLimit <= companionLevel {
			lvl = row.AbilityLevel
		}
	}
	return lvl
}

// logBattleDeckDebug dumps stats + abilities for every character of the deck.
// Stat totals are a server-side estimate: the battle itself is simulated on
// the client, so multiplier stacking there may differ slightly.
func logBattleDeckDebug(cats *runtime.Catalogs, user store.UserState, deckType model.DeckType, deckNumber int32) {
	c := getBattleDebugCatalog()
	if c == nil {
		return
	}
	if deckNumber == 0 {
		deckNumber = 1
	}
	deck, ok := user.Decks[store.DeckKey{DeckType: deckType, UserDeckNumber: deckNumber}]
	if !ok && deckType != model.DeckTypeQuest {
		// Restricted / event battles sometimes reference the quest deck.
		deck, ok = user.Decks[store.DeckKey{DeckType: model.DeckTypeQuest, UserDeckNumber: deckNumber}]
	}
	if !ok {
		log.Printf("[BattleDebug] deck not found: type=%d number=%d", deckType, deckNumber)
		return
	}

	log.Printf("[BattleDebug] ===== Battle start: user=%d deckType=%d deckNumber=%d name=%q =====",
		user.UserId, deckType, deckNumber, deck.Name)

	// Resolve the deck bonus ("Resonant") of the currently active quest, if any.
	activeQuestId := user.EventQuest.CurrentQuestId
	if activeQuestId == 0 {
		activeQuestId = user.ExtraQuest.CurrentQuestId
	}
	var questBonus *masterdata.EntityMQuestBonus
	if activeQuestId != 0 {
		if qm, ok := cats.Quest.QuestById[activeQuestId]; ok && qm.QuestBonusId != 0 {
			if b, ok := c.questBonuses[qm.QuestBonusId]; ok {
				questBonus = &b
				log.Printf("[BattleDebug] Active quest %d has deck bonus %d (charGroup=%d costumeGroup=%d costumeSettingGroup=%d weaponGroup=%d allyCharGroup=%d)",
					activeQuestId, qm.QuestBonusId, b.QuestBonusCharacterGroupId, b.QuestBonusCostumeGroupId,
					b.QuestBonusCostumeSettingGroupId, b.QuestBonusWeaponGroupId, b.QuestBonusAllyCharacterId)
			}
		}
		if questBonus == nil {
			log.Printf("[BattleDebug] Active quest %d has no deck bonus", activeQuestId)
		}
	}

	uuids := []string{deck.UserDeckCharacterUuid01, deck.UserDeckCharacterUuid02, deck.UserDeckCharacterUuid03}
	for slot, dcUuid := range uuids {
		if dcUuid == "" {
			continue
		}
		dc, ok := user.DeckCharacters[dcUuid]
		if !ok {
			continue
		}
		logBattleCharacterDebug(c, cats, &user, slot+1, dcUuid, dc, questBonus)
	}
	log.Printf("[BattleDebug] ===== end of deck dump =====")
}

// questBonusTermActive reports whether the bonus row is active now. Rows
// without a term group (id 0 / no rows) never expire.
func (c *battleDebugCatalog) questBonusTermActive(termGroupId int32) bool {
	rows := c.questBonusTerms[termGroupId]
	if termGroupId == 0 || len(rows) == 0 {
		return true
	}
	now := gametime.NowMillis()
	for _, t := range rows {
		if t.StartDatetime <= now && now < t.EndDatetime {
			return true
		}
	}
	return false
}

// abilityStatusUp expands a passive stat-up ability into its flat and
// multiplicative (permil) components via the behaviour chain. Non-status
// behaviours (passive skills, blesses) are ignored.
func (c *battleDebugCatalog) abilityStatusUp(abilityId, level, mainWeaponAttr int32) (flat, mult statBlock, skippedAttrs []int32) {
	var detailId int32
	for _, t := range c.abilityLevelTiers[abilityId] {
		if t.LevelLowerLimit <= level {
			detailId = t.AbilityDetailId
		}
	}
	det, ok := c.abilityDetails[detailId]
	if !ok {
		return
	}
	for _, bg := range c.abilityBehGroups[det.AbilityBehaviourGroupId] {
		b, ok := c.abilityBehaviours[bg.AbilityBehaviourId]
		if !ok {
			continue
		}
		for _, as := range c.abilityActionStatus[b.AbilityBehaviourActionId] {
			// Attribute-conditional stat ups (e.g. event "element up 400%")
			// only apply when the slot's main weapon matches the attribute.
			if as.AttributeConditionType != 0 && as.AttributeConditionType != mainWeaponAttr {
				skippedAttrs = append(skippedAttrs, as.AttributeConditionType)
				continue
			}
			st, ok := c.abilityStatuses[as.AbilityStatusId]
			if !ok {
				continue
			}
			blk := statBlock{int64(st.Hp), int64(st.Attack), int64(st.Vitality), int64(st.Agility), int64(st.CriticalRatioPermil), int64(st.CriticalAttackRatioPermil)}
			// changeType 2 = ratio in permil, otherwise a flat addition.
			if as.AbilityBehaviourStatusChangeType == 2 {
				mult.add(blk)
			} else {
				flat.add(blk)
			}
		}
	}
	return
}

// logQuestBonusEffects lists the abilities / extra drops of one effect group
// and folds the ability stat-ups into the running flat/mult accumulators.
// mainWeaponAttr gates attribute-conditional stat ups.
func logQuestBonusEffects(c *battleDebugCatalog, source string, effectGroupId int32, flat, mult *statBlock, mainWeaponAttr int32) {
	for _, e := range c.questBonusEffects[effectGroupId] {
		for _, a := range c.questBonusAbilities[e.QuestBonusEffectId] {
			f, m, skipped := c.abilityStatusUp(a.AbilityId, a.Level, mainWeaponAttr)
			effect := ""
			if !f.isZero() {
				flat.add(f)
				effect += " flat " + f.deltaString(false)
			}
			if !m.isZero() {
				mult.add(m)
				effect += " multiply " + m.deltaString(true)
			}
			for _, attr := range skipped {
				effect += fmt.Sprintf(" (needs main weapon attribute %d, equipped %d — not applied)", attr, mainWeaponAttr)
			}
			if effect == "" {
				effect = " (no direct stat effect)"
			}
			log.Printf("[BattleDebug]   Quest bonus %s: ability %s lv%d%s", source, c.n("abilities", a.AbilityId), a.Level, effect)
		}
		for _, d := range c.questBonusDrops[e.QuestBonusEffectId] {
			log.Printf("[BattleDebug]   Quest bonus %s: extra drop possessionType=%d possessionId=%d count=%d",
				source, d.PossessionType, d.PossessionId, d.AdditionalCount)
		}
	}
}

func logBattleCharacterDebug(c *battleDebugCatalog, cats *runtime.Catalogs, user *store.UserState, slot int, dcUuid string, dc store.DeckCharacterState, questBonus *masterdata.EntityMQuestBonus) {
	costume, ok := user.Costumes[dc.UserCostumeUuid]
	if !ok {
		log.Printf("[BattleDebug] --- Slot %d: no costume ---", slot)
		return
	}
	cm, ok := cats.Costume.Costumes[costume.CostumeId]
	if !ok {
		log.Printf("[BattleDebug] --- Slot %d: costume %d not in masterdata ---", slot, costume.CostumeId)
		return
	}

	log.Printf("[BattleDebug] --- Slot %d: costume %s (character %s) ---",
		slot, c.n("costumes", costume.CostumeId), c.n("characters", cm.CharacterId))
	log.Printf("[BattleDebug]   level=%d limitBreak=%d awaken=%d power=%d",
		costume.Level, costume.LimitBreakCount, costume.AwakenCount, dc.Power)

	// --- base stats -------------------------------------------------------
	base := c.costumeStats(cm, costume.Level)
	log.Printf("[BattleDebug]   Costume base (lv%d): %s", costume.Level, base.String())
	total := base

	var flat statBlock // flat additive bonuses (crit fields in permil)
	var mult statBlock // multiplicative bonuses, permil (30 = +3%)

	// --- weapons ----------------------------------------------------------
	logWeapon := func(kind string, w store.WeaponState) {
		wm, ok := cats.Weapon.Weapons[w.WeaponId]
		if !ok {
			log.Printf("[BattleDebug]   %s %d: not in masterdata", kind, w.WeaponId)
			return
		}
		ws := c.weaponStats(wm, w.Level)
		total.add(ws)
		log.Printf("[BattleDebug]   %s %s lv=%d lb=%d: %s", kind, c.n("weapons", w.WeaponId), w.Level, w.LimitBreakCount, ws.deltaString(false))
		abilityRows := cats.Weapon.AbilityGroupsByGroupId[wm.WeaponAbilityGroupId]
		for _, ua := range user.WeaponAbilities[w.UserWeaponUuid] {
			for _, row := range abilityRows {
				if row.SlotNumber == ua.SlotNumber {
					log.Printf("[BattleDebug]     ability slot %d: %s lv%d", ua.SlotNumber, c.n("abilities", row.AbilityId), ua.Level)
				}
			}
		}
		skillRows := cats.Weapon.SkillGroupsByGroupId[wm.WeaponSkillGroupId]
		for _, us := range user.WeaponSkills[w.UserWeaponUuid] {
			for _, row := range skillRows {
				if row.SlotNumber == us.SlotNumber {
					log.Printf("[BattleDebug]     skill slot %d: %s lv%d", us.SlotNumber, c.n("weapon_skills", row.SkillId), us.Level)
				}
			}
		}
	}
	if w, ok := user.Weapons[dc.MainUserWeaponUuid]; ok {
		logWeapon("Main weapon", w)
	}
	for _, wu := range user.DeckSubWeapons[dcUuid] {
		if w, ok := user.Weapons[wu]; ok {
			logWeapon("Sub weapon", w)
		}
	}

	// --- companion ---------------------------------------------------------
	if comp, ok := user.Companions[dc.UserCompanionUuid]; ok {
		if compM, ok := cats.Companion.CompanionById[comp.CompanionId]; ok {
			cs := c.companionStats(compM, comp.Level)
			total.add(cs)
			log.Printf("[BattleDebug]   Companion %s lv=%d: %s skill=%s",
				c.n("companions", comp.CompanionId), comp.Level, cs.deltaString(false), c.n("skills", compM.SkillId))
			abLvl := c.companionAbilityLevel(comp.Level)
			for _, row := range c.companionAbilities[compM.CompanionAbilityGroupId] {
				log.Printf("[BattleDebug]     companion ability slot %d: %s lv%d", row.SlotNumber, c.n("abilities", row.AbilityId), abLvl)
			}
		}
	}

	// --- thought (memory) ---------------------------------------------------
	if t, ok := user.Thoughts[dc.UserThoughtUuid]; ok {
		if tm, ok := c.thoughts[t.ThoughtId]; ok {
			log.Printf("[BattleDebug]   Thought %s: ability %s lv%d", c.n("thoughts", t.ThoughtId), c.n("abilities", tm.AbilityId), tm.AbilityLevel)
		} else {
			log.Printf("[BattleDebug]   Thought %s", c.n("thoughts", t.ThoughtId))
		}
	}

	// --- costume active skill ----------------------------------------------
	activeSkillLevel := int32(1)
	if s, ok := user.CostumeActiveSkills[dc.UserCostumeUuid]; ok {
		activeSkillLevel = s.Level
	}
	for _, row := range cats.Costume.ActiveSkillGroupsByGroupId[cm.CostumeActiveSkillGroupId] {
		// rows are sorted desc by limit-break lower limit; take the first match
		if row.CostumeLimitBreakCountLowerLimit <= costume.LimitBreakCount {
			log.Printf("[BattleDebug]   Active skill: %s lv%d", c.n("costume_active_skills", row.CostumeActiveSkillId), activeSkillLevel)
			break
		}
	}

	// --- costume innate abilities -------------------------------------------
	for _, row := range c.costumeAbilities[cm.CostumeAbilityGroupId] {
		lvl := c.costumeAbilityLevel(row.CostumeAbilityLevelGroupId, costume.LimitBreakCount)
		log.Printf("[BattleDebug]   Costume ability slot %d: %s lv%d", row.SlotNumber, c.n("abilities", row.AbilityId), lvl)
	}

	// --- awaken effects ------------------------------------------------------
	if aw, ok := cats.Costume.AwakenByCostumeId[costume.CostumeId]; ok && costume.AwakenCount > 0 {
		steps := cats.Costume.AwakenEffectsByGroupAndStep[aw.CostumeAwakenEffectGroupId]
		for step := int32(1); step <= costume.AwakenCount; step++ {
			eff, ok := steps[step]
			if !ok {
				continue
			}
			if model.CostumeAwakenEffectType(eff.CostumeAwakenEffectType) == model.CostumeAwakenEffectTypeAbility {
				if ab, ok := c.awakenAbilities[eff.CostumeAwakenEffectId]; ok {
					log.Printf("[BattleDebug]   Awaken step %d ability: %s lv%d", step, c.n("abilities", ab.AbilityId), ab.AbilityLevel)
				}
			}
		}
	}

	// --- character board abilities -------------------------------------------
	for key, ab := range user.CharacterBoardAbilities {
		if key.CharacterId == cm.CharacterId {
			log.Printf("[BattleDebug]   Character board ability: %s lv%d", c.n("abilities", ab.AbilityId), ab.Level)
		}
	}

	// --- lottery (karma) abilities ---------------------------------------------
	for key, ab := range user.CostumeLotteryEffectAbilities {
		if key.UserCostumeUuid == dc.UserCostumeUuid {
			log.Printf("[BattleDebug]   Karma ability slot %d: %s lv%d", key.SlotNumber, c.n("abilities", ab.AbilityId), ab.AbilityLevel)
		}
	}

	// --- quest deck bonus ("Resonant Characters/Weapons") -----------------------
	// Attribute-conditional bonuses (event "element up") are gated on the
	// slot's main weapon attribute, matching the in-game rule.
	mainWeaponAttr := int32(0)
	if w, ok := user.Weapons[dc.MainUserWeaponUuid]; ok {
		if wm, ok := cats.Weapon.Weapons[w.WeaponId]; ok {
			mainWeaponAttr = wm.AttributeType
		}
	}
	if questBonus != nil {
		for _, r := range c.questBonusAllyChars[questBonus.QuestBonusAllyCharacterId] {
			if c.questBonusTermActive(r.QuestBonusTermGroupId) {
				logQuestBonusEffects(c, "[all allies]", r.QuestBonusEffectGroupId, &flat, &mult, mainWeaponAttr)
			}
		}
		for _, r := range c.questBonusCharGroups[questBonus.QuestBonusCharacterGroupId] {
			if r.CharacterId == cm.CharacterId && c.questBonusTermActive(r.QuestBonusTermGroupId) {
				logQuestBonusEffects(c, fmt.Sprintf("[character %s]", c.n("characters", r.CharacterId)), r.QuestBonusEffectGroupId, &flat, &mult, mainWeaponAttr)
			}
		}
		for _, r := range c.questBonusCostumeGroups[questBonus.QuestBonusCostumeGroupId] {
			if r.CostumeId == costume.CostumeId && c.questBonusTermActive(r.QuestBonusTermGroupId) {
				logQuestBonusEffects(c, fmt.Sprintf("[costume %s]", c.n("costumes", r.CostumeId)), r.QuestBonusEffectGroupId, &flat, &mult, mainWeaponAttr)
			}
		}
		// Costume-setting rows are tiered by limit break; only the highest
		// matching threshold applies.
		var bestSetting *masterdata.EntityMQuestBonusCostumeSettingGroup
		for i, r := range c.questBonusCostumeSettings[questBonus.QuestBonusCostumeSettingGroupId] {
			if r.CostumeId != costume.CostumeId || r.LimitBreakCountLowerLimit > costume.LimitBreakCount {
				continue
			}
			if !c.questBonusTermActive(r.QuestBonusTermGroupId) {
				continue
			}
			rows := c.questBonusCostumeSettings[questBonus.QuestBonusCostumeSettingGroupId]
			if bestSetting == nil || rows[i].LimitBreakCountLowerLimit > bestSetting.LimitBreakCountLowerLimit {
				bestSetting = &rows[i]
			}
		}
		if bestSetting != nil {
			logQuestBonusEffects(c, fmt.Sprintf("[costume %s lb>=%d]", c.n("costumes", bestSetting.CostumeId), bestSetting.LimitBreakCountLowerLimit), bestSetting.QuestBonusEffectGroupId, &flat, &mult, mainWeaponAttr)
		}
		// Weapon rows are tiered the same way, per equipped weapon.
		logWeaponBonus := func(w store.WeaponState) {
			var best *masterdata.EntityMQuestBonusWeaponGroup
			rows := c.questBonusWeaponGroups[questBonus.QuestBonusWeaponGroupId]
			for i, r := range rows {
				if r.WeaponId != w.WeaponId || r.LimitBreakCountLowerLimit > w.LimitBreakCount {
					continue
				}
				if !c.questBonusTermActive(r.QuestBonusTermGroupId) {
					continue
				}
				if best == nil || rows[i].LimitBreakCountLowerLimit > best.LimitBreakCountLowerLimit {
					best = &rows[i]
				}
			}
			if best != nil {
				logQuestBonusEffects(c, fmt.Sprintf("[weapon %s lb>=%d]", c.n("weapons", best.WeaponId), best.LimitBreakCountLowerLimit), best.QuestBonusEffectGroupId, &flat, &mult, mainWeaponAttr)
			}
		}
		if w, ok := user.Weapons[dc.MainUserWeaponUuid]; ok {
			logWeaponBonus(w)
		}
		for _, wu := range user.DeckSubWeapons[dcUuid] {
			if w, ok := user.Weapons[wu]; ok {
				logWeaponBonus(w)
			}
		}
	}

	// --- parts (memoirs) ---------------------------------------------------------
	seriesCount := map[int32]int32{}
	for _, pu := range user.DeckParts[dcUuid] {
		p, ok := user.Parts[pu]
		if !ok {
			continue
		}
		var pieces []string
		if def, ok := cats.Parts.PartsStatusMainById[p.PartsStatusMainId]; ok {
			pieces = append(pieces, fmt.Sprintf("main %s %s", statKindName(def.StatusKindType),
				statValueString(def.StatusCalculationType, def.StatusKindType, p.PartsStatusMainValue)))
			applyStatBonus(&flat, &mult, def.StatusKindType, def.StatusCalculationType, p.PartsStatusMainValue)
		}
		for key, sub := range user.PartsStatusSubs {
			if key.UserPartsUuid != pu {
				continue
			}
			pieces = append(pieces, fmt.Sprintf("sub %s %s", statKindName(sub.StatusKindType),
				statValueString(sub.StatusCalculationType, sub.StatusKindType, sub.StatusChangeValue)))
			applyStatBonus(&flat, &mult, sub.StatusKindType, sub.StatusCalculationType, sub.StatusChangeValue)
		}
		log.Printf("[BattleDebug]   Parts %s lv=%d: %s", c.n("parts", p.PartsId), p.Level, strings.Join(pieces, ", "))

		if pm, ok := cats.Parts.PartsById[p.PartsId]; ok {
			if g, ok := c.partsGroups[pm.PartsGroupId]; ok {
				seriesCount[g.PartsSeriesId]++
			}
		}
	}
	for seriesId, count := range seriesCount {
		series, ok := c.partsSeries[seriesId]
		if !ok {
			continue
		}
		for _, row := range c.partsSeriesAbis[series.PartsSeriesBonusAbilityGroupId] {
			if row.SetCount <= count {
				log.Printf("[BattleDebug]   Parts set bonus (series %d, %d pcs): %s lv%d",
					seriesId, count, c.n("abilities", row.AbilityId), row.AbilityLevel)
			}
		}
	}

	// --- itemized stat bonuses --------------------------------------------------
	logBonus := func(source string, calcType int32, b statBlock) {
		if b.isZero() {
			return
		}
		switch model.StatusCalculationType(calcType) {
		case model.StatusCalculationTypeMultiply:
			mult.add(b)
			log.Printf("[BattleDebug]   Bonus [%s] multiply: %s", source, b.deltaString(true))
		default:
			flat.add(b)
			log.Printf("[BattleDebug]   Bonus [%s] flat: %s", source, b.deltaString(false))
		}
	}
	for key, up := range user.CharacterBoardStatusUps {
		if key.CharacterId == cm.CharacterId {
			logBonus("character board", up.StatusCalculationType,
				statBlock{int64(up.Hp), int64(up.Attack), int64(up.Vitality), int64(up.Agility), int64(up.CriticalRatio), int64(up.CriticalAttack)})
		}
	}
	for key, up := range user.CostumeAwakenStatusUps {
		if key.UserCostumeUuid == dc.UserCostumeUuid {
			logBonus("awaken", int32(up.StatusCalculationType),
				statBlock{int64(up.Hp), int64(up.Attack), int64(up.Vitality), int64(up.Agility), int64(up.CriticalRatio), int64(up.CriticalAttack)})
		}
	}
	for key, up := range user.CharacterCostumeLevelBonuses {
		if key.CharacterId == cm.CharacterId {
			logBonus("costume level bonus", int32(up.StatusCalculationType),
				statBlock{int64(up.Hp), int64(up.Attack), int64(up.Vitality), int64(up.Agility), int64(up.CriticalRatio), int64(up.CriticalAttack)})
		}
	}
	for key, up := range user.CostumeLotteryEffectStatusUps {
		if key.UserCostumeUuid == dc.UserCostumeUuid {
			logBonus("karma", up.StatusCalculationType,
				statBlock{int64(up.Hp), int64(up.Attack), int64(up.Vitality), int64(up.Agility), int64(up.CriticalRatio), int64(up.CriticalAttack)})
		}
	}
	// Stained glass ("remnant") permanent bonuses from owned important items.
	for itemId, count := range user.ImportantItems {
		if count <= 0 {
			continue
		}
		glass, ok := c.stainedGlassByItemId[itemId]
		if !ok {
			continue
		}
		matched := false
		for _, t := range c.stainedGlassTargets[glass.StainedGlassStatusUpTargetGroupId] {
			switch t.StatusUpTargetType {
			case stainedGlassTargetTypeCharacter:
				matched = t.TargetValue == cm.CharacterId
			case stainedGlassTargetTypeSkillfulWeaponType:
				matched = t.TargetValue == cm.SkillfulWeaponType
			}
			if matched {
				break
			}
		}
		if !matched {
			continue
		}
		// One glass may mix calculation types; group the rows before logging.
		byCalc := map[int32]statBlock{}
		for _, s := range c.stainedGlassStatuses[glass.StainedGlassStatusUpGroupId] {
			b := byCalc[s.StatusCalculationType]
			b.addByKind(s.StatusKindType, int64(s.EffectValue))
			byCalc[s.StatusCalculationType] = b
		}
		for calcType, b := range byCalc {
			logBonus(fmt.Sprintf("remnant %d", glass.StainedGlassId), calcType, b)
		}
	}

	// --- estimated totals ----------------------------------------------------------
	total.add(flat)
	est := statBlock{
		Hp:         total.Hp * (1000 + mult.Hp) / 1000,
		Attack:     total.Attack * (1000 + mult.Attack) / 1000,
		Vitality:   total.Vitality * (1000 + mult.Vitality) / 1000,
		Agility:    total.Agility * (1000 + mult.Agility) / 1000,
		CritRatio:  total.CritRatio + mult.CritRatio,
		CritAttack: total.CritAttack + mult.CritAttack,
	}
	log.Printf("[BattleDebug]   TOTAL (estimate, incl. quest bonus and remnants, without other ability effects): %s", est.String())
}

// applyStatBonus folds a parts stat into the flat/mult accumulators.
func applyStatBonus(flat, mult *statBlock, kind, calcType, value int32) {
	if model.StatusCalculationType(calcType) == model.StatusCalculationTypeMultiply {
		mult.addByKind(kind, int64(value))
	} else {
		flat.addByKind(kind, int64(value))
	}
}

func statKindName(kind int32) string {
	switch model.StatusKindType(kind) {
	case model.StatusKindTypeAgility:
		return "AGI"
	case model.StatusKindTypeAttack:
		return "ATK"
	case model.StatusKindTypeCriticalAttack:
		return "CritDmg"
	case model.StatusKindTypeCriticalRatio:
		return "CritRate"
	case model.StatusKindTypeEvasionRatio:
		return "Evasion"
	case model.StatusKindTypeHp:
		return "HP"
	case model.StatusKindTypeVitality:
		return "DEF"
	default:
		return fmt.Sprintf("kind%d", kind)
	}
}

// statValueString renders a raw stat value: multiply values and crit/evasion
// kinds are permil, everything else is a flat point value.
func statValueString(calcType, kind, value int32) string {
	isPermil := model.StatusCalculationType(calcType) == model.StatusCalculationTypeMultiply ||
		model.StatusKindType(kind) == model.StatusKindTypeCriticalRatio ||
		model.StatusKindType(kind) == model.StatusKindTypeCriticalAttack ||
		model.StatusKindType(kind) == model.StatusKindTypeEvasionRatio
	if isPermil {
		return fmt.Sprintf("%+.1f%%", float64(value)/10)
	}
	return fmt.Sprintf("%+d", value)
}
