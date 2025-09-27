package sender

import (
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

// SendTask 封装一次发送所需的信息
type SendTask struct {
	Target      string
	Message     interface{}
	RetryCount  int
	MaxRetries  int
	NextAttempt time.Time
	SendFunc    func(task *SendTask, client *http.Client) error
	HttpClient  *http.Client
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

// Enqueue 提交任务到队列(如果满了,可阻塞/丢弃/返回err)
func (sq *SendQueue) Enqueue(task *SendTask) {
	if task.NextAttempt.IsZero() {
		task.NextAttempt = time.Now()
	}
	select {
	case sq.taskChan <- task:
		// ok
	default:
		// 关键：看到是谁满了、当前长度多少、任务类型是什么
		logs.Error("[Node %d][SendQueue] FULL: len=%d cap=%d msg=%s target=%s",
			sq.nodeID, len(sq.taskChan), cap(sq.taskChan), task.Message, task.Target)
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
	// 幂次退避
	baseDelay := time.Second
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
