// frost/runtime/session/roast_test.go
// ROAST 会话单元测试

package session

import (
	"testing"
	"time"
)

func createTestCommittee() []Participant {
	return []Participant{
		{ID: "node1", Index: 0, Address: "addr1", IP: "127.0.0.1:6001"},
		{ID: "node2", Index: 1, Address: "addr2", IP: "127.0.0.1:6002"},
		{ID: "node3", Index: 2, Address: "addr3", IP: "127.0.0.1:6003"},
	}
}

func TestSession_Start(t *testing.T) {
	committee := createTestCommittee()
	sess := NewSession(SessionParams{
		JobID:     "job1",
		Messages:  [][]byte{[]byte("msg")},
		Committee: committee,
		Threshold: 1,
		MyIndex:   0,
	})

	if err := sess.Start(); err != nil {
		t.Fatalf("Start should succeed: %v", err)
	}
	if sess.GetState() != SignSessionStateCollectingNonces {
		t.Errorf("State should be COLLECTING_NONCES, got %v", sess.GetState())
	}
	if len(sess.SelectedSetSnapshot()) != 2 {
		t.Errorf("Selected set should have 2 members")
	}
}

func TestSession_AddNonce(t *testing.T) {
	committee := createTestCommittee()
	sess := NewSession(SessionParams{
		JobID:     "job1",
		Messages:  [][]byte{[]byte("msg")},
		Committee: committee,
		Threshold: 1,
		MyIndex:   0,
	})
	_ = sess.Start()

	err := sess.AddNonce(0, [][]byte{[]byte("hiding1")}, [][]byte{[]byte("binding1")})
	if err != nil {
		t.Fatalf("AddNonce should succeed: %v", err)
	}
	if sess.HasEnoughNonces() {
		t.Errorf("Should not have enough nonces yet")
	}

	err = sess.AddNonce(1, [][]byte{[]byte("hiding2")}, [][]byte{[]byte("binding2")})
	if err != nil {
		t.Fatalf("AddNonce should succeed: %v", err)
	}
	if !sess.HasEnoughNonces() {
		t.Errorf("Should have enough nonces")
	}
}

func TestSession_AddShare(t *testing.T) {
	committee := createTestCommittee()
	sess := NewSession(SessionParams{
		JobID:     "job1",
		Messages:  [][]byte{[]byte("msg")},
		Committee: committee,
		Threshold: 1,
		MyIndex:   0,
	})
	_ = sess.Start()
	sess.SetState(SignSessionStateCollectingShares)

	err := sess.AddShare(0, [][]byte{[]byte("share1")})
	if err != nil {
		t.Fatalf("AddShare should succeed: %v", err)
	}
	if sess.HasEnoughShares() {
		t.Errorf("Should not have enough shares yet")
	}

	err = sess.AddShare(1, [][]byte{[]byte("share2")})
	if err != nil {
		t.Fatalf("AddShare should succeed: %v", err)
	}
	if !sess.HasEnoughShares() {
		t.Errorf("Should have enough shares")
	}
}

func TestSession_ResetForRetry(t *testing.T) {
	committee := createTestCommittee()
	sess := NewSession(SessionParams{
		JobID:     "job1",
		Messages:  [][]byte{[]byte("msg")},
		Committee: committee,
		Threshold: 1,
		MyIndex:   0,
	})
	sess.SelectInitialSet()
	initial := sess.SelectedSetSnapshot()

	if !sess.ResetForRetry(2) {
		t.Fatalf("First retry should succeed")
	}
	next := sess.SelectedSetSnapshot()
	if len(next) != len(initial) {
		t.Errorf("Selected set size should remain the same")
	}

	if !sess.ResetForRetry(2) {
		t.Fatalf("Second retry should succeed")
	}
	if sess.ResetForRetry(2) {
		t.Fatalf("Third retry should fail")
	}
}

// ========== Recovery Tests ==========

func TestRecoveryManager_PersistAndRecover(t *testing.T) {
	storage := NewMemorySessionStorage()
	rm := NewRecoveryManager(storage, nil)

	committee := createTestCommittee()
	sess := NewSession(SessionParams{
		JobID:     "job1",
		Messages:  [][]byte{[]byte("test message")},
		Committee: committee,
		Threshold: 1,
		MyIndex:   0,
	})
	if err := sess.Start(); err != nil {
		t.Fatalf("Failed to start session: %v", err)
	}

	if err := rm.PersistSession(sess); err != nil {
		t.Fatalf("Failed to persist session: %v", err)
	}

	count, err := rm.GetPendingCount()
	if err != nil {
		t.Fatalf("Failed to get pending count: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 pending session, got %d", count)
	}

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

	recoveredSession := recovered[0]
	if recoveredSession.JobID != "job1" {
		t.Errorf("Expected JobID 'job1', got '%s'", recoveredSession.JobID)
	}
	if string(recoveredSession.Messages[0]) != "test message" {
		t.Errorf("Expected message 'test message', got '%s'", string(recoveredSession.Messages[0]))
	}
	if recoveredSession.State != SignSessionStateInit {
		t.Errorf("Expected state INIT after recovery, got %s", recoveredSession.State)
	}
}

func TestRecoveryManager_ExpiredSessions(t *testing.T) {
	storage := NewMemorySessionStorage()
	config := &RecoveryConfig{
		MaxRecoveryAge: 1 * time.Millisecond,
		RetryDelay:     1 * time.Second,
	}
	rm := NewRecoveryManager(storage, config)

	committee := createTestCommittee()
	sess := NewSession(SessionParams{
		JobID:     "job1",
		Messages:  [][]byte{[]byte("msg")},
		Committee: committee,
		Threshold: 1,
		MyIndex:   0,
	})
	if err := sess.Start(); err != nil {
		t.Fatalf("Failed to start session: %v", err)
	}
	if err := rm.PersistSession(sess); err != nil {
		t.Fatalf("Failed to persist session: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

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

	session := &PersistedSession{
		JobID:    "job1",
		KeyEpoch: 1,
		State:    SignSessionStateCollectingNonces,
	}
	if err := storage.SaveSession(session); err != nil {
		t.Fatalf("Failed to save session: %v", err)
	}

	loaded, err := storage.LoadSession("job1")
	if err != nil {
		t.Fatalf("Failed to load session: %v", err)
	}
	if loaded.JobID != "job1" {
		t.Errorf("Expected JobID 'job1', got '%s'", loaded.JobID)
	}

	_, err = storage.LoadSession("nonexistent")
	if err != ErrSessionNotFound {
		t.Errorf("Expected ErrSessionNotFound, got %v", err)
	}

	if err := storage.DeleteSession("job1"); err != nil {
		t.Fatalf("Failed to delete session: %v", err)
	}
	_, err = storage.LoadSession("job1")
	if err != ErrSessionNotFound {
		t.Errorf("Expected ErrSessionNotFound after delete, got %v", err)
	}
}
