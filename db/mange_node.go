package db

import (
	"dex/pb"

	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"
)

// GetAllNodeInfos 改为成员函数
func (mgr *Manager) GetAllNodeInfos() ([]*pb.NodeInfo, error) {
	var nodes []*pb.NodeInfo
	prefix := []byte(KeyNode())
	iter, err := mgr.Db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixUpperBound(prefix)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	for iter.SeekGE(prefix); iter.Valid(); iter.Next() {
		val := make([]byte, len(iter.Value()))
		copy(val, iter.Value())
		var node pb.NodeInfo
		if err := proto.Unmarshal(val, &node); err != nil {
			return nil, err
		}
		nodes = append(nodes, &node)
	}
	if err := iter.Error(); err != nil {
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
	var nodeKey string
	if node.PublicKeys != nil && len(node.PublicKeys.Keys) > 0 {
		if pk, ok := node.PublicKeys.Keys[int32(pb.SignAlgo_SIGN_ALGO_ECDSA_P256)]; ok {
			nodeKey = string(pk)
		}
	}
	if nodeKey == "" {
		nodeKey = node.Ip
	}
	key := KeyNode() + nodeKey
	mgr.EnqueueSet(key, string(data))
	return nil
}
