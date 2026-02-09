package db

import (
	"dex/logs"
	"fmt"
	"sync"

	"github.com/dgraph-io/badger/v4"
)

// NewReadOnlyManager 创建一个只读的 DBManager 实例
// 用于 Explorer 等外部进程直接读取节点数据库，不需要写队列和 VerkleStateDB
// 注意：Windows 不支持 BadgerDB 的 ReadOnly 模式，所以用 BypassLockGuard 代替
// Explorer 不初始化写队列，因此不会写入任何数据
func NewReadOnlyManager(path string, logger logs.Logger) (*Manager, error) {
	opts := badger.DefaultOptions(path).WithLoggingLevel(badger.WARNING)
	// Windows 不支持 ReadOnly，使用 BypassLockGuard 允许多进程打开同一数据库
	opts.BypassLockGuard = true
	// 使用较小的缓存，作为只读不需要太多内存
	opts.IndexCacheSize = 16 << 20 // 16MB
	opts.BlockCacheSize = 32 << 20 // 32MB
	opts.NumCompactors = 0         // 不做压缩

	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open badger db for explorer: %w", err)
	}

	manager := &Manager{
		Db:     db,
		mu:     sync.RWMutex{},
		Logger: logger,
		// StateDB = nil, 不初始化 VerkleStateDB
		// writeQueueChan = nil, 不初始化写队列
		// seq = nil, 不初始化自增发号器
		// IndexMgr = nil, 不初始化矿工索引
	}

	return manager, nil
}
