package db

import (
	"dex/logs"
	"math/rand"
	"strconv"
	"sync"
	"time"

	"github.com/RoaringBitmap/roaring"
	"github.com/dgraph-io/badger/v4"
)

// MinerIndexManager 负责：
//  1. 启动时扫描 Badger，恢复活跃矿工索引到 RoaringBitmap；
//  2. 运行中实时 Add / Remove；
//  3. 提供高性能采样 SampleK。
type MinerIndexManager struct {
	mu     sync.RWMutex
	bitmap *roaring.Bitmap
	db     *badger.DB
	Logger logs.Logger
	rng    *rand.Rand // 共享随机数生成器，避免每次分配
	rngMu  sync.Mutex // 保护 rng 的互斥锁
}

// ----------  初始化 / 恢复  ----------

func NewMinerIndexManager(db *badger.DB, logger logs.Logger) (*MinerIndexManager, error) {
	m := &MinerIndexManager{
		db:     db,
		bitmap: roaring.New(),
		Logger: logger,
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	if err := m.RebuildBitmapFromDB(); err != nil {
		return nil, err
	}
	return m, nil
}

// 在一次只读事务里迭代所有 "indexToAccount_*" 键，填充 bitmap。
func (m *MinerIndexManager) RebuildBitmapFromDB() error {
	prefix := []byte(NameOfKeyIndexToAccount())
	rebuilt := roaring.New()
	count := 0

	err := m.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			key := it.Item().Key()
			idxBytes := key[len(prefix):]
			idx, err := strconv.ParseUint(string(idxBytes), 10, 64)
			if err != nil {
				continue
			}
			rebuilt.Add(uint32(idx))
			count++
		}
		return nil
	})
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.bitmap = rebuilt
	m.mu.Unlock()
	m.Logger.Info("[MinerIndexManager] rebuilt bitmap with %d miners", count)
	return nil
}

// ----------  运行时维护  ----------

func (m *MinerIndexManager) Add(idx uint64) {
	m.mu.Lock()
	m.bitmap.Add(uint32(idx))
	m.mu.Unlock()
}

func (m *MinerIndexManager) Remove(idx uint64) {
	m.mu.Lock()
	m.bitmap.Remove(uint32(idx))
	m.mu.Unlock()
}

// SnapshotIndices returns all tracked miner indices from the in-memory bitmap.
func (m *MinerIndexManager) SnapshotIndices() []uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	card := int(m.bitmap.GetCardinality())
	if card == 0 {
		return nil
	}

	indices := make([]uint64, 0, card)
	it := m.bitmap.Iterator()
	for it.HasNext() {
		indices = append(indices, uint64(it.Next()))
	}
	return indices
}

// GetAddressByIndex 通过索引查找矿工地址
func (m *MinerIndexManager) GetAddressByIndex(index uint64) (string, error) {
	key := []byte(KeyIndexToAccount(index))
	var addr string
	err := m.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return err
		}
		val, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}
		addr = string(val)
		return nil
	})
	if err != nil {
		return "", err
	}
	return addr, nil
}

// ----------  高性能采样  ----------

func (m *MinerIndexManager) SampleK(k int) ([]uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	card := int(m.bitmap.GetCardinality())
	if card == 0 {
		return nil, nil // 没有矿工
	}
	if k > card {
		k = card
	}

	res := make([]uint64, 0, k)
	seen := make(map[uint32]struct{}, k)

	m.rngMu.Lock()
	defer m.rngMu.Unlock()

	for len(res) < k {
		r := uint32(m.rng.Intn(card)) // 使用共享 rng
		v, _ := m.bitmap.Select(r)    // O(log64 N)
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		res = append(res, uint64(v))
	}
	return res, nil
}
