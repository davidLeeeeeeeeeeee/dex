package db

import (
	"bytes"
	"dex/interfaces"
	"dex/keys"
	"dex/pb"
	"dex/utils"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"
)

func extractPriceKeyFromIndexKey(indexKey string) (string, bool) {
	priceMarker := "|price:"
	orderIDMarker := "|order_id:"
	priceStart := strings.Index(indexKey, priceMarker)
	if priceStart < 0 {
		return "", false
	}
	priceStart += len(priceMarker)
	rest := indexKey[priceStart:]
	priceEndOffset := strings.Index(rest, orderIDMarker)
	if priceEndOffset < 0 {
		return "", false
	}
	return rest[:priceEndOffset], true
}

func extractOrderIDFromIndexKeyBytes(indexKey []byte) (string, bool) {
	const marker = "|order_id:"
	idx := bytes.Index(indexKey, []byte(marker))
	if idx < 0 {
		return "", false
	}
	start := idx + len(marker)
	if start >= len(indexKey) {
		return "", false
	}
	return string(indexKey[start:]), true
}

func (manager *Manager) ScanOrderPriceIndexRange(
	pair string, side pb.OrderSide, isFilled bool,
	minPriceKey67 string, maxPriceKey67 string, limit int, reverse bool,
) (map[string][]byte, error) {
	result := make(map[string][]byte)
	if minPriceKey67 == "" || maxPriceKey67 == "" || minPriceKey67 > maxPriceKey67 {
		return result, nil
	}

	prefix := keys.KeyOrderPriceIndexPrefix(pair, side, isFilled)
	prefixBytes := []byte(prefix)
	lowSeek := []byte(fmt.Sprintf("%sprice:%s|order_id:", prefix, minPriceKey67))
	highSeek := []byte(fmt.Sprintf("%sprice:%s|order_id:%c", prefix, maxPriceKey67, 0xFF))

	iter, err := manager.Db.NewIter(&pebble.IterOptions{LowerBound: prefixBytes, UpperBound: prefixUpperBound(prefixBytes)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	count := 0
	if reverse {
		for iter.SeekLT(highSeek); iter.Valid() && bytes.HasPrefix(iter.Key(), prefixBytes); iter.Prev() {
			if limit > 0 && count >= limit {
				break
			}
			k := string(iter.Key())
			priceKey, ok := extractPriceKeyFromIndexKey(k)
			if !ok {
				continue
			}
			if priceKey < minPriceKey67 {
				break
			}
			if priceKey > maxPriceKey67 {
				continue
			}
			v := make([]byte, len(iter.Value()))
			copy(v, iter.Value())
			result[k] = v
			count++
		}
	} else {
		for iter.SeekGE(lowSeek); iter.Valid() && bytes.HasPrefix(iter.Key(), prefixBytes); iter.Next() {
			if limit > 0 && count >= limit {
				break
			}
			k := string(iter.Key())
			priceKey, ok := extractPriceKeyFromIndexKey(k)
			if !ok {
				continue
			}
			if priceKey < minPriceKey67 {
				continue
			}
			if priceKey > maxPriceKey67 {
				break
			}
			v := make([]byte, len(iter.Value()))
			copy(v, iter.Value())
			result[k] = v
			count++
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return result, nil
}

// ScanOrderPriceIndexRangeOrdered 扫描订单价格索引，按底层迭代器顺序返回有序结果。
func (manager *Manager) ScanOrderPriceIndexRangeOrdered(
	pair string, side pb.OrderSide, isFilled bool,
	minPriceKey67 string, maxPriceKey67 string, limit int, reverse bool,
) ([]interfaces.OrderIndexEntry, error) {
	result := make([]interfaces.OrderIndexEntry, 0, limit)
	if minPriceKey67 == "" || maxPriceKey67 == "" || minPriceKey67 > maxPriceKey67 {
		return result, nil
	}

	prefix := keys.KeyOrderPriceIndexPrefix(pair, side, isFilled)
	prefixBytes := []byte(prefix)
	lowSeek := []byte(fmt.Sprintf("%sprice:%s|order_id:", prefix, minPriceKey67))
	highSeek := []byte(fmt.Sprintf("%sprice:%s|order_id:%c", prefix, maxPriceKey67, 0xFF))

	iter, err := manager.Db.NewIter(&pebble.IterOptions{LowerBound: prefixBytes, UpperBound: prefixUpperBound(prefixBytes)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	count := 0
	if reverse {
		for iter.SeekLT(highSeek); iter.Valid() && bytes.HasPrefix(iter.Key(), prefixBytes); iter.Prev() {
			if limit > 0 && count >= limit {
				break
			}
			keyBytes := iter.Key()
			if bytes.Compare(keyBytes, lowSeek) < 0 {
				break
			}
			orderID, ok := extractOrderIDFromIndexKeyBytes(keyBytes)
			if !ok {
				continue
			}
			val := make([]byte, len(iter.Value()))
			copy(val, iter.Value())
			result = append(result, interfaces.OrderIndexEntry{OrderID: orderID, IndexData: val})
			count++
		}
	} else {
		for iter.SeekGE(lowSeek); iter.Valid() && bytes.HasPrefix(iter.Key(), prefixBytes); iter.Next() {
			if limit > 0 && count >= limit {
				break
			}
			keyBytes := iter.Key()
			if bytes.Compare(keyBytes, highSeek) > 0 {
				break
			}
			orderID, ok := extractOrderIDFromIndexKeyBytes(keyBytes)
			if !ok {
				continue
			}
			val := make([]byte, len(iter.Value()))
			copy(val, iter.Value())
			result = append(result, interfaces.OrderIndexEntry{OrderID: orderID, IndexData: val})
			count++
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return result, nil
}

// ScanOrdersByPairs 一次性扫描多个交易对的未成交订单
func (manager *Manager) ScanOrdersByPairs(pairs []string) (map[string]map[string][]byte, error) {
	result := make(map[string]map[string][]byte)
	unfilledMarker := "|is_filled:false|"

	snap := manager.Db.NewSnapshot()
	defer snap.Close()

	for _, pair := range pairs {
		prefix := keys.KeyOrderPriceIndexGeneralPrefix(pair, false)
		p := []byte(prefix)
		pairMap := make(map[string][]byte)
		iter, err := snap.NewIter(&pebble.IterOptions{LowerBound: p, UpperBound: prefixUpperBound(p)})
		if err != nil {
			return nil, err
		}
		for iter.SeekGE(p); iter.Valid(); iter.Next() {
			k := string(iter.Key())
			if !strings.Contains(k, unfilledMarker) {
				continue
			}
			v := make([]byte, len(iter.Value()))
			copy(v, iter.Value())
			pairMap[k] = v
		}
		if err := iter.Error(); err != nil {
			iter.Close()
			return nil, err
		}
		iter.Close()
		result[pair] = pairMap
	}
	return result, nil
}

// ========== 索引重建接口 ==========
func RebuildOrderPriceIndexes(m *Manager) (int, error) {
	prefix := "v1_order_"
	count := 0
	type indexEntry struct {
		key   string
		value string
	}
	var indexes []indexEntry

	p := []byte(prefix)
	iter, err := m.Db.NewIter(&pebble.IterOptions{LowerBound: p, UpperBound: prefixUpperBound(p)})
	if err != nil {
		return 0, err
	}
	for iter.SeekGE(p); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if !strings.HasPrefix(key, prefix) {
			break
		}
		orderData := make([]byte, len(iter.Value()))
		copy(orderData, iter.Value())
		var order pb.OrderTx
		if err := proto.Unmarshal(orderData, &order); err != nil {
			continue
		}
		isFilled := false
		orderStateKey := keys.KeyOrderState(order.Base.TxId)
		if stateData, err := m.Get(orderStateKey); err == nil && len(stateData) > 0 {
			var state pb.OrderState
			if err := proto.Unmarshal(stateData, &state); err == nil {
				isFilled = state.IsFilled
			}
		}
		pair := utils.GeneratePairKey(order.BaseToken, order.QuoteToken)
		priceKey67, err := PriceToKey128(order.Price)
		if err != nil {
			continue
		}
		indexKey := keys.KeyOrderPriceIndex(pair, order.Side, isFilled, priceKey67, order.Base.TxId)
		indexData, _ := proto.Marshal(&pb.OrderPriceIndex{Ok: true})
		indexes = append(indexes, indexEntry{key: indexKey, value: string(indexData)})
		count++
	}
	if err := iter.Error(); err != nil {
		iter.Close()
		return 0, err
	}
	iter.Close()

	for _, idx := range indexes {
		m.EnqueueSet(idx.key, idx.value)
	}
	if err := m.ForceFlush(); err != nil {
		return 0, err
	}
	return count, nil
}
