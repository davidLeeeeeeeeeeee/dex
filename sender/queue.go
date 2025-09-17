package sender

import (
	"dex/logs"
	"fmt"
	"log"
	"math"
	"reflect"
	"runtime"
	"sync"
	"time"
)

// SendTask 封装一次发送所需的信息
type SendTask struct {
	Target      string                     // 目标节点(通常是IP:port)
	Message     interface{}                // 要发送的具体payload
	RetryCount  int                        // 当前已重试次数
	MaxRetries  int                        // 最大重试次数
	NextAttempt time.Time                  // 下次可发送时间(用于幂次退避)
	SendFunc    func(task *SendTask) error // 真正执行发送的回调
}

// SendQueue 负责管理任务队列 + worker
type SendQueue struct {
	workerCount int
	taskChan    chan *SendTask
	stopChan    chan struct{}
	wg          sync.WaitGroup
}

// GlobalQueue 您可以暴露一个全局队列实例, 供其他sender函数使用
var GlobalQueue *SendQueue

// InitQueue 在程序启动时初始化全局队列
func InitQueue(workerCount, queueCapacity int) {
	GlobalQueue = &SendQueue{
		workerCount: workerCount,
		taskChan:    make(chan *SendTask, queueCapacity),
		stopChan:    make(chan struct{}),
	}
	GlobalQueue.Start()
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
		log.Println("[SendQueue] taskChan is full, discard or handle properly.")
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

	start := time.Now()
	err := task.SendFunc(task)
	elapsed := time.Since(start)

	if err != nil {
		logs.Error("[SendQueue] worker=%d,%s send to %s FAILED after %v: %v",
			workerID, task.FuncName(), task.Target, elapsed, err)
	} else {
		logs.Debug("[SendQueue] worker=%d,%s send to %s success in %v",
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
