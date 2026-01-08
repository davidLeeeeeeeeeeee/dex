package db

import (
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// 触发后台批量写并等待其完成
func flushAndWait(mgr *Manager) {
	mgr.ForceFlush()
	time.Sleep(50 * time.Millisecond) // 让 flushBatch 有时间跑完
}

// -----------------------------------------------------------------------------
// TestIndexAllocator - 验证 FIFO 复用逻辑
// -----------------------------------------------------------------------------
func TestIndexAllocator(t *testing.T) {
	// 1. 内存 Badger
	db, err := badger.Open(badger.DefaultOptions("").WithInMemory(true))
	if err != nil {
		t.Fatalf("open badger: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// 2. Manager
	mgr := &Manager{Db: db}
	mgr.seq, _ = db.GetSequence([]byte("seq"), 1000)
	mgr.InitWriteQueue(1_000, 200*time.Millisecond) // flushInterval 随便设

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
	flushAndWait(mgr)

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
	flushAndWait(mgr)

	// ---------- 回收 idx1 ----------
	mgr.writeQueueChan <- removeIndex(idx1)
	flushAndWait(mgr)

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
	flushAndWait(mgr)

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
	flushAndWait(mgr)

	mgr.Close()
}

// -----------------------------------------------------------------------------
// TestIndexAllocatorPerformance - 分配 2 000 个 index 的速度
// -----------------------------------------------------------------------------
func TestIndexAllocatorPerformance(t *testing.T) {
	const n = 10

	db, err := badger.Open(badger.DefaultOptions("").WithInMemory(true))
	if err != nil {
		t.Fatalf("open badger: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mgr := &Manager{Db: db}
	mgr.seq, _ = db.GetSequence([]byte("seq"), 1000)
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
		flushAndWait(mgr) // 保证 meta:max_index 立刻可见
	}
	dur := time.Since(start)
	t.Logf("allocated %d indexes in %s (%.2f µs/idx)", n, dur, float64(dur.Microseconds())/float64(n))

	if dur > time.Second {
		t.Fatalf("too slow: %s for %d allocations", dur, n)
	}

	mgr.Close()
}
