package db

import (
	"github.com/dgraph-io/badger/v4"
	"google.golang.org/protobuf/proto"
)

// GetAllNodeInfos 改为成员函数
func (mgr *Manager) GetAllNodeInfos() ([]*NodeInfo, error) {
	var nodes []*NodeInfo
	err := mgr.Db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()

		prefix := []byte(KeyNode())
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			val, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			var node NodeInfo
			if err := proto.Unmarshal(val, &node); err != nil {
				return err
			}
			nodes = append(nodes, &node)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return nodes, nil
}

// SaveNodeInfo 改为成员函数
func (mgr *Manager) SaveNodeInfo(node *NodeInfo) error {
	data, err := ProtoMarshal(node)
	if err != nil {
		return err
	}
	key := KeyNode() + node.PublicKey
	mgr.EnqueueSet(key, string(data))
	return nil
}
