package service

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/google/uuid"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/masterdata"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"

	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	DefaultConvertBatchSize int32 = 5000
	SpecialPackBatchSize    int32 = 10000
)

type ShopServiceServer struct {
	pb.UnimplementedShopServiceServer
	users    store.UserRepository
	sessions store.SessionRepository
	holder   *runtime.Holder
}

func NewShopServiceServer(users store.UserRepository, sessions store.SessionRepository, holder *runtime.Holder) *ShopServiceServer {
	return &ShopServiceServer{users: users, sessions: sessions, holder: holder}
}

func (s *ShopServiceServer) Buy(ctx context.Context, req *pb.BuyRequest) (*pb.BuyResponse, error) {
	log.Printf("[ShopService] Buy: shopId=%d items=%v", req.ShopId, req.ShopItems)

	cat := s.holder.Get()
	catalog := cat.Shop
	granter := cat.QuestHandler.Granter
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	// Type-21 missions use option group 2 for "buy in the Item Shop" and 0 for
	// "buy anywhere"; the raw ShopId would never match either, so normalize it.
	missionTargetId := int32(0)
	if catalog.ItemShopIds[req.ShopId] {
		missionTargetId = 2
	}

	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		for shopItemId, qty := range req.ShopItems {
			item, ok := catalog.Items[shopItemId]
			if !ok {
				log.Printf("[ShopService] Buy: unknown shopItemId=%d, skipping", shopItemId)
				continue
			}

			totalPrice := item.Price * qty
			if err := store.DeductPrice(user, item.PriceType, item.PriceId, totalPrice); err != nil {
				log.Printf("[ShopService] Buy: deduct failed shopItemId=%d: %v", shopItemId, err)
				continue
			}

			for _, content := range catalog.Contents[shopItemId] {
				granter.GrantFull(user,
					model.PossessionType(content.PossessionType),
					content.PossessionId,
					content.Count*qty,
					nowMillis,
				)
			}

			applyShopContentEffects(catalog, user, shopItemId, qty, nowMillis)

			si := user.ShopItems[shopItemId]
			si.ShopItemId = shopItemId
			if shopStockAutoResets(catalog, item) {
				// Stock refresh was removed: items whose limited stock used to
				// refill on a daily/weekly/monthly reset are never consumed.
				// Heal any stale counter left over from before the change.
				if si.BoughtCount != 0 {
					si.BoughtCount = 0
					si.LatestVersion = nowMillis
					user.ShopItems[shopItemId] = si
				}
			} else {
				// Items with a hard lifetime limit still count up as before.
				si.BoughtCount += qty
				si.LatestBoughtCountChangedDatetime = nowMillis
				si.LatestVersion = nowMillis
				user.ShopItems[shopItemId] = si
			}
			// Count total quantity purchased instead of unique item types
			ApplyMissionProgressEvent(user, cat.Mission, MissionProgressEvent{ConditionType: missionConditionShopPurchase, Delta: qty, TargetId: missionTargetId}, nowMillis)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("shop buy: %w", err)
	}
	return &pb.BuyResponse{
		OverflowPossession: []*pb.Possession{},
	}, nil
}

func (s *ShopServiceServer) RefreshUserData(ctx context.Context, req *pb.RefreshRequest) (*pb.RefreshResponse, error) {
	log.Printf("[ShopService] RefreshUserData: isGemUsed=%v", req.IsGemUsed)

	catalog := s.holder.Get().Shop
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		if len(user.ShopReplaceableLineup) == 0 && len(catalog.ItemShopPool) > 0 {
			for i, itemId := range catalog.ItemShopPool {
				slot := int32(i + 1)
				user.ShopReplaceableLineup[slot] = store.UserShopReplaceableLineupState{
					SlotNumber:    slot,
					ShopItemId:    itemId,
					LatestVersion: nowMillis,
				}
			}
		}
		if req.IsGemUsed {
			user.ShopReplaceable.LineupUpdateCount++
			user.ShopReplaceable.LatestLineupUpdateDatetime = nowMillis
			for _, itemId := range catalog.ItemShopPool {
				if si, ok := user.ShopItems[itemId]; ok {
					si.BoughtCount = 0
					si.LatestVersion = nowMillis
					user.ShopItems[itemId] = si
				}
			}
		}
	})
	if err != nil {
		return nil, fmt.Errorf("shop refresh: %w", err)
	}

	return &pb.RefreshResponse{}, nil
}

func (s *ShopServiceServer) GetCesaLimit(_ context.Context, _ *emptypb.Empty) (*pb.GetCesaLimitResponse, error) {
	log.Printf("[ShopService] GetCesaLimit")
	return &pb.GetCesaLimitResponse{
		CesaLimit: []*pb.CesaLimit{},
	}, nil
}

func (s *ShopServiceServer) CreatePurchaseTransaction(ctx context.Context, req *pb.CreatePurchaseTransactionRequest) (*pb.CreatePurchaseTransactionResponse, error) {
	log.Printf("[ShopService] CreatePurchaseTransaction: shopId=%d shopItemId=%d productId=%s",
		req.ShopId, req.ShopItemId, req.ProductId)

	cat := s.holder.Get()
	catalog := cat.Shop
	granter := cat.QuestHandler.Granter
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	// Premium Blocked
	/* contents, hasContents := catalog.Contents[req.ShopItemId]
	if hasContents {
		for _, content := range contents {
			if content.PossessionType == 11 { // Paid Gem
				log.Printf("[ShopService] Premium gem pack BLOCKED softly: shopItemId=%d", req.ShopItemId)

				// Convert items to Gems based on ShopItemId
				_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
					switch req.ShopItemId {
					case 140117: // Limited-Time Pack - Anniversary EvePack A
						ConvertPremiumST(user, nowMillis)

					case 140118: // Limited-Time Pack - Anniversary EvePack B
						ConvertStamina(user, nowMillis)
						ConvertDarkMemoriesST(user, nowMillis, SpecialPackBatchSize)

					case 140119: // Limited-Time Pack - Anniversary EvePack C
						ConvertEventST(user, nowMillis, SpecialPackBatchSize)

					case 140124: // Limited-Time Pack - Anniversary EvePack D
						ConvertChapterST(user, nowMillis, SpecialPackBatchSize)

					default: 
						ConvertPremiumST(user, nowMillis)
						ConvertStamina(user, nowMillis)
						ConvertDarkMemoriesST(user, nowMillis, DefaultConvertBatchSize)
						ConvertEventST(user, nowMillis, DefaultConvertBatchSize)
						ConvertChapterST(user, nowMillis, DefaultConvertBatchSize)
					}
				})
				if err != nil {
					return nil, fmt.Errorf("conversion transaction failed: %w", err)
				}

				return &pb.CreatePurchaseTransactionResponse{
					PurchaseTransactionId: "premium_blocked",
				}, nil
			}
		}
	} */

	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		item, ok := catalog.Items[req.ShopItemId]
		if !ok {
			log.Printf("[ShopService] CreatePurchaseTransaction: unknown shopItemId=%d", req.ShopItemId)
			return
		}
		
		contents := catalog.Contents[req.ShopItemId]
		if len(contents) == 0 {
			log.Printf("[ShopService] CreatePurchaseTransaction: shopItemId=%d has missions, purchase rejected", req.ShopItemId)
			return
		}

		if len(contents) == 1 && contents[0].PossessionType == 16 {
			log.Printf("[ShopService] CreatePurchaseTransaction: shopItemId=%d contains only pass, purchase rejected", req.ShopItemId)
			return
		}
		
		hasType12 := false
		hasItem1006 := false
		hasNon11 := false

		for _, content := range contents {
			if content.PossessionType == 12 {
				hasType12 = true
			}
			if content.PossessionId == 1006 {
				hasItem1006 = true
			}
			if content.PossessionType != 11 {
				hasNon11 = true
			}

			if hasType12 || hasItem1006 {
				break
			}
		}
		if hasType12 {
			log.Printf("[ShopService] CreatePurchaseTransaction: shopItemId=%d rejected (contains Free Gems)", req.ShopItemId)
			return
		}
		if hasItem1006 {
			log.Printf("[ShopService] CreatePurchaseTransaction: shopItemId=%d rejected (contains premium ticket)", req.ShopItemId)
			return
		}
		if !hasNon11 {
			log.Printf("[ShopService] CreatePurchaseTransaction: shopItemId=%d rejected (contains only Paid Gems)", req.ShopItemId)
			return
		}
		
		price := item.Price
		if item.PriceType == model.PriceTypePlatformPayment && item.Price == 0 {
			// The shop item's PriceId references m_platform_payment, whose gem
			// price lives in m_platform_payment_price.
			if tablePrice, ok := catalog.PlatformPaymentPrices[item.PriceId]; ok && tablePrice > 0 {
				price = tablePrice
			} else {
				log.Printf("[ShopService] CreatePurchaseTransaction: no platform payment price for shopItemId=%d priceId=%d, using default", req.ShopItemId, item.PriceId)
				price = 10000
			}
		}

		if err := store.DeductPrice(user, item.PriceType, item.PriceId, price); err != nil {
			log.Printf("[ShopService] CreatePurchaseTransaction: Not enough Paid gems: %v", err)
			return
		}

		for _, content := range catalog.Contents[req.ShopItemId] {
			if content.PossessionType == 11 {
				log.Printf("[ShopService] Skipping Paid gems for shopItemId=%d", req.ShopItemId)
				continue
			}

			granter.GrantFull(user,
				model.PossessionType(content.PossessionType),
				content.PossessionId,
				content.Count,
				nowMillis,
			)
		}

		applyShopContentEffects(catalog, user, req.ShopItemId, 1, nowMillis)

		si := user.ShopItems[req.ShopItemId]
		si.ShopItemId = req.ShopItemId
		if shopStockAutoResets(catalog, item) {
			// Stock refresh was removed: refilling limited stock is never
			// consumed; heal any stale counter from before the change.
			if si.BoughtCount != 0 {
				si.BoughtCount = 0
				si.LatestVersion = nowMillis
				user.ShopItems[req.ShopItemId] = si
			}
		} else {
			si.BoughtCount++
			if item.ShopItemLimitedStockId > 0 {
				if maxCount, ok := catalog.LimitedStock[item.ShopItemLimitedStockId]; ok && si.BoughtCount >= maxCount {
					si.BoughtCount = 0
				}
			}
			si.LatestBoughtCountChangedDatetime = nowMillis
			si.LatestVersion = nowMillis
			user.ShopItems[req.ShopItemId] = si
		}
	})
	if err != nil {
		return nil, fmt.Errorf("create purchase transaction: %w", err)
	}

	txId := fmt.Sprintf("tx_%d_%d_%d", userId, req.ShopItemId, nowMillis)

	return &pb.CreatePurchaseTransactionResponse{
		PurchaseTransactionId: txId,
	}, nil
}

// shopStockAutoResets reports whether the item's limited stock used to be
// refilled on a daily/weekly/monthly reset schedule. Shop resets were
// removed, so purchases of such items must not consume their stock
// counters; items without a reset rule (hard lifetime limits) keep
// counting up as before.
func shopStockAutoResets(catalog *masterdata.ShopCatalog, item masterdata.EntityMShopItem) bool {
	if item.ShopItemLimitedStockId <= 0 {
		return false
	}
	rule, ok := catalog.LimitedStockReset[item.ShopItemLimitedStockId]
	if !ok {
		return false
	}
	switch rule.ResetType {
	case model.ShopItemAutoResetDaily, model.ShopItemAutoResetMonthly:
		return true
	default:
		return false
	}
}

func (s *ShopServiceServer) PurchaseGooglePlayStoreProduct(ctx context.Context, req *pb.PurchaseGooglePlayStoreProductRequest) (*pb.PurchaseGooglePlayStoreProductResponse, error) {
	log.Printf("[ShopService] PurchaseGooglePlayStoreProduct: txId=%s", req.PurchaseTransactionId)

	userId := CurrentUserId(ctx, s.users, s.sessions)
	_, err := s.users.LoadUser(userId)
	if err != nil {
		return nil, fmt.Errorf("purchase google play: %w", err)
	}

	return &pb.PurchaseGooglePlayStoreProductResponse{
		OverflowPossession: []*pb.Possession{},
	}, nil
}

func applyShopContentEffects(catalog *masterdata.ShopCatalog, user *store.UserState, shopItemId, qty int32, nowMillis int64) {
	for _, effect := range catalog.Effects[shopItemId] {
		switch effect.EffectTargetType {
		case model.EffectTargetStaminaRecovery:
			maxMillis := catalog.MaxStaminaMillis[user.Status.Level]
			millis := store.ResolveStaminaEffectMillis(effect.EffectValueType, effect.EffectValue, maxMillis)
			store.RecoverStamina(user, millis*qty, maxMillis, nowMillis)
		default:
			log.Printf("[ShopService] unhandled effect: shopItemId=%d targetType=%d", shopItemId, effect.EffectTargetType)
		}
	}
}

func ConvertItemToGems(u *store.UserState, itemId int32, multiplier int32) int32 {
	count, exists := u.ConsumableItems[itemId]
	if !exists || count <= 0 {
		return 0
	}

	gemsToGive := count * multiplier
	delete(u.ConsumableItems, itemId)

	log.Printf("[ShopService] Converted item %d (x%d) → %d gems",
		itemId, count, gemsToGive)

	return gemsToGive
}

func ConvertStamina(u *store.UserState, now int64) {
	totalStamina := int32(0)

	for itemID := int32(3001); itemID <= 3003; itemID++ {
		if count, exists := u.ConsumableItems[itemID]; exists && count > 0 {
			totalStamina += count
		}
	}

	for itemID := int32(3007); itemID <= 3116; itemID++ {
		if count, exists := u.ConsumableItems[itemID]; exists && count > 0 {
			totalStamina += count
		}
	}

	for itemID := int32(93001); itemID <= 93014; itemID++ {
		if count, exists := u.ConsumableItems[itemID]; exists && count > 0 {
			totalStamina += count
		}
	}

	for itemID := int32(93017); itemID <= 93032; itemID++ {
		if count, exists := u.ConsumableItems[itemID]; exists && count > 0 {
			totalStamina += count
		}
	}

	for itemID := int32(4001); itemID <= 4005; itemID++ {
		if count, exists := u.ConsumableItems[itemID]; exists && count > 0 {
			totalStamina += count
		}
	}

	// If total stamina is less than 10, don't convert
	if totalStamina < 10 {
		return
	}

	remainder := totalStamina % 10
	convertibleCount := totalStamina - remainder
	skipTicketsToGive := convertibleCount / 10

	for itemID := int32(3001); itemID <= 3003; itemID++ {
		delete(u.ConsumableItems, itemID)
	}

	for itemID := int32(3007); itemID <= 3116; itemID++ {
		delete(u.ConsumableItems, itemID)
	}

	for itemID := int32(93001); itemID <= 93014; itemID++ {
		delete(u.ConsumableItems, itemID)
	}

	for itemID := int32(93017); itemID <= 93032; itemID++ {
		delete(u.ConsumableItems, itemID)
	}

	for itemID := int32(4001); itemID <= 4005; itemID++ {
		delete(u.ConsumableItems, itemID)
	}

	if remainder > 0 {
		u.ConsumableItems[3001] = remainder
	}

	u.AddGift(store.NotReceivedGiftState{
		GiftCommon: store.GiftCommonState{
			PossessionType: 6,
			PossessionId:   2003,
			Count:          skipTicketsToGive,
			GrantDatetime:  now,
		},
		ExpirationDatetime: now + int64(30*24*time.Hour/time.Millisecond),
		UserGiftUuid:       uuid.New().String(),
	})

	log.Printf("[ShopService] Converted %d stamina → %d skip tickets (remainder: %d)", convertibleCount, skipTicketsToGive, remainder)
}

func ConvertPremiumST(u *store.UserState, now int64) {
	var totalGems int32 = 0
	
	totalGems += ConvertItemToGems(u, 1002, 300)  // Premium Summon Ticket
	totalGems += ConvertItemToGems(u, 1003, 3000) // Premium Summon Ticket x10
	totalGems += ConvertItemToGems(u, 1004, 1500) // Silver Summon Ticket x10
	totalGems += ConvertItemToGems(u, 1005, 1800) // ***+ Premium Summon Ticket
	totalGems += ConvertItemToGems(u, 1006, 6000) // **** Premium Summon Ticket
	
	totalGems += ConvertItemToGems(u, 1201, 3000) // Mama's Choice Summon Ticket
	totalGems += ConvertItemToGems(u, 1202, 3000) // Mama's Choice Summon Ticket

	for itemID := int32(1203); itemID <= 1224; itemID++ {
		totalGems += ConvertItemToGems(u, itemID, 9000) // All Guaranteed Summon Tickets
	}

	if totalGems > 0 {
		u.AddGift(store.NotReceivedGiftState{
			GiftCommon: store.GiftCommonState{
				PossessionType: 12,
				PossessionId:   0,
				Count:          totalGems,
				GrantDatetime:  now,
			},
			ExpirationDatetime: now + int64(30*24*time.Hour/time.Millisecond),
			UserGiftUuid:       uuid.New().String(),
		})
		
		log.Printf("[ShopService] Sent consolidated gift of %d gems", totalGems)
	}
}

func ConvertDarkMemoriesST(u *store.UserState, now int64, maxBatchSize int32) {
	const (
		itemId int32 = 2002
	)

	count, exists := u.ConsumableItems[itemId]
	if !exists || count <= 0 {
		return
	}

	ticketsToConvert := count
	if ticketsToConvert > maxBatchSize {
		ticketsToConvert = maxBatchSize
	}

	type ConversionTier struct {
		coins       int32
		probability int32
	}

	tiers := []ConversionTier{
		{coins: 10, probability: 450}, // 45.0%
		{coins: 20, probability: 300}, // 30.0%
		{coins: 30, probability: 150}, // 15.0%
		{coins: 40, probability: 75},  // 7.5%
		{coins: 50, probability: 20},  // 2.0%
		{coins: 100, probability: 4},  // 0.4%
		{coins: 1000, probability: 1}, // 0.1%
	}

	var totalCoinsGiven int32

	for i := int32(0); i < ticketsToConvert; i++ {
		randVal := int32(rand.Intn(1000))
		var cumulative int32

		for _, tier := range tiers {
			cumulative += tier.probability
			if randVal < cumulative {
				totalCoinsGiven += tier.coins
				break
			}
		}
	}

	u.ConsumableItems[itemId] -= ticketsToConvert
	if u.ConsumableItems[itemId] <= 0 {
		delete(u.ConsumableItems, itemId)
	}

	u.AddGift(store.NotReceivedGiftState{
		GiftCommon: store.GiftCommonState{
			PossessionType: 6,
			PossessionId:   8,
			Count:          totalCoinsGiven,
			GrantDatetime:  now,
		},
		ExpirationDatetime: now + int64(30*24*time.Hour/time.Millisecond),
		UserGiftUuid:       uuid.New().String(),
	})

	log.Printf("[ShopService] Converted %d Dark Memories Summon Tickets → %d Dark Coins sent as gift. Remaining: %d",
		ticketsToConvert, totalCoinsGiven, u.ConsumableItems[itemId])
}

func ConvertChapterST(u *store.UserState, now int64, maxTicketsLimit int32) {
	const (
		minTicketId int32 = 1008
		maxTicketId int32 = 1031
	)

	// Item groups definition[cite: 1]
	groups := [][]int32{
		{330001, 330005, 330009, 330013, 330017, 330021}, // Group 1[cite: 1]
		{330002, 330006, 330010, 330014, 330018, 330022,  // Group 2[cite: 1]
			301001, 301003, 301005, 301007, 301009, 301011},
		{330003, 330007, 330011, 330015, 330019, 330023,  // Group 3[cite: 1]
			301002, 301004, 301006, 301008, 301010, 301012},
		{330004, 330008, 330012, 330016, 330020, 330024}, // Group 4[cite: 1]
		{2001},                                           // Group 5[cite: 1]
	}

	// Base group probabilities for starting (1008) and ending (1031) tickets (out of 1000)
	startProbs := []int32{570, 228, 114, 38, 50} // For ticket 1008
	endProbs := []int32{250, 250, 230, 220, 50}  // For ticket 1031

	var totalConverted int32 = 0
	droppedItems := make(map[int32]int32) // Accumulate total dropped count per itemId

	// Process tickets sequentially from 1008 to 1031
	for ticketId := minTicketId; ticketId <= maxTicketId; ticketId++ {
		if totalConverted >= maxTicketsLimit {
			break // Batch capacity reached
		}

		count := u.ConsumableItems[ticketId]
		if count <= 0 {
			continue
		}

		// Cap ticket processing count based on remaining batch capacity
		toProcess := count
		if totalConverted+toProcess > maxTicketsLimit {
			toProcess = maxTicketsLimit - totalConverted
		}

		// Calculate dynamic group probabilities using linear interpolation
		stepsTotal := maxTicketId - minTicketId
		currentStep := ticketId - minTicketId

		probs := make([]int32, 5)
		var sumProbs int32 = 0
		for g := 0; g < 4; g++ {
			diff := endProbs[g] - startProbs[g]
			// Formula: Start + (End - Start) * (CurrentStep / TotalSteps)
			probs[g] = startProbs[g] + (diff * currentStep / stepsTotal)
			sumProbs += probs[g]
		}
		probs[4] = 1000 - sumProbs // Absorbs rounding errors to ensure exact sum of 1000

		// Process each individual ticket roll
		for i := int32(0); i < toProcess; i++ {
			// Round 1: Select target group based on tier probabilities
			randValGroup := int32(rand.Intn(1000))
			var cumulative int32 = 0
			selectedGroupIdx := -1

			for g, p := range probs {
				cumulative += p
				if randValGroup < cumulative {
					selectedGroupIdx = g
					break
				}
			}

			// Fallback guard against floating rounding issues
			if selectedGroupIdx == -1 {
				selectedGroupIdx = 4
			}

			// Round 2: Select a random item within the group with equal weight
			groupItems := groups[selectedGroupIdx]
			itemIdx := rand.Intn(len(groupItems))
			selectedItem := groupItems[itemIdx]

			// Accumulate result
			droppedItems[selectedItem]++
		}

		// Deduct processed tickets from inventory
		u.ConsumableItems[ticketId] -= toProcess
		if u.ConsumableItems[ticketId] <= 0 {
			delete(u.ConsumableItems, ticketId)
		}

		totalConverted += toProcess
	}

	// Exit early if no tickets were processed
	if totalConverted == 0 {
		return
	}

	const duration30DaysMs = int64(30 * 24 * time.Hour / time.Millisecond)

	// Send accumulated items via gift mail
	for itemId, itemCount := range droppedItems {
		possessionType := int32(5) // Default material item type[cite: 1]
		if itemId == 2001 {
			possessionType = 6 // Consumable ticket type[cite: 1]
		}

		u.AddGift(store.NotReceivedGiftState{
			GiftCommon: store.GiftCommonState{
				PossessionType: possessionType,
				PossessionId:   itemId,
				Count:          itemCount,
				GrantDatetime:  now,
			},
			ExpirationDatetime: now + duration30DaysMs,
			UserGiftUuid:       uuid.New().String(),
		})
	}

	log.Printf("[ShopService] Converted %d Chapter Tickets into %d unique reward types",
		totalConverted, len(droppedItems))
}

// HasItemByPossessionType checks if the user already owns a specific item by its possession type and ID.
func HasItemByPossessionType(u *store.UserState, possessionType int32, itemId int32) bool {
	switch possessionType {
	case 2: // Weapon
		for _, weapon := range u.Weapons {
			if weapon.WeaponId == itemId {
				return true
			}
		}
	case 3: // Companion
		for _, companion := range u.Companions {
			if companion.CompanionId == itemId {
				return true
			}
		}
	}
	return false
}

// RewardItem is a helper struct to store item data cleanly.
type RewardItem struct {
	Id             int32
	PossessionType int32
	Count          int32
}

// AccKey is used as a map key to group accumulated items by ID and Type.
type AccKey struct {
	ItemId         int32
	PossessionType int32
}

func ConvertEventST(u *store.UserState, now int64, maxTicketsLimit int32) {

	// Generate target ticket IDs: 1007 and 6001-6072
	ticketIDs := []int32{1007}
	for id := int32(6001); id <= 6072; id++ {
		ticketIDs = append(ticketIDs, id)
	}

	// ---------------------------------------------------------
	// Define Item Groups[cite: 2]
	// ---------------------------------------------------------
	itemGroup4 := []RewardItem{
		{Id: 301001, PossessionType: 5, Count: 1}, {Id: 301003, PossessionType: 5, Count: 1},
		{Id: 301005, PossessionType: 5, Count: 1}, {Id: 301007, PossessionType: 5, Count: 1},
		{Id: 301009, PossessionType: 5, Count: 1}, {Id: 301011, PossessionType: 5, Count: 1},
	}
	itemGroup5 := []RewardItem{
		{Id: 301002, PossessionType: 5, Count: 1}, {Id: 301004, PossessionType: 5, Count: 1},
		{Id: 301006, PossessionType: 5, Count: 1}, {Id: 301008, PossessionType: 5, Count: 1},
		{Id: 301010, PossessionType: 5, Count: 1}, {Id: 301012, PossessionType: 5, Count: 1},
	}

	// Master list for Group 6 (Weapons and Companions)[cite: 2]
	group6Master := []RewardItem{
		// Weapons
		{Id: 340031, PossessionType: 2, Count: 1}, {Id: 250141, PossessionType: 2, Count: 1},
		{Id: 310131, PossessionType: 2, Count: 1}, {Id: 220071, PossessionType: 2, Count: 1},
		{Id: 330061, PossessionType: 2, Count: 1}, {Id: 210021, PossessionType: 2, Count: 1},
		{Id: 320071, PossessionType: 2, Count: 1}, {Id: 230121, PossessionType: 2, Count: 1},
		{Id: 340271, PossessionType: 2, Count: 1}, {Id: 250181, PossessionType: 2, Count: 1},
		{Id: 320161, PossessionType: 2, Count: 1}, {Id: 230171, PossessionType: 2, Count: 1},
		{Id: 350211, PossessionType: 2, Count: 1}, {Id: 310281, PossessionType: 2, Count: 1},
		{Id: 320221, PossessionType: 2, Count: 1}, {Id: 340371, PossessionType: 2, Count: 1},
		{Id: 330301, PossessionType: 2, Count: 1}, {Id: 350291, PossessionType: 2, Count: 1},
		{Id: 310361, PossessionType: 2, Count: 1}, {Id: 330371, PossessionType: 2, Count: 1},
		{Id: 310391, PossessionType: 2, Count: 1}, {Id: 350351, PossessionType: 2, Count: 1},
		{Id: 320361, PossessionType: 2, Count: 1}, {Id: 310491, PossessionType: 2, Count: 1},
		{Id: 340561, PossessionType: 2, Count: 1}, {Id: 310501, PossessionType: 2, Count: 1},
		{Id: 330511, PossessionType: 2, Count: 1}, {Id: 340641, PossessionType: 2, Count: 1},
		{Id: 350541, PossessionType: 2, Count: 1}, {Id: 320491, PossessionType: 2, Count: 1},
		{Id: 310581, PossessionType: 2, Count: 1}, {Id: 350611, PossessionType: 2, Count: 1},
		{Id: 310631, PossessionType: 2, Count: 1}, {Id: 350651, PossessionType: 2, Count: 1},
		{Id: 340791, PossessionType: 2, Count: 1}, {Id: 320591, PossessionType: 2, Count: 1},
		{Id: 330681, PossessionType: 2, Count: 1}, {Id: 320631, PossessionType: 2, Count: 1},
		{Id: 330731, PossessionType: 2, Count: 1}, {Id: 320021, PossessionType: 2, Count: 1},
		{Id: 330311, PossessionType: 2, Count: 1},
		// Companions
		{Id: 46, PossessionType: 3, Count: 1}, {Id: 47, PossessionType: 3, Count: 1},
	}

	itemGroup10 := []RewardItem{
		{Id: 312021, PossessionType: 5, Count: 1}, {Id: 312022, PossessionType: 5, Count: 1},
		{Id: 312023, PossessionType: 5, Count: 1}, {Id: 312024, PossessionType: 5, Count: 1},
		{Id: 312025, PossessionType: 5, Count: 1}, {Id: 312026, PossessionType: 5, Count: 1},
	}

	// Filter Group 6 items to include ONLY items the player does NOT currently own
	var availableGroup6 []RewardItem
	for _, item := range group6Master {
		if !HasItemByPossessionType(u, item.PossessionType, item.Id) {
			availableGroup6 = append(availableGroup6, item)
		}
	}

	// Drop weights for Groups 1-9 (1 decimal precision logic)
	// Percentages multiplied by 10 to use integers. Total weight sum is 1000 (representing 100.0%)
	weights := []int32{
		330, // Group 1: 33.0%
		264, // Group 2: 26.4%
		150, // Group 3: 15.0%
		130, // Group 4: 13.0%
		70,  // Group 5: 7.0%
		50,  // Group 6: 5.0%
		3,   // Group 7: 0.3%
		2,   // Group 8: 0.2%
		1,   // Group 9: 0.1%
	}
	const totalWeight int32 = 1000

	var totalConverted int32 = 0
	accumulatedItems := make(map[AccKey]int32)

	// Process all valid ticket IDs
	for _, ticketId := range ticketIDs {
		if totalConverted >= maxTicketsLimit {
			break // Global batch capacity reached
		}

		count := u.ConsumableItems[ticketId]
		if count <= 0 {
			continue
		}

		// Calculate how many of this specific ticket we can process
		toProcess := count
		if totalConverted+toProcess > maxTicketsLimit {
			toProcess = maxTicketsLimit - totalConverted
		}

		// Roll for each processed ticket
		for i := int32(0); i < toProcess; i++ {
			randGroup := int32(rand.Intn(int(totalWeight))) // 0 to 999
			var cumulative int32 = 0
			selectedGroupIdx := -1

			// Round 1: Group Selection based on assigned weights
			for g, weight := range weights {
				cumulative += weight
				if randGroup < cumulative {
					selectedGroupIdx = g + 1 // Convert array index (0-8) to Group Number (1-9)
					break
				}
			}

			// Fallback guard
			if selectedGroupIdx == -1 {
				selectedGroupIdx = 1
			}

			// Round 2: Item Selection based on chosen group[cite: 2]
			var selectedItem RewardItem

			switch selectedGroupIdx {
			case 1:
				selectedItem = RewardItem{Id: 401001, PossessionType: 5, Count: 10}
			case 2:
				selectedItem = RewardItem{Id: 401002, PossessionType: 5, Count: 5}
			case 3:
				selectedItem = RewardItem{Id: 401003, PossessionType: 5, Count: 1}
			case 4:
				selectedItem = itemGroup4[rand.Intn(len(itemGroup4))]
			case 5:
				selectedItem = itemGroup5[rand.Intn(len(itemGroup5))]
			case 6:
				if len(availableGroup6) > 0 {
					// Pick an item from the filtered unowned pool
					idx := rand.Intn(len(availableGroup6))
					selectedItem = availableGroup6[idx]

					// Remove the picked item from the pool to avoid duplicate drops in the same batch
					availableGroup6 = append(availableGroup6[:idx], availableGroup6[idx+1:]...)
				} else {
					// Player owns all Group 6 items, fallback to Group 10[cite: 2]
					selectedItem = itemGroup10[rand.Intn(len(itemGroup10))]
				}
			case 7:
				selectedItem = RewardItem{Id: 325001, PossessionType: 5, Count: 1}
			case 8:
				selectedItem = RewardItem{Id: 315002, PossessionType: 5, Count: 1}
			case 9:
				selectedItem = RewardItem{Id: 314001, PossessionType: 5, Count: 1}
			}

			// Accumulate rolled item
			key := AccKey{ItemId: selectedItem.Id, PossessionType: selectedItem.PossessionType}
			accumulatedItems[key] += selectedItem.Count
		}

		// Deduct processed tickets from inventory
		u.ConsumableItems[ticketId] -= toProcess
		if u.ConsumableItems[ticketId] <= 0 {
			delete(u.ConsumableItems, ticketId)
		}

		totalConverted += toProcess
	}

	// Exit early if no tickets were processed
	if totalConverted == 0 {
		return
	}

	const duration30DaysMs = int64(30 * 24 * time.Hour / time.Millisecond)

	// Dispatch accumulated rewards as gift mails
	for key, count := range accumulatedItems {
		u.AddGift(store.NotReceivedGiftState{
			GiftCommon: store.GiftCommonState{
				PossessionType: key.PossessionType,
				PossessionId:   key.ItemId,
				Count:          count,
				GrantDatetime:  now,
			},
			ExpirationDatetime: now + duration30DaysMs,
			UserGiftUuid:       uuid.New().String(),
		})
	}

	log.Printf("[ShopService] Converted %d Event Tickets into %d unique reward types",
		totalConverted, len(accumulatedItems))
}
