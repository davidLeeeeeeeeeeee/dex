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
}

// ----------  初始化 / 恢复  ----------

func NewMinerIndexManager(db *badger.DB) (*MinerIndexManager, error) {
	m := &MinerIndexManager{
		db:     db,
		bitmap: roaring.New(),
	}
	if err := m.RebuildBitmapFromDB(); err != nil {
		return nil, err
	}
	return m, nil
}

// 在一次只读事务里迭代所有 "indexToAccount_*" 键，填充 bitmap。
func (m *MinerIndexManager) RebuildBitmapFromDB() error {
	prefix := []byte(NameOfKeyIndexToAccount())

	return m.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()

		count := 0
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			key := it.Item().Key()
			idxBytes := key[len(prefix):]
			idx, err := strconv.ParseUint(string(idxBytes), 10, 64)
			if err != nil {
				continue
			}
			m.bitmap.Add(uint32(idx))
			count++
		}
		logs.Info("[MinerIndexManager] rebuilt bitmap with %d miners", count)
		return nil
	})
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
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	seen := make(map[uint32]struct{}, k)

	for len(res) < k {
		r := uint32(rng.Intn(card)) // [0, card)
		v, _ := m.bitmap.Select(r)  // O(log64 N)
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		res = append(res, uint64(v))
	}
	return res, nil
}
