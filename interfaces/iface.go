package interfaces

import (
	"context"
	"crypto/ecdsa"
	"dex/pb"
	"dex/types"
	"time"
)

type BlockStore interface {
	Add(block *types.Block) (bool, error)
	Get(id string) (*types.Block, bool)
	GetByHeight(height uint64) []*types.Block
	GetLastAccepted() (string, uint64)
	GetFinalizedAtHeight(height uint64) (*types.Block, bool)
	GetBlocksFromHeight(from, to uint64) []*types.Block
	GetCurrentHeight() uint64

	// 候选区块相关
	GetPendingBlocksCount() int
	GetPendingBlocks() []*types.Block

	SetFinalized(height uint64, blockID string) error
}

type ConsensusEngine interface {
	Start(ctx context.Context) error
	RegisterQuery(nodeID types.NodeID, requestID uint32, blockID string, height uint64) string
	SubmitChit(nodeID types.NodeID, queryKey string, preferredID string, chitSignature []byte)
	GetActiveQueryCount() int
	GetPreference(height uint64) string
}

type Event interface {
	Type() types.EventType
	Data() interface{}
}

// ============================================
// VRF接口定义
// ============================================

// VRFResult VRF计算结果
type VRFResult struct {
	Output []byte // VRF输出，用于计算出块概率
	Proof  []byte // VRF证明，用于验证
}

// VRFProvider VRF提供者接口
type VRFProvider interface {
	// GenerateVRF 生成VRF证明和输出
	// privateKey: 矿工私钥
	// height: 区块高度
	// window: 时间窗口
	// parentBlockHash: 父区块哈希
	// nodeID: 节点ID
	GenerateVRF(privateKey *ecdsa.PrivateKey, height uint64, window int, parentBlockHash string, nodeID types.NodeID) (*VRFResult, error)

	// VerifyVRF 验证VRF证明
	// publicKey: 矿工公钥
	// height: 区块高度
	// window: 时间窗口
	// parentBlockHash: 父区块哈希
	// nodeID: 节点ID
	// proof: VRF证明
	// output: VRF输出
	VerifyVRF(publicKey *ecdsa.PublicKey, height uint64, window int, parentBlockHash string, nodeID types.NodeID, proof []byte, output []byte) error
}

// ============================================
// 区块提案接口定义
// ============================================

// 定义了区块提案的接口
type BlockProposer interface {
	// ProposeBlock 生成一个新的区块提案
	// parentID: 父区块ID
	// height: 区块高度
	// proposer: 提案者ID
	// window: 时间窗口（替代原来的round）
	ProposeBlock(parentID string, height uint64, proposer types.NodeID, window int) (*types.Block, error)

	// ShouldPropose 决定是否应该在当前时间窗口提出区块
	// nodeID: 节点ID
	// window: 当前时间窗口
	// currentBlocks: 当前高度已存在的区块数量
	// currentHeight: 当前高度
	// proposeHeight: 提案高度
	// lastBlockTime: 上次出块时间
	ShouldPropose(nodeID types.NodeID, window int, currentBlocks int, currentHeight int, proposeHeight int, lastBlockTime time.Time, parentID string) bool
}

// ============================================
// 网络传输层接口
// ============================================

type Transport interface {
	Send(to types.NodeID, msg types.Message) error
	Receive() <-chan types.Message
	Broadcast(msg types.Message, peers []types.NodeID)
	SamplePeers(exclude types.NodeID, count int) []types.NodeID
	GetAllPeers(exclude types.NodeID) []types.NodeID // VRF 确定性采样：获取所有已知节点
}

// NodeSigner 节点签名器接口（每个节点持有一个 KeyManager 实例）
type NodeSigner interface {
	// Sign 对 digest 进行 ECDSA 签名，返回 r||s（各 32 字节，共 64 字节）
	Sign(digest []byte) ([]byte, error)
	// PublicKeyBytes 返回压缩公钥字节（33 字节）
	PublicKeyBytes() []byte
}

type EventBus interface {
	Subscribe(topic types.EventType, handler EventHandler)
	Publish(event Event)
	PublishAsync(event Event)
}

type EventHandler func(Event)

// ============================================
// 数据库层接口 (收敛至此以解决循环依赖)
// ============================================

// OrderIndexEntry 表示订单价格索引扫描返回的一条有序记录。
// 顺序由底层数据库迭代器决定，作为共识执行的确定性输入。
type OrderIndexEntry struct {
	OrderID   string
	IndexData []byte
}

// DBSession 数据库会话接口
type DBSession interface {
	Get(key string) ([]byte, error)
	ApplyStateUpdate(height uint64, updates []interface{}) ([]byte, error)
	Commit() error
	Rollback() error
	Close() error
}

// DBManager 数据库管理器接口
type DBManager interface {
	EnqueueSet(key, value string)
	EnqueueDel(key string)
	ForceFlush() error
	Get(key string) ([]byte, error)
	Scan(prefix string) (map[string][]byte, error)
	ScanKVWithLimit(prefix string, limit int) (map[string][]byte, error)
	ScanKVWithLimitReverse(prefix string, limit int) (map[string][]byte, error)
	ScanOrdersByPairs(pairs []string) (map[string]map[string][]byte, error)
	GetKV(key string) ([]byte, error)
	NewSession() (DBSession, error)

	// Frost 相关方法
	GetFrostVaultTransition(key string) (*pb.VaultTransitionState, error)
	SetFrostVaultTransition(key string, state *pb.VaultTransitionState) error
	GetFrostDkgCommitment(key string) (*pb.FrostVaultDkgCommitment, error)
	SetFrostDkgCommitment(key string, commitment *pb.FrostVaultDkgCommitment) error
	GetFrostDkgShare(key string) (*pb.FrostVaultDkgShare, error)
	SetFrostDkgShare(key string, share *pb.FrostVaultDkgShare) error
	GetFrostVaultState(key string) (*pb.FrostVaultState, error)
	SetFrostVaultState(key string, state *pb.FrostVaultState) error
	GetFrostDkgComplaint(key string) (*pb.FrostVaultDkgComplaint, error)
	SetFrostDkgComplaint(key string, complaint *pb.FrostVaultDkgComplaint) error
}
