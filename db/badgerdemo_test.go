package db

import (
	"testing"

	"github.com/dgraph-io/badger/v4"
)

// 读取：在一个普通只读事务里遍历该 key 的全部版本，挑选 “<= readTs 的最大版本”
func readAtTs(db *badger.DB, key []byte, readTs uint64) ([]byte, uint64, error) {
	var bestVal []byte
	var bestTs uint64

	err := db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.IteratorOptions{
			PrefetchValues: true,
			AllVersions:    true, // 关键：拿到该 key 的所有有效版本
			Prefix:         key,  // 小优化：只扫这个 key
		})
		defer it.Close()

		it.Seek(key)
		for ; it.ValidForPrefix(key); it.Next() {
			item := it.Item()
			ts := item.Version()
			// 选择 <= readTs 的“最新”（最大的 ts）
			if ts <= readTs && ts >= bestTs {
				v, err := item.ValueCopy(nil)
				if err != nil {
					return err
				}
				bestTs = ts
				bestVal = v
			}
		}
		if bestVal == nil {
			return badger.ErrKeyNotFound
		}
		return nil
	})
	return bestVal, bestTs, err
}

// 每次单独事务写入，然后马上读取该 key 的 Version() 来记录提交的 Ts
func writeAndGetTs(db *badger.DB, key, val []byte) (uint64, error) {
	// 写
	if err := db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, val)
	}); err != nil {
		return 0, err
	}
	// 读出该键当前可见版本的 Ts（就是刚提交的事务 Ts）
	var ts uint64
	err := db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return err
		}
		ts = item.Version()
		return nil
	})
	return ts, err
}

func Test_KeyHistory_ByTs(t *testing.T) {
	// 用内存模式，避免落盘；并把版本保留数调大，确保历史版本不会被立即丢弃
	opts := badger.DefaultOptions("").
		WithInMemory(true).
		WithNumVersionsToKeep(10)

	db, err := badger.Open(opts)
	if err != nil {
		t.Fatalf("open badger: %v", err)
	}
	defer db.Close()

	key := []byte("v1_account_bc1q123")

	// 1) 连续多次写入同一 key，并记录每次提交后的 Ts
	ts1, err := writeAndGetTs(db, key, []byte("value-1"))
	if err != nil {
		t.Fatalf("write #1: %v", err)
	}
	ts2, err := writeAndGetTs(db, key, []byte("value-2"))
	if err != nil {
		t.Fatalf("write #2: %v", err)
	}
	ts3, err := writeAndGetTs(db, key, []byte("value-3"))
	if err != nil {
		t.Fatalf("write #3: %v", err)
	}

	// 2) 按记录下来的 Ts 回读历史版本
	type probe struct {
		ts       uint64
		expected string
	}
	tests := []probe{
		{ts: ts1, expected: "value-1"},
		{ts: ts2, expected: "value-2"},
		{ts: ts3, expected: "value-3"},
	}

	for i, c := range tests {
		got, tsFound, err := readAtTs(db, key, c.ts)
		if err != nil {
			t.Fatalf("readAtTs #%d (ts=%d) err: %v", i+1, c.ts, err)
		}
		if string(got) != c.expected {
			t.Fatalf("readAtTs #%d mismatch: want %q got %q (tsFound=%d)",
				i+1, c.expected, string(got), tsFound)
		}
	}

	// 3) 也可以测试“介于 ts1 与 ts2 之间”的读（应当仍返回 value-1）
	midTs := (ts1 + ts2) / 2
	got, tsFound, err := readAtTs(db, key, midTs)
	if err != nil {
		t.Fatalf("readAtTs mid err: %v", err)
	}
	if string(got) != "value-1" {
		t.Fatalf("readAtTs mid mismatch: want %q got %q (tsFound=%d)", "value-1", string(got), tsFound)
	}
}
