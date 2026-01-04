// frost/security/idempotency.go
// 幂等性检查工具

package security

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// IdempotencyChecker 幂等性检查器
type IdempotencyChecker struct {
	mu      sync.RWMutex
	seen    map[string]time.Time
	ttl     time.Duration
	maxSize int
}

// NewIdempotencyChecker 创建幂等性检查器
func NewIdempotencyChecker(ttl time.Duration, maxSize int) *IdempotencyChecker {
	ic := &IdempotencyChecker{
		seen:    make(map[string]time.Time),
		ttl:     ttl,
		maxSize: maxSize,
	}

	// 启动清理 goroutine
	go ic.cleanupLoop()

	return ic
}

// Check 检查并标记已处理
// 返回 true 表示这是首次处理
// 返回 false 表示已经处理过（重复）
func (ic *IdempotencyChecker) Check(key string) bool {
	ic.mu.Lock()
	defer ic.mu.Unlock()

	if _, exists := ic.seen[key]; exists {
		return false // 重复
	}

	// 超过最大容量时，清理最旧的条目
	if len(ic.seen) >= ic.maxSize {
		ic.evictOldest()
	}

	ic.seen[key] = time.Now()
	return true // 首次处理
}

// CheckWithHash 使用哈希作为 key
func (ic *IdempotencyChecker) CheckWithHash(data []byte) bool {
	hash := sha256.Sum256(data)
	key := hex.EncodeToString(hash[:])
	return ic.Check(key)
}

// evictOldest 清除最旧的条目
func (ic *IdempotencyChecker) evictOldest() {
	var oldestKey string
	var oldestTime time.Time

	for k, t := range ic.seen {
		if oldestKey == "" || t.Before(oldestTime) {
			oldestKey = k
			oldestTime = t
		}
	}

	if oldestKey != "" {
		delete(ic.seen, oldestKey)
	}
}

// cleanupLoop 定期清理过期条目
func (ic *IdempotencyChecker) cleanupLoop() {
	ticker := time.NewTicker(ic.ttl / 2)
	defer ticker.Stop()

	for range ticker.C {
		ic.cleanup()
	}
}

// cleanup 清理过期条目
func (ic *IdempotencyChecker) cleanup() {
	ic.mu.Lock()
	defer ic.mu.Unlock()

	now := time.Now()
	for k, t := range ic.seen {
		if now.Sub(t) > ic.ttl {
			delete(ic.seen, k)
		}
	}
}

// GenerateJobID 生成唯一的 Job ID
func GenerateJobID(chain string, vaultID uint32, epoch uint64, seq uint64) string {
	h := sha256.New()
	h.Write([]byte("frost_job"))
	h.Write([]byte(chain))
	h.Write([]byte(fmt.Sprintf("%d", vaultID)))
	h.Write([]byte(fmt.Sprintf("%d", epoch)))
	h.Write([]byte(fmt.Sprintf("%d", seq)))
	h.Write([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// GenerateWithdrawID 生成唯一的 Withdraw ID
func GenerateWithdrawID(chain string, asset string, seq uint64, height uint64) string {
	h := sha256.New()
	h.Write([]byte("frost_withdraw"))
	h.Write([]byte(chain))
	h.Write([]byte(asset))
	h.Write([]byte(fmt.Sprintf("%d", seq)))
	h.Write([]byte(fmt.Sprintf("%d", height)))
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// Size 返回当前跟踪的条目数
func (ic *IdempotencyChecker) Size() int {
	ic.mu.RLock()
	defer ic.mu.RUnlock()
	return len(ic.seen)
}

