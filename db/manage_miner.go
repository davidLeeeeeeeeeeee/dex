package db

import (
	"dex/logs"
	"errors"
	"fmt"
	"github.com/dgraph-io/badger/v4"
)

// 从索引空间里直接随机采样
func GetRandomMinersFast(mgr *Manager, k int) ([]*Account, error) {
	// 使用传入的manager参数而不是创建新的
	if mgr == nil {
		return nil, fmt.Errorf("GetRandomMiners: db manager is nil")
	}

	idxs, err := mgr.IndexMgr.SampleK(k)
	if err != nil {
		return nil, err
	}
	if len(idxs) < k {
		logs.Trace("[GetRandomMinersFast] sampled %d indices k=%d", len(idxs), k)
		return nil, fmt.Errorf("GetRandomMiners: k out of range")
	}

	accounts := make([]*Account, 0, len(idxs))
	for _, idx := range idxs {
		acc, err := mgr.getAccountByIndex(idx) // 使用传入的mgr
		if errors.Is(badger.ErrKeyNotFound, err) {
			logs.Error("[GetRandomMinersFast] index %s not exist", idx)
			continue // 理论不会发生；防御
		}
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, acc)
	}
	return accounts, nil
}
