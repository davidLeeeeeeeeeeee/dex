// vm/frost_dkg_handlers_test.go
// 测试 DKG 投诉裁决的 punish/slash 逻辑

package vm

import (
	"dex/keys"
	"dex/pb"
	"math/big"
	"testing"

	"google.golang.org/protobuf/proto"
)

// TestSlashBond 测试罚没 bond
func TestSlashBond(t *testing.T) {
	sv := NewMockStateView()

	// 创建测试账户（不含余额）
	account := &pb.Account{
		Address: "0x0001",
	}
	accountKey := keys.KeyAccount("0x0001")
	accountData, _ := proto.Marshal(account)
	sv.Set(accountKey, accountData)

	// 使用分离存储设置余额
	bal := &pb.TokenBalanceRecord{
		Balance: &pb.TokenBalance{
			Balance:            "1000000000000000000000", // 1000 FB
			MinerLockedBalance: "0",
		},
	}
	balKey := keys.KeyBalance("0x0001", "FB")
	balData, _ := proto.Marshal(bal)
	sv.Set(balKey, balData)

	// 测试罚没 bond
	bondAmount, _ := new(big.Int).SetString("100000000000000000000", 10) // 100 FB
	ops, err := slashBond(sv, "0x0001", bondAmount, "test false complaint")
	if err != nil {
		t.Fatalf("slashBond failed: %v", err)
	}

	if len(ops) != 1 {
		t.Fatalf("expected 1 WriteOp, got %d", len(ops))
	}

	// 验证余额已更新（使用分离存储读取）
	updatedBal := GetBalance(sv, "0x0001", "FB")
	newBalance, _ := new(big.Int).SetString(updatedBal.Balance, 10)
	expectedBalance, _ := new(big.Int).SetString("900000000000000000000", 10) // 900 FB
	if newBalance.Cmp(expectedBalance) != 0 {
		t.Fatalf("expected balance %s, got %s", expectedBalance.String(), newBalance.String())
	}
}

// TestSlashMinerStake 测试罚没矿工质押金
func TestSlashMinerStake(t *testing.T) {
	sv := NewMockStateView()

	// 创建测试账户（不含余额）
	account := &pb.Account{
		Address: "0x0002",
	}
	accountKey := keys.KeyAccount("0x0002")
	accountData, _ := proto.Marshal(account)
	sv.Set(accountKey, accountData)

	// 使用分离存储设置余额（有质押金）
	bal := &pb.TokenBalanceRecord{
		Balance: &pb.TokenBalance{
			Balance:            "500000000000000000000",  // 500 FB
			MinerLockedBalance: "1000000000000000000000", // 1000 FB 质押
		},
	}
	balKey := keys.KeyBalance("0x0002", "FB")
	balData, _ := proto.Marshal(bal)
	sv.Set(balKey, balData)

	// 测试罚没质押金（100%）
	ops, err := slashMinerStake(sv, "0x0002", "test dealer fault")
	if err != nil {
		t.Fatalf("slashMinerStake failed: %v", err)
	}

	if len(ops) != 1 {
		t.Fatalf("expected 1 WriteOp, got %d", len(ops))
	}

	// 验证质押金已清零（使用分离存储读取）
	updatedBal := GetBalance(sv, "0x0002", "FB")
	if updatedBal.MinerLockedBalance != "0" {
		t.Fatalf("expected MinerLockedBalance to be 0, got %s", updatedBal.MinerLockedBalance)
	}

	// 验证可用余额未变
	if updatedBal.Balance != "500000000000000000000" {
		t.Fatalf("expected Balance to remain 500 FB, got %s", updatedBal.Balance)
	}
}

// NewMockStateView 创建模拟的 StateView 用于测试
type MockStateView struct {
	data map[string][]byte
}

func NewMockStateView() *MockStateView {
	return &MockStateView{
		data: make(map[string][]byte),
	}
}

func (m *MockStateView) Get(key string) ([]byte, bool, error) {
	val, exists := m.data[key]
	return val, exists, nil
}

func (m *MockStateView) Set(key string, val []byte) {
	m.data[key] = val
}

func (m *MockStateView) Del(key string) {
	delete(m.data, key)
}

func (m *MockStateView) Snapshot() int {
	return 0
}

func (m *MockStateView) Revert(snap int) error {
	return nil
}

func (m *MockStateView) Diff() []WriteOp {
	return nil
}

func (m *MockStateView) Scan(prefix string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	for k, v := range m.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			result[k] = v
		}
	}
	return result, nil
}
