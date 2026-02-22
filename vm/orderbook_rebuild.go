package vm

import (
	iface "dex/interfaces"
	"dex/keys"
	"dex/matching"
	"dex/pb"
	"sort"
	"strings"
	"time"
)

const orderBookRebuildSideLimit = 500

var (
	orderPriceMinKey67 = strings.Repeat("0", 67)
	orderPriceMaxKey67 = strings.Repeat("9", 67)
)

type pairOrderPriceBounds struct {
	hasBuy          bool
	maxBuyPriceKey  string
	hasSell         bool
	minSellPriceKey string
}

type orderPriceRangeScanner interface {
	ScanOrderPriceIndexRange(
		pair string,
		side pb.OrderSide,
		isFilled bool,
		minPriceKey67 string,
		maxPriceKey67 string,
		limit int,
		reverse bool,
	) (map[string][]byte, error)
}

type orderPriceRangeScannerOrdered interface {
	ScanOrderPriceIndexRangeOrdered(
		pair string,
		side pb.OrderSide,
		isFilled bool,
		minPriceKey67 string,
		maxPriceKey67 string,
		limit int,
		reverse bool,
	) ([]iface.OrderIndexEntry, error)
}

type kvBatchGetter interface {
	GetKVs(keys []string) (map[string][]byte, error)
}

type indexedOrderCandidate struct {
	orderID   string
	indexData []byte
}

type orderBookRebuildStats struct {
	pairs           int
	candidates      int
	stateKeys       int
	stateHits       int
	loadedFromState int
	loadedLegacy    int
	durScan         time.Duration
	durStateLoad    time.Duration
	durLoadBook     time.Duration
}

// rebuildOrderBooksForPairs 为给定的交易对重建内存订单簿
func (x *Executor) rebuildOrderBooksForPairs(
	pairs []string,
	pairBounds map[string]pairOrderPriceBounds,
	collectStats bool,
) (map[string]*matching.OrderBook, *orderBookRebuildStats, error) {
	var stats *orderBookRebuildStats
	if collectStats {
		stats = &orderBookRebuildStats{pairs: len(pairs)}
	}
	if len(pairs) == 0 {
		return make(map[string]*matching.OrderBook), stats, nil
	}

	pairBooks := make(map[string]*matching.OrderBook, len(pairs))

	for _, pair := range pairs {
		ob := matching.NewOrderBookWithSink(nil)
		bounds := pairBounds[pair]
		candidates := make([]indexedOrderCandidate, 0, orderBookRebuildSideLimit*2)

		// 2. 加载该交易对的活跃限价单 (未完全成交)
		// 2. 分别加载买盘和卖盘的未成交订单 (is_filled:false)
		if !bounds.hasBuy && !bounds.hasSell {
			if stats != nil {
				scanAt := time.Now()
				if buyOrders, err := x.scanOrderIndexesForSide(
					pair, pb.OrderSide_BUY, "", "", orderBookRebuildSideLimit, true,
				); err == nil {
					candidates = append(candidates, buyOrders...)
				}
				stats.durScan += time.Since(scanAt)

				scanAt = time.Now()
				if sellOrders, err := x.scanOrderIndexesForSide(
					pair, pb.OrderSide_SELL, "", "", orderBookRebuildSideLimit, false,
				); err == nil {
					candidates = append(candidates, sellOrders...)
				}
				stats.durScan += time.Since(scanAt)
			} else {
				if buyOrders, err := x.scanOrderIndexesForSide(
					pair, pb.OrderSide_BUY, "", "", orderBookRebuildSideLimit, true,
				); err == nil {
					candidates = append(candidates, buyOrders...)
				}
				if sellOrders, err := x.scanOrderIndexesForSide(
					pair, pb.OrderSide_SELL, "", "", orderBookRebuildSideLimit, false,
				); err == nil {
					candidates = append(candidates, sellOrders...)
				}
			}
		} else {
			if bounds.hasSell {
				if stats != nil {
					scanAt := time.Now()
					if buyOrders, err := x.scanOrderIndexesForSide(
						pair, pb.OrderSide_BUY, bounds.minSellPriceKey, "", orderBookRebuildSideLimit, true,
					); err == nil {
						candidates = append(candidates, buyOrders...)
					}
					stats.durScan += time.Since(scanAt)
				} else {
					if buyOrders, err := x.scanOrderIndexesForSide(
						pair, pb.OrderSide_BUY, bounds.minSellPriceKey, "", orderBookRebuildSideLimit, true,
					); err == nil {
						candidates = append(candidates, buyOrders...)
					}
				}
			}
			if bounds.hasBuy {
				if stats != nil {
					scanAt := time.Now()
					if sellOrders, err := x.scanOrderIndexesForSide(
						pair, pb.OrderSide_SELL, "", bounds.maxBuyPriceKey, orderBookRebuildSideLimit, false,
					); err == nil {
						candidates = append(candidates, sellOrders...)
					}
					stats.durScan += time.Since(scanAt)
				} else {
					if sellOrders, err := x.scanOrderIndexesForSide(
						pair, pb.OrderSide_SELL, "", bounds.maxBuyPriceKey, orderBookRebuildSideLimit, false,
					); err == nil {
						candidates = append(candidates, sellOrders...)
					}
				}
			}
		}

		if stats != nil {
			stats.candidates += len(candidates)
		}

		var (
			stateByOrderID map[string][]byte
			stateKeys      int
		)
		if stats != nil {
			stateLoadAt := time.Now()
			stateByOrderID, stateKeys = x.batchLoadOrderStates(candidates)
			stats.durStateLoad += time.Since(stateLoadAt)
			stats.stateKeys += stateKeys
			stats.stateHits += len(stateByOrderID)
		} else {
			stateByOrderID, _ = x.batchLoadOrderStates(candidates)
		}

		if stats != nil {
			loadBookAt := time.Now()
			for _, candidate := range candidates {
				if x.loadOrderToBook(candidate.orderID, candidate.indexData, stateByOrderID[candidate.orderID], ob) {
					stats.loadedFromState++
				} else {
					stats.loadedLegacy++
				}
			}
			stats.durLoadBook += time.Since(loadBookAt)
		} else {
			for _, candidate := range candidates {
				x.loadOrderToBook(candidate.orderID, candidate.indexData, stateByOrderID[candidate.orderID], ob)
			}
		}
		pairBooks[pair] = ob
	}

	return pairBooks, stats, nil
}

// scanOrderIndexesForSide scans order-price index keys for a side, optionally by price range.
func (x *Executor) scanOrderIndexesForSide(
	pair string,
	side pb.OrderSide,
	minPriceKey67 string,
	maxPriceKey67 string,
	limit int,
	reverse bool,
) ([]indexedOrderCandidate, error) {
	minKey := minPriceKey67
	maxKey := maxPriceKey67
	if minKey == "" {
		minKey = orderPriceMinKey67
	}
	if maxKey == "" {
		maxKey = orderPriceMaxKey67
	}

	if orderedScanner, ok := x.DB.(orderPriceRangeScannerOrdered); ok {
		entries, err := orderedScanner.ScanOrderPriceIndexRangeOrdered(
			pair, side, false, minKey, maxKey, limit, reverse,
		)
		if err != nil {
			return nil, err
		}
		candidates := make([]indexedOrderCandidate, 0, len(entries))
		for _, entry := range entries {
			if entry.OrderID == "" {
				continue
			}
			candidates = append(candidates, indexedOrderCandidate{
				orderID:   entry.OrderID,
				indexData: entry.IndexData,
			})
		}
		return candidates, nil
	}

	// 兼容旧接口：仍允许下游返回 map，兜底做确定性排序。
	if rangeScanner, ok := x.DB.(orderPriceRangeScanner); ok {
		indexOrders, err := rangeScanner.ScanOrderPriceIndexRange(pair, side, false, minKey, maxKey, limit, reverse)
		if err != nil {
			return nil, err
		}
		return appendOrderCandidates(make([]indexedOrderCandidate, 0, len(indexOrders)), indexOrders), nil
	}
	prefix := keys.KeyOrderPriceIndexPrefix(pair, side, false)
	var indexOrders map[string][]byte
	var err error
	if reverse {
		indexOrders, err = x.DB.ScanKVWithLimitReverse(prefix, limit)
	} else {
		indexOrders, err = x.DB.ScanKVWithLimit(prefix, limit)
	}
	if err != nil {
		return nil, err
	}
	return appendOrderCandidates(make([]indexedOrderCandidate, 0, len(indexOrders)), indexOrders), nil
}

func appendOrderCandidates(
	dst []indexedOrderCandidate,
	indexOrders map[string][]byte,
) []indexedOrderCandidate {
	keysInOrder := make([]string, 0, len(indexOrders))
	for k := range indexOrders {
		keysInOrder = append(keysInOrder, k)
	}
	sort.Strings(keysInOrder)
	for _, indexKey := range keysInOrder {
		orderID := extractOrderIDFromIndexKey(indexKey)
		if orderID == "" {
			continue
		}
		dst = append(dst, indexedOrderCandidate{
			orderID:   orderID,
			indexData: indexOrders[indexKey],
		})
	}
	return dst
}

func (x *Executor) batchLoadOrderStates(candidates []indexedOrderCandidate) (map[string][]byte, int) {
	stateByOrderID := make(map[string][]byte, len(candidates))
	if len(candidates) == 0 {
		return stateByOrderID, 0
	}
	stateKeys := make([]string, 0, len(candidates))
	keyToOrderID := make(map[string]string, len(candidates))
	seenOrderID := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if _, ok := seenOrderID[candidate.orderID]; ok {
			continue
		}
		seenOrderID[candidate.orderID] = struct{}{}
		stateKey := keys.KeyOrderState(candidate.orderID)
		stateKeys = append(stateKeys, stateKey)
		keyToOrderID[stateKey] = candidate.orderID
	}
	if batchGetter, ok := x.DB.(kvBatchGetter); ok {
		if kvs, err := batchGetter.GetKVs(stateKeys); err == nil {
			for key, val := range kvs {
				if len(val) == 0 {
					continue
				}
				if orderID, exists := keyToOrderID[key]; exists {
					stateByOrderID[orderID] = val
				}
			}
			return stateByOrderID, len(stateKeys)
		}
	}
	for _, stateKey := range stateKeys {
		orderID := keyToOrderID[stateKey]
		orderStateData, err := x.DB.Get(stateKey)
		if err == nil && len(orderStateData) > 0 {
			stateByOrderID[orderID] = orderStateData
		}
	}
	return stateByOrderID, len(stateKeys)
}

func (x *Executor) loadOrderToBook(orderID string, indexData []byte, orderStateData []byte, ob *matching.OrderBook) bool {
	if len(orderStateData) > 0 {
		var orderState pb.OrderState
		if err := unmarshalProtoCompat(orderStateData, &orderState); err == nil {
			matchOrder, _ := convertOrderStateToMatchingOrder(&orderState)
			if matchOrder != nil {
				ob.AddOrderWithoutMatch(matchOrder)
				return true
			}
		}
	}
	var orderTx pb.OrderTx
	if err := unmarshalProtoCompat(indexData, &orderTx); err == nil {
		matchOrder, _ := convertToMatchingOrderLegacy(&orderTx)
		if matchOrder != nil {
			ob.AddOrderWithoutMatch(matchOrder)
			return false
		}
	}
	return false
}

func extractOrderIDFromIndexKey(indexKey string) string {
	parts := strings.Split(indexKey, "|order_id:")
	if len(parts) != 2 {
		return ""
	}
	return parts[1]
}
