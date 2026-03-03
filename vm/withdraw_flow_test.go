package vm

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"dex/frost/chain/btc"
	"dex/keys"
	"dex/pb"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"google.golang.org/protobuf/proto"
)

// ========================================================================
// 提现流程端到端测试 — 绕过共识，直接调 Handler.DryRun
// 两步：
//   1. FrostWithdrawRequestTx  → 生成 QUEUED withdraw
//   2. FrostWithdrawSignedTx   → 验签 → QUEUED → SIGNED
// ========================================================================

// ---------- 辅助：构建纯内存 StateView ----------

func newMemoryStateView(base map[string][]byte) StateView {
	readFn := func(key string) ([]byte, error) {
		v, ok := base[key]
		if !ok {
			return nil, nil
		}
		out := make([]byte, len(v))
		copy(out, v)
		return out, nil
	}
	scanFn := func(prefix string) (map[string][]byte, error) {
		out := make(map[string][]byte)
		for k, v := range base {
			if strings.HasPrefix(k, prefix) {
				c := make([]byte, len(v))
				copy(c, v)
				out[k] = c
			}
		}
		return out, nil
	}
	return NewStateView(readFn, scanFn)
}

// ---------- 辅助：构建完整 fixture ----------

type withdrawFlowFixture struct {
	sv           StateView
	privKey      *btcec.PrivateKey
	vaultID      uint32
	userAddr     string
	scriptPubKey []byte
}

func buildWithdrawFlowFixture(t *testing.T) *withdrawFlowFixture {
	t.Helper()

	privKey, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatalf("new private key: %v", err)
	}

	vaultID := uint32(1)
	userAddr := "user_withdraw_test"

	// 构建 taproot scriptPubKey
	xOnly := schnorr.SerializePubKey(privKey.PubKey())
	scriptPubKey := make([]byte, 34)
	scriptPubKey[0] = 0x51
	scriptPubKey[1] = 0x20
	copy(scriptPubKey[2:], xOnly)

	base := map[string][]byte{}
	sv := newMemoryStateView(base)

	// 预置 vault 状态
	vaultState := &pb.FrostVaultState{
		VaultId:     vaultID,
		Chain:       "btc",
		KeyEpoch:    1,
		GroupPubkey: privKey.PubKey().SerializeCompressed(),
		SignAlgo:    pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
		Status:      "ACTIVE",
	}
	vaultStateData, _ := proto.Marshal(vaultState)
	sv.Set(keys.KeyFrostVaultState("btc", vaultID), vaultStateData)

	// 预置 vault 可用余额
	sv.Set(keys.KeyFrostVaultAvailableBalance("btc", "BTC", vaultID), []byte("50000"))

	// 预置 UTXO（两个 input）
	utxo1TxID := strings.Repeat("aa", 32)
	utxo2TxID := strings.Repeat("bb", 32)
	for i, txid := range []string{utxo1TxID, utxo2TxID} {
		utxo := &pb.FrostUtxo{
			Txid:         txid,
			Vout:         0,
			Amount:       "20000",
			ScriptPubkey: append([]byte(nil), scriptPubKey...),
			VaultId:      vaultID,
		}
		utxoData, _ := proto.Marshal(utxo)
		sv.Set(keys.KeyFrostBtcUtxo(vaultID, txid, 0), utxoData)
		// FIFO 索引
		seq := uint64(i + 1)
		sv.Set(keys.KeyFrostBtcUtxoFIFOIndex(vaultID, seq), utxoData)
	}
	sv.Set(keys.KeyFrostBtcUtxoFIFOSeq(vaultID), []byte("2"))
	sv.Set(keys.KeyFrostBtcUtxoFIFOHead(vaultID), []byte("1"))

	return &withdrawFlowFixture{
		sv:           sv,
		privKey:      privKey,
		vaultID:      vaultID,
		userAddr:     userAddr,
		scriptPubKey: scriptPubKey,
	}
}

// ========================================================================
// 测试 1: 仅测试提现请求阶段
// ========================================================================

func TestWithdrawRequestDryRun_Standalone(t *testing.T) {
	base := map[string][]byte{}
	sv := newMemoryStateView(base)

	handler := &FrostWithdrawRequestTxHandler{}

	tx := &pb.AnyTx{
		Content: &pb.AnyTx_FrostWithdrawRequestTx{
			FrostWithdrawRequestTx: &pb.FrostWithdrawRequestTx{
				Base: &pb.BaseMessage{
					TxId:           "tx_withdraw_req_001",
					FromAddress:    "alice",
					Status:         pb.Status_PENDING,
					ExecutedHeight: 10,
				},
				Chain:  "btc",
				Asset:  "BTC",
				To:     "bc1qtest",
				Amount: "5000",
			},
		},
	}

	ops, receipt, err := handler.DryRun(tx, sv)
	if err != nil {
		t.Fatalf("DryRun failed: %v", err)
	}
	if receipt.Status != "SUCCEED" {
		t.Fatalf("unexpected status: %s, error: %s", receipt.Status, receipt.Error)
	}
	if len(ops) != 4 {
		t.Fatalf("expected 4 write ops, got %d", len(ops))
	}

	// 验证 seq 递增到 1
	seqData, exists, _ := sv.Get(keys.KeyFrostWithdrawFIFOSeq("btc", "BTC"))
	if !exists || string(seqData) != "1" {
		t.Fatalf("expected seq=1, got %s", string(seqData))
	}

	// 验证 withdraw 状态为 QUEUED
	withdrawID := generateWithdrawID("btc", "BTC", 1, 10)
	withdrawData, exists, _ := sv.Get(keys.KeyFrostWithdraw(withdrawID))
	if !exists {
		t.Fatal("withdraw not found after DryRun")
	}
	withdraw, _ := unmarshalWithdrawRequest(withdrawData)
	if withdraw.Status != WithdrawStatusQueued {
		t.Fatalf("expected QUEUED, got %s", withdraw.Status)
	}

	// 幂等测试：重复提交同一交易应成功但不产生 WriteOps
	ops2, receipt2, err2 := handler.DryRun(tx, sv)
	if err2 != nil {
		t.Fatalf("idempotent DryRun failed: %v", err2)
	}
	if receipt2.Status != "SUCCEED" {
		t.Fatalf("idempotent: unexpected status: %s", receipt2.Status)
	}
	if len(ops2) != 0 {
		t.Fatalf("idempotent: expected 0 ops, got %d", len(ops2))
	}

	t.Logf("✅ WithdrawRequest DryRun 通过：withdraw_id=%s, seq=1, status=QUEUED", withdrawID)
}

// ========================================================================
// 测试 2: 完整提现流程 (Request → Signed)
// ========================================================================

func TestWithdrawFlowEndToEnd(t *testing.T) {
	f := buildWithdrawFlowFixture(t)

	// ---- 第一步：提现请求 ----
	reqHandler := &FrostWithdrawRequestTxHandler{}
	reqTx := &pb.AnyTx{
		Content: &pb.AnyTx_FrostWithdrawRequestTx{
			FrostWithdrawRequestTx: &pb.FrostWithdrawRequestTx{
				Base: &pb.BaseMessage{
					TxId:           "tx_e2e_withdraw_req",
					FromAddress:    f.userAddr,
					Status:         pb.Status_PENDING,
					ExecutedHeight: 100,
				},
				Chain:  "btc",
				Asset:  "BTC",
				To:     "bc1qtestaddress",
				Amount: "10000",
			},
		},
	}

	_, receipt, err := reqHandler.DryRun(reqTx, f.sv)
	if err != nil {
		t.Fatalf("WithdrawRequest DryRun failed: %v", err)
	}
	if receipt.Status != "SUCCEED" {
		t.Fatalf("WithdrawRequest failed: %s", receipt.Error)
	}

	// 提取 withdraw_id
	withdrawID := generateWithdrawID("btc", "BTC", 1, 100)
	t.Logf("第一步完成: withdraw_id=%s", withdrawID)

	// 设置 FIFO head（模拟 Scanner 就绪）
	f.sv.Set(keys.KeyFrostWithdrawFIFOHead("btc", "BTC"), []byte("1"))

	// ---- 第二步：构建 BTC Template + 签名 ----
	utxo1TxID := strings.Repeat("aa", 32)
	utxo2TxID := strings.Repeat("bb", 32)

	template := &btc.BTCTemplate{
		Version:  2,
		LockTime: 0,
		Inputs: []btc.TxInput{
			{TxID: utxo1TxID, Vout: 0, Amount: 20000, Sequence: btc.DefaultSequence},
			{TxID: utxo2TxID, Vout: 0, Amount: 20000, Sequence: btc.DefaultSequence},
		},
		Outputs: []btc.TxOutput{
			{Address: "bc1qtestaddress", Amount: 10000},
			{Address: "bc1qchange", Amount: 29000}, // 找零
		},
		VaultID:     f.vaultID,
		KeyEpoch:    1,
		WithdrawIDs: []string{withdrawID},
	}

	templateData, err := template.ToJSON()
	if err != nil {
		t.Fatalf("template ToJSON: %v", err)
	}

	// 计算 sighash 并签名
	scriptPubkeys := [][]byte{
		append([]byte(nil), f.scriptPubKey...),
		append([]byte(nil), f.scriptPubKey...),
	}
	sighashes, err := template.ComputeTaprootSighash(scriptPubkeys, btc.SighashDefault)
	if err != nil {
		t.Fatalf("compute sighash: %v", err)
	}

	inputSigs := make([][]byte, len(sighashes))
	for i, msg := range sighashes {
		sig, err := schnorr.Sign(f.privKey, msg)
		if err != nil {
			t.Fatalf("sign input[%d]: %v", i, err)
		}
		inputSigs[i] = sig.Serialize()
	}

	// ---- 预验签（与 DryRun 内部 verifySignature 保持一致）----
	for i, sig := range inputSigs {
		valid, err := verifySignature(pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340, f.privKey.PubKey().SerializeCompressed(), sighashes[i], sig)
		if err != nil {
			t.Fatalf("预验签 input[%d] 出错: %v", i, err)
		}
		if !valid {
			t.Fatalf("预验签 input[%d] 失败: 签名无效", i)
		}
	}
	t.Logf("预验签通过: %d 个 input 签名均有效", len(inputSigs))

	// 构造 job_id
	jobSeed := "btc|BTC|" + strconv.FormatUint(uint64(f.vaultID), 10) + "|1|" + hex.EncodeToString(template.TemplateHash()) + "|1"
	jobHash := sha256.Sum256([]byte(jobSeed))
	jobID := "job_" + hex.EncodeToString(jobHash[:8])

	// 构造 FrostWithdrawSignedTx
	signedHandler := &FrostWithdrawSignedTxHandler{}
	signedTx := &pb.AnyTx{
		Content: &pb.AnyTx_FrostWithdrawSignedTx{
			FrostWithdrawSignedTx: &pb.FrostWithdrawSignedTx{
				Base: &pb.BaseMessage{
					TxId:           "tx_e2e_withdraw_signed",
					ExecutedHeight: 101,
					FromAddress:    "signer_node",
				},
				JobId:              jobID,
				SignedPackageBytes: []byte("mock_signed_pkg"),
				WithdrawIds:        []string{withdrawID},
				VaultId:            f.vaultID,
				KeyEpoch:           1,
				TemplateHash:       template.TemplateHash(),
				Chain:              "btc",
				Asset:              "BTC",
				TemplateData:       templateData,
				InputSigs:          inputSigs,
				ScriptPubkeys:      scriptPubkeys,
			},
		},
	}

	ops, receipt, err := signedHandler.DryRun(signedTx, f.sv)
	if err != nil {
		t.Fatalf("WithdrawSigned DryRun failed: %v\nreceipt: %+v", err, receipt)
	}
	if receipt.Status != "SUCCEED" {
		t.Fatalf("WithdrawSigned failed: %s", receipt.Error)
	}
	t.Logf("第二步完成: %d write ops, job_id=%s", len(ops), jobID)

	// ---- 验证最终状态 ----

	// 1. withdraw 状态应为 SIGNED
	withdrawData, exists, _ := f.sv.Get(keys.KeyFrostWithdraw(withdrawID))
	if !exists {
		t.Fatal("withdraw not found after signed")
	}
	withdraw, _ := unmarshalWithdrawRequest(withdrawData)
	if withdraw.Status != WithdrawStatusSigned {
		t.Fatalf("expected SIGNED, got %s", withdraw.Status)
	}
	if withdraw.JobID != jobID {
		t.Fatalf("expected job_id=%s, got %s", jobID, withdraw.JobID)
	}

	// 2. UTXO 应被锁定
	for _, txid := range []string{utxo1TxID, utxo2TxID} {
		lockKey := keys.KeyFrostBtcLockedUtxo(f.vaultID, txid, 0)
		lockVal, locked, _ := f.sv.Get(lockKey)
		if !locked || string(lockVal) != jobID {
			t.Fatalf("UTXO %s:0 not locked by job %s", txid[:8], jobID)
		}
	}

	// 3. FIFO head 应推进
	headData, _, _ := f.sv.Get(keys.KeyFrostWithdrawFIFOHead("btc", "BTC"))
	if string(headData) != "2" {
		t.Fatalf("expected withdraw FIFO head=2, got %s", string(headData))
	}

	// 4. SignedPackage 应存在
	pkgData, pkgExists, _ := f.sv.Get(keys.KeyFrostSignedPackage(jobID, 0))
	if !pkgExists || len(pkgData) == 0 {
		t.Fatal("signed package not written")
	}

	t.Logf("✅ 提现流程 E2E 测试通过：QUEUED → SIGNED, UTXO locked, FIFO advanced")
}
