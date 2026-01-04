package db

import (
	"dex/pb"

	"github.com/dgraph-io/badger/v4"
	"google.golang.org/protobuf/proto"
)

// GetAllNodeInfos 改为成员函数
func (mgr *Manager) GetAllNodeInfos() ([]*pb.NodeInfo, error) {
	var nodes []*pb.NodeInfo
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
			var node pb.NodeInfo
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
func (mgr *Manager) SaveNodeInfo(node *pb.NodeInfo) error {
	data, err := ProtoMarshal(node)
	if err != nil {
		return err
	}
	// 从 PublicKeys 中获取 ECDSA_P256 公钥作为 key，若不存在则使用 IP
	var nodeKey string
	if node.PublicKeys != nil && len(node.PublicKeys.Keys) > 0 {
		if pk, ok := node.PublicKeys.Keys[int32(pb.SignAlgo_SIGN_ALGO_ECDSA_P256)]; ok {
			nodeKey = string(pk)
		}
	}
	if nodeKey == "" {
		nodeKey = node.Ip // fallback to IP
	}
	key := KeyNode() + nodeKey
	mgr.EnqueueSet(key, string(data))
	return nil
}
