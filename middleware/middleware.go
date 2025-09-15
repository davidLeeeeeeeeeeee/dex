package middleware

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// 全局变量，用于记录每个 IP 在当前时间窗口内的请求次数以及最后一次更新时间
var (
	ipRequestCount = make(map[string]int)
	ipLastReset    = make(map[string]time.Time)
	mu             sync.Mutex
)

// 配置参数
const (
	requestLimit    = 1000000000000   // 每个 IP 每个窗口允许的最大请求次数
	resetInterval   = time.Second     // 请求计数的时间窗口，1分钟
	cleanupInterval = 2 * time.Minute // 清理间隔，每2分钟清理一次不活跃记录
)

// RateLimit 是一个中间件，用于限制每个 IP 在 resetInterval 内的请求次数
func RateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 获取客户端 IP（简单处理，不处理 IPv6 格式等情况）
		clientIP := strings.Split(r.RemoteAddr, ":")[0]

		mu.Lock()
		now := time.Now()
		// 如果该 IP 不存在记录，或者上次记录的时间已经超过了 resetInterval，则重置计数
		if last, ok := ipLastReset[clientIP]; !ok || now.Sub(last) > resetInterval {
			ipRequestCount[clientIP] = 0
			ipLastReset[clientIP] = now
		}

		// 累加请求次数
		ipRequestCount[clientIP]++
		// 如果超过阈值，则返回 429 Too Many Requests
		if ipRequestCount[clientIP] > requestLimit {
			mu.Unlock()
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		mu.Unlock()

		// 调用下一个 Handler
		next.ServeHTTP(w, r)
	})
}

// StartIPCleanup 启动一个后台 goroutine，定时清理不活跃的 IP 记录
func StartIPCleanup() {
	ticker := time.NewTicker(cleanupInterval)
	go func() {
		for range ticker.C {
			mu.Lock()
			now := time.Now()
			// 遍历所有 IP，如果上次记录的时间超过 2 * resetInterval，则删除该 IP 记录
			for ip, last := range ipLastReset {
				if now.Sub(last) > 2*resetInterval {
					delete(ipLastReset, ip)
					delete(ipRequestCount, ip)
				}
			}
			mu.Unlock()
		}
	}()
}
