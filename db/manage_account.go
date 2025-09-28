package db

import (
	"fmt"
	_ "fmt"
	"github.com/dgraph-io/badger/v4"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"
	"strings"
)

// SaveAccount stores an Account in the database
func (mgr *Manager) SaveAccount(account *Account) error {
	key := "account_" + account.Address
	data, err := ProtoMarshal(account)
	if err != nil {
		return err
	}
	//logs.Trace("SaveAccount key:%s", key)
	mgr.EnqueueSet(key, string(data))
	return nil
}

// GetAccount retrieves an Account from the database
func (mgr *Manager) GetAccount(address string) (*Account, error) {
	key := "account_" + address
	//logs.Trace("GetAccount key:%s", key)
	val, err := mgr.Read(key)
	if err != nil {
		return nil, err
	}
	account := &Account{}
	if err := ProtoUnmarshal([]byte(val), account); err != nil {
		return nil, err
	}
	return account, nil
}

// CalcStake = receive_votes + FB.miner_locked_balance
func CalcStake(acc *Account) (decimal.Decimal, error) {
	rv, err := decimal.NewFromString(acc.ReceiveVotes)
	if err != nil {
		rv = decimal.Zero // 如果acc.ReceiveVotes空或解析失败，可设为0
	}

	fbBal, ok := acc.Balances["FB"]
	if !ok {
		// 说明没有任何FB余额，锁定余额也为0
		return rv, nil
	}
	ml, err2 := decimal.NewFromString(fbBal.MinerLockedBalance)
	if err2 != nil {
		ml = decimal.Zero
	}
	return rv.Add(ml), nil
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

	return "stakeIndex_" + finalStr + "_address:" + address
}

// getAccountByIndex 根据顺序索引取出完整 Account。
// 1. 先用 "indexToAccount_<idx>" 拿到地址字符串
// 2. 再用 "account_<addr>" 拿到 Account 序列化字节并反序列化
func (mgr *Manager) getAccountByIndex(idx uint64) (*Account, error) {
	//----------------------------------------
	// ① 取出地址字符串
	//----------------------------------------
	var addr string
	indexKey := []byte(fmt.Sprintf("indexToAccount_%d", idx))

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
	acc := &Account{}
	if err := proto.Unmarshal(accBytes, acc); err != nil {
		return nil, err
	}
	return acc, nil
}
