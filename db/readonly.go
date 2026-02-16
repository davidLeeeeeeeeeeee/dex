package db

import (
	"dex/logs"
	"fmt"
	"sync"

	"github.com/cockroachdb/pebble"
)

// NewReadOnlyManager 创建一个只读的 DBManager 实例
// 用于 Explorer 等外部进程直接读取节点数据库，不需要写队列和 VerkleStateDB
func NewReadOnlyManager(path string, logger logs.Logger) (*Manager, error) {
	opts := &pebble.Options{
		ReadOnly:     true,
		MaxOpenFiles: 100,
	}
	db, err := pebble.Open(path, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open pebble db for explorer: %w", err)
	}
	return &Manager{
		Db:     db,
		mu:     sync.RWMutex{},
		Logger: logger,
	}, nil
}
