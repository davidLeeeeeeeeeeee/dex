// frost/runtime/session/roast_test.go
// ROAST 会话单元测试

package session

import (
	"testing"
	"time"
)

func createTestParticipants() []Participant {
	return []Participant{
		{Index: 1, Address: "addr1", IP: "127.0.0.1:6001"},
		{Index: 2, Address: "addr2", IP: "127.0.0.1:6002"},
		{Index: 3, Address: "addr3", IP: "127.0.0.1:6003"},
	}
}

func TestROASTSession_Start(t *testing.T) {
	participants := createTestParticipants()
	config := DefaultSignSessionConfig()
	config.AggregatorIndex = 1

	// 聚合者启动
	session := NewROASTSession("job1", 1, []byte("msg"), participants, 1, config)
	err := session.Start()
	if err != nil {
		t.Errorf("Aggregator should be able to start: %v", err)
	}

	if session.GetState() != SignSessionStateCollectingNonces {
		t.Errorf("State should be COLLECTING_NONCES, got %v", session.GetState())
	}

	// 非聚合者不能启动
	session2 := NewROASTSession("job2", 1, []byte("msg"), participants, 2, config)
	err = session2.Start()
	if err != ErrNotAggregator {
		t.Errorf("Non-aggregator should not be able to start: %v", err)
	}
}

func TestROASTSession_AddNonce(t *testing.T) {
	participants := createTestParticipants()
	config := DefaultSignSessionConfig()
	config.AggregatorIndex = 1
	config.MinSigners = 2

	session := NewROASTSession("job1", 1, []byte("msg"), participants, 1, config)
	_ = session.Start()

	// 添加第一个 nonce
	err := session.AddNonce(1, []byte("hiding1"), []byte("binding1"))
	if err != nil {
		t.Errorf("AddNonce should succeed: %v", err)
	}

	// 状态应该还是收集 nonce
	if session.GetState() != SignSessionStateCollectingNonces {
		t.Errorf("State should still be COLLECTING_NONCES")
	}

	// 添加第二个 nonce，应该转换状态
	err = session.AddNonce(2, []byte("hiding2"), []byte("binding2"))
	if err != nil {
		t.Errorf("AddNonce should succeed: %v", err)
	}

	if session.GetState() != SignSessionStateCollectingShares {
		t.Errorf("State should be COLLECTING_SHARES, got %v", session.GetState())
	}
}

func TestROASTSession_AddShare(t *testing.T) {
	participants := createTestParticipants()
	config := DefaultSignSessionConfig()
	config.AggregatorIndex = 1
	config.MinSigners = 2

	session := NewROASTSession("job1", 1, []byte("msg"), participants, 1, config)
	_ = session.Start()

	// 先收集 nonce
	_ = session.AddNonce(1, []byte("hiding1"), []byte("binding1"))
	_ = session.AddNonce(2, []byte("hiding2"), []byte("binding2"))

	// 添加签名份额
	err := session.AddShare(1, []byte("share1"))
	if err != nil {
		t.Errorf("AddShare should succeed: %v", err)
	}

	if session.GetState() != SignSessionStateCollectingShares {
		t.Errorf("State should still be COLLECTING_SHARES")
	}

	// 添加第二个份额，应该完成
	err = session.AddShare(2, []byte("share2"))
	if err != nil {
		t.Errorf("AddShare should succeed: %v", err)
	}

	if session.GetState() != SignSessionStateComplete {
		t.Errorf("State should be COMPLETE, got %v", session.GetState())
	}
}

func TestROASTSession_Retry(t *testing.T) {
	participants := createTestParticipants()
	config := DefaultSignSessionConfig()
	config.AggregatorIndex = 1
	config.MinSigners = 2
	config.MaxRetries = 2

	session := NewROASTSession("job1", 1, []byte("msg"), participants, 1, config)
	_ = session.Start()

	initialSet := session.GetSelectedSet()

	// 第一次重试
	err := session.Retry()
	if err != nil {
		t.Errorf("First retry should succeed: %v", err)
	}

	// 验证选择了不同的子集
	newSet := session.GetSelectedSet()
	if len(newSet) != len(initialSet) {
		t.Errorf("Set size should be same")
	}

	// 第二次重试
	err = session.Retry()
	if err != nil {
		t.Errorf("Second retry should succeed: %v", err)
	}

	// 第三次重试应该失败
	err = session.Retry()
	if err != ErrMaxRetriesExceeded {
		t.Errorf("Third retry should fail with ErrMaxRetriesExceeded: %v", err)
	}

	if session.GetState() != SignSessionStateFailed {
		t.Errorf("State should be FAILED")
	}
}

func TestROASTSession_SwitchAggregator(t *testing.T) {
	participants := createTestParticipants()
	config := DefaultSignSessionConfig()
	config.AggregatorIndex = 1

	session := NewROASTSession("job1", 1, []byte("msg"), participants, 1, config)

	// 切换聚合者
	newAgg := session.SwitchAggregator()
	if newAgg != 2 {
		t.Errorf("New aggregator should be 2, got %d", newAgg)
	}

	// 再次切换
	newAgg = session.SwitchAggregator()
	if newAgg != 3 {
		t.Errorf("New aggregator should be 3, got %d", newAgg)
	}

	// 轮回
	newAgg = session.SwitchAggregator()
	if newAgg != 1 {
		t.Errorf("New aggregator should wrap to 1, got %d", newAgg)
	}
}

// ========== Recovery Tests ==========

func TestRecoveryManager_PersistAndRecover(t *testing.T) {
	storage := NewMemorySessionStorage()
	rm := NewRecoveryManager(storage, nil)

	// 创建会话
	participants := createTestParticipants()
	config := DefaultSignSessionConfig()
	session := NewROASTSession("job1", 1, []byte("test message"), participants, 1, config)

	// 启动会话
	if err := session.Start(); err != nil {
		t.Fatalf("Failed to start session: %v", err)
	}

	// 持久化
	if err := rm.PersistSession(session); err != nil {
		t.Fatalf("Failed to persist session: %v", err)
	}

	// 检查持久化成功
	count, err := rm.GetPendingCount()
	if err != nil {
		t.Fatalf("Failed to get pending count: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 pending session, got %d", count)
	}

	// 恢复会话
	recovered, expired, err := rm.RecoverSessions()
	if err != nil {
		t.Fatalf("Failed to recover sessions: %v", err)
	}
	if len(expired) != 0 {
		t.Errorf("Expected no expired sessions, got %d", len(expired))
	}
	if len(recovered) != 1 {
		t.Fatalf("Expected 1 recovered session, got %d", len(recovered))
	}

	// 验证恢复的会话
	recoveredSession := recovered[0]
	if recoveredSession.JobID != "job1" {
		t.Errorf("Expected JobID 'job1', got '%s'", recoveredSession.JobID)
	}
	if string(recoveredSession.Message) != "test message" {
		t.Errorf("Expected message 'test message', got '%s'", string(recoveredSession.Message))
	}
	// 恢复后状态应该重置为 INIT
	if recoveredSession.State != SignSessionStateInit {
		t.Errorf("Expected state INIT after recovery, got %s", recoveredSession.State)
	}
}

func TestRecoveryManager_ExpiredSessions(t *testing.T) {
	storage := NewMemorySessionStorage()
	config := &RecoveryConfig{
		MaxRecoveryAge: 1 * time.Millisecond, // 极短的过期时间
		RetryDelay:     1 * time.Second,
	}
	rm := NewRecoveryManager(storage, config)

	// 创建并持久化会话
	participants := createTestParticipants()
	sessionConfig := DefaultSignSessionConfig()
	session := NewROASTSession("job1", 1, []byte("msg"), participants, 1, sessionConfig)
	if err := session.Start(); err != nil {
		t.Fatalf("Failed to start session: %v", err)
	}
	if err := rm.PersistSession(session); err != nil {
		t.Fatalf("Failed to persist session: %v", err)
	}

	// 等待过期
	time.Sleep(10 * time.Millisecond)

	// 恢复会话
	recovered, expired, err := rm.RecoverSessions()
	if err != nil {
		t.Fatalf("Failed to recover sessions: %v", err)
	}
	if len(recovered) != 0 {
		t.Errorf("Expected no recovered sessions, got %d", len(recovered))
	}
	if len(expired) != 1 {
		t.Errorf("Expected 1 expired session, got %d", len(expired))
	}
	if expired[0] != "job1" {
		t.Errorf("Expected expired job 'job1', got '%s'", expired[0])
	}
}

func TestMemorySessionStorage(t *testing.T) {
	storage := NewMemorySessionStorage()

	// 保存会话
	session := &PersistedSession{
		JobID:    "job1",
		KeyEpoch: 1,
		Message:  []byte("test"),
		State:    SignSessionStateCollectingNonces,
	}
	if err := storage.SaveSession(session); err != nil {
		t.Fatalf("Failed to save session: %v", err)
	}

	// 加载会话
	loaded, err := storage.LoadSession("job1")
	if err != nil {
		t.Fatalf("Failed to load session: %v", err)
	}
	if loaded.JobID != "job1" {
		t.Errorf("Expected JobID 'job1', got '%s'", loaded.JobID)
	}

	// 加载不存在的会话
	_, err = storage.LoadSession("nonexistent")
	if err != ErrSessionNotFound {
		t.Errorf("Expected ErrSessionNotFound, got %v", err)
	}

	// 删除会话
	if err := storage.DeleteSession("job1"); err != nil {
		t.Fatalf("Failed to delete session: %v", err)
	}
	_, err = storage.LoadSession("job1")
	if err != ErrSessionNotFound {
		t.Errorf("Expected ErrSessionNotFound after delete, got %v", err)
	}
}
