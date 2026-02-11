package main

import (
	"dex/keys"
	"dex/logs"
	"dex/pb"
	mrand "math/rand"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// TxSimulator 交易模拟器
type TxSimulator struct {
	nodes  []*NodeInstance
	nonces map[string]uint64
	mu     sync.Mutex
	paused atomic.Bool
}

func NewTxSimulator(nodes []*NodeInstance) *TxSimulator {
	return &TxSimulator{
		nodes:  nodes,
		nonces: make(map[string]uint64),
	}
}

func (s *TxSimulator) getNextNonce(addr string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nonces == nil {
		s.nonces = make(map[string]uint64)
	}
	s.nonces[addr]++
	return s.nonces[addr]
}

func (s *TxSimulator) Start() {
	if s == nil {
		logs.Error("SIMULATOR: TxSimulator is nil, skip start")
		return
	}
	go s.monitorTxPool() // 监控交易池压力
	go s.runRandomTransfers()
	go s.runWitnessRegistration() // 见证者质押注册
	go s.runRechargeScenario()    // 见证者上账请求
	go s.runWitnessVoteWorker()   // 自动化见证者投票
	go s.runWithdrawScenario()
	go s.runOrderScenario()             // 订单交易模拟
	go s.RunMassOrderScenarioAsync(300) // 自动触发大批量订单场景测试 ShortTxs
}

func (s *TxSimulator) monitorTxPool() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if len(s.nodes) == 0 || s.nodes[0] == nil || s.nodes[0].TxPool == nil {
			continue
		}

		// 检查第一个节点的交易池作为参考（或计算平均值）
		pending := s.nodes[0].TxPool.PendingLen()

		if pending > 5000 {
			if !s.paused.Load() {
				logs.Warn("SIMULATOR: TxPool overload detected (%d pending), PAUSING tx generation", pending)
				s.paused.Store(true)
			}
		} else if pending < 2000 {
			if s.paused.Load() {
				logs.Info("SIMULATOR: TxPool pressure relieved (%d pending), RESUMING tx generation", pending)
				s.paused.Store(false)
			}
		}
	}
}

func (s *TxSimulator) submitTx(node *NodeInstance, tx *pb.AnyTx) {
	if node == nil || tx == nil {
		return
	}
	// 使用 SubmitTx 发送到交易池，并触发广播回调
	err := node.TxPool.SubmitTx(tx, "127.0.0.1", func(txID string) {
		if node.SenderManager != nil {
			node.SenderManager.BroadcastTx(tx)
		}
	})
	if err != nil {
		logs.Error("Simulator: Failed to submit tx %s: %v", tx.GetBase().TxId, err)
	}
}

func (s *TxSimulator) runRandomTransfers() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if s.paused.Load() {
			continue
		}
		// 随机选择一个发送方和接收方
		if len(s.nodes) == 0 {
			continue
		}
		fromIdx := mrand.Intn(len(s.nodes))
		toIdx := mrand.Intn(len(s.nodes))
		fromNode := s.nodes[fromIdx]
		toNode := s.nodes[toIdx]

		if fromNode == nil || toNode == nil {
			continue
		}

		nonce := s.getNextNonce(fromNode.Address)

		tx := generateTransferTx(fromNode.Address, toNode.Address, "FB", "10", "1", nonce)

		s.submitTx(fromNode, tx)
		logs.Trace("Simulator: Added random transfer %s from %s to %s (nonce=%d)", tx.GetBase().TxId, fromNode.Address, toNode.Address, nonce)
	}
}

func (s *TxSimulator) runWitnessRegistration() {
	// 设置日志上下文为第一个节点，使得模拟器核心日志能显示在第一个节点的 Explorer 日志中
	if len(s.nodes) > 0 && s.nodes[0] != nil {
		logs.SetThreadNodeContext(s.nodes[0].Address)
	}

	// 注册见证者不需要等待 Vault 激活，可以尽早执行
	time.Sleep(15 * time.Second) // 等待节点完全启动并清空旧数据
	if len(s.nodes) < 2 {
		logs.Info("SIMULATOR: Not enough nodes for witness registration (%d)", len(s.nodes))
		return
	}

	logs.Info("SIMULATOR: Starting witness registration for 5 nodes...")
	for i := 1; i < 6 && i < len(s.nodes); i++ {
		witnessNode := s.nodes[i]
		if witnessNode == nil {
			continue
		}

		nonce := s.getNextNonce(witnessNode.Address)

		// 质押 10,000,000 units (Genesis balance is 1,000,000,000)
		stakeAmount := "10000000"
		stakeTx := generateWitnessStakeTx(
			witnessNode.Address,
			pb.OrderOp_ADD,
			stakeAmount,
			nonce,
		)

		logs.Info("SIMULATOR: Submitting WitnessStakeTx for Node %d (%s), Amount: %s, Nonce: %d", i, witnessNode.Address, stakeAmount, nonce)
		s.submitTx(witnessNode, stakeTx)
		time.Sleep(100 * time.Millisecond) // 稍微错开
	}
	logs.Info("SIMULATOR: Witness registration transactions submitted.")
}

func (s *TxSimulator) runRechargeScenario() {
	// 设置日志上下文
	if len(s.nodes) > 0 && s.nodes[0] != nil {
		logs.SetThreadNodeContext(s.nodes[0].Address)
	}

	// 周期性发送上账请求，演示全流程。见证人会自动投票并完成上账。
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if len(s.nodes) == 0 || s.nodes[0] == nil {
			continue
		}

		// 检测是否有 ACTIVE 的 Vault (上账需要 Vault 存在)
		if !s.checkVaultActive() {
			logs.Trace("Simulator: No ACTIVE vault yet, waiting to send recharge request...")
			continue
		}

		userNode := s.nodes[0]
		nonce := s.getNextNonce(userNode.Address)

		reqTx := generateWitnessRequestTx(
			userNode.Address,
			"btc",
			"tx_hash_"+time.Now().Format("150405"),
			0,                     // 模拟 Vout 0
			[]byte("lock_script"), // 模拟锁定脚本
			"BTC",
			"500",
			userNode.Address,
			"5",
			nonce,
		)

		s.submitTx(userNode, reqTx)
		logs.Info("SIMULATOR: Submitted WitnessRequestTx %s (nonce=%d) for User %s", reqTx.GetBase().TxId, nonce, userNode.Address)
	}
}

// runWitnessVoteWorker 见证者自发投票工人
func (s *TxSimulator) runWitnessVoteWorker() {
	// 周期性扫描并为待处理请求投票
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// 记录已经投过票的请求，避免重复投票
	votedRequests := make(map[string]bool)

	for range ticker.C {
		if len(s.nodes) < 2 {
			continue
		}

		// 简单的模拟逻辑：检测是否有正在进行的投票请求，并让所有见证者都投赞成票
		// 在实际系统中，这通常由节点的本地事件触发
		for _, node := range s.nodes {
			if node == nil {
				continue
			}
			if node.WitnessService == nil {
				continue
			}

			// 获取所有活跃请求
			activeRequests := node.WitnessService.GetActiveRequests()
			for _, req := range activeRequests {
				if req.Status != pb.RechargeRequestStatus_RECHARGE_VOTING {
					continue
				}

				// 检查当前节点（见证者）是否在选定列表中
				isTarget := false
				for _, addr := range req.SelectedWitnesses {
					if addr == node.Address {
						isTarget = true
						break
					}
				}

				if !isTarget {
					continue
				}

				// 检查本节点是否已经投过票
				voteKey := req.RequestId + ":" + node.Address
				if votedRequests[voteKey] {
					continue
				}

				// 提交投票
				nonce := s.getNextNonce(node.Address)
				voteTx := generateWitnessVoteTx(
					node.Address,
					req.RequestId,
					pb.WitnessVoteType_VOTE_PASS,
					nonce,
				)

				// 切换日志上下文到投票节点，让投票记录显示在见证者节点的控制面板里
				logs.SetThreadNodeContext(node.Address)
				s.submitTx(node, voteTx)
				logs.Info("SIMULATOR: Witness %s auto-voted PASS for request %s (nonce=%d)", node.Address, req.RequestId, nonce)
				votedRequests[voteKey] = true
			}
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

	for range ticker.C {
		logs.Info("Simulator: Starting Withdraw Scenario...")
		if len(s.nodes) == 0 {
			continue
		}

		userNode := s.nodes[mrand.Intn(len(s.nodes))]
		if userNode == nil {
			continue
		}

		nonce := s.getNextNonce(userNode.Address)

		withdrawTx := generateWithdrawRequestTx(
			userNode.Address,
			"btc",
			"BTC",
			"bc1qtestaddress",
			"50",
			nonce,
		)

		s.submitTx(userNode, withdrawTx)
		logs.Info("Simulator: Withdraw Request %s sent (nonce=%d)", withdrawTx.GetBase().TxId, nonce)
	}
}

// runOrderScenario 模拟订单交易（买单/卖单）
func (s *TxSimulator) runOrderScenario() {
	// 延迟启动，等待账户有足够余额
	time.Sleep(5 * time.Second)

	// 周期性生成新订单（每 0.5 秒生成一批订单）
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		if s.paused.Load() {
			continue
		}
		if len(s.nodes) == 0 {
			continue
		}

		// 每次生成 10-20 笔订单
		orderCount := 10 + mrand.Intn(50)
		for i := 0; i < orderCount; i++ {
			// 随机选择一个节点
			nodeIdx := mrand.Intn(len(s.nodes))
			node := s.nodes[nodeIdx]
			if node == nil {
				continue
			}

			nonce := s.getNextNonce(node.Address)

			// 随机价格和数量（使用较小的数量，避免余额不足）
			basePrice := int64(10 + mrand.Intn(10))
			amount := int64(1 + mrand.Intn(5))

			// 随机决定买单还是卖单
			isBuyOrder := mrand.Intn(2) == 0

			var tx *pb.AnyTx
			if isBuyOrder {
				// 买单：用 USDT 买 FB
				tx = generateOrderTx(
					node.Address,
					"FB",                             // base_token - 想要买入的代币
					"USDT",                           // quote_token - 用于支付的代币
					strconv.FormatInt(amount, 10),    // 想买入的 FB 数量
					strconv.FormatInt(basePrice, 10), // 每个 FB 的价格（以 USDT 计）
					nonce,
					pb.OrderSide_BUY,
				)
				s.submitTx(node, tx)
				logs.Trace("Simulator: Added BUY order %s (nonce=%d) from %s, buy %d FB @ %d USDT",
					tx.GetBase().TxId, nonce, node.Address, amount, basePrice)
			} else {
				// 卖单：卖 FB 换 USDT
				tx = generateOrderTx(
					node.Address,
					"FB",                             // base_token - 要卖出的代币
					"USDT",                           // quote_token - 想要获得的代币
					strconv.FormatInt(amount, 10),    // 要卖出的 FB 数量
					strconv.FormatInt(basePrice, 10), // 每个 FB 的价格（以 USDT 计）
					nonce,
					pb.OrderSide_SELL,
				)
				s.submitTx(node, tx)
				logs.Trace("Simulator: Added SELL order %s (nonce=%d) from %s, sell %d FB @ %d USDT",
					tx.GetBase().TxId, nonce, node.Address, amount, basePrice)
			}
		}
	}
}

// RunMassOrderScenario 生成大批量订单以测试 ShortTxs 模式
// 调用此方法会一次性生成指定数量 of orders (default 6000, which exceeds the threshold of MaxTxsPerBlock=2500 for ShortTxs and MaxTxsLimitPerBlock=5000)
func (s *TxSimulator) RunMassOrderScenario(orderCount int) {
	if orderCount <= 0 {
		orderCount = 6000 // 默认 6000 笔，超过所有限制
	}

	logs.Info("SIMULATOR: Starting Mass Order Scenario with %d orders...", orderCount)

	if len(s.nodes) == 0 {
		logs.Error("SIMULATOR: No nodes available for mass order scenario")
		return
	}

	startTime := time.Now()
	submitted := 0

	// 平滑发送配置
	const batchSize = 100                    // 每批发送数量
	const batchDelay = 50 * time.Millisecond // 批次间隔

	for i := 0; i < orderCount; i++ {
		// 压力测试下也要尊重暂停标志
		for s.paused.Load() {
			time.Sleep(1 * time.Second)
		}

		// 轮流使用所有节点
		nodeIdx := i % len(s.nodes)
		node := s.nodes[nodeIdx]
		if node == nil {
			continue
		}

		nonce := s.getNextNonce(node.Address)

		// 生成随机订单参数
		basePrice := int64(1 + mrand.Intn(100))
		amount := int64(1 + mrand.Intn(10))
		isBuyOrder := mrand.Intn(2) == 0

		var side pb.OrderSide
		if isBuyOrder {
			side = pb.OrderSide_BUY
		} else {
			side = pb.OrderSide_SELL
		}

		tx := generateOrderTx(
			node.Address,
			"FB",
			"USDT",
			strconv.FormatInt(amount, 10),
			strconv.FormatInt(basePrice, 10),
			nonce,
			side,
		)

		s.submitTx(node, tx)
		submitted++

		// 每 batchSize 笔暂停一下，让 Gossip 有时间扩散
		if submitted%batchSize == 0 {
			logs.Info("SIMULATOR: Mass Order Progress: %d/%d submitted, pausing %v for gossip...",
				submitted, orderCount, batchDelay)
			time.Sleep(batchDelay)
		}
	}

	elapsed := time.Since(startTime)
	logs.Info("SIMULATOR: Mass Order Scenario completed: %d orders in %v (%.0f tx/s)",
		submitted, elapsed, float64(submitted)/elapsed.Seconds())
}

// RunMassOrderScenarioAsync 异步版本，在后台运行大批量订单测试
func (s *TxSimulator) RunMassOrderScenarioAsync(orderCount int) {
	go func() {
		// 等待系统稳定后再开始
		time.Sleep(30 * time.Second)
		s.RunMassOrderScenario(orderCount)
	}()
}
