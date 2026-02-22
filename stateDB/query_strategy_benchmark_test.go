package statedb

import (
	"bytes"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"testing"
)

const orderStateBenchPrefix = "v1_orderstate_bench_"

func buildOrderStateDataset(t testing.TB, db *DB, total, batchSize int) []string {
	t.Helper()
	if total <= 0 {
		t.Fatalf("total must be > 0, got %d", total)
	}
	if batchSize <= 0 {
		batchSize = 1000
	}

	keys := make([]string, 0, total)
	updates := make([]KVUpdate, 0, batchSize)
	height := uint64(1)

	for i := 0; i < total; i++ {
		key := fmt.Sprintf("%s%06d", orderStateBenchPrefix, i)
		val := []byte(strconv.Itoa(i))

		keys = append(keys, key)
		updates = append(updates, KVUpdate{Key: key, Value: val})

		if len(updates) == batchSize {
			if err := db.ApplyAccountUpdate(height, updates...); err != nil {
				t.Fatalf("apply updates at height=%d failed: %v", height, err)
			}
			updates = updates[:0]
			height++
		}
	}
	if len(updates) > 0 {
		if err := db.ApplyAccountUpdate(height, updates...); err != nil {
			t.Fatalf("apply tail updates at height=%d failed: %v", height, err)
		}
	}
	return keys
}

func pickQueryKeys(all []string, n int, seed int64) []string {
	if n <= 0 || len(all) == 0 {
		return nil
	}
	if n > len(all) {
		n = len(all)
	}
	r := rand.New(rand.NewSource(seed))
	idx := r.Perm(len(all))[:n]
	out := make([]string, 0, n)
	for _, i := range idx {
		out = append(out, all[i])
	}
	sort.Strings(out)
	return out
}

// getManyByPrefixFilter mimics "iterator scan then filter wanted set".
func getManyByPrefixFilter(db *DB, prefix string, keys []string) (map[string][]byte, error) {
	wanted := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		wanted[key] = struct{}{}
	}

	out := make(map[string][]byte, len(wanted))
	err := db.IterateLatestByPrefix(prefix, func(key string, value []byte) error {
		if _, ok := wanted[key]; !ok {
			return nil
		}
		copied := make([]byte, len(value))
		copy(copied, value)
		out[key] = copied
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// getManyByPointGet is a baseline: one Get per unique key.
func getManyByPointGet(db *DB, keys []string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(keys))
	seen := make(map[string]struct{}, len(keys))

	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		val, exists, err := db.Get(key)
		if err != nil {
			return nil, err
		}
		if exists {
			out[key] = val
		}
	}
	return out, nil
}

func assertKVMapEqual(t testing.TB, got, want map[string][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("result size mismatch: got=%d want=%d", len(got), len(want))
	}
	for key, wantVal := range want {
		gotVal, ok := got[key]
		if !ok {
			t.Fatalf("missing key in got: %s", key)
		}
		if !bytes.Equal(gotVal, wantVal) {
			t.Fatalf("value mismatch for key=%s got=%q want=%q", key, string(gotVal), string(wantVal))
		}
	}
}

func TestGetManyVsPrefixFilterAndPointGet(t *testing.T) {
	cfg := testPebbleConfig(t.TempDir())
	cfg.CheckpointEvery = 1 << 30 // keep write path stable for this read-path comparison

	db, err := New(cfg)
	if err != nil {
		t.Fatalf("new db failed: %v", err)
	}
	defer db.Close()

	allKeys := buildOrderStateDataset(t, db, 20000, 1000)
	queryKeys := pickQueryKeys(allKeys, 1000, 20260222)
	queryKeys = append(
		queryKeys,
		"v1_orderstate_bench_missing_a",
		"v1_orderstate_bench_missing_b",
		queryKeys[0],
		queryKeys[10],
		"",
	)

	pointGot, err := getManyByPointGet(db, queryKeys)
	if err != nil {
		t.Fatalf("point-get baseline failed: %v", err)
	}
	if len(pointGot) != 1000 {
		t.Fatalf("expected 1000 hits from baseline, got %d", len(pointGot))
	}

	getManyGot, err := db.GetMany(queryKeys)
	if err != nil {
		t.Fatalf("GetMany failed: %v", err)
	}
	scanFilterGot, err := getManyByPrefixFilter(db, "v1_orderstate_", queryKeys)
	if err != nil {
		t.Fatalf("prefix scan+filter failed: %v", err)
	}

	assertKVMapEqual(t, getManyGot, pointGot)
	assertKVMapEqual(t, scanFilterGot, pointGot)
}

func BenchmarkGetManyStrategies(b *testing.B) {
	cfg := testPebbleConfig(b.TempDir())
	cfg.CheckpointEvery = 1 << 30

	db, err := New(cfg)
	if err != nil {
		b.Fatalf("new db failed: %v", err)
	}
	defer db.Close()

	b.StopTimer()
	allKeys := buildOrderStateDataset(b, db, 50000, 1000)
	queryKeys := pickQueryKeys(allKeys, 1000, 20260222)
	queryKeys = append(queryKeys, "v1_orderstate_bench_missing_a", "v1_orderstate_bench_missing_b")

	expect, err := getManyByPointGet(db, queryKeys)
	if err != nil {
		b.Fatalf("baseline get failed: %v", err)
	}
	expectHits := len(expect)

	getManyGot, err := db.GetMany(queryKeys)
	if err != nil {
		b.Fatalf("GetMany warmup failed: %v", err)
	}
	assertKVMapEqual(b, getManyGot, expect)

	scanFilterGot, err := getManyByPrefixFilter(db, "v1_orderstate_", queryKeys)
	if err != nil {
		b.Fatalf("prefix scan+filter warmup failed: %v", err)
	}
	assertKVMapEqual(b, scanFilterGot, expect)
	b.StartTimer()

	b.Run("GetMany_pebble_seek", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			got, err := db.GetMany(queryKeys)
			if err != nil {
				b.Fatalf("GetMany failed: %v", err)
			}
			if len(got) != expectHits {
				b.Fatalf("unexpected hits: got=%d want=%d", len(got), expectHits)
			}
		}
	})

	b.Run("IteratePrefix_filter", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			got, err := getManyByPrefixFilter(db, "v1_orderstate_", queryKeys)
			if err != nil {
				b.Fatalf("prefix scan+filter failed: %v", err)
			}
			if len(got) != expectHits {
				b.Fatalf("unexpected hits: got=%d want=%d", len(got), expectHits)
			}
		}
	})

	b.Run("PointGet_loop", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			got, err := getManyByPointGet(db, queryKeys)
			if err != nil {
				b.Fatalf("point get loop failed: %v", err)
			}
			if len(got) != expectHits {
				b.Fatalf("unexpected hits: got=%d want=%d", len(got), expectHits)
			}
		}
	})
}

