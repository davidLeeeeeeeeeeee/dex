package sender

import (
	"dex/config"
	"dex/logs"
	"fmt"
	"log"
	"math"
	"net/http"
	"reflect"
	"runtime"
	"sync"
	"time"
)

// 添加任务类型枚举
type TaskPriority int

const (
	PriorityControl TaskPriority = iota // 控制面：consensus相关
	PriorityData                        // 数据面：普通数据传输
)

// SendTask 封装一次发送所需的信息
type SendTask struct {
	Target      string
	Message     interface{}
	RetryCount  int
	MaxRetries  int
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
	dataChan           chan *SendTask // 数据面队列（交易/同步）
	stopChan           chan struct{}
	wg                 sync.WaitGroup
	httpClient         *http.Client
	nodeID             int              // 只用作log,不参与业务逻辑
	address            string           // 节点地址
	Logger             logs.Logger      // 注入的 Logger
	InflightMap        map[string]int32 // 目标->在途请求数
	InflightMutex      sync.RWMutex
}

// 创建新的发送队列（双队列模式）
// controlWorkers: 控制面 worker 数量（处理共识消息）
// dataWorkers: 数据面 worker 数量（处理交易/同步）
func NewSendQueue(workerCount, queueCapacity int, httpClient *http.Client, nodeID int, address string, logger logs.Logger) *SendQueue {
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
		dataChan:           make(chan *SendTask, dataCapacity),
		stopChan:           make(chan struct{}),
		httpClient:         httpClient,
		InflightMap:        make(map[string]int32),
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
	if task.NextAttempt.IsZero() {
		task.NextAttempt = time.Now()
	}
	now := time.Now()
	if task.NextAttempt.After(now) {
		// 未到执行时间：先等到 NextAttempt，再真正入队
		delay := time.Until(task.NextAttempt)
		go func(t *SendTask, d time.Duration) {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-timer.C:
				sq.enqueueNow(t) // 见下
			case <-sq.stopChan: // 若你有 stopChan
				return
			}
		}(task, delay)
		return
	}
	sq.enqueueNow(task)
}

func (sq *SendQueue) enqueueNow(task *SendTask) {
	cfg := config.DefaultConfig()

	if task.Priority == PriorityControl {
		// 控制面任务：使用 controlChan，给一个短暂的阻塞窗口
		select {
		case sq.controlChan <- task:
			return
		case <-time.After(cfg.Sender.ControlTaskTimeout):
			sq.Logger.Error("[SendQueue] Control task timeout: len=%d cap=%d target=%s",
				len(sq.controlChan), cap(sq.controlChan), task.Target)
			return
		}
	} else {
		// 数据面任务：使用 dataChan，队列满了直接丢弃
		select {
		case sq.dataChan <- task:
			// 成功入队
		default:
			sq.Logger.Debug("[SendQueue] Data task dropped: queue full len=%d, target=%s",
				len(sq.dataChan), task.Target)
		}
	}
}

// workerLoop 逐个获取队列任务并执行
func (sq *SendQueue) workerLoop(workerID int, taskChan chan *SendTask, queueType string) {
	defer sq.wg.Done()

	for {
		select {
		case <-sq.stopChan:
			return
		case task := <-taskChan:
			if task == nil {
				return
			}
			now := time.Now()
			if task.NextAttempt.After(now) {
				sleepDur := task.NextAttempt.Sub(now)
				time.Sleep(sleepDur)
			}

			sq.InflightMutex.Lock()
			sq.InflightMap[task.Target]++
			sq.InflightMutex.Unlock()

			err := sq.doSend(task, workerID, queueType)

			sq.InflightMutex.Lock()
			sq.InflightMap[task.Target]--
			if sq.InflightMap[task.Target] <= 0 {
				delete(sq.InflightMap, task.Target)
			}
			sq.InflightMutex.Unlock()

			if err != nil {
				sq.handleRetry(task, err)
			}
		}
	}
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

	if err != nil {
		sq.Logger.Error("[SendQueue][%s] worker=%d,%s send to %s FAILED after %v: %v",
			queueType, workerID, task.FuncName(), task.Target, elapsed, err)
	} else {
		sq.Logger.Trace("[SendQueue][%s] worker=%d,%s send to %s success in %v",
			queueType, workerID, task.FuncName(), task.Target, elapsed)
	}
	return err
}

func (sq *SendQueue) handleRetry(task *SendTask, sendErr error) {
	task.RetryCount++
	if task.RetryCount > task.MaxRetries {
		sq.Logger.Debug("[SendQueue] Exceed max retries(%d) target=%s, giving up",
			task.MaxRetries, task.Target)
		return
	}
	cfg := config.DefaultConfig()
	// 幂次退避
	baseDelay := cfg.Sender.BaseRetryDelay
	backoff := baseDelay * time.Duration(math.Pow(2, float64(task.RetryCount-1)))
	task.NextAttempt = time.Now().Add(backoff)
	sq.Enqueue(task)
	sq.Logger.Debug("[SendQueue] Retry %d/%d after %v for %s (err=%v)",
		task.RetryCount, task.MaxRetries, backoff, task.Target, sendErr)
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
