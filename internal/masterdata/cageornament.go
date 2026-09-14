package masterdata

import (
	"log"
	"lunar-tear/server/internal/utils"
	"math/rand"
)

type CageOrnamentReward struct {
	PossessionType int32
	PossessionId   int32
	Count          int32
}

type CageOrnamentCatalog struct {
	ornamentToRewardId map[int32]int32
	rewards            map[int32]CageOrnamentReward
	rewardPool         []CageOrnamentReward // Pool for random selection (Fickle Black Birds)
}

func (c *CageOrnamentCatalog) LookupReward(cageOrnamentId int32) (CageOrnamentReward, bool) {
	rewardId, ok := c.ornamentToRewardId[cageOrnamentId]
	if !ok || rewardId == 0 {
		return CageOrnamentReward{}, false
	}
	entry, ok := c.rewards[rewardId]
	return entry, ok
}

func (c *CageOrnamentCatalog) RandomReward() CageOrnamentReward {
	if len(c.rewardPool) == 0 {
		return CageOrnamentReward{}
	}
	return c.rewardPool[rand.Intn(len(c.rewardPool))]
}

func LoadCageOrnamentCatalog() *CageOrnamentCatalog {
	ornaments, err := utils.ReadTable[EntityMCageOrnament]("m_cage_ornament")
	if err != nil {
		log.Fatalf("load cage ornament table: %v", err)
	}
	rewards, err := utils.ReadTable[EntityMCageOrnamentReward]("m_cage_ornament_reward")
	if err != nil {
		log.Fatalf("load cage ornament reward table: %v", err)
	}

	cat := &CageOrnamentCatalog{
		ornamentToRewardId: make(map[int32]int32, len(ornaments)),
		rewards:            make(map[int32]CageOrnamentReward, len(rewards)),
		rewardPool:         make([]CageOrnamentReward, 0, len(rewards)),
	}
	for _, o := range ornaments {
		cat.ornamentToRewardId[o.CageOrnamentId] = o.CageOrnamentRewardId
	}
	for _, r := range rewards {
		reward := CageOrnamentReward{
			PossessionType: r.PossessionType,
			PossessionId:   r.PossessionId,
			Count:          r.Count,
		}
		cat.rewards[r.CageOrnamentRewardId] = reward
		cat.rewardPool = append(cat.rewardPool, reward)
	}
	log.Printf("[CageOrnament] Loaded %d rewards for random selection (Fickle Black Birds)", len(cat.rewardPool))
	return cat
}
