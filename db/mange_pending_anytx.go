package db

import (
	"dex/pb"
	"fmt"

	"github.com/dgraph-io/badger/v4"
	"google.golang.org/protobuf/proto"
)

// SavePendingAnyTx saves a pending AnyTx to the database
//
// ⚠️ INTERNAL API - DO NOT CALL DIRECTLY FROM OUTSIDE DB PACKAGE
// This method is used internally by db package for legacy compatibility.
// New code should use VM's unified write path (applyResult) instead.
//
// Deprecated: Use VM's WriteOp mechanism for all state changes.
func (mgr *Manager) SavePendingAnyTx(tx *pb.AnyTx) error {
	txID := tx.GetTxId()
	if txID == "" {
		return fmt.Errorf("SavePendingAnyTx: empty txID")
	}
	data, err := ProtoMarshal(tx)
	if err != nil {
		return err
	}
	key := KeyPendingAnyTx(txID)
	mgr.EnqueueSet(key, string(data))
	return nil
}

// DeletePendingAnyTx 改为成员函数
func (mgr *Manager) DeletePendingAnyTx(txID string) error {
	if txID == "" {
		return fmt.Errorf("DeletePendingAnyTx: empty txID")
	}
	key := KeyPendingAnyTx(txID)
	mgr.EnqueueDelete(key)
	return nil
}

// LoadPendingAnyTx 改为成员函数
func (mgr *Manager) LoadPendingAnyTx() ([]*pb.AnyTx, error) {
	mgr.mu.RLock()
	db := mgr.Db
	mgr.mu.RUnlock()
	if db == nil {
		return nil, fmt.Errorf("database is not initialized or closed")
	}
	var result []*pb.AnyTx

	err := db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()

		prefix := []byte(KeyPendingAnyTx(""))
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			val, e := item.ValueCopy(nil)
			if e != nil {
				return e
			}
			var anyTx pb.AnyTx
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
