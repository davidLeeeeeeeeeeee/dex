package db

import (
	"dex/logs"
	"errors"
	"fmt"
	"github.com/dgraph-io/badger/v4"
)

// GetRandomMinersFast 从索引空间里直接随机采样
func GetRandomMinersFast(k int) ([]*Account, error) {
	// 1) 读出上限
	mgr, err := NewManager("")
	if err != nil {
		return nil, fmt.Errorf("GetRandomMiners: failed to get db instance: %v", err)
	}

	idxs, err := mgr.IndexMgr.SampleK(k)
	if err != nil {
		return nil, err
	}
	logs.Debug("[GetRandomMinersFast] sampled %d indices", len(idxs))

	accounts := make([]*Account, 0, len(idxs))
	for _, idx := range idxs {
		acc, err := mgr.getAccountByIndex(idx) // 你已有的封装
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
