// interfaces/interfaces.go
package interfaces

import (
	"dex/db"
)

// DBManager 数据库管理接口
type DBManager interface {
	// 基础操作
	Get(key string) ([]byte, error)
	Set(key string, value []byte) error
	Delete(key string) error

	// 批量操作
	EnqueueSet(key string, value string)
	ForceFlush()

	// 账户相关
	GetAccount(address string) (*db.Account, error)
	SaveAccount(account *db.Account) error

	// 区块相关
	GetBlock(height uint64) (*db.Block, error)
	SaveBlock(block *db.Block) error
	GetLatestBlockHeight() (uint64, error)

	// 交易相关
	GetAnyTx(txID string) (*db.AnyTx, error)
	SaveAnyTx(tx *db.AnyTx) error
	SavePendingAnyTx(tx *db.AnyTx) error
	DeletePendingAnyTx(txID string) error
	LoadPendingAnyTx() ([]*db.AnyTx, error)
}

// TxPool 交易池接口
type TxPool interface {
	// 基础操作
	AddTx(tx *db.AnyTx) error
	RemoveTx(txID string) error
	HasTx(txID string) bool
	GetTx(txID string) *db.AnyTx

	// 批量操作
	GetPendingTxs(limit int) []*db.AnyTx
	GetTxsByShortHashes(hashes [][]byte, isPending bool) []*db.AnyTx
	AnalyzeProposalTxs(proposalTxs []byte) (missing map[string]bool, exist map[string]bool)

	// 统计
	GetPendingCount() int
	Clear() error
}

// Network 网络管理接口
type Network interface {
	// 节点管理
	AddOrUpdateNode(pubKey, ip string, isOnline bool) error
	GetNodeByPubKey(pubKey string) *db.NodeInfo
	GetAllNodes() []*db.NodeInfo
	IsKnownNode(pubKey string) bool

	// 矿工相关
	GetTop100CouncilNodes() ([]*db.NodeInfo, error)

	// P2P通信
	SendMessage(peerAddr string, msg interface{}) error
	Broadcast(msg interface{}) error
}

// Executor 执行器接口
type Executor interface {
	// 交易执行
	ExecuteTx(tx *db.AnyTx) error
	ExecuteBatch(txs []*db.AnyTx) error

	// 验证
	ValidateTx(tx *db.AnyTx) error

	// 状态查询
	GetBalance(address, token string) (string, error)
	GetAccountState(address string) (*db.Account, error)
}

// Consensus 共识接口
type Consensus interface {
	// 验证
	CheckAnyTx(tx *db.AnyTx) error

	// 奖励分发
	DistributeRewards(blockHash string, minerAddress string) error

	// 区块相关
	ProposeBlock(height uint64) (*db.Block, error)
	ValidateBlock(block *db.Block) error
}
