package consensus

import (
	"fmt"
	"time"
)

// ============================================
// 工具函数
// ============================================

func Logf(format string, args ...interface{}) {
	now := time.Now()
	timestamp := now.Format("15:04:05.999")
	fmt.Printf("[%s] %s", timestamp, fmt.Sprintf(format, args...))
}

func minUint64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}
