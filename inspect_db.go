package main

import (
	"dex/pb"
	"fmt"
	"strings"

	"github.com/dgraph-io/badger/v2"
	"google.golang.org/protobuf/proto"
)

func main() {
	dbPath := "./data/data_node_0"
	opts := badger.DefaultOptions(dbPath).WithLogger(nil)
	store, err := badger.Open(opts)
	if err != nil {
		fmt.Printf("Failed to open DB: %v\n", err)
		return
	}
	defer store.Close()

	pair := "FB_USDT"
	prefix := "v1_pair:" + pair + "|side:0|is_filled:false|"
	fmt.Printf("Scanning prefix: %s\n", prefix)

	err = store.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()

		count := 0
		p := []byte(prefix)
		for it.Seek(p); it.ValidForPrefix(p); it.Next() {
			count++
			if count > 20 {
				break
			}
			item := it.Item()
			key := string(item.Key())
			fmt.Printf("Key: %s\n", key)

			// Try to find OrderID
			const marker = "|order_id:"
			idx := strings.LastIndex(key, marker)
			if idx != -1 {
				orderID := key[idx+len(marker):]
				// Get OrderState
				stateKey := "v1_orderstate_" + orderID
				val, err := txn.Get([]byte(stateKey))
				if err == nil {
					var state pb.OrderState
					data, _ := val.ValueCopy(nil)
					proto.Unmarshal(data, &state)
					fmt.Printf("  -> OrderState: Price=%s, Side=%v, Amount=%s\n", state.Price, state.Side, state.Amount)
				}
			}
		}
		fmt.Printf("Total found in prefix: %d (stopped at 20)\n", count)
		return nil
	})
	if err != nil {
		fmt.Printf("Error during scan: %v\n", err)
	}
}
