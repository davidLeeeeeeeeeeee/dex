package db

import (
	"dex/config"
	"dex/keys"
	"dex/logs"
	"dex/pb"
	statedb "dex/stateDB"
	"dex/utils"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v4"
	"google.golang.org/protobuf/proto"
)

// Manager 封装 BadgerDB 的管理器
type Manager struct {
	Db      *badger.DB
	StateDB *statedb.DB
	mu      sync.RWMutex

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
	cachedBlocks   []*pb.Block
	cachedBlocksMu sync.RWMutex
	Logger         logs.Logger
}

// NewManager 创建一个新的 DBManager 实例
func NewManager(path string, logger logs.Logger) (*Manager, error) {
	cfg := config.DefaultConfig()
	opts := badger.DefaultOptions(path).WithLoggingLevel(badger.INFO).
		// 将单个 vlog 文件限制到 64 MB，比如 64 << 20
		WithValueLogFileSize(cfg.Database.ValueLogFileSize)
	// 如果依然想用 mmap，可以保持默认 (MemoryMap) 或自己设 WithValueLogLoadingMode(options.MemoryMap)
	// .WithValueLogLoadingMode(options.MemoryMap)
	//
	// 可选：让Badger启动时自动截断不完整的日志，能避免某些不一致问题
	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open badger db: %w", err)
	}

	indexMgr, err := NewMinerIndexManager(db, logger)
	if err != nil {
		_ = db.Close() // 清理已打开的数据库
		return nil, fmt.Errorf("failed to create index manager: %w", err)
	}

	// ① 创建 Sequence（一次预取 1 000 个号段，可按业务量调大/调小）
	seq, err := db.GetSequence([]byte("meta:max_index"), cfg.Database.SequenceBandwidth)
	if err != nil {
		_ = db.Close() // 清理已打开的数据库
		return nil, fmt.Errorf("failed to create sequence: %w", err)
	}

	// 初始化 StateDB，使用主 DB 目录下的 state 子目录
	stateCfg := statedb.Config{
		DataDir:         filepath.Join(path, "state"),
		EpochSize:       40000,
		ShardHexWidth:   1,
		PageSize:        1000,
		UseWAL:          true,
		VersionsToKeep:  10,
		AccountNSPrefix: "v1_account_",
	}

	stateDB, err := statedb.New(stateCfg)
	if err != nil {
		_ = seq.Release()
		_ = db.Close()
		return nil, fmt.Errorf("failed to create StateDB: %w", err)
	}

	manager := &Manager{
		Db:       db,
		StateDB:  stateDB,
		IndexMgr: indexMgr,
		seq:      seq,
		Logger:   logger,
	}

	return manager, nil
}

func (manager *Manager) InitWriteQueue(maxBatchSize int, flushInterval time.Duration) {
	cfg := config.DefaultConfig()
	manager.maxBatchSize = maxBatchSize
	manager.flushInterval = flushInterval
	manager.writeQueueChan = make(chan WriteTask, cfg.Database.WriteQueueSize) // 缓冲区大小可酌情调大

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

// ForceFlush triggers a batch queue flush
func (manager *Manager) ForceFlush() error {
	select {
	case manager.forceFlushChan <- struct{}{}:
	default:
		// 如果通道已满，不阻塞
	}
	return nil
}

// Scan scans all keys with the given prefix and returns a map of key-value pairs
func (manager *Manager) Scan(prefix string) (map[string][]byte, error) {
	result := make(map[string][]byte)

	err := manager.Db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		it := txn.NewIterator(opts)
		defer it.Close()

		p := []byte(prefix)
		for it.Seek(p); it.ValidForPrefix(p); it.Next() {
			item := it.Item()
			k := item.KeyCopy(nil)
			v, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			result[string(k)] = v
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ScanOrdersByPairs 一次性扫描多个交易对的未成交订单
// 返回：map[pair]map[indexKey][]byte
func (manager *Manager) ScanOrdersByPairs(pairs []string) (map[string]map[string][]byte, error) {
	result := make(map[string]map[string][]byte)

	// 单个 txn 内完成所有扫描
	err := manager.Db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions

		for _, pair := range pairs {
			// 生成该交易对的未成交订单索引前缀
			prefix := keys.KeyOrderPriceIndexPrefix(pair, false)
			p := []byte(prefix)

			pairMap := make(map[string][]byte)
			it := txn.NewIterator(opts)

			for it.Seek(p); it.ValidForPrefix(p); it.Next() {
				item := it.Item()
				k := item.KeyCopy(nil)
				v, err := item.ValueCopy(nil)
				if err != nil {
					it.Close()
					return err
				}
				pairMap[string(k)] = v
			}
			it.Close()

			result[pair] = pairMap
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return result, nil
}

// ScanByPrefix 扫描指定前缀的所有键值对
// 返回 map[key]value，最多返回 limit 条记录
func (manager *Manager) ScanByPrefix(prefix string, limit int) (map[string]string, error) {
	result := make(map[string]string)

	err := manager.Db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		it := txn.NewIterator(opts)
		defer it.Close()

		p := []byte(prefix)
		count := 0
		for it.Seek(p); it.ValidForPrefix(p); it.Next() {
			if limit > 0 && count >= limit {
				break
			}
			item := it.Item()
			k := item.KeyCopy(nil)
			v, err := item.ValueCopy(nil)
			if err != nil {
				continue
			}
			result[string(k)] = string(v)
			count++
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return result, nil
}

// EnqueueDel wraps EnqueueDelete for interface compatibility
func (manager *Manager) EnqueueDel(key string) {
	manager.EnqueueDelete(key)
}

// 这里 flushBatch 就是把 batch 里所有请求一次性地在一个事务中提交到 BadgerDB。
func (manager *Manager) flushBatch(batch []WriteTask) {
	if len(batch) == 0 {
		return
	}
	cfg := config.DefaultConfig()
	// 保守软上限，留出 Badger 元数据开销余量
	softLimitBytes := cfg.Database.WriteBatchSoftLimit // 8 MiB
	maxCountPerTxn := cfg.Database.MaxCountPerTxn      // 也保留条数上限，双重保险
	perEntryOverhead := cfg.Database.PerEntryOverhead  // 估算每条附加开销

	// 1) 先按“字节+条数”把batch切成若干 sub-batch
	type sliceRange struct{ i, j int }
	subRanges := make([]sliceRange, 0, (len(batch)+maxCountPerTxn-1)/maxCountPerTxn)

	curStart, curBytes, curCount := 0, 0, 0
	for idx, t := range batch {
		entryBytes := len(t.Key) + len(t.Value) + perEntryOverhead
		// 如果加上当前条会超过限制，就先封口开新段
		if curCount > 0 && (int64(curBytes+entryBytes) > softLimitBytes || curCount >= maxCountPerTxn) {
			subRanges = append(subRanges, sliceRange{curStart, idx})
			curStart, curBytes, curCount = idx, 0, 0
		}
		curBytes += entryBytes
		curCount++
	}
	// 收尾
	if curStart < len(batch) {
		subRanges = append(subRanges, sliceRange{curStart, len(batch)})
	}

	// 2) 提交每个 sub-batch；若仍报过大，二分退让
	for _, r := range subRanges {
		i, j := r.i, r.j
		for i < j {
			ok := manager.tryFlushRange(batch, i, j)
			if ok {
				break // 这个范围提交成功
			}
			// 失败：把范围二分
			mid := i + (j-i)/2
			// 先尝试左半
			if !manager.tryFlushRange(batch, i, mid) {
				// 左半都太大：继续缩左半
				j = mid
				continue
			}
			// 左半成功，再提交右半（循环下一轮处理右半）
			i = mid
		}
	}
}

// 返回是否提交成功；如果整个范围是一条但仍然过大，会打印明确错误并返回false
func (manager *Manager) tryFlushRange(batch []WriteTask, start, end int) bool {
	if start >= end {
		return true
	}
	sub := batch[start:end]

	err := manager.Db.Update(func(txn *badger.Txn) error {
		for _, task := range sub {
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
	if err == nil {
		return true
	}

	// Badger 的典型报错文案里包含 "Txn is too big"
	if strings.Contains(err.Error(), "Txn is too big") {
		if end-start == 1 {
			// 单条仍过大：给出清晰提示
			key := string(sub[0].Key)
			valSz := len(sub[0].Value)
			manager.Logger.Error("[flushBatch] single entry still too big: key=%q size=%d bytes; "+
				"consider compressing, chunking, or storing out-of-DB", key, valSz)
			return false
		}
		// 交给上层继续二分
		return false
	}

	// 其他错误：记录并继续
	logs.Error("[flushBatch] subBatch [%d:%d] error: %v", start, end, err)
	return true // 避免卡死：把它当“已处理”，不中断后续
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

	if manager.StateDB != nil {
		_ = manager.StateDB.Close()
		manager.StateDB = nil
	}

	if manager.Db != nil {
		_ = manager.Db.Close()
		manager.Db = nil
	}
}

// Read 读取键对应的值
// 对于状态数据（账户、订单状态、Token、Witness 等）优先从 StateDB 读取
// 对于流水数据（区块、交易原文、历史记录）直接从 KV 读取
func (manager *Manager) Read(key string) (string, error) {
	// 1. 对于状态数据，优先尝试从 StateDB 读取
	if keys.IsStatefulKey(key) && manager.StateDB != nil {
		if val, exists, err := manager.StateDB.Get(key); err == nil && exists && len(val) > 0 {
			return string(val), nil
		}
		// StateDB 没有找到，继续回退到 KV
	}

	// 2. 从 KV 读取
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

// Get 实现 vm.DBManager 接口，返回 []byte
// 对于状态数据（账户、订单状态、Token、Witness 等）优先从 StateDB 读取
// 对于流水数据（区块、交易原文、历史记录）直接从 KV 读取
func (manager *Manager) Get(key string) ([]byte, error) {
	// 1. 对于状态数据，优先尝试从 StateDB 读取
	if keys.IsStatefulKey(key) && manager.StateDB != nil {
		if val, exists, err := manager.StateDB.Get(key); err == nil && exists && len(val) > 0 {
			return val, nil
		}
		// StateDB 没有找到，继续回退到 KV
	}

	// 2. 从 KV 读取
	manager.mu.RLock()
	db := manager.Db
	manager.mu.RUnlock()

	if db == nil {
		return nil, fmt.Errorf("database is not initialized or closed")
	}

	var value []byte
	err := db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}
		val, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}
		value = val
		return nil
	})
	if err != nil {
		return nil, err
	}
	return value, nil
}

// 将 db.Transaction 序列化为 [][]byte
func SerializeAllTransactions(txCopy []*pb.AnyTx) [][]byte {

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
func serializeTransaction(tx *pb.AnyTx) []byte {
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

// ========== 索引重建接口 ==========

//	从订单数据重建价格索引
//
// 用于轻节点同步后重建索引，提升查询性能
// 返回重建的索引数量
func RebuildOrderPriceIndexes(m *Manager) (int, error) {
	// 使用 withVer 获取正确的前缀
	prefix := "v1_order_"
	count := 0

	// 先收集所有需要写入的索引，避免在 View 事务中调用 EnqueueSet
	type indexEntry struct {
		key   string
		value string
	}
	var indexes []indexEntry

	err := m.Db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		it := txn.NewIterator(opts)
		defer it.Close()

		p := []byte(prefix)
		for it.Seek(p); it.ValidForPrefix(p); it.Next() {
			item := it.Item()
			key := string(item.Key())

			// 确保 key 以前缀开头
			if !strings.HasPrefix(key, prefix) {
				break
			}

			orderData, err := item.ValueCopy(nil)
			if err != nil {
				continue
			}

			// 反序列化订单
			var order pb.OrderTx
			if err := proto.Unmarshal(orderData, &order); err != nil {
				continue
			}

			// 尝试从 OrderState 获取最新的 is_filled 状态
			isFilled := false
			orderStateKey := keys.KeyOrderState(order.Base.TxId)
			if stateData, err := m.Get(orderStateKey); err == nil && len(stateData) > 0 {
				var state pb.OrderState
				if err := proto.Unmarshal(stateData, &state); err == nil {
					isFilled = state.IsFilled
				}
			}

			// 重建价格索引
			pair := utils.GeneratePairKey(order.BaseToken, order.QuoteToken)
			priceKey67, err := PriceToKey128(order.Price)
			if err != nil {
				continue
			}

			indexKey := keys.KeyOrderPriceIndex(pair, isFilled, priceKey67, order.Base.TxId)
			indexData, _ := proto.Marshal(&pb.OrderPriceIndex{Ok: true})

			indexes = append(indexes, indexEntry{
				key:   indexKey,
				value: string(indexData),
			})
			count++
		}
		return nil
	})

	if err != nil {
		return 0, err
	}

	// 写入所有索引
	for _, idx := range indexes {
		m.EnqueueSet(idx.key, idx.value)
	}

	// 强制刷盘
	if err := m.ForceFlush(); err != nil {
		return 0, err
	}

	return count, nil
}

// ========== StateDB 同步接口 ==========

// SyncToStateDB 同步状态变化到 StateDB
// 这是 VM 的 applyResult 调用的接口，用于将 WriteOp 同步到 StateDB
// 注意：只有被 keys.IsStatefulKey 判定为状态数据的 key 才会被同步
func (m *Manager) SyncToStateDB(height uint64, updates []interface{}) error {
	if m.StateDB == nil {
		return fmt.Errorf("StateDB is not initialized")
	}

	// 将 WriteOp 转换为 StateDB 的 KVUpdate
	// 使用 keys.IsStatefulKey 进行二次过滤，确保只有状态数据才会同步
	kvUpdates := make([]statedb.KVUpdate, 0, len(updates))
	for _, u := range updates {
		// 使用接口类型断言（避免循环依赖）
		type writeOpInterface interface {
			GetKey() string
			GetValue() []byte
			IsDel() bool
		}

		if writeOp, ok := u.(writeOpInterface); ok {
			key := writeOp.GetKey()
			// 只同步状态数据（可变证明状态）
			// 流水数据（不可变区块/交易）和索引数据不会同步到 StateDB
			if !keys.IsStatefulKey(key) {
				continue
			}
			kvUpdates = append(kvUpdates, statedb.KVUpdate{
				Key:     key,
				Value:   writeOp.GetValue(),
				Deleted: writeOp.IsDel(),
			})
		} else {
			m.Logger.Warn("[DB] Failed to convert update to WriteOp: %T", u)
		}
	}

	// 调用 StateDB 的 ApplyAccountUpdate
	if len(kvUpdates) > 0 {
		if err := m.StateDB.ApplyAccountUpdate(height, kvUpdates...); err != nil {
			logs.Error("[DB] Failed to sync to StateDB: %v", err)
			return err
		}
	}

	return nil
}
