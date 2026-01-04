package network

import (
	"dex/db"
	"dex/logs"
	"dex/pb"
	"sync"
)

// Network 负责维护对等节点列表、从DB加载或更新
type Network struct {
	dbManager *db.Manager
	mu        sync.RWMutex
	nodes     map[string]*pb.NodeInfo // key=publicKey
}

// NewNetwork 创建一个 Network 实例并从DB加载已有节点
func NewNetwork(dbMgr *db.Manager) *Network {
	n := &Network{
		dbManager: dbMgr,
		nodes:     make(map[string]*pb.NodeInfo),
	}

	// 如果需要初始化时加载 DB 里的 node信息
	nodes, err := dbMgr.GetAllNodeInfos()
	if err != nil {
		logs.Verbose("[Network] Failed to load nodes from DB: %v", err)
	} else {
		for _, node := range nodes {
			// 从 PublicKeys 中获取 ECDSA_P256 公钥作为 key
			if node.PublicKeys != nil {
				if pk, ok := node.PublicKeys.Keys[int32(pb.SignAlgo_SIGN_ALGO_ECDSA_P256)]; ok {
					n.nodes[string(pk)] = node
				}
			}
		}
	}
	return n
}

// AddOrUpdateNode 更新或新增节点信息
func (n *Network) AddOrUpdateNode(pubKey, ip string, isOnline bool) {
	n.mu.Lock()
	defer n.mu.Unlock()

	info := &pb.NodeInfo{
		PublicKeys: &pb.PublicKeys{
			Keys: map[int32][]byte{
				int32(pb.SignAlgo_SIGN_ALGO_ECDSA_P256): []byte(pubKey),
			},
		},
		Ip:       ip,
		IsOnline: isOnline,
	}
	n.nodes[pubKey] = info

	// 同步写DB
	if err := n.dbManager.SaveNodeInfo(info); err != nil {
		logs.Verbose("[Network] Failed to save node info: %v", err)
	}
}

// GetNodeByPubKey 获取某个公钥对应的NodeInfo
func (n *Network) GetNodeByPubKey(pubKey string) *pb.NodeInfo {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.nodes[pubKey]
}

// GetAllNodes 返回所有节点信息
func (n *Network) GetAllNodes() []*pb.NodeInfo {
	n.mu.RLock()
	defer n.mu.RUnlock()

	var result []*pb.NodeInfo
	for _, node := range n.nodes {
		result = append(result, node)
	}
	return result
}

// IsKnownNode 判断节点是否在本地路由表里
func (n *Network) IsKnownNode(pubKey string) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	_, ok := n.nodes[pubKey]
	return ok
}

// GetTop100CouncilNodes 从数据库中读取“stakeIndex_”键获取前100个议员（矿工）的地址，
// 然后根据这些地址在 network.nodes 映射中查找相应的节点信息并返回。
// 如果未能找到 100 个议员，则返回错误。
