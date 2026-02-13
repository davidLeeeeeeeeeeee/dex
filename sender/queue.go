package sender

import (
	"dex/config"
	"dex/logs"
	"dex/stats"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 添加任务类型枚举
type TaskPriority int

const (
	PriorityData      TaskPriority = iota // 数据面：普通数据传输
	PriorityControl                       // 控制面：consensus相关
	PriorityImmediate                     // 紧急：区块丢失请求等需立即处理的任务
)

// SendTask 封装一次发送所需的信息
type SendTask struct {
	Target      string
	Message     interface{}
	RetryCount  int
	MaxRetries  int
	CreatedAt   time.Time // 任务创建时间，用于检测过期
	NextAttempt time.Time
	SendFunc    func(task *SendTask, client *http.Client) error
	HttpClient  *http.Client
	Priority    TaskPriority // 任务优先级
}

// SendQueue 负责管理任务队列 + worker
// 使用双队列：controlChan 用于共识消息，dataChan 用于普通数据
type SendQueue struct {
	controlWorkerCount int
	dataWorkerCount    int
	controlChan        chan *SendTask // 控制面队列（共识消息）
	immediateChan      chan *SendTask // 紧急面队列（区块补全消息）
	dataChan           chan *SendTask // 数据面队列（交易/同步）
	stopChan           chan struct{}
	wg                 sync.WaitGroup
	httpClient         *http.Client
	cfg                *config.Config
	nodeID             int              // 只用作log,不参与业务逻辑
	address            string           // 节点地址
	Logger             logs.Logger      // 注入的 Logger
	InflightMap        map[string]int32 // 目标->在途请求数
	InflightMutex      sync.RWMutex
	latency            *stats.LatencyRecorder

	// 发送侧关键计数（用于定位 hidden backlog / drop / timeout）
	delayedTimerBacklog atomic.Int64
	nextAttemptSleeping atomic.Int64
	dropImmediateFull   atomic.Uint64
	dropControlFull     atomic.Uint64
	dropDataFull        atomic.Uint64
	dropStaleWorker     atomic.Uint64
	dropStaleOverload   atomic.Uint64
	inflightRequeue     atomic.Uint64
	retryExhausted      atomic.Uint64
	retryExpired        atomic.Uint64
	sendSuccess         atomic.Uint64
	sendError           atomic.Uint64
	sendTimeout         atomic.Uint64
}

// 创建新的发送队列（双队列模式）
// controlWorkers: 控制面 worker 数量（处理共识消息）
// dataWorkers: 数据面 worker 数量（处理交易/同步）
func NewSendQueue(
	workerCount, queueCapacity int,
	httpClient *http.Client,
	nodeID int,
	address string,
	logger logs.Logger,
	cfg *config.Config,
) *SendQueue {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}

	// 分配 worker：控制面占 1/3，数据面占 2/3，最少各 1 个
	controlWorkers := workerCount / 3
	if controlWorkers < 1 {
		controlWorkers = 1
	}
	dataWorkers := workerCount - controlWorkers
	if dataWorkers < 1 {
		dataWorkers = 1
	}

	// 队列容量：控制面较小但更重要，数据面较大
	controlCapacity := queueCapacity / 4
	if controlCapacity < 64 {
		controlCapacity = 64
	}
	dataCapacity := queueCapacity - controlCapacity

	sq := &SendQueue{
		nodeID:             nodeID,
		address:            address,
		Logger:             logger,
		controlWorkerCount: controlWorkers,
		dataWorkerCount:    dataWorkers,
		controlChan:        make(chan *SendTask, controlCapacity),
		immediateChan:      make(chan *SendTask, 1024), // 紧急队列增加容量
		dataChan:           make(chan *SendTask, dataCapacity),
		stopChan:           make(chan struct{}),
		httpClient:         httpClient,
		cfg:                cfg,
		InflightMap:        make(map[string]int32),
		latency:            stats.NewLatencyRecorder(4096),
	}
	sq.Start()
	return sq
}

// Start 启动 worker 协程
func (sq *SendQueue) Start() {
	// 启动控制面 worker
	sq.wg.Add(sq.controlWorkerCount)
	for i := 0; i < sq.controlWorkerCount; i++ {
		go sq.workerLoop(i, sq.controlChan, "control")
	}

	// 启动紧急面 worker (共享控制面 worker 逻辑，但监听不同 chan)
	const immediateWorkers = 8
	sq.wg.Add(immediateWorkers)
	for i := 0; i < immediateWorkers; i++ {
		go sq.workerLoop(900+i, sq.immediateChan, "immediate")
	}

	// 启动数据面 worker
	sq.wg.Add(sq.dataWorkerCount)
	for i := 0; i < sq.dataWorkerCount; i++ {
		go sq.workerLoop(i, sq.dataChan, "data")
	}

	sq.Logger.Verbose("[SendQueue] Started with %d control workers + %d data workers",
		sq.controlWorkerCount, sq.dataWorkerCount)
}

// Stop 停止队列, 等待所有worker退出
func (sq *SendQueue) Stop() {
	close(sq.stopChan)
	sq.wg.Wait()
	log.Println("[SendQueue] Stopped.")
}

func (sq *SendQueue) Enqueue(task *SendTask) {
	if task == nil {
		return
	}
	// 首次入队时设置创建时间
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now()
	}
	if task.NextAttempt.IsZero() {
		task.NextAttempt = time.Now()
	}
	now := time.Now()
	if task.NextAttempt.After(now) {
		// 未到执行时间：先等到 NextAttempt，再真正入队
		delay := time.Until(task.NextAttempt)
		sq.delayedTimerBacklog.Add(1)
		go func(t *SendTask, d time.Duration) {
			defer sq.delayedTimerBacklog.Add(-1)
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-timer.C:
				sq.enqueueNow(t)
			case <-sq.stopChan:
				return
			}
		}(task, delay)
		return
	}
	sq.enqueueNow(task)
}

func (sq *SendQueue) enqueueNow(task *SendTask) {

	if task.Priority == PriorityImmediate {
		// 紧急任务：非阻塞尝试，失败则同步等待一个非常短的时间。
		select {
		case sq.immediateChan <- task:
			return
		default:
			// 如果紧急队列满了，强制丢弃老的，塞入新的（此处不实现复杂逻辑，仅快速失败记录）
			sq.dropImmediateFull.Add(1)
			sq.Logger.Warn("[SendQueue] Immediate task DROPPED: queue full target=%s", task.Target)
			return
		}
	} else if task.Priority == PriorityControl {
		// 控制面任务：非阻塞，满了直接丢弃并打警告，避免阻塞调用方
		select {
		case sq.controlChan <- task:
			return
		default:
			sq.dropControlFull.Add(1)
			sq.Logger.Warn("[SendQueue] Control queue FULL, dropping task target=%s func=%s len=%d",
				task.Target, task.FuncName(), len(sq.controlChan))
			return
		}
	} else {
		// 数据面任务：使用 dataChan，队列满了直接丢弃
		select {
		case sq.dataChan <- task:
			// 成功入队
		default:
			sq.dropDataFull.Add(1)
			sq.Logger.Debug("[SendQueue] Data task dropped: queue full len=%d, target=%s",
				len(sq.dataChan), task.Target)
		}
	}
}

// workerLoop 逐个获取队列任务并执行
func (sq *SendQueue) workerLoop(workerID int, taskChan chan *SendTask, queueType string) {
	defer sq.wg.Done()
	cfg := sq.cfg

	for {
		select {
		case <-sq.stopChan:
			return
		case task := <-taskChan:
			if task == nil {
				return
			}

			// 检查任务是否过期（超过 QueryTimeout 的请求直接丢弃）
			// 过期阈值 = QueryTimeout (3s)，避免发送已经无意义的请求
			taskAge := time.Since(task.CreatedAt)
			if taskAge > cfg.Sender.TaskExpireTimeout {
				sq.dropStaleWorker.Add(1)
				sq.Logger.Debug("[SendQueue][%s] Dropping stale task: age=%v target=%s func=%s",
					queueType, taskAge, task.Target, task.FuncName())
				continue
			}

			now := time.Now()
			if task.NextAttempt.After(now) {
				sleepDur := task.NextAttempt.Sub(now)
				sq.nextAttemptSleeping.Add(1)
				time.Sleep(sleepDur)
				sq.nextAttemptSleeping.Add(-1)
			}

			if !sq.tryAcquireInflight(task.Target, queueType) {
				sq.requeueForTargetOverload(task, queueType)
				continue
			}

			err := sq.doSend(task, workerID, queueType)

			sq.releaseInflight(task.Target)

			if err != nil {
				sq.handleRetry(task, err)
			}
		}
	}
}

func (sq *SendQueue) maxInflightPerTarget(queueType string) int {
	if sq == nil || sq.cfg == nil {
		return 0
	}
	switch queueType {
	case "control":
		return sq.cfg.Sender.ControlMaxInflightPerTarget
	case "immediate":
		return sq.cfg.Sender.ImmediateMaxInflightPerTarget
	default:
		return sq.cfg.Sender.DataMaxInflightPerTarget
	}
}

func (sq *SendQueue) tryAcquireInflight(target, queueType string) bool {
	limit := sq.maxInflightPerTarget(queueType)

	sq.InflightMutex.Lock()
	defer sq.InflightMutex.Unlock()

	current := sq.InflightMap[target]
	if limit > 0 && int(current) >= limit {
		return false
	}
	sq.InflightMap[target] = current + 1
	return true
}

func (sq *SendQueue) releaseInflight(target string) {
	sq.InflightMutex.Lock()
	defer sq.InflightMutex.Unlock()

	sq.InflightMap[target]--
	if sq.InflightMap[target] <= 0 {
		delete(sq.InflightMap, target)
	}
}

func (sq *SendQueue) requeueForTargetOverload(task *SendTask, queueType string) {
	if task == nil {
		return
	}
	cfg := sq.cfg
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	if time.Since(task.CreatedAt) > cfg.Sender.TaskExpireTimeout {
		sq.dropStaleOverload.Add(1)
		sq.Logger.Debug("[SendQueue][%s] Drop stale overloaded task: age=%v target=%s func=%s",
			queueType, time.Since(task.CreatedAt), task.Target, task.FuncName())
		return
	}

	delay := cfg.Sender.InflightRequeueDelay
	if delay <= 0 {
		delay = 25 * time.Millisecond
	}
	// 轻微抖动，避免大量任务同一时刻回灌
	jitterRange := float64(delay) * 0.2
	jitter := time.Duration(jitterRange * (rand.Float64()*2 - 1))
	task.NextAttempt = time.Now().Add(delay + jitter)
	sq.inflightRequeue.Add(1)
	sq.Enqueue(task)
}

func (sq *SendQueue) doSend(task *SendTask, workerID int, queueType string) error {
	if task.SendFunc == nil {
		return fmt.Errorf("SendFunc is nil, cannot send")
	}
	// 设置任务的 HTTP 客户端
	task.HttpClient = sq.httpClient

	start := time.Now()
	err := task.SendFunc(task, sq.httpClient)
	elapsed := time.Since(start)
	if sq.latency != nil {
		sq.latency.Record("sendqueue.do_send."+queueType, elapsed)
		if funcName := compactFuncName(task.FuncName()); funcName != "" {
			sq.latency.Record("sendqueue.do_send.func."+funcName, elapsed)
		}
	}

	if err != nil {
		sq.sendError.Add(1)
		if isTimeoutSendError(err) {
			sq.sendTimeout.Add(1)
		}
		sq.Logger.Error("[SendQueue][%s] worker=%d,%s send to %s FAILED after %v: %v",
			queueType, workerID, task.FuncName(), task.Target, elapsed, err)
	} else {
		sq.sendSuccess.Add(1)
		sq.Logger.Trace("[SendQueue][%s] worker=%d,%s send to %s success in %v",
			queueType, workerID, task.FuncName(), task.Target, elapsed)
	}
	return err
}

func (sq *SendQueue) handleRetry(task *SendTask, sendErr error) {
	task.RetryCount++
	cfg := sq.cfg

	// 检查是否超过最大重试次数
	if task.RetryCount > task.MaxRetries {
		sq.retryExhausted.Add(1)
		sq.Logger.Debug("[SendQueue] Exceed max retries(%d) target=%s func=%s, giving up",
			task.MaxRetries, task.Target, task.FuncName())
		return
	}

	// 检查任务是否已过期（即使还有重试次数，过期也放弃）
	if time.Since(task.CreatedAt) > cfg.Sender.TaskExpireTimeout {
		sq.retryExpired.Add(1)
		sq.Logger.Debug("[SendQueue] Task expired after %v, giving up retry target=%s func=%s",
			time.Since(task.CreatedAt), task.Target, task.FuncName())
		return
	}

	// 指数退避 + 随机抖动
	// backoff = baseDelay * 2^(retryCount-1) * (1 ± jitter)
	baseDelay := cfg.Sender.BaseRetryDelay
	backoff := baseDelay * time.Duration(math.Pow(2, float64(task.RetryCount-1)))

	// 限制最大退避时间
	if backoff > cfg.Sender.MaxRetryDelay {
		backoff = cfg.Sender.MaxRetryDelay
	}

	// 添加随机抖动：±JitterFactor (例如 ±30%)
	jitterRange := float64(backoff) * cfg.Sender.JitterFactor
	jitter := time.Duration(jitterRange * (rand.Float64()*2 - 1)) // -jitterRange 到 +jitterRange
	backoff += jitter

	task.NextAttempt = time.Now().Add(backoff)
	sq.Enqueue(task)
	sq.Logger.Debug("[SendQueue] Retry %d/%d after %v for %s (err=%v)",
		task.RetryCount, task.MaxRetries, backoff, task.Target, sendErr)
}

func isTimeoutSendError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "timeout") || strings.Contains(low, "deadline exceeded")
}

func compactFuncName(full string) string {
	if full == "" {
		return ""
	}
	// e.g. dex/sender.doSendPullQuery -> sender.doSendPullQuery
	if idx := strings.LastIndex(full, "/"); idx >= 0 && idx < len(full)-1 {
		return full[idx+1:]
	}
	return full
}

type SendQueueRuntimeStats struct {
	DelayedTimerBacklog int64
	NextAttemptSleeping int64
	DropImmediateFull   uint64
	DropControlFull     uint64
	DropDataFull        uint64
	DropStaleWorker     uint64
	DropStaleOverload   uint64
	InflightRequeue     uint64
	RetryExhausted      uint64
	RetryExpired        uint64
	SendSuccess         uint64
	SendError           uint64
	SendTimeout         uint64
}

func (task *SendTask) FuncName() string {
	if task.SendFunc == nil {
		return ""
	}
	// 通过反射拿到函数指针，然后用 runtime.FuncForPC 获得函数信息
	funcPtr := reflect.ValueOf(task.SendFunc).Pointer()
	f := runtime.FuncForPC(funcPtr)
	if f != nil {
		return f.Name()
	} else {
		return ""
	}
}

// QueueLen 返回两个队列的总长度（用于监控）
func (sq *SendQueue) QueueLen() int {
	return len(sq.controlChan) + len(sq.dataChan)
}

// ControlQueueLen 返回控制面队列长度
func (sq *SendQueue) ControlQueueLen() int {
	return len(sq.controlChan)
}

// DataQueueLen 返回数据面队列长度
func (sq *SendQueue) DataQueueLen() int {
	return len(sq.dataChan)
}

// ImmediateQueueLen 返回紧急队列长度
func (sq *SendQueue) ImmediateQueueLen() int {
	return len(sq.immediateChan)
}

// GetChannelStats 返回所有队列的 channel 状态
func (sq *SendQueue) GetChannelStats() []stats.ChannelStat {
	return []stats.ChannelStat{
		stats.NewChannelStat("controlChan", "SendQueue", len(sq.controlChan), cap(sq.controlChan)),
		stats.NewChannelStat("immediateChan", "SendQueue", len(sq.immediateChan), cap(sq.immediateChan)),
		stats.NewChannelStat("dataChan", "SendQueue", len(sq.dataChan), cap(sq.dataChan)),
	}
}

func (sq *SendQueue) GetRuntimeStats() SendQueueRuntimeStats {
	if sq == nil {
		return SendQueueRuntimeStats{}
	}
	return SendQueueRuntimeStats{
		DelayedTimerBacklog: sq.delayedTimerBacklog.Load(),
		NextAttemptSleeping: sq.nextAttemptSleeping.Load(),
		DropImmediateFull:   sq.dropImmediateFull.Load(),
		DropControlFull:     sq.dropControlFull.Load(),
		DropDataFull:        sq.dropDataFull.Load(),
		DropStaleWorker:     sq.dropStaleWorker.Load(),
		DropStaleOverload:   sq.dropStaleOverload.Load(),
		InflightRequeue:     sq.inflightRequeue.Load(),
		RetryExhausted:      sq.retryExhausted.Load(),
		RetryExpired:        sq.retryExpired.Load(),
		SendSuccess:         sq.sendSuccess.Load(),
		SendError:           sq.sendError.Load(),
		SendTimeout:         sq.sendTimeout.Load(),
	}
}

func (sq *SendQueue) GetLatencyStats(reset bool) map[string]stats.LatencySummary {
	if sq == nil || sq.latency == nil {
		return nil
	}
	return sq.latency.Snapshot(reset)
}
