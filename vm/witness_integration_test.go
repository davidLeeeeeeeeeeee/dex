package vm_test

import (
	"fmt"
	"testing"

	"dex/keys"
	"dex/pb"
	"dex/vm"
	"dex/witness"

	"google.golang.org/protobuf/proto"
)

// ========== Witness 集成测试 ==========

// TestExecutorWithWitnessService 测试 Executor 与 WitnessService 的集成
func TestExecutorWithWitnessService(t *testing.T) {
	db := NewMockDB()
	reg := vm.NewHandlerRegistry()
	vm.RegisterDefaultHandlers(reg)

	// 使用带 WitnessService 的 Executor
	executor := vm.NewExecutorWithWitness(db, reg, nil, nil)

	if executor.GetWitnessService() == nil {
		t.Fatal("WitnessService should not be nil")
	}

	t.Log("Executor with WitnessService created successfully")
}

// TestWitnessStakeTxHandler 测试见证者质押交易处理
func TestWitnessStakeTxHandler(t *testing.T) {
	db := NewMockDB()
	reg := vm.NewHandlerRegistry()
	vm.RegisterDefaultHandlers(reg)

	executor := vm.NewExecutorWithWitness(db, reg, nil, nil)

	// 准备测试账户
	address := "0xTestWitness001"
	account := &pb.Account{
		Address: address,
		Balances: map[string]*pb.TokenBalance{
			"FB": {Balance: "10000000000000000000000", WitnessLockedBalance: "0"}, // 10000 FB
		},
	}
	accountData, _ := proto.Marshal(account)
	db.data[keys.KeyAccount(address)] = accountData

	// 创建质押交易
	stakeTx := &pb.AnyTx{
		Content: &pb.AnyTx_WitnessStakeTx{
			WitnessStakeTx: &pb.WitnessStakeTx{
				Base: &pb.BaseMessage{
					TxId:           "stake_tx_001",
					FromAddress:    address,
					ExecutedHeight: 100,
				},
				Amount: "1000000000000000000000", // 1000 FB
				Op:     pb.OrderOp_ADD,
			},
		},
	}

	// 创建区块
	block := &pb.Block{
		BlockHash:     "block_001",
		PrevBlockHash: "genesis",
		Height:        100,
		Body:          []*pb.AnyTx{stakeTx},
	}

	// 预执行区块
	result, err := executor.PreExecuteBlock(block)
	if err != nil {
		t.Fatalf("PreExecuteBlock failed: %v", err)
	}

	if !result.Valid {
		t.Fatalf("Block should be valid, reason: %s", result.Reason)
	}

	if len(result.Receipts) != 1 {
		t.Fatalf("Expected 1 receipt, got %d", len(result.Receipts))
	}

	if result.Receipts[0].Status != "SUCCEED" {
		t.Fatalf("Expected SUCCEED, got %s, error: %s", result.Receipts[0].Status, result.Receipts[0].Error)
	}

	// 验证 WriteOp
	if len(result.Diff) < 2 {
		t.Fatalf("Expected at least 2 WriteOps (account + witness), got %d", len(result.Diff))
	}

	t.Logf("Stake transaction processed successfully with %d WriteOps", len(result.Diff))

	// 验证 WitnessService 内存状态
	witnessSvc := executor.GetWitnessService()
	witnessInfo, err := witnessSvc.GetWitnessInfo(address)
	if err != nil {
		t.Logf("WitnessInfo not found in service (expected if not synced): %v", err)
	} else {
		t.Logf("WitnessInfo in service: %+v", witnessInfo)
	}
}

// TestWitnessRequestTxHandler 测试入账见证请求处理
func TestWitnessRequestTxHandler(t *testing.T) {
	db := NewMockDB()
	reg := vm.NewHandlerRegistry()
	vm.RegisterDefaultHandlers(reg)

	executor := vm.NewExecutorWithWitness(db, reg, nil, nil)

	// 准备活跃见证者（WitnessService 需要有见证者才能选择）
	for i := 0; i < 5; i++ {
		witnessInfo := &pb.WitnessInfo{
			Address:     fmt.Sprintf("0xWitness%03d", i),
			StakeAmount: "1000000000000000000000", // 1000 FB
			Status:      pb.WitnessStatus_WITNESS_ACTIVE,
		}
		executor.GetWitnessService().LoadWitness(witnessInfo)
	}

	// 准备 Token
	token := &pb.Token{
		Address: "0xUSDT",
		Symbol:  "USDT",
	}
	tokenData, _ := proto.Marshal(token)
	db.data[keys.KeyToken("0xUSDT")] = tokenData

	// 创建入账请求交易
	requestTx := &pb.AnyTx{
		Content: &pb.AnyTx_WitnessRequestTx{
			WitnessRequestTx: &pb.WitnessRequestTx{
				Base: &pb.BaseMessage{
					TxId:           "request_tx_001",
					FromAddress:    "0xRequester001",
					ExecutedHeight: 100,
				},
				NativeChain:     "ETH",
				NativeTxHash:    "0xNativeTxHash001",
				TokenAddress:    "0xUSDT",
				Amount:          "1000000000000000000", // 1 USDT
				ReceiverAddress: "0xReceiver001",
				RechargeFee:     "1000000000000000", // 0.001 USDT
			},
		},
	}

	block := &pb.Block{
		BlockHash:     "block_002",
		PrevBlockHash: "block_001",
		Height:        101,
		Body:          []*pb.AnyTx{requestTx},
	}

	result, err := executor.PreExecuteBlock(block)
	if err != nil {
		t.Fatalf("PreExecuteBlock failed: %v", err)
	}

	if !result.Valid {
		t.Fatalf("Block should be valid, reason: %s", result.Reason)
	}

	if result.Receipts[0].Status != "SUCCEED" {
		t.Fatalf("Expected SUCCEED, got %s, error: %s", result.Receipts[0].Status, result.Receipts[0].Error)
	}

	t.Logf("Request transaction processed successfully with %d WriteOps", len(result.Diff))
}

// TestWitnessVoteTxHandler 测试见证投票处理
func TestWitnessVoteTxHandler(t *testing.T) {
	db := NewMockDB()
	reg := vm.NewHandlerRegistry()
	vm.RegisterDefaultHandlers(reg)

	executor := vm.NewExecutorWithWitness(db, reg, nil, nil)

	// 准备入账请求（状态为 VOTING，并设置 deadline）
	request := &pb.RechargeRequest{
		RequestId:         "request_001",
		NativeChain:       "ETH",
		NativeTxHash:      "0xNativeTxHash001",
		TokenAddress:      "0xUSDT",
		Amount:            "1000000000000000000",
		ReceiverAddress:   "0xReceiver001",
		RequesterAddress:  "0xRequester001",
		Status:            pb.RechargeRequestStatus_RECHARGE_VOTING,
		CreateHeight:      100,
		DeadlineHeight:    200, // 设置足够大的 deadline
		SelectedWitnesses: []string{"0xWitness001", "0xWitness002", "0xWitness003"},
	}
	requestData, _ := proto.Marshal(request)
	db.data[keys.KeyRechargeRequest("request_001")] = requestData

	// 同时加载到 WitnessService 内存中
	executor.GetWitnessService().LoadRequest(request)

	// 创建投票交易
	voteTx := &pb.AnyTx{
		Content: &pb.AnyTx_WitnessVoteTx{
			WitnessVoteTx: &pb.WitnessVoteTx{
				Base: &pb.BaseMessage{
					TxId:           "vote_tx_001",
					FromAddress:    "0xWitness001",
					ExecutedHeight: 101,
				},
				Vote: &pb.WitnessVote{
					RequestId:      "request_001",
					WitnessAddress: "0xWitness001",
					VoteType:       pb.WitnessVoteType_VOTE_PASS,
				},
			},
		},
	}

	block := &pb.Block{
		BlockHash:     "block_003",
		PrevBlockHash: "block_002",
		Height:        101,
		Body:          []*pb.AnyTx{voteTx},
	}

	result, err := executor.PreExecuteBlock(block)
	if err != nil {
		t.Fatalf("PreExecuteBlock failed: %v", err)
	}

	if !result.Valid {
		t.Fatalf("Block should be valid, reason: %s", result.Reason)
	}

	if result.Receipts[0].Status != "SUCCEED" {
		t.Fatalf("Expected SUCCEED, got %s, error: %s", result.Receipts[0].Status, result.Receipts[0].Error)
	}

	t.Logf("Vote transaction processed successfully with %d WriteOps", len(result.Diff))
}

// TestWitnessServiceReset 测试 WitnessService 重置功能
func TestWitnessServiceReset(t *testing.T) {
	config := witness.DefaultConfig()
	svc := witness.NewService(config)
	_ = svc.Start()
	defer svc.Stop()

	// 加载一些数据
	witnessInfo := &pb.WitnessInfo{
		Address:     "0xWitness001",
		StakeAmount: "1000000000000000000000",
		Status:      pb.WitnessStatus_WITNESS_ACTIVE,
	}
	svc.LoadWitness(witnessInfo)

	// 验证数据已加载
	info, err := svc.GetWitnessInfo("0xWitness001")
	if err != nil {
		t.Fatalf("GetWitnessInfo failed: %v", err)
	}
	if info.StakeAmount != "1000000000000000000000" {
		t.Fatalf("Expected stake amount 1000000000000000000000, got %s", info.StakeAmount)
	}

	// 重置
	svc.Reset()

	// 验证数据已清空
	_, err = svc.GetWitnessInfo("0xWitness001")
	if err == nil {
		t.Fatal("Expected error after reset, got nil")
	}

	t.Log("WitnessService reset successfully")
}

// TestWitnessChallengeTxHandler 测试挑战交易处理
func TestWitnessChallengeTxHandler(t *testing.T) {
	db := NewMockDB()
	reg := vm.NewHandlerRegistry()
	vm.RegisterDefaultHandlers(reg)

	executor := vm.NewExecutorWithWitness(db, reg, nil, nil)

	// 准备入账请求（处于公示期）
	request := &pb.RechargeRequest{
		RequestId:        "request_002",
		NativeChain:      "ETH",
		NativeTxHash:     "0xNativeTxHash002",
		TokenAddress:     "0xUSDT",
		Amount:           "1000000000000000000",
		ReceiverAddress:  "0xReceiver001",
		RequesterAddress: "0xRequester001",
		Status:           pb.RechargeRequestStatus_RECHARGE_CHALLENGE_PERIOD,
		CreateHeight:     100,
	}
	requestData, _ := proto.Marshal(request)
	db.data[keys.KeyRechargeRequest("request_002")] = requestData

	// 准备挑战者账户
	challenger := &pb.Account{
		Address: "0xChallenger001",
		Balances: map[string]*pb.TokenBalance{
			"FB": {Balance: "10000000000000000000000"}, // 10000 FB
		},
	}
	challengerData, _ := proto.Marshal(challenger)
	db.data[keys.KeyAccount("0xChallenger001")] = challengerData

	// 创建挑战交易
	challengeTx := &pb.AnyTx{
		Content: &pb.AnyTx_WitnessChallengeTx{
			WitnessChallengeTx: &pb.WitnessChallengeTx{
				Base: &pb.BaseMessage{
					TxId:           "challenge_tx_001",
					FromAddress:    "0xChallenger001",
					ExecutedHeight: 102,
				},
				RequestId:   "request_002",
				StakeAmount: "100000000000000000000", // 100 FB
				Reason:      "Invalid native tx hash",
			},
		},
	}

	block := &pb.Block{
		BlockHash:     "block_004",
		PrevBlockHash: "block_003",
		Height:        102,
		Body:          []*pb.AnyTx{challengeTx},
	}

	result, err := executor.PreExecuteBlock(block)
	if err != nil {
		t.Fatalf("PreExecuteBlock failed: %v", err)
	}

	if !result.Valid {
		t.Fatalf("Block should be valid, reason: %s", result.Reason)
	}

	if result.Receipts[0].Status != "SUCCEED" {
		t.Fatalf("Expected SUCCEED, got %s, error: %s", result.Receipts[0].Status, result.Receipts[0].Error)
	}

	t.Logf("Challenge transaction processed successfully with %d WriteOps", len(result.Diff))
}

// TestArbitrationVoteTxHandler 测试仲裁投票处理
func TestArbitrationVoteTxHandler(t *testing.T) {
	db := NewMockDB()
	reg := vm.NewHandlerRegistry()
	vm.RegisterDefaultHandlers(reg)

	executor := vm.NewExecutorWithWitness(db, reg, nil, nil)

	// 准备挑战记录（处于仲裁期）
	challenge := &pb.ChallengeRecord{
		ChallengeId:       "challenge_001",
		RequestId:         "request_001",
		ChallengerAddress: "0xChallenger001",
		StakeAmount:       "100000000000000000000",
		CreateHeight:      100,
		DeadlineHeight:    200,
		Arbitrators:       []string{"0xArbiter001", "0xArbiter002", "0xArbiter003"},
	}
	challengeData, _ := proto.Marshal(challenge)
	db.data[keys.KeyChallengeRecord("challenge_001")] = challengeData

	// 加载到 WitnessService
	executor.GetWitnessService().LoadChallenge(challenge)

	// 创建仲裁投票交易（使用 WitnessVote 结构）
	arbVoteTx := &pb.AnyTx{
		Content: &pb.AnyTx_ArbitrationVoteTx{
			ArbitrationVoteTx: &pb.ArbitrationVoteTx{
				Base: &pb.BaseMessage{
					TxId:           "arb_vote_tx_001",
					FromAddress:    "0xArbiter001",
					ExecutedHeight: 105,
				},
				ChallengeId: "challenge_001",
				Vote: &pb.WitnessVote{
					RequestId:      "request_001",
					WitnessAddress: "0xArbiter001",
					VoteType:       pb.WitnessVoteType_VOTE_PASS, // 支持原判定
					Reason:         "Evidence supports original decision",
				},
			},
		},
	}

	block := &pb.Block{
		BlockHash:     "block_005",
		PrevBlockHash: "block_004",
		Height:        105,
		Body:          []*pb.AnyTx{arbVoteTx},
	}

	result, err := executor.PreExecuteBlock(block)
	if err != nil {
		t.Fatalf("PreExecuteBlock failed: %v", err)
	}

	if !result.Valid {
		t.Fatalf("Block should be valid, reason: %s", result.Reason)
	}

	if result.Receipts[0].Status != "SUCCEED" {
		t.Fatalf("Expected SUCCEED, got %s, error: %s", result.Receipts[0].Status, result.Receipts[0].Error)
	}

	t.Logf("Arbitration vote transaction processed successfully with %d WriteOps", len(result.Diff))
}

// TestWitnessClaimRewardTxHandler 测试领取奖励处理
func TestWitnessClaimRewardTxHandler(t *testing.T) {
	db := NewMockDB()
	reg := vm.NewHandlerRegistry()
	vm.RegisterDefaultHandlers(reg)

	executor := vm.NewExecutorWithWitness(db, reg, nil, nil)

	// 准备见证者账户（有未领取奖励）
	witnessAddr := "0xWitness001"
	witnessAccount := &pb.Account{
		Address: witnessAddr,
		Balances: map[string]*pb.TokenBalance{
			"FB": {Balance: "1000000000000000000000"}, // 1000 FB
		},
	}
	witnessAccountData, _ := proto.Marshal(witnessAccount)
	db.data[keys.KeyAccount(witnessAddr)] = witnessAccountData

	// 准备见证者信息（有未领取奖励）
	witnessInfo := &pb.WitnessInfo{
		Address:       witnessAddr,
		StakeAmount:   "1000000000000000000000",
		Status:        pb.WitnessStatus_WITNESS_ACTIVE,
		PendingReward: "50000000000000000000", // 50 FB 未领取奖励
	}
	witnessInfoData, _ := proto.Marshal(witnessInfo)
	db.data[keys.KeyWitnessInfo(witnessAddr)] = witnessInfoData

	// 加载到 WitnessService
	executor.GetWitnessService().LoadWitness(witnessInfo)

	// 创建领取奖励交易
	claimTx := &pb.AnyTx{
		Content: &pb.AnyTx_WitnessClaimRewardTx{
			WitnessClaimRewardTx: &pb.WitnessClaimRewardTx{
				Base: &pb.BaseMessage{
					TxId:           "claim_tx_001",
					FromAddress:    witnessAddr,
					ExecutedHeight: 110,
				},
			},
		},
	}

	block := &pb.Block{
		BlockHash:     "block_006",
		PrevBlockHash: "block_005",
		Height:        110,
		Body:          []*pb.AnyTx{claimTx},
	}

	result, err := executor.PreExecuteBlock(block)
	if err != nil {
		t.Fatalf("PreExecuteBlock failed: %v", err)
	}

	if !result.Valid {
		t.Fatalf("Block should be valid, reason: %s", result.Reason)
	}

	if result.Receipts[0].Status != "SUCCEED" {
		t.Fatalf("Expected SUCCEED, got %s, error: %s", result.Receipts[0].Status, result.Receipts[0].Error)
	}

	t.Logf("Claim reward transaction processed successfully with %d WriteOps", len(result.Diff))
}

// TestWitnessUnstakeTxHandler 测试见证者解质押交易处理
func TestWitnessUnstakeTxHandler(t *testing.T) {
	db := NewMockDB()
	reg := vm.NewHandlerRegistry()
	vm.RegisterDefaultHandlers(reg)

	executor := vm.NewExecutorWithWitness(db, reg, nil, nil)

	// 准备测试账户（已质押）
	address := "0xTestWitness002"
	account := &pb.Account{
		Address: address,
		Balances: map[string]*pb.TokenBalance{
			"FB": {Balance: "5000000000000000000000", WitnessLockedBalance: "1000000000000000000000"}, // 5000 FB + 1000 FB locked
		},
	}
	accountData, _ := proto.Marshal(account)
	db.data[keys.KeyAccount(address)] = accountData

	// 准备见证者信息
	witnessInfo := &pb.WitnessInfo{
		Address:     address,
		StakeAmount: "1000000000000000000000",
		Status:      pb.WitnessStatus_WITNESS_ACTIVE,
	}
	witnessInfoData, _ := proto.Marshal(witnessInfo)
	db.data[keys.KeyWitnessInfo(address)] = witnessInfoData

	// 加载到 WitnessService
	executor.GetWitnessService().LoadWitness(witnessInfo)

	// 创建解质押交易
	unstakeTx := &pb.AnyTx{
		Content: &pb.AnyTx_WitnessStakeTx{
			WitnessStakeTx: &pb.WitnessStakeTx{
				Base: &pb.BaseMessage{
					TxId:           "unstake_tx_001",
					FromAddress:    address,
					ExecutedHeight: 100,
				},
				Amount: "500000000000000000000", // 解质押 500 FB
				Op:     pb.OrderOp_REMOVE,
			},
		},
	}

	block := &pb.Block{
		BlockHash:     "block_007",
		PrevBlockHash: "block_006",
		Height:        100,
		Body:          []*pb.AnyTx{unstakeTx},
	}

	result, err := executor.PreExecuteBlock(block)
	if err != nil {
		t.Fatalf("PreExecuteBlock failed: %v", err)
	}

	if !result.Valid {
		t.Fatalf("Block should be valid, reason: %s", result.Reason)
	}

	if result.Receipts[0].Status != "SUCCEED" {
		t.Fatalf("Expected SUCCEED, got %s, error: %s", result.Receipts[0].Status, result.Receipts[0].Error)
	}

	t.Logf("Unstake transaction processed successfully with %d WriteOps", len(result.Diff))
}
