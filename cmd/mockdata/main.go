// cmd/mockdata/main.go
// 模拟数据生成器 - 用于生成 Protocol 页面（Frost/Witness）的测试数据
package main

import (
	"crypto/rand"
	"dex/db"
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"math/big"
	"time"

	"google.golang.org/protobuf/proto"
)

var (
	dbPath         = flag.String("db", "./data/db", "数据库路径")
	withdrawCount  = flag.Int("withdraws", 10, "生成提现请求数量")
	rechargeCount  = flag.Int("recharges", 8, "生成入账请求数量")
	dkgSessionsNum = flag.Int("dkg", 3, "生成 DKG 会话数量")
	clean          = flag.Bool("clean", false, "清理现有模拟数据后再生成")
)

func main() {
	flag.Parse()

	// 创建简单的 logger
	logger := logs.NewNodeLogger("mockdata", 100)

	// 打开数据库
	dbMgr, err := db.NewManager(*dbPath, logger)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer dbMgr.Close()

	// 初始化写队列
	dbMgr.InitWriteQueue(100, 100*time.Millisecond)

	if *clean {
		cleanExistingData(dbMgr)
	}

	// 生成模拟数据
	log.Println("=== Generating Mock Protocol Data ===")

	if err := generateWithdraws(dbMgr, *withdrawCount); err != nil {
		log.Printf("Error generating withdraws: %v", err)
	}

	if err := generateRechargeRequests(dbMgr, *rechargeCount); err != nil {
		log.Printf("Error generating recharge requests: %v", err)
	}

	if err := generateDKGSessions(dbMgr, *dkgSessionsNum); err != nil {
		log.Printf("Error generating DKG sessions: %v", err)
	}

	// 强制刷盘
	log.Println("Flushing to disk...")
	if err := dbMgr.ForceFlush(); err != nil {
		log.Printf("Warning: ForceFlush error: %v", err)
	}
	// 等待写队列处理完成
	time.Sleep(500 * time.Millisecond)

	log.Println("=== Mock Data Generation Complete ===")
}

func cleanExistingData(dbMgr *db.Manager) {
	log.Println("Cleaning existing mock data...")
	prefixes := []string{
		"v1_frost_withdraw_",
		"v1_recharge_request_",
		"v1_frost_vault_transition_",
	}
	for _, prefix := range prefixes {
		results, err := dbMgr.Scan(prefix)
		if err != nil {
			continue
		}
		log.Printf("Found %d items with prefix %s", len(results), prefix)
	}
}

func randomHex(n int) string {
	bytes := make([]byte, n)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

func randomAddress() string {
	return "0x" + randomHex(20)
}

func generateWithdraws(dbMgr *db.Manager, count int) error {
	log.Printf("Generating %d withdraw requests...", count)

	chains := []string{"BTC", "ETH", "TRON"}
	assets := []string{"BTC", "USDT", "ETH"}
	statuses := []string{"QUEUED", "SIGNED", "QUEUED", "QUEUED"} // 多些 QUEUED

	for i := 0; i < count; i++ {
		withdrawID := randomHex(16)
		chain := chains[i%len(chains)]
		asset := assets[i%len(assets)]
		status := statuses[i%len(statuses)]

		amount := big.NewInt(int64((i + 1) * 100000000)) // 0.1, 0.2, ...

		state := &pb.FrostWithdrawState{
			WithdrawId:    withdrawID,
			TxId:          randomHex(32),
			Seq:           uint64(i + 1),
			Chain:         chain,
			Asset:         asset,
			To:            randomAddress(),
			Amount:        amount.String(),
			RequestHeight: uint64(1000 + i*10),
			Status:        status,
			VaultId:       uint32(i % 3),
		}

		if status == "SIGNED" {
			state.JobId = "job_" + randomHex(8)
		}

		data, err := proto.Marshal(state)
		if err != nil {
			return fmt.Errorf("marshal withdraw %d: %w", i, err)
		}

		key := keys.KeyFrostWithdraw(withdrawID)
		dbMgr.EnqueueSet(key, string(data))

		log.Printf("  [%d] Withdraw %s: %s %s %s", i+1, withdrawID[:8], chain, asset, status)
	}

	return nil
}

func generateRechargeRequests(dbMgr *db.Manager, count int) error {
	log.Printf("Generating %d recharge requests...", count)

	chains := []string{"BTC", "ETH", "TRON", "SOL"}
	statuses := []pb.RechargeRequestStatus{
		pb.RechargeRequestStatus_RECHARGE_PENDING,
		pb.RechargeRequestStatus_RECHARGE_VOTING,
		pb.RechargeRequestStatus_RECHARGE_VOTING,
		pb.RechargeRequestStatus_RECHARGE_CHALLENGE_PERIOD,
		pb.RechargeRequestStatus_RECHARGE_FINALIZED,
	}

	for i := 0; i < count; i++ {
		requestID := randomHex(16)
		chain := chains[i%len(chains)]
		status := statuses[i%len(statuses)]
		amount := big.NewInt(int64((i + 1) * 50000000)) // 0.5, 1.0, 1.5 ...

		// 生成模拟见证者
		witnesses := make([]string, 3)
		for j := 0; j < 3; j++ {
			witnesses[j] = randomAddress()
		}

		// 生成模拟投票
		var votes []*pb.WitnessVote
		if status >= pb.RechargeRequestStatus_RECHARGE_VOTING {
			for j := 0; j < 2; j++ {
				votes = append(votes, &pb.WitnessVote{
					RequestId:      requestID,
					WitnessAddress: witnesses[j],
					VoteType:       pb.WitnessVoteType_VOTE_PASS,
					Timestamp:      uint64(1010 + i*10 + j),
				})
			}
		}

		req := &pb.RechargeRequest{
			RequestId:         requestID,
			NativeChain:       chain,
			NativeTxHash:      "0x" + randomHex(32),
			TokenAddress:      "FB",
			Amount:            amount.String(),
			ReceiverAddress:   randomAddress(),
			RequesterAddress:  randomAddress(),
			Status:            status,
			CreateHeight:      uint64(1000 + i*10),
			DeadlineHeight:    uint64(1100 + i*10),
			Round:             1,
			SelectedWitnesses: witnesses,
			Votes:             votes,
			PassCount:         uint32(len(votes)),
			VaultId:           uint32(i % 3),
		}

		data, err := proto.Marshal(req)
		if err != nil {
			return fmt.Errorf("marshal recharge %d: %w", i, err)
		}

		key := keys.KeyRechargeRequest(requestID)
		dbMgr.EnqueueSet(key, string(data))

		log.Printf("  [%d] Recharge %s: %s %s", i+1, requestID[:8], chain, status.String())
	}

	return nil
}

func generateDKGSessions(dbMgr *db.Manager, count int) error {
	log.Printf("Generating %d DKG sessions...", count)

	chains := []string{"BTC", "ETH", "TRON"}
	dkgStatuses := []string{
		"COMMITTING",
		"SHARING",
		"KEY_READY",
	}

	for i := 0; i < count; i++ {
		chain := chains[i%len(chains)]
		vaultID := uint32(i)
		epochID := uint64(i + 1)
		dkgStatus := dkgStatuses[i%len(dkgStatuses)]

		// 生成委员会成员
		oldMembers := make([]string, 5)
		newMembers := make([]string, 5)
		for j := 0; j < 5; j++ {
			oldMembers[j] = randomAddress()
			newMembers[j] = randomAddress()
		}

		state := &pb.VaultTransitionState{
			Chain:               chain,
			VaultId:             vaultID,
			EpochId:             epochID,
			SignAlgo:            pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
			TriggerHeight:       uint64(900 + i*100),
			OldCommitteeMembers: oldMembers,
			NewCommitteeMembers: newMembers,
			DkgStatus:           dkgStatus,
			DkgSessionId:        fmt.Sprintf("dkg_%s_%d_%d", chain, vaultID, epochID),
			DkgThresholdT:       3,
			DkgN:                5,
			DkgCommitDeadline:   uint64(950 + i*100),
			DkgDisputeDeadline:  uint64(980 + i*100),
			ValidationStatus:    "NOT_STARTED",
			Lifecycle:           "ACTIVE",
		}

		if dkgStatus == "KEY_READY" {
			state.NewGroupPubkey = []byte(randomHex(16))
			state.ValidationStatus = "PASSED"
		}

		data, err := proto.Marshal(state)
		if err != nil {
			return fmt.Errorf("marshal dkg %d: %w", i, err)
		}

		// 使用正确的 key 格式
		key := keys.KeyFrostVaultTransition(chain, vaultID, epochID)
		dbMgr.EnqueueSet(key, string(data))

		log.Printf("  [%d] DKG %s Vault#%d Epoch#%d: %s", i+1, chain, vaultID, epochID, dkgStatus)
	}

	return nil
}
