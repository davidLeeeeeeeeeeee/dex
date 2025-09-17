package db

import (
	"fmt"
	"github.com/dgraph-io/badger/v4"
	"google.golang.org/protobuf/proto"
)

// db/mange_pending_anytx.go
func SavePendingAnyTx(mgr *Manager, tx *AnyTx) error {
	txID := tx.GetTxId()
	if txID == "" {
		return fmt.Errorf("SavePendingAnyTx: empty txID")
	}
	data, err := ProtoMarshal(tx)
	if err != nil {
		return err
	}
	key := "pending_anytx_" + txID
	// 直接调用 manager.EnqueueSet
	mgr.EnqueueSet(key, string(data))
	return nil
}

func DeletePendingAnyTx(mgr *Manager, txID string) error {
	if txID == "" {
		return fmt.Errorf("DeletePendingAnyTx: empty txID")
	}
	key := "pending_anytx_" + txID
	mgr.EnqueueDelete(key)
	return nil
}

func LoadPendingAnyTx(mgr *Manager) ([]*AnyTx, error) {
	mgr.mu.RLock()
	db := mgr.Db
	mgr.mu.RUnlock()
	if db == nil {
		return nil, fmt.Errorf("database is not initialized or closed")
	}
	var result []*AnyTx

	err := db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()

		prefix := []byte("pending_anytx_")
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			val, e := item.ValueCopy(nil)
			if e != nil {
				return e
			}
			var anyTx AnyTx
			if uerr := proto.Unmarshal(val, &anyTx); uerr != nil {
				continue
			}
			result = append(result, &anyTx)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
