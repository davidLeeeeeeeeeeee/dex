package db

import (
	"dex/pb"
	"errors"
	"strings"

	"github.com/cockroachdb/pebble"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"
)

// SaveAccount stores an Account in the database
//
// ⚠️ DEPRECATED API - 仅用于过渡期兼容
func (mgr *Manager) SaveAccount(account *pb.Account) error {
	key := KeyAccount(account.Address)
	data, err := ProtoMarshal(account)
	if err != nil {
		return err
	}
	mgr.EnqueueSet(key, string(data))
	return nil
}

// GetAccount retrieves an Account from the database
func (mgr *Manager) GetAccount(address string) (*pb.Account, error) {
	key := KeyAccount(address)
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

// CalcStake 计算账户质押金额
func (mgr *Manager) CalcStake(addr string) (decimal.Decimal, error) {
	balKey := KeyBalance(addr, "FB")
	val, err := mgr.Read(balKey)
	if err != nil || val == "" {
		return decimal.Zero, nil
	}
	var record pb.TokenBalanceRecord
	if err := ProtoUnmarshal([]byte(val), &record); err != nil || record.Balance == nil {
		return decimal.Zero, nil
	}
	ml, err := decimal.NewFromString(record.Balance.MinerLockedBalance)
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
		_ = mgr.DeleteKey(oldKey)
		mgr.EnqueueSet(newKey, "")
	}
	return nil
}

var maxStake, _ = decimal.NewFromString("1e30")

func buildStakeIndexKey(stake decimal.Decimal, address string) string {
	if stake.Cmp(decimal.Zero) < 0 {
		stake = decimal.Zero
	}
	inv := maxStake.Sub(stake)
	if inv.Cmp(decimal.Zero) < 0 {
		inv = decimal.Zero
	}
	invStr := inv.String()
	padNeeded := 32 - len(invStr)
	if padNeeded < 0 {
		padNeeded = 0
	}
	return KeyStakeIndex(strings.Repeat("0", padNeeded)+invStr, address)
}

func (mgr *Manager) getAccountByIndex(idx uint64) (*pb.Account, error) {
	indexKey := []byte(KeyIndexToAccount(idx))

	raw, closer, err := mgr.Db.Get(indexKey)
	if err != nil {
		return nil, err
	}
	addrBytes := make([]byte, len(raw))
	copy(addrBytes, raw)
	closer.Close()

	raw2, closer2, err := mgr.Db.Get(addrBytes)
	if err != nil {
		return nil, err
	}
	accBytes := make([]byte, len(raw2))
	copy(accBytes, raw2)
	closer2.Close()

	var acc pb.Account
	if err := proto.Unmarshal(accBytes, &acc); err != nil {
		return nil, err
	}
	return &acc, nil
}

// GetToken 获取 Token 信息
func (mgr *Manager) GetToken(tokenAddress string) (*pb.Token, error) {
	key := KeyToken(tokenAddress)
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
func (mgr *Manager) GetTokenRegistry() (*pb.TokenRegistry, error) {
	key := KeyTokenRegistry()
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

// suppress unused import
var _ = errors.Is
var _ = pebble.ErrNotFound
