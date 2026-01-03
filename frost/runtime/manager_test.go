package runtime

import (
	"context"
	"sync"
	"testing"
	"time"
)

// FakeNotifier 用于测试的 fake FinalityNotifier
type FakeNotifier struct {
	mu        sync.Mutex
	callbacks []func(height uint64)
}

func NewFakeNotifier() *FakeNotifier {
	return &FakeNotifier{
		callbacks: make([]func(height uint64), 0),
	}
}

// SubscribeBlockFinalized 实现 FinalityNotifier 接口
func (f *FakeNotifier) SubscribeBlockFinalized(fn func(height uint64)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callbacks = append(f.callbacks, fn)
}

// TriggerFinalized 触发 finalized 事件
func (f *FakeNotifier) TriggerFinalized(height uint64) {
	f.mu.Lock()
	callbacks := make([]func(height uint64), len(f.callbacks))
	copy(callbacks, f.callbacks)
	f.mu.Unlock()

	for _, cb := range callbacks {
		cb(height)
	}
}

func TestManager_StartStop(t *testing.T) {
	notifier := NewFakeNotifier()

	m := NewManager(
		ManagerConfig{NodeID: "test-node-1"},
		ManagerDeps{Notifier: notifier},
	)

	ctx := context.Background()

	// 验证初始状态
	if m.IsRunning() {
		t.Fatal("Manager should not be running before Start")
	}

	// 启动
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if !m.IsRunning() {
		t.Fatal("Manager should be running after Start")
	}

	// 停止
	m.Stop()

	if m.IsRunning() {
		t.Fatal("Manager should not be running after Stop")
	}
}

func TestManager_ReceiveFinalizedEvent(t *testing.T) {
	notifier := NewFakeNotifier()

	m := NewManager(
		ManagerConfig{NodeID: "test-node-1"},
		ManagerDeps{Notifier: notifier},
	)

	ctx := context.Background()

	// 用于同步的通道
	receivedHeights := make(chan uint64, 10)
	m.SetOnBlockFinalized(func(height uint64) {
		receivedHeights <- height
	})

	// 启动 Manager
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer m.Stop()

	// 触发 finalized 事件
	testHeights := []uint64{100, 101, 102, 200, 500}

	for _, h := range testHeights {
		notifier.TriggerFinalized(h)
	}

	// 验证收到的事件
	for _, expected := range testHeights {
		select {
		case received := <-receivedHeights:
			if received != expected {
				t.Errorf("Expected height %d, got %d", expected, received)
			}
		case <-time.After(time.Second):
			t.Fatalf("Timeout waiting for height %d", expected)
		}
	}

	// 验证 LastFinalizedHeight
	if got := m.LastFinalizedHeight(); got != 500 {
		t.Errorf("LastFinalizedHeight() = %d, want 500", got)
	}
}

func TestManager_IgnoreEventsAfterStop(t *testing.T) {
	notifier := NewFakeNotifier()

	m := NewManager(
		ManagerConfig{NodeID: "test-node-1"},
		ManagerDeps{Notifier: notifier},
	)

	ctx := context.Background()

	callCount := 0
	m.SetOnBlockFinalized(func(height uint64) {
		callCount++
	})

	// 启动并接收一个事件
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	notifier.TriggerFinalized(100)
	time.Sleep(10 * time.Millisecond) // 等待处理

	// 停止
	m.Stop()

	// 再触发事件
	notifier.TriggerFinalized(101)
	notifier.TriggerFinalized(102)
	time.Sleep(10 * time.Millisecond)

	// 验证只收到了停止前的事件
	if callCount != 1 {
		t.Errorf("Expected 1 call, got %d", callCount)
	}
}

