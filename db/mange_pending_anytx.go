package db

import (
	"dex/pb"
	"fmt"

	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"
)

// SavePendingAnyTx saves a pending AnyTx to the database
func (mgr *Manager) SavePendingAnyTx(tx *pb.AnyTx) error {
	txID := tx.GetTxId()
	if txID == "" {
		return fmt.Errorf("SavePendingAnyTx: empty txID")
	}
	data, err := ProtoMarshal(tx)
	if err != nil {
		return err
	}
	mgr.EnqueueSet(KeyPendingAnyTx(txID), string(data))
	return nil
}

// DeletePendingAnyTx 改为成员函数
func (mgr *Manager) DeletePendingAnyTx(txID string) error {
	if txID == "" {
		return fmt.Errorf("DeletePendingAnyTx: empty txID")
	}
	mgr.EnqueueDelete(KeyPendingAnyTx(txID))
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
	prefix := []byte(KeyPendingAnyTx(""))
	iter, err := db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixUpperBound(prefix)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	for iter.SeekGE(prefix); iter.Valid(); iter.Next() {
		val := make([]byte, len(iter.Value()))
		copy(val, iter.Value())
		var anyTx pb.AnyTx
		if uerr := proto.Unmarshal(val, &anyTx); uerr != nil {
			continue
		}
		result = append(result, &anyTx)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return result, nil
}
