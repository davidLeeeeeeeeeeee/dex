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
type SendQueue struct {
	workerCount int
	taskChan    chan *SendTask
	stopChan    chan struct{}
	wg          sync.WaitGroup
	httpClient  *http.Client
	nodeID      int // 只用作log,不参与业务逻辑
}

// 移除 GlobalQueue 和 InitQueue

// 创建新的发送队列
func NewSendQueue(workerCount, queueCapacity int, httpClient *http.Client, nodeID int) *SendQueue {
	sq := &SendQueue{
		nodeID:      nodeID,
		workerCount: workerCount,
		taskChan:    make(chan *SendTask, queueCapacity),
		stopChan:    make(chan struct{}),
		httpClient:  httpClient,
	}
	sq.Start()
	return sq
}

// Start 启动 workerCount 个协程
func (sq *SendQueue) Start() {
	sq.wg.Add(sq.workerCount)
	for i := 0; i < sq.workerCount; i++ {
		go sq.workerLoop(i)
	}
	logs.Verbose("[SendQueue] Started with %d workers", sq.workerCount)
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
	// 控制面任务：给一个短暂的阻塞窗口
	if task.Priority == PriorityControl {
		select {
		case sq.taskChan <- task:
			// 成功入队
			return
		case <-time.After(cfg.Sender.ControlTaskTimeout):

			// 80ms 后仍无法入队，记录错误
			logs.Error("[Node %d][SendQueue] Control task timeout: len=%d cap=%d target=%s",
				sq.nodeID, len(sq.taskChan), cap(sq.taskChan), task.Target)
			// 可以选择丢弃或者进一步处理
			return
		}
	}

	// 数据面任务：队列满了直接丢弃
	select {
	case sq.taskChan <- task:
		// 成功入队
	default:
		logs.Debug("[Node %d][SendQueue] Data task dropped: queue full, target=%s",
			sq.nodeID, task.Target)
	}
}

// workerLoop 逐个获取队列任务并执行
func (sq *SendQueue) workerLoop(workerID int) {
	defer sq.wg.Done()

	for {
		select {
		case <-sq.stopChan:
			return
		case task := <-sq.taskChan:
			if task == nil {
				return
			}
			now := time.Now()
			if task.NextAttempt.After(now) {
				sleepDur := task.NextAttempt.Sub(now)
				time.Sleep(sleepDur)
			}
			err := sq.doSend(task, workerID)
			if err != nil {
				sq.handleRetry(task, err)
			}
		}
	}
}

func (sq *SendQueue) doSend(task *SendTask, workerID int) error {
	if task.SendFunc == nil {
		return fmt.Errorf("SendFunc is nil, cannot send")
	}
	// 设置任务的 HTTP 客户端
	task.HttpClient = sq.httpClient

	start := time.Now()
	err := task.SendFunc(task, sq.httpClient)
	elapsed := time.Since(start)

	if err != nil {
		logs.Error("[SendQueue] worker=%d,%s send to %s FAILED after %v: %v",
			workerID, task.FuncName(), task.Target, elapsed, err)
	} else {
		logs.Trace("[SendQueue] worker=%d,%s send to %s success in %v",
			workerID, task.FuncName(), task.Target, elapsed)
	}
	return err
}

func (sq *SendQueue) handleRetry(task *SendTask, sendErr error) {
	task.RetryCount++
	if task.RetryCount > task.MaxRetries {
		logs.Debug("[SendQueue] Exceed max retries(%d) target=%s, giving up",
			task.MaxRetries, task.Target)
		return
	}
	cfg := config.DefaultConfig()
	// 幂次退避
	baseDelay := cfg.Sender.BaseRetryDelay
	backoff := baseDelay * time.Duration(math.Pow(2, float64(task.RetryCount-1)))
	task.NextAttempt = time.Now().Add(backoff)
	sq.Enqueue(task)
	logs.Debug("[SendQueue] Retry %d/%d after %v for %s (err=%v)",
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
