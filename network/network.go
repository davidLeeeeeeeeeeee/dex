// network/network.go
package network

import (
	"context"
	"dex/config"
	"dex/db"
	"dex/interfaces"
	"dex/logs"
	"fmt"
	"sync"
	"time"
)

// Network 重构后的网络管理器
type Network struct {
	// 配置和依赖
	cfg       *config.NetworkConfig
	dbManager interfaces.DBManager

	// 内部状态
	mu    sync.RWMutex
	nodes map[string]*db.NodeInfo // key=publicKey
	peers map[string]*Peer        // 活跃的对等节点

	// 消息处理
	msgHandler MessageHandler

	// 生命周期管理
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Peer 对等节点
type Peer struct {
	PublicKey string
	IP        string
	IsOnline  bool
	LastSeen  time.Time
	conn      interface{} // 实际的连接对象
}

// MessageHandler 消息处理器接口
type MessageHandler interface {
	HandleMessage(from string, msg interface{}) error
}

// NewNetwork 创建网络管理器（不再是单例）
func NewNetwork(
	cfg config.NetworkConfig,
	dbManager interfaces.DBManager,
) (*Network, error) {
	ctx, cancel := context.WithCancel(context.Background())

	n := &Network{
		cfg:       &cfg,
		dbManager: dbManager,
		nodes:     make(map[string]*db.NodeInfo),
		peers:     make(map[string]*Peer),
		ctx:       ctx,
		cancel:    cancel,
	}

	// 从数据库加载已知节点
	if err := n.loadNodesFromDB(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to load nodes: %w", err)
	}

	return n, nil
}

// SetMessageHandler 设置消息处理器（避免循环依赖）
func (n *Network) SetMessageHandler(handler MessageHandler) {
	n.msgHandler = handler
}

// Start 启动网络服务
func (n *Network) Start() error {
	// 启动节点发现
	n.wg.Add(1)
	go n.discoveryLoop()

	// 启动心跳检测
	n.wg.Add(1)
	go n.heartbeatLoop()

	// 启动P2P监听
	n.wg.Add(1)
	go n.listenLoop()

	logs.Info("Network service started")
	return nil
}

// Stop 停止网络服务
func (n *Network) Stop() error {
	n.cancel()
	n.wg.Wait()

	// 关闭所有连接
	n.mu.Lock()
	for _, peer := range n.peers {
		// 关闭peer连接
		_ = peer
	}
	n.mu.Unlock()

	logs.Info("Network service stopped")
	return nil
}

// AddOrUpdateNode 添加或更新节点（实现接口）
func (n *Network) AddOrUpdateNode(pubKey, ip string, isOnline bool) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	info := &db.NodeInfo{
		PublicKey: pubKey,
		Ip:        ip,
		IsOnline:  isOnline,
	}

	n.nodes[pubKey] = info

	// 同步到数据库（异步）
	go func() {
		if err := n.saveNodeToDB(info); err != nil {
			logs.Error("Failed to save node info: %v", err)
		}
	}()

	// 如果节点在线，尝试建立连接
	if isOnline {
		n.wg.Add(1)
		go n.connectToPeer(pubKey, ip)
	}

	return nil
}

// GetNodeByPubKey 获取节点信息（实现接口）
func (n *Network) GetNodeByPubKey(pubKey string) *db.NodeInfo {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.nodes[pubKey]
}

// GetAllNodes 获取所有节点（实现接口）
func (n *Network) GetAllNodes() []*db.NodeInfo {
	n.mu.RLock()
	defer n.mu.RUnlock()

	result := make([]*db.NodeInfo, 0, len(n.nodes))
	for _, node := range n.nodes {
		result = append(result, node)
	}
	return result
}

// IsKnownNode 检查是否为已知节点（实现接口）
func (n *Network) IsKnownNode(pubKey string) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	_, exists := n.nodes[pubKey]
	return exists
}

// GetTop100CouncilNodes 获取前100个议员节点（实现接口）
func (n *Network) GetTop100CouncilNodes() ([]*db.NodeInfo, error) {
	// 从数据库读取stake信息并排序
	// 这里需要与db层交互
	return nil, nil
}

// SendMessage 发送消息到指定节点（实现接口）
func (n *Network) SendMessage(peerAddr string, msg interface{}) error {
	n.mu.RLock()
	peer, exists := n.peers[peerAddr]
	n.mu.RUnlock()

	if !exists {
		return fmt.Errorf("peer %s not found", peerAddr)
	}

	// 实际发送逻辑
	return n.sendToPeer(peer, msg)
}

// Broadcast 广播消息（实现接口）
func (n *Network) Broadcast(msg interface{}) error {
	n.mu.RLock()
	peers := make([]*Peer, 0, len(n.peers))
	for _, peer := range n.peers {
		peers = append(peers, peer)
	}
	n.mu.RUnlock()

	var wg sync.WaitGroup
	for _, peer := range peers {
		wg.Add(1)
		go func(p *Peer) {
			defer wg.Done()
			if err := n.sendToPeer(p, msg); err != nil {
				logs.Error("Failed to send to peer %s: %v", p.IP, err)
			}
		}(peer)
	}
	wg.Wait()

	return nil
}

// 内部方法

func (n *Network) loadNodesFromDB() error {
	// 使用注入的dbManager而不是全局单例

	return nil
}

func (n *Network) saveNodeToDB(info *db.NodeInfo) error {
	return nil
}

func (n *Network) connectToPeer(pubKey, ip string) {
	defer n.wg.Done()

	// 连接逻辑
	// ...

	// 成功后加入peers
	n.mu.Lock()
	n.peers[pubKey] = &Peer{
		PublicKey: pubKey,
		IP:        ip,
		IsOnline:  true,
		LastSeen:  time.Now(),
	}
	n.mu.Unlock()
}

func (n *Network) sendToPeer(peer *Peer, msg interface{}) error {
	// 实际的发送实现
	return nil
}

func (n *Network) discoveryLoop() {
	defer n.wg.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
			n.discoverNewPeers()
		}
	}
}

func (n *Network) heartbeatLoop() {
	defer n.wg.Done()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
			n.checkPeerHealth()
		}
	}
}

func (n *Network) listenLoop() {
	defer n.wg.Done()

	// P2P监听逻辑
	for {
		select {
		case <-n.ctx.Done():
			return
		default:
			// 接受新连接
			// 处理消息
		}
	}
}

func (n *Network) discoverNewPeers() {
	// 节点发现逻辑
}

func (n *Network) checkPeerHealth() {
	n.mu.Lock()
	defer n.mu.Unlock()

	now := time.Now()
	for pubKey, peer := range n.peers {
		if now.Sub(peer.LastSeen) > 30*time.Second {
			peer.IsOnline = false
			// 可以选择移除或标记为离线
			delete(n.peers, pubKey)
			logs.Info("Peer %s went offline", pubKey)
		}
	}
}
