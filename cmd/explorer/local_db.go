package main

// local_db.go - Explorer 直接从节点数据库读取数据的本地查询方法
// 当节点不可用时，Explorer 可通过这些方法继续提供区块/交易/账户等查询服务

import (
	"dex/db"
	"dex/keys"
	"dex/pb"
	"fmt"
	"log"
	"strings"

	"google.golang.org/protobuf/proto"
)

// ===================== 区块查询 =====================

// localGetBlockByHeight 从本地节点数据库按高度查询区块
func localGetBlockByHeight(nodeDB *db.Manager, height uint64) (*pb.Block, error) {
	if nodeDB == nil {
		return nil, fmt.Errorf("node database not available")
	}
	return nodeDB.GetBlock(height)
}

// localGetBlockByID 从本地节点数据库按 BlockHash 查询区块
func localGetBlockByID(nodeDB *db.Manager, blockID string) (*pb.Block, error) {
	if nodeDB == nil {
		return nil, fmt.Errorf("node database not available")
	}
	return nodeDB.GetBlockByID(blockID)
}

// localGetLatestHeight 从本地节点数据库获取最新区块高度
func localGetLatestHeight(nodeDB *db.Manager) (uint64, error) {
	if nodeDB == nil {
		return 0, fmt.Errorf("node database not available")
	}
	return nodeDB.GetLatestBlockHeight()
}

// localGetRecentBlocks 从本地节点数据库获取最近的区块列表
func localGetRecentBlocks(nodeDB *db.Manager, count int) ([]*pb.Block, error) {
	if nodeDB == nil {
		return nil, fmt.Errorf("node database not available")
	}
	latestHeight, err := nodeDB.GetLatestBlockHeight()
	if err != nil {
		return nil, err
	}

	startHeight := uint64(0)
	if latestHeight > uint64(count) {
		startHeight = latestHeight - uint64(count) + 1
	}

	return nodeDB.GetBlocksByRange(startHeight, latestHeight)
}

// ===================== 交易查询 =====================

// localGetAnyTxByID 从本地节点数据库获取交易
func localGetAnyTxByID(nodeDB *db.Manager, txID string) (*pb.AnyTx, error) {
	if nodeDB == nil {
		return nil, fmt.Errorf("node database not available")
	}
	return nodeDB.GetAnyTxById(txID)
}

// localGetTxReceipt 从本地节点数据库获取交易回执
func localGetTxReceipt(nodeDB *db.Manager, txID string) (*pb.Receipt, error) {
	if nodeDB == nil {
		return nil, fmt.Errorf("node database not available")
	}
	return nodeDB.GetTxReceipt(txID)
}

// ===================== 账户查询 =====================

// localGetAccount 从本地节点数据库获取账户信息
func localGetAccount(nodeDB *db.Manager, address string) (*pb.Account, error) {
	if nodeDB == nil {
		return nil, fmt.Errorf("node database not available")
	}
	return nodeDB.GetAccount(address)
}

// localGetAccountBalances 从本地节点数据库扫描账户的所有代币余额
func localGetAccountBalances(nodeDB *db.Manager, address string) map[string]*pb.TokenBalance {
	if nodeDB == nil {
		return nil
	}
	balances := make(map[string]*pb.TokenBalance)
	prefix := keys.KeyBalancePrefix(address)

	kvResults, err := nodeDB.ScanByPrefix(prefix, 100)
	if err != nil {
		log.Printf("[localGetAccountBalances] scan error for %s: %v", address, err)
		return balances
	}
	for key, val := range kvResults {
		token := localExtractTokenFromBalanceKey(key, address)
		if token == "" {
			continue
		}
		var record pb.TokenBalanceRecord
		if err := proto.Unmarshal([]byte(val), &record); err == nil && record.Balance != nil {
			balances[token] = record.Balance
		}
	}
	return balances
}

// localExtractTokenFromBalanceKey 从余额 key 中提取 token 地址
func localExtractTokenFromBalanceKey(key, address string) string {
	prefix := keys.KeyBalancePrefix(address)
	if !strings.HasPrefix(key, prefix) {
		return ""
	}
	return key[len(prefix):]
}

// ===================== Token 查询 =====================

// localGetToken 从本地节点数据库获取 Token 信息
func localGetToken(nodeDB *db.Manager, tokenAddress string) (*pb.Token, error) {
	if nodeDB == nil {
		return nil, fmt.Errorf("node database not available")
	}
	return nodeDB.GetToken(tokenAddress)
}
