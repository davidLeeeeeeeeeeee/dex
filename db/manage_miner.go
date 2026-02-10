package db

import (
	"dex/logs"
	"dex/pb"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// GetRandomMinersFast samples active miners directly from in-memory snapshot.
func (mgr *Manager) GetRandomMinersFast(k int) ([]*pb.Account, error) {
	if mgr == nil {
		return nil, fmt.Errorf("GetRandomMiners: db manager is nil")
	}
	if k <= 0 {
		return []*pb.Account{}, nil
	}
	if err := mgr.ensureMinerCacheReady(); err != nil {
		return nil, err
	}

	mgr.minerCacheMu.RLock()
	miners := mgr.minerParticipants
	mgr.minerCacheMu.RUnlock()

	if len(miners) == 0 {
		logs.Warn("[GetRandomMinersFast] no miners available, k=%d", k)
		return nil, fmt.Errorf("GetRandomMiners: no miners available")
	}
	if k > len(miners) {
		k = len(miners)
	}

	accounts := make([]*pb.Account, 0, k)

	mgr.minerSampleRandMu.Lock()
	if mgr.minerSampleRand == nil {
		mgr.minerSampleRand = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	// When k is large, use permutation to avoid excessive collision retries.
	if k*2 >= len(miners) {
		perm := mgr.minerSampleRand.Perm(len(miners))
		for i := 0; i < k; i++ {
			accounts = append(accounts, miners[perm[i]])
		}
		mgr.minerSampleRandMu.Unlock()
		return accounts, nil
	}

	seen := make(map[int]struct{}, k)
	for len(accounts) < k {
		i := mgr.minerSampleRand.Intn(len(miners))
		if _, exists := seen[i]; exists {
			continue
		}
		seen[i] = struct{}{}
		accounts = append(accounts, miners[i])
	}
	mgr.minerSampleRandMu.Unlock()
	return accounts, nil
}

// RefreshMinerParticipantsByHeight refreshes the in-memory miner snapshot when epoch changes.
func (mgr *Manager) RefreshMinerParticipantsByHeight(height uint64) error {
	if mgr == nil {
		return fmt.Errorf("RefreshMinerParticipantsByHeight: db manager is nil")
	}
	return mgr.RefreshMinerParticipantsForEpoch(mgr.minerEpochByHeight(height))
}

// RefreshMinerParticipantsForEpoch refreshes in-memory miner snapshot exactly once per epoch.
func (mgr *Manager) RefreshMinerParticipantsForEpoch(epoch uint64) error {
	if mgr == nil {
		return fmt.Errorf("RefreshMinerParticipantsForEpoch: db manager is nil")
	}

	mgr.minerCacheMu.RLock()
	if mgr.minerCacheReady && mgr.minerCacheEpoch >= epoch {
		mgr.minerCacheMu.RUnlock()
		return nil
	}
	mgr.minerCacheMu.RUnlock()

	miners, err := mgr.loadMinerParticipantsFromDB()
	if err != nil {
		return err
	}

	mgr.minerCacheMu.Lock()
	if mgr.minerCacheReady && mgr.minerCacheEpoch >= epoch {
		mgr.minerCacheMu.Unlock()
		return nil
	}
	mgr.minerParticipants = miners
	mgr.minerCacheEpoch = epoch
	mgr.minerCacheReady = true
	mgr.minerCacheMu.Unlock()

	logs.Debug("[MinerCache] refreshed epoch=%d miners=%d", epoch, len(miners))
	return nil
}

func (mgr *Manager) ensureMinerCacheReady() error {
	mgr.minerCacheMu.RLock()
	ready := mgr.minerCacheReady
	mgr.minerCacheMu.RUnlock()
	if ready {
		return nil
	}

	height, err := mgr.GetLatestBlockHeight()
	if err != nil {
		// Height may be unavailable before first block; use epoch 0 bootstrap.
		return mgr.RefreshMinerParticipantsForEpoch(0)
	}
	return mgr.RefreshMinerParticipantsForEpoch(mgr.minerEpochByHeight(height))
}

func (mgr *Manager) minerEpochByHeight(height uint64) uint64 {
	epochBlocks := uint64(1)
	if mgr != nil && mgr.cfg != nil && mgr.cfg.Frost.Committee.EpochBlocks > 0 {
		epochBlocks = uint64(mgr.cfg.Frost.Committee.EpochBlocks)
	}
	return height / epochBlocks
}

func (mgr *Manager) loadMinerParticipantsFromDB() ([]*pb.Account, error) {
	if mgr.IndexMgr == nil {
		return nil, fmt.Errorf("index manager is not initialized")
	}
	if mgr.Db == nil {
		return nil, fmt.Errorf("database is not initialized or closed")
	}

	indices := mgr.IndexMgr.SnapshotIndices()
	if len(indices) == 0 {
		return []*pb.Account{}, nil
	}

	miners := make([]*pb.Account, 0, len(indices))
	err := mgr.Db.View(func(txn *badger.Txn) error {
		for _, idx := range indices {
			item, err := txn.Get([]byte(KeyIndexToAccount(idx)))
			if errors.Is(err, badger.ErrKeyNotFound) {
				continue
			}
			if err != nil {
				return err
			}

			accountKey, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}

			accountItem, err := txn.Get(accountKey)
			if errors.Is(err, badger.ErrKeyNotFound) {
				continue
			}
			if err != nil {
				return err
			}

			accountBytes, err := accountItem.ValueCopy(nil)
			if err != nil {
				return err
			}

			account := &pb.Account{}
			if err := ProtoUnmarshal(accountBytes, account); err != nil {
				logs.Warn("[MinerCache] failed to unmarshal account idx=%d: %v", idx, err)
				continue
			}
			if !account.IsMiner {
				continue
			}
			miners = append(miners, account)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return miners, nil
}
