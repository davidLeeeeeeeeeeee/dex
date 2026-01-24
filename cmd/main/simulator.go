package main

import (
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"fmt"
	mrand "math/rand"
	"time"
)

// TxSimulator 交易模拟器
type TxSimulator struct {
	nodes []*NodeInstance
}

func NewTxSimulator(nodes []*NodeInstance) *TxSimulator {
	return &TxSimulator{nodes: nodes}
}

func (s *TxSimulator) Start() {
	go s.runRandomTransfers()
	go s.runWitnessScenario()
	go s.runWithdrawScenario()
	go s.runOrderScenario() // 订单交易模拟
}

func (s *TxSimulator) runRandomTransfers() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	nonceMap := make(map[string]uint64)

	for range ticker.C {
		// 随机选择一个发送方和接收方
		fromIdx := mrand.Intn(len(s.nodes))
		toIdx := mrand.Intn(len(s.nodes))
		fromNode := s.nodes[fromIdx]
		toNode := s.nodes[toIdx]

		if fromNode == nil || toNode == nil {
			continue
		}

		nonceMap[fromNode.Address]++
		nonce := nonceMap[fromNode.Address]

		tx := generateTransferTx(fromNode.Address, toNode.Address, "FB", "10", "1", nonce)

		if err := fromNode.TxPool.StoreAnyTx(tx); err == nil {
			logs.Trace("Simulator: Added random transfer %s from %s to %s", tx.GetBase().TxId, fromNode.Address, toNode.Address)
		}
	}
}

func (s *TxSimulator) runWitnessScenario() {
	// 周期性检测 Vault 状态，有 ACTIVE 的 Vault 才发送上账请求
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	nonceMap := make(map[string]uint64)
	witnessesRegistered := false
	vaultReady := false
	requestSent := false
	var pendingReqID string

	for range ticker.C {
		if len(s.nodes) == 0 || s.nodes[0] == nil {
			continue
		}

		// 首先确保有见证者注册（只做一次）
		if !witnessesRegistered {
			logs.Info("Simulator: Registering witnesses...")
			for i := 1; i < 5 && i < len(s.nodes); i++ {
				witnessNode := s.nodes[i]
				nonceMap[witnessNode.Address]++

				stakeTx := generateWitnessStakeTx(
					witnessNode.Address,
					pb.OrderOp_ADD,
					"1000000000000000000000", // 质押 1000 Token (18位小数)
					nonceMap[witnessNode.Address],
				)
				if err := witnessNode.TxPool.StoreAnyTx(stakeTx); err != nil {
					logs.Error("Simulator: Failed to submit witness stake tx: %v", err)
				} else {
					logs.Info("Simulator: Witness %s staked", witnessNode.Address)
				}
			}
			witnessesRegistered = true
			continue
		}

		// 检测是否有 ACTIVE 的 Vault
		if !vaultReady {
			vaultReady = s.checkVaultActive()
			if !vaultReady {
				logs.Trace("Simulator: No ACTIVE vault yet, waiting...")
				continue
			}
			logs.Info("Simulator: Detected ACTIVE vault, ready to send witness requests")
		}

		// 发送上账请求（只发一次用于测试）
		if !requestSent {
			userNode := s.nodes[0]
			nonceMap[userNode.Address]++

			reqTx := generateWitnessRequestTx(
				userNode.Address,
				"btc",
				"tx_hash_"+time.Now().Format("150405"),
				"BTC",
				"500",
				userNode.Address,
				"5",
				nonceMap[userNode.Address],
			)

			pendingReqID = reqTx.GetBase().TxId
			if err := userNode.TxPool.StoreAnyTx(reqTx); err != nil {
				logs.Error("Simulator: Failed to submit witness request tx: %v", err)
				continue
			}
			logs.Info("Simulator: Submitted WitnessRequestTx %s", pendingReqID)
			requestSent = true
			continue // 下一轮再投票
		}

		// 发送见证者投票
		if pendingReqID != "" {
			logs.Info("Simulator: Submitting witness votes for request %s", pendingReqID)
			for i := 1; i < 5 && i < len(s.nodes); i++ {
				witnessNode := s.nodes[i]
				nonceMap[witnessNode.Address]++

				voteTx := generateWitnessVoteTx(
					witnessNode.Address,
					pendingReqID,
					pb.WitnessVoteType_VOTE_PASS,
					nonceMap[witnessNode.Address],
				)
				if err := witnessNode.TxPool.StoreAnyTx(voteTx); err != nil {
					logs.Error("Simulator: Failed to submit vote tx: %v", err)
				} else {
					logs.Info("Simulator: Witness %s voted PASS for %s", witnessNode.Address, pendingReqID)
				}
			}
			logs.Info("Simulator: Witness Scenario completed for Request %s", pendingReqID)
			pendingReqID = ""
			// 测试完成后停止
			return
		}
	}
}

// checkVaultActive 检查是否有任何 Vault 处于 ACTIVE 状态
func (s *TxSimulator) checkVaultActive() bool {
	if len(s.nodes) == 0 || s.nodes[0] == nil || s.nodes[0].DBManager == nil {
		return false
	}

	dbMgr := s.nodes[0].DBManager

	// 检查常见链的 Vault 0 状态
	chains := []string{"btc", "BTC", "eth", "ETH"}
	for _, chain := range chains {
		key := keys.KeyFrostVaultState(chain, 0)
		state, err := dbMgr.GetFrostVaultState(key)
		if err != nil {
			continue
		}
		if state != nil && state.Status == "ACTIVE" {
			logs.Info("Simulator: Found ACTIVE vault: chain=%s, vault_id=0, epoch=%d", chain, state.KeyEpoch)
			return true
		}
	}
	return false
}

func (s *TxSimulator) runWithdrawScenario() {
	ticker := time.NewTicker(45 * time.Second)
	defer ticker.Stop()

	nonceMap := make(map[string]uint64)

	for range ticker.C {
		logs.Info("Simulator: Starting Withdraw Scenario...")

		userNode := s.nodes[mrand.Intn(len(s.nodes))]
		if userNode == nil {
			continue
		}

		nonceMap[userNode.Address]++

		withdrawTx := generateWithdrawRequestTx(
			userNode.Address,
			"btc",
			"BTC",
			"bc1qtestaddress",
			"50",
			nonceMap[userNode.Address],
		)

		if err := userNode.TxPool.StoreAnyTx(withdrawTx); err == nil {
			logs.Info("Simulator: Withdraw Request %s sent", withdrawTx.GetBase().TxId)
		}
	}
}

// runOrderScenario 模拟订单交易（买单/卖单）
func (s *TxSimulator) runOrderScenario() {
	// 延迟启动，等待账户有足够余额
	time.Sleep(5 * time.Second)

	// 周期性生成新订单（每 2 秒生成一笔订单）
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	nonceMap := make(map[string]uint64)

	for range ticker.C {
		if len(s.nodes) == 0 {
			continue
		}

		// 每次生成 5-10 笔订单
		orderCount := 5 + mrand.Intn(6)
		for i := 0; i < orderCount; i++ {
			// 随机选择一个节点
			nodeIdx := mrand.Intn(len(s.nodes))
			node := s.nodes[nodeIdx]
			if node == nil {
				continue
			}

			nonceMap[node.Address]++

			// 随机价格和数量（使用较小的数量，避免余额不足）
			basePrice := 1.0 + float64(mrand.Intn(10))*0.1
			amount := 1.0 + float64(mrand.Intn(5))

			// 随机决定买单还是卖单
			isBuyOrder := mrand.Intn(2) == 0

			var tx *pb.AnyTx
			if isBuyOrder {
				// 买单：用 USDT 买 FB
				tx = generateOrderTx(
					node.Address,
					"FB",                           // base_token - 想要买入的代币
					"USDT",                         // quote_token - 用于支付的代币
					fmt.Sprintf("%.2f", amount),    // 想买入的 FB 数量
					fmt.Sprintf("%.2f", basePrice), // 每个 FB 的价格（以 USDT 计）
					nonceMap[node.Address],
					pb.OrderSide_BUY,
				)
				logs.Trace("Simulator: Added BUY order %s from %s, buy %.2f FB @ %.2f USDT",
					tx.GetBase().TxId, node.Address, amount, basePrice)
			} else {
				// 卖单：卖 FB 换 USDT
				tx = generateOrderTx(
					node.Address,
					"FB",                           // base_token - 要卖出的代币
					"USDT",                         // quote_token - 想要获得的代币
					fmt.Sprintf("%.2f", amount),    // 要卖出的 FB 数量
					fmt.Sprintf("%.2f", basePrice), // 每个 FB 的价格（以 USDT 计）
					nonceMap[node.Address],
					pb.OrderSide_SELL,
				)
				logs.Trace("Simulator: Added SELL order %s from %s, sell %.2f FB @ %.2f USDT",
					tx.GetBase().TxId, node.Address, amount, basePrice)
			}

			if err := node.TxPool.StoreAnyTx(tx); err != nil {
				logs.Error("Simulator: Failed to add order: %v", err)
			}
		}
	}
}
