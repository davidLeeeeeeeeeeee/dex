// frost/runtime/session/roast_test.go
// ROAST 会话单元测试

package session

import (
	"testing"
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

