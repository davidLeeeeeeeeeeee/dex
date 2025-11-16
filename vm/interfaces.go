package vm

import "dex/pb"

// ========== 核心接口定义 ==========

// StateView 状态视图接口
type StateView interface {
	//读/写/删某个 key 的状态；写入只写进这个视图，不直接落到底层 DB。
	Get(key string) ([]byte, bool, error)
	Set(key string, val []byte)
	Del(key string)
	//做一个快照点、必要时回滚到该点，实现幂等预执行与失败回滚。
	Snapshot() int
	Revert(snap int) error
	//把这段预执行期间累积的写入集合（写集）导出来，给后续“真正落库”用。
	Diff() []WriteOp
	// 扫描指定前缀下的所有键值对（用于订单簿重建等场景）
	Scan(prefix string) (map[string][]byte, error)
}

// TxHandler 交易处理器接口
type TxHandler interface {
	//标识这个 Handler 处理哪种交易类型（比如 "order"）。
	Kind() string
	//在给定 StateView 上预执行，返回写集 []WriteOp 与执行回执 *Receipt（成功/失败、错误原因、写入条数等）
	DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error)
	//可选的“兜底”落库方法（也可以返回 ErrNotImplemented，让你统一用 Diff() + DB 批量入库）。
	Apply(tx *pb.AnyTx) error // 可选兜底；或返回 ErrNotImplemented
}

// （预执行结果缓存）
// 给“按区块维度的预执行结果（合法性、写集、回执）”做缓存
type SpecExecCache interface {
	//以 blockID 为键获取/写入预执行结果 SpecResult
	Get(blockID string) (*SpecResult, bool)
	Put(res *SpecResult)
	//把低于某高度的缓存逐步淘汰，防止内存无限增长。
	//这能避免同一候选区块在多轮投票中反复预执行，直接复用结果，减少延迟与 CPU 开销。
	EvictBelow(height uint64)
}

// DBManager 数据库管理器接口
type DBManager interface {
	EnqueueSet(key, value string)
	EnqueueDel(key string)
	ForceFlush() error
	Get(key string) ([]byte, error)
	// 前缀扫描，返回所有以 prefix 开头的键值对
	Scan(prefix string) (map[string][]byte, error)
	// 一次性扫描多个交易对的订单索引
	ScanOrdersByPairs(pairs []string) (map[string]map[string][]byte, error)
}

// （读穿函数）
// 当 StateView.Get 本地 overlay 没命中时，定义“如何从底层存储读真实值”的函数签名
// 让 预执行的 StateView 在不落库的情况下依然能读到链上最新持久状态，从而实现 预执行 + 延迟落库 的闭环。
type ReadThroughFn func(key string) ([]byte, error)

// ScanFn 用于 StateView 从底层存储做前缀扫描
type ScanFn func(prefix string) (map[string][]byte, error)

// （交易类型提取函数）
// 给 AnyTx 提取“交易种类”的小工具。默认实现会优先看 tx.Type，否则看 tx.Kind，失败则报错；这让 VM 能用 Kind() 去路由到正确的 TxHandler
type KindFn func(tx *pb.AnyTx) (string, error)
