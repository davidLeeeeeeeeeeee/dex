package main

import (
	"dex/config"
	"dex/db"
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"dex/utils"
	"dex/vm"
	"fmt"
	"strconv"
	"time"

	"google.golang.org/protobuf/proto"
)

// 注册所有节点信息到每个节点的数据库
func registerAllNodes(nodes []*NodeInstance, frostCfg config.FrostConfig) {
	// 准备 Top10000 数据
	maxCount := len(nodes)
	if frostCfg.Committee.TopN > 0 && frostCfg.Committee.TopN < maxCount {
		maxCount = frostCfg.Committee.TopN
	}
	top10000 := &pb.FrostTop10000{
		Height:     0,
		Indices:    make([]uint64, maxCount),
		Addresses:  make([]string, maxCount),
		PublicKeys: make([][]byte, maxCount),
	}

	for i, node := range nodes {
		if i >= maxCount {
			break
		}
		top10000.Indices[i] = uint64(i)
		if node == nil {
			continue
		}
		top10000.Addresses[i] = node.Address

		normalizedKey, err := normalizeSecpPrivKey(node.PrivateKey)
		if err != nil {
			logs.Warn("Failed to normalize private key for node %d: %v", i, err)
			continue
		}
		privKey, err := utils.ParseSecp256k1PrivateKey(normalizedKey)
		if err != nil {
			logs.Warn("Failed to parse private key for node %d: %v", i, err)
			continue
		}
		top10000.PublicKeys[i] = privKey.PubKey().SerializeCompressed()
	}

	vaultConfigs := buildVaultConfigs(frostCfg, len(top10000.Indices))

	for i, node := range nodes {
		if node == nil || node.DBManager == nil {
			continue
		}

		// 注册节点 ID 和 端口 到地址的映射，用于日志归集（Explorer 仍需该映射）
		logs.RegisterNodeMapping(strconv.Itoa(node.ID), node.Address)
		logs.RegisterNodeMapping(node.Port, node.Address)
		logs.RegisterNodeMapping(fmt.Sprintf("127.0.0.1:%s", node.Port), node.Address) // host:port 格式
		logs.RegisterNodeMapping(node.Address, node.Address)                           // 地址本身也注册，确保日志缓冲区正确初始化

		if err := applyFrostBootstrap(node.DBManager, frostCfg, top10000, vaultConfigs); err != nil {
			logs.Warn("Failed to apply frost bootstrap for node %d: %v", node.ID, err)
		}

		// 在当前节点的数据库中注册所有其他节点
		for j, otherNode := range nodes {
			if otherNode == nil || i == j {
				continue
			}

			// 保存其他节点的账户信息（不含余额）
			account := &pb.Account{
				Address: otherNode.Address,
				PublicKeys: &pb.PublicKeys{
					Keys: map[int32][]byte{
						int32(pb.SignAlgo_SIGN_ALGO_ECDSA_P256): []byte(utils.GetKeyManager().GetPublicKey()),
					},
				},
				Ip:      fmt.Sprintf("127.0.0.1:%s", otherNode.Port),
				Index:   uint64(j),
				IsMiner: true,
			}

			// 从创世配置初始化余额（使用分离存储）
			applyGenesisBalances(account.Address, node.DBManager)

			node.DBManager.SaveAccount(account)

			// 保存节点信息
			nodeInfo := &pb.NodeInfo{
				PublicKeys: &pb.PublicKeys{
					Keys: map[int32][]byte{
						int32(pb.SignAlgo_SIGN_ALGO_ECDSA_P256): []byte(fmt.Sprintf("node_%d_pub", j)),
					},
				},
				Ip:       fmt.Sprintf("127.0.0.1:%s", otherNode.Port),
				IsOnline: true,
			}
			node.DBManager.SaveNodeInfo(nodeInfo)
			// 保存索引映射
			indexKey := db.KeyIndexToAccount(uint64(j))
			accountKey := db.KeyAccount(otherNode.Address)
			node.DBManager.EnqueueSet(indexKey, accountKey)

		}

		// 初始化本节点的账户信息（如果还没有）
		account, _ := node.DBManager.GetAccount(node.Address)
		if account == nil {
			account = &pb.Account{
				Address: node.Address,
				PublicKeys: &pb.PublicKeys{
					Keys: map[int32][]byte{
						int32(pb.SignAlgo_SIGN_ALGO_ECDSA_P256): []byte(utils.GetKeyManager().GetPublicKey()),
					},
				},
				Ip:      fmt.Sprintf("127.0.0.1:%s", node.Port),
				Index:   uint64(i),
				IsMiner: true,
			}
			applyGenesisBalances(account.Address, node.DBManager)
			node.DBManager.SaveAccount(account)
		}

		// Initializing tokens
		if err := initGenesisTokens(node.DBManager); err != nil {
			logs.Error("Node %d: initGenesisTokens failed: %v", node.ID, err)
		}

		// Force flush to ensure all registrations are persisted
		node.DBManager.ForceFlush()
		time.Sleep(100 * time.Millisecond) // 确保写入完成

		// 重新扫描数据库重建 bitmap
		if err := node.DBManager.IndexMgr.RebuildBitmapFromDB(); err != nil {
			logs.Error("Failed to rebuild bitmap: %v", err)
		}
	}
}

func buildVaultConfigs(frostCfg config.FrostConfig, minerCount int) map[string]*pb.FrostVaultConfig {
	configs := make(map[string]*pb.FrostVaultConfig)

	for name, chainCfg := range frostCfg.Chains {
		vc := &pb.FrostVaultConfig{
			Chain:          name,
			SignAlgo:       parseSignAlgo(chainCfg.SignAlgo),
			VaultCount:     uint32(chainCfg.VaultsPerChain),
			ThresholdRatio: float32(frostCfg.Vault.ThresholdRatio),
			CommitteeSize:  uint32(frostCfg.Vault.DefaultK),
		}
		configs[name] = vc
	}
	return configs
}

func parseSignAlgo(raw string) pb.SignAlgo {
	switch raw {
	case "ECDSA_P256":
		return pb.SignAlgo_SIGN_ALGO_ECDSA_P256
	case "BLS_BN256_G2":
		return pb.SignAlgo_SIGN_ALGO_BLS_BN256_G2
	case "SCHNORR_SECP256K1_BIP340":
		return pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340
	case "SCHNORR_ALT_BN128":
		return pb.SignAlgo_SIGN_ALGO_SCHNORR_ALT_BN128
	case "ED25519":
		return pb.SignAlgo_SIGN_ALGO_ED25519
	case "ECDSA_SECP256K1":
		return pb.SignAlgo_SIGN_ALGO_ECDSA_SECP256K1
	default:
		return pb.SignAlgo_SIGN_ALGO_UNSPECIFIED
	}
}

func applyFrostBootstrap(dbManager *db.Manager, frostCfg config.FrostConfig, top10000 *pb.FrostTop10000, vaultConfigs map[string]*pb.FrostVaultConfig) error {
	if dbManager == nil || top10000 == nil || len(vaultConfigs) == 0 {
		return nil
	}

	readFn := func(key string) ([]byte, error) {
		val, err := dbManager.Get(key)
		if err != nil {
			if isNotFoundError(err) {
				return nil, nil
			}
			return nil, err
		}
		return val, nil
	}
	scanFn := func(prefix string) (map[string][]byte, error) {
		return dbManager.Scan(prefix)
	}
	sv := vm.NewStateView(readFn, scanFn)

	if existing, exists, err := sv.Get(keys.KeyFrostTop10000()); err != nil {
		return err
	} else if !exists || len(existing) == 0 {
		data, err := proto.Marshal(top10000)
		if err != nil {
			return err
		}
		sv.Set(keys.KeyFrostTop10000(), data)
	}

	epochID := uint64(1)
	triggerHeight := uint64(0)
	commitWindow := uint64(frostCfg.Transition.DkgCommitWindowBlocks)
	sharingWindow := uint64(frostCfg.Transition.DkgSharingWindowBlocks)
	disputeWindow := uint64(frostCfg.Transition.DkgDisputeWindowBlocks)

	for chainName, cfg := range vaultConfigs {
		if cfg == nil {
			continue
		}
		if err := vm.InitVaultConfig(sv, chainName, cfg); err != nil {
			logs.Warn("InitVaultConfig failed for chain %s: %v", chainName, err)
		}
		if err := vm.InitVaultStates(sv, chainName, epochID); err != nil {
			logs.Warn("InitVaultStates failed for chain %s: %v", chainName, err)
		}
		if err := vm.InitVaultTransitions(sv, chainName, epochID, triggerHeight, commitWindow, sharingWindow, disputeWindow); err != nil {
			logs.Warn("InitVaultTransitions failed for chain %s: %v", chainName, err)
		}
	}

	for _, op := range sv.Diff() {
		if op.Del {
			dbManager.EnqueueDel(op.Key)
			continue
		}
		dbManager.EnqueueSet(op.Key, string(op.Value))
	}

	return dbManager.ForceFlush()
}

// initGenesisTokens 初始化创世代币到数据库
func initGenesisTokens(dbManager *db.Manager) error {
	if genesisConfig == nil {
		return nil
	}

	// 创建空的 TokenRegistry（不再包含 Tokens map）
	registry := &pb.TokenRegistry{}

	for _, t := range genesisConfig.Tokens {
		token := &pb.Token{
			Address:     t.Address,
			Symbol:      t.Symbol,
			Name:        t.Name,
			Owner:       "0x0",
			TotalSupply: t.TotalSupply,
			CanMint:     true,
		}

		// 存入单个 Token 详情（KeyToken 独立存储）
		tokenData, _ := proto.Marshal(token)
		tokenKey := keys.KeyToken(t.Address)
		dbManager.EnqueueSet(tokenKey, string(tokenData))
	}

	// 存入空 Registry（保留兼容性）
	registryData, _ := proto.Marshal(registry)
	dbManager.EnqueueSet(keys.KeyTokenRegistry(), string(registryData))

	return nil
}

// applyGenesisBalances 从创世配置应用余额到账户（使用分离存储）
func applyGenesisBalances(address string, dbManager *db.Manager) {
	if genesisConfig == nil || dbManager == nil {
		return
	}

	// 检查该地址是否有创世分配
	alloc, exists := genesisConfig.Alloc[address]
	if !exists {
		// 如果没有具体地址分配，尝试使用 "default" 模板
		alloc, exists = genesisConfig.Alloc["default"]
		if !exists {
			return
		}
	}

	// 使用 KeyBalance 分离存储余额
	for tokenAddr, gb := range alloc.Balances {
		bal := &pb.TokenBalanceRecord{
			Balance: &pb.TokenBalance{
				Balance:            gb.Balance,
				MinerLockedBalance: gb.MinerLockedBalance,
			},
		}
		balData, _ := proto.Marshal(bal)
		balKey := keys.KeyBalance(address, tokenAddr)
		dbManager.EnqueueSet(balKey, string(balData))
	}
}
