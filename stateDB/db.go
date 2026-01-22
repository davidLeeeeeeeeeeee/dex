// statedb/db.go
package statedb

import (
	"fmt"
	"os"
	"sync"

	"github.com/dgraph-io/badger/v4"
)

// ====== StateDB 主体 ======

type DB struct {
	conf     Config
	bdb      *badger.DB
	curEpoch uint64 // 当前内存窗口所属 Epoch 起始高度 (E)

	// 内存 diff 窗口
	mem *memWindow

	// 序号（用于 h<->seq 映射）
	seq *badger.Sequence

	// WAL（可选，用于崩溃恢复）
	wal *WAL

	// 并发保护
	mu sync.RWMutex

	// 预计算的 shard 列表
	shards []string
}

func New(cfg Config) (*DB, error) {
	if cfg.EpochSize == 0 {
		cfg.EpochSize = 40000
	}
	if cfg.ShardHexWidth != 1 && cfg.ShardHexWidth != 2 {
		cfg.ShardHexWidth = 1
	}
	if cfg.PageSize <= 0 {
		cfg.PageSize = 1000
	}
	if cfg.VersionsToKeep <= 0 {
		cfg.VersionsToKeep = 10
	}
	if cfg.AccountNSPrefix == "" {
		cfg.AccountNSPrefix = "v1_account_"
	}

	opts := badger.DefaultOptions(cfg.DataDir).
		WithNumVersionsToKeep(cfg.VersionsToKeep).
		WithSyncWrites(false).
		WithLogger(nil) // 你工程里可接自己的 logger

	db, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}

	seq, err := db.GetSequence([]byte("meta:commit_seq"), 1000)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	// 构造 shard 列表
	var shards []string
	if cfg.ShardHexWidth == 1 {
		for i := 0; i < 16; i++ {
			shards = append(shards, fmt.Sprintf("%x", i))
		}
	} else {
		for i := 0; i < 256; i++ {
			shards = append(shards, fmt.Sprintf("%02x", i))
		}
	}

	s := &DB{
		conf:   cfg,
		bdb:    db,
		mem:    newMemWindow(shards),
		seq:    seq,
		shards: shards,
	}

	// 如果启用了 WAL，则进行恢复和初始化
	if cfg.UseWAL {
		// 尝试恢复最近的 WAL（假设从 Epoch 0 开始，实际可以从元数据读取）
		// 这里简化处理：尝试恢复可能存在的 WAL 文件
		if err := s.recoverFromWAL(); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("WAL recovery failed: %w", err)
		}
	}

	return s, nil
}

func (s *DB) Close() error {
	if s.seq != nil {
		_ = s.seq.Release()
	}
	if s.wal != nil {
		_ = s.wal.Close()
	}
	return s.bdb.Close()
}

func epochOf(height, epochSize uint64) uint64 {
	return (height / epochSize) * epochSize
}

// recoverFromWAL 尝试从 WAL 恢复当前 Epoch 的内存数据
func (s *DB) recoverFromWAL() error {
	// 遍历 WAL 目录，找到最新的 WAL 文件
	// 简化实现：假设只有一个当前 Epoch 的 WAL
	// 生产环境中应该从元数据中读取当前 Epoch
	walDir := fmt.Sprintf("%s/wal", s.conf.DataDir)
	entries, err := os.ReadDir(walDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // WAL 目录不存在，跳过恢复
		}
		return err
	}

	// 找到最新的 WAL 文件（按文件名排序，最大的就是最新的）
	var latestWAL string
	var latestEpoch uint64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		// 解析文件名获取 Epoch
		var epoch uint64
		_, err := fmt.Sscanf(entry.Name(), "wal_E%020d.log", &epoch)
		if err == nil && epoch >= latestEpoch {
			latestEpoch = epoch
			latestWAL = entry.Name()
		}
	}

	if latestWAL == "" {
		return nil // 没有 WAL 文件，跳过恢复
	}

	// 恢复 WAL
	walPath := fmt.Sprintf("%s/%s", walDir, latestWAL)
	err = ReplayWAL(walPath, func(rec WalRecord) error {
		// 设置当前 Epoch
		if s.curEpoch == 0 {
			s.curEpoch = rec.Epoch
		}
		// 应用到内存窗口
		key := string(rec.Key)
		sh := shardOf(key, s.conf.ShardHexWidth)
		deleted := rec.Op == 1 // 1=DEL
		// 这里使用 height=0 作为占位，因为 WAL 中没有记录具体高度
		// 如果需要精确高度，需要在 WalRecord 中添加 Height 字段
		s.mem.apply(rec.Epoch, key, rec.Value, deleted, sh)
		return nil
	})

	return err
}
