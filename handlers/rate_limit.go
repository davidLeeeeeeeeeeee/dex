package handlers

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// pathDropStats 用于记录各个路径丢弃的请求统计
type pathDropStats struct {
	mu          sync.Mutex
	dropCount   int64
	lastLogTime time.Time
}

// RateLimiter 针对不同 API 路由的速率限制器
type RateLimiter struct {
	mu       sync.RWMutex
	limiters map[string]*rate.Limiter

	// 存储配置好的速率，以便动态设置
	rates map[string]float64
	stats sync.Map // 存储 string(path) -> *pathDropStats
}

// NewRateLimiter 生成一个新的 RateLimiter
func NewRateLimiter(rates map[string]float64) *RateLimiter {
	if rates == nil {
		rates = make(map[string]float64)
	}
	return &RateLimiter{
		limiters: make(map[string]*rate.Limiter),
		rates:    rates,
	}
}

// getLimiter 获取对应路径的 limiter，如果预配置了限制就会应用，否则由于没有预设会被放行（默认允许）
func (rl *RateLimiter) getLimiter(path string) *rate.Limiter {
	rl.mu.RLock()
	limiter, exists := rl.limiters[path]
	rl.mu.RUnlock()

	if exists {
		return limiter
	}

	// 如果不存在，查找是否配置了限流速率
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Double check
	limiter, exists = rl.limiters[path]
	if exists {
		return limiter
	}

	rps, hasRate := rl.rates[path]
	if !hasRate || rps <= 0 {
		// 没有配置限制，放行, 返回 nil limiter 表示不限速
		rl.limiters[path] = nil
		return nil
	}

	// 创建新的 limiter (RPS, Burst 设为 RPS 方便适应瞬时流量，如果 RPS < 1 则 burst 设为 1)
	burst := int(rps)
	if burst < 1 {
		burst = 1
	}
	limiter = rate.NewLimiter(rate.Limit(rps), burst)
	rl.limiters[path] = limiter
	return limiter
}

// LimitMiddleware 速率限制中间件
func (rl *RateLimiter) LimitMiddleware(path string, handlerFunc http.HandlerFunc, logger interface {
	Warn(format string, args ...interface{})
}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limiter := rl.getLimiter(path)
		if limiter != nil && !limiter.Allow() {
			// 如果有配置的 limiter 且不允许执行了，就返回 429
			// 解析出客户端 IP (去除端口号)
			clientIP := r.RemoteAddr
			if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
				clientIP = host
			}

			rl.logDropEvent(path, clientIP, logger)

			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}

		handlerFunc.ServeHTTP(w, r)
	}
}

// logDropEvent 记录丢弃信息并定时汇总打印
func (rl *RateLimiter) logDropEvent(path string, clientIP string, logger interface {
	Warn(format string, args ...interface{})
}) {
	if logger == nil {
		return
	}

	val, _ := rl.stats.LoadOrStore(path, &pathDropStats{
		lastLogTime: time.Now(),
	})
	st := val.(*pathDropStats)

	st.mu.Lock()
	st.dropCount++
	now := time.Now()

	// 汇总 5 秒内的限流命中次数
	if now.Sub(st.lastLogTime) >= 5*time.Second {
		count := st.dropCount
		st.dropCount = 0
		st.lastLogTime = now
		st.mu.Unlock()

		// 打印汇总日志
		logger.Warn("[RateLimit] Path: %s is overloaded! Dropped %d requests in the last 5s (Latest from IP: %s)", path, count, clientIP)
		return
	}
	st.mu.Unlock()
}
