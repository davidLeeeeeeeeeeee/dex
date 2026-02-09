package vm

import (
	"dex/keys"
	"dex/pb"

	"google.golang.org/protobuf/proto"
)

// ============================================
// 余额读写辅助函数
// 使用分离存储模式：v1_balance_{address}_{token}
// ============================================

// GetBalance 获取账户的单个代币余额
// 如果不存在返回零余额（不返回 error）
func GetBalance(sv StateView, addr, token string) *pb.TokenBalance {
	key := keys.KeyBalance(addr, token)
	data, exists, err := sv.Get(key)
	if err != nil || !exists || len(data) == 0 {
		return &pb.TokenBalance{
			Balance: "0",
		}
	}

	var record pb.TokenBalanceRecord
	if err := unmarshalProtoCompat(data, &record); err != nil {
		return &pb.TokenBalance{
			Balance: "0",
		}
	}

	if record.Balance == nil {
		return &pb.TokenBalance{
			Balance: "0",
		}
	}

	return record.Balance
}

// SetBalance 设置账户的单个代币余额
func SetBalance(sv StateView, addr, token string, balance *pb.TokenBalance) {
	key := keys.KeyBalance(addr, token)
	record := &pb.TokenBalanceRecord{
		Address: addr,
		Token:   token,
		Balance: balance,
	}
	data, _ := proto.Marshal(record)
	sv.Set(key, data)
}

// GetBalanceOrCreate 获取余额，如果不存在则创建零余额
// 返回余额和是否是新创建的标志
func GetBalanceOrCreate(sv StateView, addr, token string) (*pb.TokenBalance, bool) {
	key := keys.KeyBalance(addr, token)
	data, exists, err := sv.Get(key)
	if err != nil || !exists || len(data) == 0 {
		return &pb.TokenBalance{
			Balance: "0",
		}, true
	}

	var record pb.TokenBalanceRecord
	if err := unmarshalProtoCompat(data, &record); err != nil {
		return &pb.TokenBalance{
			Balance: "0",
		}, true
	}

	if record.Balance == nil {
		return &pb.TokenBalance{
			Balance: "0",
		}, true
	}

	return record.Balance, false
}

// EnsureBalance 确保账户有对应代币的余额记录
// 如果不存在则创建零余额
func EnsureBalance(sv StateView, addr, token string) *pb.TokenBalance {
	bal, isNew := GetBalanceOrCreate(sv, addr, token)
	if isNew {
		SetBalance(sv, addr, token, bal)
	}
	return bal
}
