package db

import (
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
)

// 触发后台批量写并等待其完成
func flushAndWait(t *testing.T, mgr *Manager) {
	t.Helper()
	if err := mgr.ForceFlush(); err != nil {
		t.Fatalf("force flush: %v", err)
	}
}

func openMemDB(t *testing.T) *pebble.DB {
	t.Helper()
	db, err := pebble.Open("", &pebble.Options{FS: vfs.NewMem()})
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	return db
}

func TestForceFlushIsSynchronous(t *testing.T) {
	db := openMemDB(t)
	mgr := &Manager{Db: db}
	mgr.InitWriteQueue(1_000, time.Hour)
	t.Cleanup(func() { mgr.Close() })

	mgr.EnqueueSet("sync:test:key", "sync-value")
	if err := mgr.ForceFlush(); err != nil {
		t.Fatalf("force flush: %v", err)
	}
	got, err := mgr.GetKV("sync:test:key")
	if err != nil {
		t.Fatalf("read after force flush: %v", err)
	}
	if string(got) != "sync-value" {
		t.Fatalf("expected sync-value, got %q", string(got))
	}
}

// TestIndexAllocator - 验证 FIFO 复用逻辑
func TestIndexAllocator(t *testing.T) {
	db := openMemDB(t)

	mgr := &Manager{Db: db}
	mgr.InitWriteQueue(1_000, 200*time.Millisecond)

	// ---------- 首次 idx1 ----------
	idx1, tasks, err := getNewIndex(mgr)
	if err != nil {
		t.Fatalf("getNewIndex-1: %v", err)
	}
	if idx1 != 1 {
		t.Fatalf("expected idx1==1, got %d", idx1)
	}
	for _, w := range tasks {
		mgr.writeQueueChan <- w
	}
	flushAndWait(t, mgr)

	// ---------- idx2 ----------
	idx2, tasks, err := getNewIndex(mgr)
	if err != nil {
		t.Fatalf("getNewIndex-2: %v", err)
	}
	if idx2 != 2 {
		t.Fatalf("expected idx2==2, got %d", idx2)
	}
	for _, w := range tasks {
		mgr.writeQueueChan <- w
	}
	flushAndWait(t, mgr)

	// ---------- 回收 idx1 ----------
	mgr.writeQueueChan <- removeIndex(idx1)
	flushAndWait(t, mgr)

	// ---------- idx3 - 应复用 idx1 ----------
	idx3, tasks, err := getNewIndex(mgr)
	if err != nil {
		t.Fatalf("getNewIndex-3: %v", err)
	}
	if idx3 != idx1 {
		t.Fatalf("expected recycled idx %d, got %d", idx1, idx3)
	}
	for _, w := range tasks {
		mgr.writeQueueChan <- w
	}
	flushAndWait(t, mgr)

	// ---------- idx4 - 新增应为 3 ----------
	idx4, tasks, err := getNewIndex(mgr)
	if err != nil {
		t.Fatalf("getNewIndex-4: %v", err)
	}
	if idx4 != 3 {
		t.Fatalf("expected idx4==3, got %d", idx4)
	}
	for _, w := range tasks {
		mgr.writeQueueChan <- w
	}
	flushAndWait(t, mgr)
	mgr.Close()
}

// TestIndexAllocatorPerformance - 分配 10 个 index 的速度
func TestIndexAllocatorPerformance(t *testing.T) {
	const n = 10
	db := openMemDB(t)

	mgr := &Manager{Db: db}
	mgr.InitWriteQueue(100000, 200*time.Millisecond)

	start := time.Now()
	for i := 1; i <= n; i++ {
		idx, tasks, err := getNewIndex(mgr)
		if err != nil {
			t.Fatalf("alloc %d: %v", i, err)
		}
		if uint64(i) != idx {
			t.Fatalf("expect %d, got %d", i, idx)
		}
		for _, w := range tasks {
			mgr.writeQueueChan <- w
		}
		flushAndWait(t, mgr)
	}
	dur := time.Since(start)
	t.Logf("allocated %d indexes in %s (%.2f µs/idx)", n, dur, float64(dur.Microseconds())/float64(n))
	if dur > time.Second {
		t.Fatalf("too slow: %s for %d allocations", dur, n)
	}
	mgr.Close()
}
