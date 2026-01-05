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

// ========== 消息序号防重放 ==========

// SeqReplayGuard 基于 (sender, seq) 的消息防重放验证器
// 记录每个发送方的最大已见 seq，拒绝 seq <= maxSeen 的消息
type SeqReplayGuard struct {
	mu      sync.RWMutex
	maxSeqs map[string]uint64 // key = sender address, value = max seen seq
	window  uint64            // 允许的序号窗口（用于防止过大的 seq 跳跃）
}

// NewSeqReplayGuard 创建序号防重放验证器
// window: 允许的最大 seq 跳跃范围（0 表示不限制）
func NewSeqReplayGuard(window uint64) *SeqReplayGuard {
	return &SeqReplayGuard{
		maxSeqs: make(map[string]uint64),
		window:  window,
	}
}

// Check 检查消息序号是否有效（非重放）
// 返回: valid, isReplay, error
// - valid=true: 消息有效，已更新 maxSeq
// - valid=false, isReplay=true: 重放攻击（seq <= maxSeen）
// - valid=false, isReplay=false: 序号跳跃过大
func (g *SeqReplayGuard) Check(sender string, seq uint64) (valid bool, isReplay bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	maxSeen := g.maxSeqs[sender]

	// 检查是否为重放（seq <= maxSeen）
	if seq <= maxSeen && maxSeen > 0 {
		return false, true // 重放攻击
	}

	// 检查序号跳跃是否过大
	if g.window > 0 && seq > maxSeen+g.window {
		return false, false // 跳跃过大
	}

	// 更新 maxSeen
	g.maxSeqs[sender] = seq
	return true, false
}

// GetMaxSeq 获取发送方的最大已见序号
func (g *SeqReplayGuard) GetMaxSeq(sender string) uint64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.maxSeqs[sender]
}

// Reset 重置发送方的序号（用于测试或节点重启）
func (g *SeqReplayGuard) Reset(sender string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.maxSeqs, sender)
}

// ResetAll 重置所有序号记录
func (g *SeqReplayGuard) ResetAll() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.maxSeqs = make(map[string]uint64)
}
