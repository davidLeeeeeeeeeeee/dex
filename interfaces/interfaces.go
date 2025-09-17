// interfaces/interfaces.go
package interfaces

// 注意：不再导入 dex/db，使用 interface{} 避免循环依赖
// 这个包是所有接口的定义中心，其他包实现这些接口但不需要导入这个包

// DBManager 数据库管理接口
type DBManager interface {
	// 基础操作
	Get(key string) ([]byte, error)
	Set(key string, value []byte) error
	Delete(key string) error

	// 批量操作
	EnqueueSet(key string, value string)
	ForceFlush()

	// 账户相关 - 使用 interface{} 避免导入 db 包
	GetAccount(address string) (interface{}, error)
	SaveAccount(account interface{}) error

	// 区块相关
	GetBlock(height uint64) (interface{}, error)
	SaveBlock(block interface{}) error
	GetLatestBlockHeight() (uint64, error)

	// 交易相关
	GetAnyTx(txID string) (interface{}, error)
	SaveAnyTx(tx interface{}) error
	SavePendingAnyTx(tx interface{}) error
	DeletePendingAnyTx(txID string) error
	LoadPendingAnyTx() ([]interface{}, error)
}

// TxPool 交易池接口
type TxPool interface {
	// 生命周期管理
	Start() error
	Stop() error

	// 基础操作
	AddTx(tx interface{}) error
	RemoveTx(txID string) error
	HasTx(txID string) bool
	GetTx(txID string) interface{}

	// 批量操作
	GetPendingTxs(limit int) []interface{}
	GetTxsByShortHashes(hashes [][]byte, isPending bool) []interface{}
	AnalyzeProposalTxs(proposalTxs []byte) (missing map[string]bool, exist map[string]bool)

	// 统计
	GetPendingCount() int
	Clear() error
}

// Network 网络管理接口
type Network interface {
	// 生命周期管理
	Start() error
	Stop() error

	// 节点管理
	AddOrUpdateNode(pubKey, ip string, isKnown bool) error
	GetNodeByPubKey(pubKey string) interface{}
	GetAllNodes() []interface{}
	IsKnownNode(pubKey string) bool

	// 矿工相关
	GetTop100CouncilNodes() ([]interface{}, error)

	// P2P通信
	SendMessage(peerAddr string, msg interface{}) error
	Broadcast(msg interface{}) error
}

// Executor 执行器接口
type Executor interface {
	// 交易执行
	ExecuteTx(tx interface{}) error
	ExecuteBatch(txs []interface{}) error

	// 验证
	ValidateTx(tx interface{}) error

	// 状态查询
	GetBalance(address, token string) (string, error)
	GetAccountState(address string) (interface{}, error)
}

// Consensus 共识接口
type Consensus interface {
	// 生命周期管理
	Start() error
	Stop() error
	Run(ctx interface{}) // context.Context as interface{}

	// 验证
	CheckAnyTx(tx interface{}) error

	// 奖励分发
	DistributeRewards(blockHash string, minerAddress string) error

	// 区块相关
	ProposeBlock(height uint64) (interface{}, error)
	ValidateBlock(block interface{}) error
}
