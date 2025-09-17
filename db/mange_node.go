package db

import (
	"github.com/dgraph-io/badger/v4"
	"google.golang.org/protobuf/proto"
)

func GetAllNodeInfos(mgr *Manager) ([]*NodeInfo, error) {
	var nodes []*NodeInfo
	err := mgr.Db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()

		prefix := []byte("node_")
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

func SaveNodeInfo(mgr *Manager, node *NodeInfo) error {
	data, err := ProtoMarshal(node)
	if err != nil {
		return err
	}
	key := "node_" + node.PublicKey
	mgr.EnqueueSet(key, string(data))
	return nil
}
