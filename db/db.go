package db

import (
	"dex/logs"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// Manager 封装 BadgerDB 的管理器
type Manager struct {
	Db *badger.DB
	mu sync.RWMutex

	// 队列通道，批量写的 goroutine 用它来取写请求
	writeQueueChan chan WriteTask
	// 强制刷盘通道
	forceFlushChan chan struct{}
	// 用于通知写队列 goroutine 停止
	stopChan chan struct{}

	// 还可以增加两个参数，用来控制"写多少/多长时间"就落库
	maxBatchSize  int                // 累计多少条就写一次
	flushInterval time.Duration      // 间隔多久强制写一次
	IndexMgr      *MinerIndexManager // 新增
	seq           *badger.Sequence   //自增发号器
	// 你可以在这里做一个 wait group，保证 close 的时候能等 goroutine 退出
	wg sync.WaitGroup
	// 缓存的区块切片，最多存 10 个
	cachedBlocks   []*Block
	cachedBlocksMu sync.RWMutex
}

// NewManager 创建一个新的 DBManager 实例
func NewManager(path string) (*Manager, error) {
	opts := badger.DefaultOptions(path).WithLoggingLevel(badger.INFO).
		// 将单个 vlog 文件限制到 64 MB，比如 64 << 20
		WithValueLogFileSize(64 << 20)
	// 如果依然想用 mmap，可以保持默认 (MemoryMap) 或自己设 WithValueLogLoadingMode(options.MemoryMap)
	// .WithValueLogLoadingMode(options.MemoryMap)
	//
	// 可选：让Badger启动时自动截断不完整的日志，能避免某些不一致问题
	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open badger db: %w", err)
	}

	indexMgr, err := NewMinerIndexManager(db)
	if err != nil {
		db.Close() // 清理已打开的数据库
		return nil, fmt.Errorf("failed to create index manager: %w", err)
	}

	// ① 创建 Sequence（一次预取 1 000 个号段，可按业务量调大/调小）
	seq, err := db.GetSequence([]byte("meta:max_index"), 1000)
	if err != nil {
		db.Close() // 清理已打开的数据库
		return nil, fmt.Errorf("failed to create sequence: %w", err)
	}

	manager := &Manager{
		Db:       db,
		IndexMgr: indexMgr,
		seq:      seq,
	}

	return manager, nil
}

func (manager *Manager) InitWriteQueue(maxBatchSize int, flushInterval time.Duration) {
	manager.maxBatchSize = maxBatchSize
	manager.flushInterval = flushInterval
	manager.writeQueueChan = make(chan WriteTask, 100000) // 缓冲区大小可酌情调大

	// 新建 forceFlushChan
	manager.forceFlushChan = make(chan struct{}, 1)

	manager.stopChan = make(chan struct{})

	manager.wg.Add(1)
	go manager.runWriteQueue()
}

// 写队列的核心 goroutine 逻辑
func (manager *Manager) runWriteQueue() {
	defer manager.wg.Done()

	// 用于临时收集写请求
	var batch []WriteTask
	batch = make([]WriteTask, 0, manager.maxBatchSize)

	// 定时器：到了 flushInterval 就要提交
	ticker := time.NewTicker(manager.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-manager.stopChan:
			// 收到停止信号，先把手头的 batch flush 一下再退出
			manager.flushBatch(batch)
			return

		case task := <-manager.writeQueueChan:
			// 收到一条写请求，加入 batch
			batch = append(batch, task)
			if len(batch) >= manager.maxBatchSize {
				// 超过阈值，立即 flush
				manager.flushBatch(batch)
				// flush 完要重置 batch
				batch = batch[:0]
			}

		case <-ticker.C:
			// 到了时间间隔，也要 flush
			if len(batch) > 0 {
				manager.flushBatch(batch)
				batch = batch[:0]
			}

		case <-manager.forceFlushChan:
			// 收到"强制刷盘"请求
			if len(batch) > 0 {
				manager.flushBatch(batch)
				batch = batch[:0]
			}
		}
	}
}

// 提供一个无参函数，触发批量队列 flush
func (manager *Manager) ForceFlush() {
	select {
	case manager.forceFlushChan <- struct{}{}:
	default:
		// 如果通道已满，不阻塞
	}
}

// 这里 flushBatch 就是把 batch 里所有请求一次性地在一个事务中提交到 BadgerDB。
func (manager *Manager) flushBatch(batch []WriteTask) {
	if len(batch) == 0 {
		return
	}

	const maxSubBatch = 500 // 每个小批最多 500 条
	start := 0
	for start < len(batch) {
		end := start + maxSubBatch
		if end > len(batch) {
			end = len(batch)
		}
		subBatch := batch[start:end]

		err := manager.Db.Update(func(txn *badger.Txn) error {
			for _, task := range subBatch {
				switch task.Op {
				case OpSet:
					if e := txn.Set(task.Key, task.Value); e != nil {
						return e
					}
				case OpDelete:
					if e := txn.Delete(task.Key); e != nil {
						return e
					}
				}
			}
			return nil
		})
		if err != nil {
			logs.Error("[runWriteQueue] flushBatch error: %v\n", err)
			// 看你需求，是否继续写剩余subbatch 或者重试
		}

		start = end
	}
}

func (manager *Manager) View(fn func(txn *TransactionView) error) error {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.Db.View(func(badgerTxn *badger.Txn) error {
		return fn(&TransactionView{badgerTxn})
	})
}

// TransactionView 包装 badger.Txn
type TransactionView struct {
	Txn *badger.Txn
}

func (tv *TransactionView) NewIterator() *badger.Iterator {
	return tv.Txn.NewIterator(badger.DefaultIteratorOptions)
}

// 提供"投递写请求"的方法（替换原先的 Write/WriteBatch）

func (manager *Manager) EnqueueSet(key, value string) {
	manager.writeQueueChan <- WriteTask{
		Key:   []byte(key),
		Value: []byte(value),
		Op:    OpSet,
	}
}

func (manager *Manager) EnqueueDelete(key string) {
	manager.writeQueueChan <- WriteTask{
		Key: []byte(key),
		Op:  OpDelete,
	}
}

func (manager *Manager) Close() {
	// 1. 通知写队列 goroutine 停止
	if manager.stopChan != nil {
		close(manager.stopChan)
	}

	// 2. 等待 goroutine 退出
	manager.wg.Wait()

	// 3. 这时所有队列里的数据都已经flush完了，可以安全关闭DB
	manager.mu.Lock()
	defer manager.mu.Unlock()

	if manager.seq != nil {
		_ = manager.seq.Release() // 无须处理返回值；Close() 时 Badger 仍会安全落盘
		manager.seq = nil
	}

	if manager.Db != nil {
		_ = manager.Db.Close()
		manager.Db = nil
	}
}

// Read 读取键对应的值
func (manager *Manager) Read(key string) (string, error) {
	manager.mu.RLock()
	db := manager.Db
	manager.mu.RUnlock()

	if db == nil {
		return "", fmt.Errorf("database is not initialized or closed")
	}

	var value string
	err := db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}
		val, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}
		value = string(val)
		return nil
	})
	if err != nil {
		return "", err
	}
	return value, nil
}

// 将 db.Transaction 序列化为 [][]byte
func SerializeAllTransactions(txCopy []*AnyTx) [][]byte {

	// 1) 按 TxId 排序
	sort.Slice(txCopy, func(i, j int) bool {
		return txCopy[i].GetTxId() < txCopy[j].GetTxId()
	})

	// 2) 逐笔序列化
	var result [][]byte
	for _, tx := range txCopy {
		result = append(result, serializeTransaction(tx))
	}
	return result
}

// 根据 db.Transaction 的字段进行序列化
// 实际中应根据业务需求将交易变成字节切片，这里只作简单示例
func serializeTransaction(tx *AnyTx) []byte {
	data := []byte(tx.GetTxId() + "|" + tx.GetBase().FromAddress)
	// 可以增加更多字段序列化逻辑
	return data
}

// NextIndex 获取下一个自增索引
func (m *Manager) NextIndex() (uint64, error) {
	id, err := m.seq.Next() // Badger 自动并发安全
	if err != nil {
		return 0, err
	}
	return id + 1, nil // 让索引依旧从 1 开始
}
