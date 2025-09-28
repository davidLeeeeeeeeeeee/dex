package db

import (
	"fmt"
	"github.com/dgraph-io/badger/v4"
	"google.golang.org/protobuf/proto"
)

// SavePendingAnyTx 改为成员函数
func (mgr *Manager) SavePendingAnyTx(tx *AnyTx) error {
	txID := tx.GetTxId()
	if txID == "" {
		return fmt.Errorf("SavePendingAnyTx: empty txID")
	}
	data, err := ProtoMarshal(tx)
	if err != nil {
		return err
	}
	key := "pending_anytx_" + txID
	mgr.EnqueueSet(key, string(data))
	return nil
}

// DeletePendingAnyTx 改为成员函数
func (mgr *Manager) DeletePendingAnyTx(txID string) error {
	if txID == "" {
		return fmt.Errorf("DeletePendingAnyTx: empty txID")
	}
	key := "pending_anytx_" + txID
	mgr.EnqueueDelete(key)
	return nil
}

// LoadPendingAnyTx 改为成员函数
func (mgr *Manager) LoadPendingAnyTx() ([]*AnyTx, error) {
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
