package db

import (
	"dex/pb"
	"strings"

	"github.com/dgraph-io/badger/v4"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"
)

// SaveAccount stores an Account in the database
//
// ⚠️ DEPRECATED API - 仅用于过渡期兼容
// 账户状态现在由 VM 的 applyResult 统一处理：
//   - 可变状态数据写入 StateDB（通过 SyncToStateDB）
//   - 所有数据同时写入 KV（当前阶段保持双写以确保兼容）
//
// 新代码不应直接调用此方法，而应使用 VM 的 WriteOp 机制。
func (mgr *Manager) SaveAccount(account *pb.Account) error {
	key := KeyAccount(account.Address)
	data, err := ProtoMarshal(account)
	if err != nil {
		return err
	}
	// 写入 KV（当前阶段保持双写，后续可移除）
	mgr.EnqueueSet(key, string(data))
	// 注意：StateDB 同步已由 VM 的 applyResult -> SyncToStateDB 统一处理
	// 此处不再重复调用 StateDB.ApplyAccountUpdate
	return nil
}

// GetAccount retrieves an Account from the database
// 优先从 StateDB 读取（最新状态），如果没有则回退到 KV
func (mgr *Manager) GetAccount(address string) (*pb.Account, error) {
	key := KeyAccount(address)

	// 1. 优先尝试从 StateDB 读取（如果已初始化）
	if mgr.StateDB != nil {
		if val, exists, err := mgr.StateDB.Get(key); err == nil && exists && len(val) > 0 {
			account := &pb.Account{}
			if err := ProtoUnmarshal(val, account); err == nil {
				return account, nil
			}
			// 解析失败，回退到 KV
		}
	}

	// 2. 回退到 KV 读取
	val, err := mgr.Read(key)
	if err != nil {
		return nil, err
	}
	account := &pb.Account{}
	if err := ProtoUnmarshal([]byte(val), account); err != nil {
		return nil, err
	}
	return account, nil
}

// CalcStake = FB.miner_locked_balance
func CalcStake(acc *pb.Account) (decimal.Decimal, error) {
	fbBal, ok := acc.Balances["FB"]
	if !ok {
		// 说明没有任何FB余额，锁定余额也为0
		return decimal.Zero, nil
	}
	ml, err := decimal.NewFromString(fbBal.MinerLockedBalance)
	if err != nil {
		ml = decimal.Zero
	}
	return ml, nil
}

// 用来删掉旧stakeIndex再插入新的
func (mgr *Manager) UpdateStakeIndex(oldStake, newStake decimal.Decimal, address string) error {
	oldKey := buildStakeIndexKey(oldStake, address)
	newKey := buildStakeIndexKey(newStake, address)

	if oldKey != newKey {
		// 删除旧
		_ = mgr.DeleteKey(oldKey)
		// 写新
		mgr.EnqueueSet(newKey, "")
	}
	return nil
}

// 假设 maxStake = 1e30 (看你需要多少量级)
var maxStake, _ = decimal.NewFromString("1e30")

// 将 stake 转为倒排字符串
func buildStakeIndexKey(stake decimal.Decimal, address string) string {
	// 0) 若stake<0可能要特殊处理，这里简单max(0, stake)
	if stake.Cmp(decimal.Zero) < 0 {
		stake = decimal.Zero
	}
	// 1) inverted = maxStake - stake
	inv := maxStake.Sub(stake)
	if inv.Cmp(decimal.Zero) < 0 {
		inv = decimal.Zero
	}
	invStr := inv.String()

	// 2) 左零填充(长度看你需要多大，示例设 32)
	padNeeded := 32 - len(invStr)
	if padNeeded < 0 {
		padNeeded = 0
	}
	zeros := strings.Repeat("0", padNeeded)
	finalStr := zeros + invStr

	return KeyStakeIndex(finalStr, address)
}

func (mgr *Manager) getAccountByIndex(idx uint64) (*pb.Account, error) {
	//----------------------------------------
	// ① 取出地址字符串
	//----------------------------------------
	var addr string
	indexKey := []byte(KeyIndexToAccount(idx))

	err := mgr.Db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(indexKey)
		if err != nil {
			return err // badger.ErrKeyNotFound 等
		}
		val, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}
		addr = string(val) // value 就是一段地址字符串
		return nil
	})
	if err != nil {
		return nil, err
	}

	//----------------------------------------
	// ② 取出 Account 对象
	//----------------------------------------
	var accBytes []byte
	accountKey := []byte(addr)

	err = mgr.Db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(accountKey)
		if err != nil {
			return err
		}
		accBytes, err = item.ValueCopy(nil)
		return err
	})
	if err != nil {
		return nil, err
	}

	//----------------------------------------
	// ③ 反序列化并返回
	//----------------------------------------
	acc := &pb.Account{}
	if err := proto.Unmarshal(accBytes, acc); err != nil {
		return nil, err
	}
	return acc, nil
}

// GetToken 获取 Token 信息
// 优先从 StateDB 读取（最新状态），如果没有则回退到 KV
func (mgr *Manager) GetToken(tokenAddress string) (*pb.Token, error) {
	key := KeyToken(tokenAddress)

	// 1. 优先尝试从 StateDB 读取
	if mgr.StateDB != nil {
		if val, exists, err := mgr.StateDB.Get(key); err == nil && exists && len(val) > 0 {
			token := &pb.Token{}
			if err := ProtoUnmarshal(val, token); err == nil {
				return token, nil
			}
		}
	}

	// 2. 回退到 KV 读取
	val, err := mgr.Read(key)
	if err != nil {
		return nil, err
	}
	if val == "" {
		return nil, nil
	}
	token := &pb.Token{}
	if err := ProtoUnmarshal([]byte(val), token); err != nil {
		return nil, err
	}
	return token, nil
}

// GetTokenRegistry 获取 Token 注册表
// 优先从 StateDB 读取（最新状态），如果没有则回退到 KV
func (mgr *Manager) GetTokenRegistry() (*pb.TokenRegistry, error) {
	key := KeyTokenRegistry()

	// 1. 优先尝试从 StateDB 读取
	if mgr.StateDB != nil {
		if val, exists, err := mgr.StateDB.Get(key); err == nil && exists && len(val) > 0 {
			registry := &pb.TokenRegistry{}
			if err := ProtoUnmarshal(val, registry); err == nil {
				return registry, nil
			}
		}
	}

	// 2. 回退到 KV 读取
	val, err := mgr.Read(key)
	if err != nil {
		return nil, err
	}
	if val == "" {
		return nil, nil
	}
	registry := &pb.TokenRegistry{}
	if err := ProtoUnmarshal([]byte(val), registry); err != nil {
		return nil, err
	}
	return registry, nil
}
