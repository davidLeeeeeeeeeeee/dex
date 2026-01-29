package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	iface "dex/interfaces"
	"dex/pb"
	"dex/vm"
)

// SimpleSession 简单的会话实现
type SimpleSession struct {
	db *SimpleDB
}

func (s *SimpleSession) Get(key string) ([]byte, error) {
	return s.db.Get(key)
}

func (s *SimpleSession) ApplyStateUpdate(height uint64, updates []interface{}) ([]byte, error) {
	for _, u := range updates {
		type writeOpInterface interface {
			GetKey() string
			GetValue() []byte
			IsDel() bool
		}
		if op, ok := u.(writeOpInterface); ok {
			if op.IsDel() {
				delete(s.db.data, op.GetKey())
			} else {
				s.db.data[op.GetKey()] = op.GetValue()
			}
		}
	}
	return []byte("simple_root"), nil
}

func (s *SimpleSession) Commit() error   { return nil }
func (s *SimpleSession) Rollback() error { return nil }
func (s *SimpleSession) Close() error    { return nil }

// SimpleDB 简单的内存数据库实现
type SimpleDB struct {
	data map[string][]byte
}

func NewSimpleDB() *SimpleDB {
	return &SimpleDB{
		data: make(map[string][]byte),
	}
}

func (db *SimpleDB) NewSession() (iface.DBSession, error) {
	return &SimpleSession{db: db}, nil
}

func (db *SimpleDB) CommitRoot(height uint64, root []byte) {
	// Simple 实现：简单记录
}

func (db *SimpleDB) Get(key string) ([]byte, error) {
	val, exists := db.data[key]
	if !exists {
		return nil, nil
	}
	return val, nil
}

func (db *SimpleDB) GetKV(key string) ([]byte, error) {
	return db.Get(key)
}

func (db *SimpleDB) EnqueueSet(key, value string) {
	db.data[key] = []byte(value)
}

func (db *SimpleDB) EnqueueDel(key string) {
	delete(db.data, key)
}

func (db *SimpleDB) ForceFlush() error {
	// 内存数据库，无需刷新
	return nil
}

func (db *SimpleDB) Scan(prefix string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	for k, v := range db.data {
		if strings.HasPrefix(k, prefix) {
			valCopy := make([]byte, len(v))
			copy(valCopy, v)
			result[k] = valCopy
		}
	}
	return result, nil
}

func (db *SimpleDB) ScanOrdersByPairs(pairs []string) (map[string]map[string][]byte, error) {
	result := make(map[string]map[string][]byte)
	for _, pair := range pairs {
		result[pair] = make(map[string][]byte)
	}
	return result, nil
}

// ========== Frost 相关方法 ==========

func (db *SimpleDB) GetFrostVaultTransition(key string) (*pb.VaultTransitionState, error) {
	data, err := db.Get(key)
	if err != nil || data == nil {
		return nil, err
	}
	var state pb.VaultTransitionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func (db *SimpleDB) SetFrostVaultTransition(key string, state *pb.VaultTransitionState) error {
	data, _ := json.Marshal(state)
	db.data[key] = data
	return nil
}

func (db *SimpleDB) GetFrostDkgCommitment(key string) (*pb.FrostVaultDkgCommitment, error) {
	data, err := db.Get(key)
	if err != nil || data == nil {
		return nil, err
	}
	var commitment pb.FrostVaultDkgCommitment
	if err := json.Unmarshal(data, &commitment); err != nil {
		return nil, err
	}
	return &commitment, nil
}

func (db *SimpleDB) SetFrostDkgCommitment(key string, commitment *pb.FrostVaultDkgCommitment) error {
	data, _ := json.Marshal(commitment)
	db.data[key] = data
	return nil
}

func (db *SimpleDB) GetFrostDkgShare(key string) (*pb.FrostVaultDkgShare, error) {
	data, err := db.Get(key)
	if err != nil || data == nil {
		return nil, err
	}
	var share pb.FrostVaultDkgShare
	if err := json.Unmarshal(data, &share); err != nil {
		return nil, err
	}
	return &share, nil
}

func (db *SimpleDB) SetFrostDkgShare(key string, share *pb.FrostVaultDkgShare) error {
	data, _ := json.Marshal(share)
	db.data[key] = data
	return nil
}

func (db *SimpleDB) GetFrostVaultState(key string) (*pb.FrostVaultState, error) {
	data, err := db.Get(key)
	if err != nil || data == nil {
		return nil, err
	}
	var state pb.FrostVaultState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func (db *SimpleDB) SetFrostVaultState(key string, state *pb.FrostVaultState) error {
	data, _ := json.Marshal(state)
	db.data[key] = data
	return nil
}

func (db *SimpleDB) GetFrostDkgComplaint(key string) (*pb.FrostVaultDkgComplaint, error) {
	data, err := db.Get(key)
	if err != nil || data == nil {
		return nil, err
	}
	var complaint pb.FrostVaultDkgComplaint
	if err := json.Unmarshal(data, &complaint); err != nil {
		return nil, err
	}
	return &complaint, nil
}

func (db *SimpleDB) SetFrostDkgComplaint(key string, complaint *pb.FrostVaultDkgComplaint) error {
	data, _ := json.Marshal(complaint)
	db.data[key] = data
	return nil
}

// CustomHandler 自定义Handler示例
type CustomHandler struct {
	name string
}

func NewCustomHandler(name string) *CustomHandler {
	return &CustomHandler{name: name}
}

func (h *CustomHandler) Kind() string {
	return h.name
}

func (h *CustomHandler) DryRun(tx *pb.AnyTx, sv vm.StateView) ([]vm.WriteOp, *vm.Receipt, error) {
	// 简单示例：将交易数据直接写入
	txID := tx.GetTxId()
	key := fmt.Sprintf("%s_%s", h.name, txID)

	// 将整个pb.AnyTx序列化存储
	data, _ := json.Marshal(tx)
	ws := []vm.WriteOp{
		{Key: key, Value: data, Del: false},
	}

	return ws, &vm.Receipt{
		TxID:       txID,
		Status:     "SUCCEED",
		WriteCount: len(ws),
	}, nil
}

func (h *CustomHandler) Apply(tx *pb.AnyTx) error {
	return vm.ErrNotImplemented
}

func main() {
	// 1. 初始化组件
	db := NewSimpleDB()
	registry := vm.NewHandlerRegistry()
	cache := vm.NewSpecExecLRU(100)

	// 2. 注册Handler
	// 使用默认Handler
	if err := vm.RegisterDefaultHandlers(registry); err != nil {
		log.Fatal("Failed to register handlers:", err)
	}

	// 添加自定义Handler
	customHandler := NewCustomHandler("custom")
	if err := registry.Register(customHandler); err != nil {
		log.Fatal("Failed to register custom handler:", err)
	}

	// 3. 创建执行器
	executor := vm.NewExecutor(db, registry, cache)

	// 4. 初始化一些账户数据
	db.data["balance_alice_token123"] = []byte("10000")
	db.data["balance_bob_token123"] = []byte("5000")
	db.data["balance_charlie_token123"] = []byte("3000")

	fmt.Println("Initial balances:")
	fmt.Printf("  Alice: %s token123\n", db.data["balance_alice_token123"])
	fmt.Printf("  Bob: %s token123\n", db.data["balance_bob_token123"])
	fmt.Printf("  Charlie: %s token123\n", db.data["balance_charlie_token123"])

	// 5. 创建pb.AnyTx交易
	txs := []*pb.AnyTx{
		// 转账交易1
		{
			Content: &pb.AnyTx_Transaction{
				Transaction: &pb.Transaction{
					Base: &pb.BaseMessage{
						TxId:        "tx_001",
						FromAddress: "alice",
						Status:      pb.Status_PENDING,
					},
					To:           "bob",
					TokenAddress: "token123",
					Amount:       "100",
				},
			},
		},
		// 转账交易2
		{
			Content: &pb.AnyTx_Transaction{
				Transaction: &pb.Transaction{
					Base: &pb.BaseMessage{
						TxId:        "tx_002",
						FromAddress: "bob",
						Status:      pb.Status_PENDING,
					},
					To:           "charlie",
					TokenAddress: "token123",
					Amount:       "50",
				},
			},
		},
		// 矿工奖励
		{
			Content: &pb.AnyTx_MinerTx{
				MinerTx: &pb.MinerTx{
					Base: &pb.BaseMessage{
						TxId:           "tx_003",
						FromAddress:    "alice",
						ExecutedHeight: 1,
						Status:         pb.Status_PENDING,
					},
					Op:     pb.OrderOp_ADD,
					Amount: "100",
				},
			},
		},
	}

	// 6. 创建pb.Block区块
	block := &pb.Block{
		BlockHash: "block_001",
		Header: &pb.BlockHeader{
			PrevBlockHash: "genesis",
			Height:        1,
		},
		Body: txs,
	}

	// 7. 预执行区块
	fmt.Println("\n=== Pre-executing block ===")
	result, err := executor.PreExecuteBlock(block)
	if err != nil {
		log.Fatal("PreExecute failed:", err)
	}

	fmt.Printf("Block Valid: %v\n", result.Valid)
	if !result.Valid {
		fmt.Printf("Reason: %s\n", result.Reason)
		return
	}

	fmt.Printf("Transactions processed: %d\n", len(result.Receipts))
	for _, receipt := range result.Receipts {
		fmt.Printf("  %s: %s (writes: %d)\n",
			receipt.TxID, receipt.Status, receipt.WriteCount)
		if receipt.Error != "" {
			fmt.Printf("    Error: %s\n", receipt.Error)
		}
	}

	fmt.Printf("State changes: %d\n", len(result.Diff))

	// 8. 模拟共识过程
	fmt.Println("\n=== Simulating consensus ===")
	fmt.Println("Block accepted by consensus")

	// 9. 提交区块
	fmt.Println("\n=== Committing block ===")
	if err := executor.CommitFinalizedBlock(block); err != nil {
		log.Fatal("Commit failed:", err)
	}
	fmt.Println("Block committed successfully")

	// 10. 验证最终状态
	fmt.Println("\n=== Final state ===")

	// 检查区块提交状态
	if committed, blockID := executor.IsBlockCommitted(1); committed {
		fmt.Printf("Block at height 1: %s\n", blockID)
	}

	// 检查交易状态
	for _, tx := range txs {
		txID := tx.GetTxId()
		status, _ := executor.GetTransactionStatus(txID)
		fmt.Printf("Transaction %s: %s\n", txID, status)
	}

	// 显示最终余额（实际应用中应该正确处理余额变更）
	fmt.Println("\nFinal balances (示例，实际需要正确的余额计算):")
	fmt.Printf("  Alice: %s token123\n", db.data["balance_alice_token123"])
	fmt.Printf("  Bob: %s token123\n", db.data["balance_bob_token123"])
	fmt.Printf("  Charlie: %s token123\n", db.data["balance_charlie_token123"])

	// 11. 演示缓存效果
	fmt.Println("\n=== Cache demonstration ===")

	// 再次执行相同区块（从缓存获取）
	result2, err := executor.PreExecuteBlock(block)
	if err != nil {
		log.Fatal("Second PreExecute failed:", err)
	}

	if result2.BlockID == result.BlockID {
		fmt.Println("Successfully retrieved result from cache")
	}

	// 12. 清理旧缓存
	fmt.Println("\n=== Cache cleanup ===")
	executor.CleanupCache(100) // 清理高度100以下的缓存
	fmt.Println("Old cache entries cleaned")

	// 13. 显示注册的Handler
	fmt.Println("\n=== Registered handlers ===")
	handlers := registry.List()
	for _, h := range handlers {
		fmt.Printf("  - %s\n", h)
	}

	fmt.Println("\n=== Example completed successfully ===")
}

func mustMarshal(v interface{}) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
