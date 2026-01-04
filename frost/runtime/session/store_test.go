// frost/runtime/session/store_test.go
// SessionStore 单元测试

package session

import (
	"bytes"
	"testing"
)

func TestSessionStore_BindNonce(t *testing.T) {
	store := NewSessionStore(nil)

	nonce := []byte("test_nonce_32_bytes_padding_here")
	msg := []byte("test message")

	// 首次绑定应成功
	err := store.BindNonce(nonce, msg, 1)
	if err != nil {
		t.Errorf("First bind should succeed: %v", err)
	}

	// 相同消息再次绑定应成功（幂等）
	err = store.BindNonce(nonce, msg, 1)
	if err != nil {
		t.Errorf("Idempotent bind should succeed: %v", err)
	}

	// 不同消息绑定应失败
	differentMsg := []byte("different message")
	err = store.BindNonce(nonce, differentMsg, 1)
	if err != ErrNonceAlreadyBound {
		t.Errorf("Expected ErrNonceAlreadyBound, got %v", err)
	}
}

func TestSessionStore_ValidateNonceForMessage(t *testing.T) {
	store := NewSessionStore(nil)

	nonce := []byte("test_nonce_32_bytes_padding_here")
	msg := []byte("test message")

	// 未绑定的 nonce 应该可用
	if !store.ValidateNonceForMessage(nonce, msg) {
		t.Error("Unbound nonce should be valid for any message")
	}

	// 绑定后
	_ = store.BindNonce(nonce, msg, 1)

	// 相同消息应该可用
	if !store.ValidateNonceForMessage(nonce, msg) {
		t.Error("Bound nonce should be valid for same message")
	}

	// 不同消息应该不可用
	differentMsg := []byte("different message")
	if store.ValidateNonceForMessage(nonce, differentMsg) {
		t.Error("Bound nonce should not be valid for different message")
	}
}

func TestSessionStore_GetNonceBind(t *testing.T) {
	store := NewSessionStore(nil)

	nonce := []byte("test_nonce_32_bytes_padding_here")
	msg := []byte("test message")

	// 未绑定时应返回 false
	_, _, exists := store.GetNonceBind(nonce)
	if exists {
		t.Error("Unbound nonce should not exist")
	}

	// 绑定后应返回正确信息
	_ = store.BindNonce(nonce, msg, 42)

	msgHash, keyEpoch, exists := store.GetNonceBind(nonce)
	if !exists {
		t.Error("Bound nonce should exist")
	}
	if keyEpoch != 42 {
		t.Errorf("Expected keyEpoch 42, got %d", keyEpoch)
	}

	// 验证 msgHash 是消息的 SHA256
	expectedHash := hashMessage(msg)
	if !bytes.Equal(msgHash, expectedHash) {
		t.Error("MsgHash should match")
	}
}

func TestSessionStore_ReleaseNonce(t *testing.T) {
	store := NewSessionStore(nil)

	nonce := []byte("test_nonce_32_bytes_padding_here")
	msg := []byte("test message")

	// 绑定
	_ = store.BindNonce(nonce, msg, 1)

	// 释放
	err := store.ReleaseNonce(nonce)
	if err != nil {
		t.Errorf("Release should succeed: %v", err)
	}

	// 释放后应不存在
	_, _, exists := store.GetNonceBind(nonce)
	if exists {
		t.Error("Released nonce should not exist")
	}

	// 释放后可以绑定到不同消息
	differentMsg := []byte("different message")
	err = store.BindNonce(nonce, differentMsg, 2)
	if err != nil {
		t.Errorf("Bind after release should succeed: %v", err)
	}
}

func TestMemoryNonceStore(t *testing.T) {
	store := NewMemoryNonceStore()

	nonce := []byte("test_nonce")
	msgHash := []byte("test_hash")

	// 保存
	err := store.SaveNonce(nonce, msgHash, 100)
	if err != nil {
		t.Errorf("SaveNonce should succeed: %v", err)
	}

	// 获取
	hash, epoch, exists := store.GetNonceBind(nonce)
	if !exists {
		t.Error("Nonce should exist")
	}
	if !bytes.Equal(hash, msgHash) {
		t.Error("Hash should match")
	}
	if epoch != 100 {
		t.Errorf("Expected epoch 100, got %d", epoch)
	}

	// 删除
	err = store.DeleteNonce(nonce)
	if err != nil {
		t.Errorf("DeleteNonce should succeed: %v", err)
	}

	_, _, exists = store.GetNonceBind(nonce)
	if exists {
		t.Error("Deleted nonce should not exist")
	}
}

