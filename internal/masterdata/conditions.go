package masterdata

import (
	"fmt"

	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/utils"
)

const defaultGroupIndex = 1

type ConditionResolver struct {
	requiredQuestByCondId map[int32]int32
}

func LoadConditionResolver() (*ConditionResolver, error) {
	conditions, err := utils.ReadTable[EntityMEvaluateCondition]("m_evaluate_condition")
	if err != nil {
		return nil, fmt.Errorf("load evaluate condition table: %w", err)
	}
	valueGroups, err := utils.ReadTable[EntityMEvaluateConditionValueGroup]("m_evaluate_condition_value_group")
	if err != nil {
		return nil, fmt.Errorf("load evaluate condition value group table: %w", err)
	}

	condById := make(map[int32]EntityMEvaluateCondition, len(conditions))
	for _, c := range conditions {
		condById[c.EvaluateConditionId] = c
	}

	type vgKey struct {
		GroupId    int32
		GroupIndex int32
	}
	vgByKey := make(map[vgKey]int64, len(valueGroups))
	for _, vg := range valueGroups {
		vgByKey[vgKey{vg.EvaluateConditionValueGroupId, vg.GroupIndex}] = vg.Value
	}

	resolved := make(map[int32]int32)
	for _, c := range conditions {
		if model.EvaluateConditionFunctionType(c.EvaluateConditionFunctionType) == model.EvaluateConditionFunctionTypeQuestClear &&
			model.EvaluateConditionEvaluateType(c.EvaluateConditionEvaluateType) == model.EvaluateConditionEvaluateTypeIdContain {
			if questId, ok := vgByKey[vgKey{c.EvaluateConditionValueGroupId, defaultGroupIndex}]; ok {
				resolved[c.EvaluateConditionId] = int32(questId)
			}
		}
	}

	return &ConditionResolver{requiredQuestByCondId: resolved}, nil
}

func (r *ConditionResolver) RequiredQuestId(conditionId int32) (int32, bool) {
	qid, ok := r.requiredQuestByCondId[conditionId]
	return qid, ok
}
