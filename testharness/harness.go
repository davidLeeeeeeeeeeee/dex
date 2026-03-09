// testharness/harness.go
// 通用集成测试框架：在一个进程内模拟多节点 + VM + FROST Runtime 的协作
//
// 用法：
//   h := testharness.New(t, 3)                 // 3 个 committee 成员
//   h.SeedAccount("alice", "FB", "1000000")    // 种账户
//   h.SeedActiveVault("btc", 0, 1)             // 种 Vault
//   h.SubmitTx(transferTx)                     // 提交 tx（立即通过 VM 执行）
//   h.AdvanceHeight(5)                         // 推进高度
//   h.AssertBalance("alice", "FB", "999000")   // 断言余额
//
// DKG 测试：
//   h.StartDKG("btc", 0, 1)                   // 启动所有节点的 DKG session
//   h.RunUntil(func() bool { ... }, 30s)       // 等待条件满足
//   h.AssertVaultActive("btc", 0)              // 断言 Vault ACTIVE

package testharness

import (
	"context"
	"dex/frost/runtime/types"
	"dex/keys"
	"dex/pb"
	"dex/utils"
	"dex/vm"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
)

// ---------------------------------------------------------------------------
// Harness 核心
// ---------------------------------------------------------------------------

// Harness 通用集成测试线束
type Harness struct {
	t      *testing.T
	mu     sync.RWMutex
	data   map[string][]byte // 共享内存 KV（模拟链上状态）
	height uint64            // 当前区块高度（原子操作）
	blockN uint64            // 已提交区块数（用于自增 blockHash）
	reg    *vm.HandlerRegistry
	kindFn vm.KindFn
	txLog  []txLogEntry // 提交日志

	// committee 相关
	Committee   []string                     // 成员地址列表
	PrivateKeys map[string]string            // address → hex private key
	PublicKeys  map[string][]byte            // address → compressed pubkey
	KeyManagers map[string]*utils.KeyManager // address → KeyManager（可用于签名）
}

type txLogEntry struct {
	Height uint64
	TxID   string
	Kind   string
	Status string
	Error  string
}

// New 创建测试线束，committeeSize 个虚拟 committee 成员
func New(t *testing.T, committeeSize int) *Harness {
	t.Helper()
	reg := vm.NewHandlerRegistry()
	if err := vm.RegisterDefaultHandlers(reg); err != nil {
		t.Fatalf("register handlers: %v", err)
	}
	h := &Harness{
		t:           t,
		data:        make(map[string][]byte),
		height:      0,
		reg:         reg,
		Committee:   make([]string, 0, committeeSize),
		PrivateKeys: make(map[string]string),
		PublicKeys:  make(map[string][]byte),
		KeyManagers: make(map[string]*utils.KeyManager),
	}
	h.kindFn = vm.DefaultKindFn
	// 写入初始高度
	h.rawSet(keys.KeyLatestHeight(), []byte("0"))
	// 生成 committee 成员（真实 secp256k1 密钥 + bech32 地址）
	ids := utils.GenerateTestIdentities(committeeSize)
	for _, id := range ids {
		h.Committee = append(h.Committee, id.Address)
		h.PrivateKeys[id.Address] = id.PrivateKeyHex
		h.PublicKeys[id.Address] = id.PublicKey
		km := utils.NewKeyManager()
		if err := km.InitKey(id.PrivateKeyHex); err != nil {
			t.Fatalf("init key for committee member: %v", err)
		}
		h.KeyManagers[id.Address] = km
	}
	return h
}

// ---------------------------------------------------------------------------
// 底层 KV 操作
// ---------------------------------------------------------------------------

func (h *Harness) rawSet(key string, val []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.data[key] = val
}

func (h *Harness) rawGet(key string) ([]byte, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	v, ok := h.data[key]
	return v, ok
}

func (h *Harness) rawDel(key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.data, key)
}

// ---------------------------------------------------------------------------
// StateView 实现（给 VM DryRun 用）
// ---------------------------------------------------------------------------

type harnessStateView struct {
	h       *Harness
	overlay map[string][]byte
	deleted map[string]bool
}

func (h *Harness) newStateView() *harnessStateView {
	return &harnessStateView{h: h, overlay: make(map[string][]byte), deleted: make(map[string]bool)}
}

func (sv *harnessStateView) Get(key string) ([]byte, bool, error) {
	if sv.deleted[key] {
		return nil, false, nil
	}
	if v, ok := sv.overlay[key]; ok {
		return v, true, nil
	}
	v, ok := sv.h.rawGet(key)
	return v, ok, nil
}

func (sv *harnessStateView) Set(key string, val []byte) {
	delete(sv.deleted, key)
	sv.overlay[key] = val
}

func (sv *harnessStateView) Del(key string) {
	delete(sv.overlay, key)
	sv.deleted[key] = true
}

func (sv *harnessStateView) Snapshot() int      { return 0 }
func (sv *harnessStateView) Revert(_ int) error { return nil }
func (sv *harnessStateView) Diff() []vm.WriteOp { return nil }

func (sv *harnessStateView) Scan(prefix string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	sv.h.mu.RLock()
	for k, v := range sv.h.data {
		if strings.HasPrefix(k, prefix) && !sv.deleted[k] {
			result[k] = v
		}
	}
	sv.h.mu.RUnlock()
	for k, v := range sv.overlay {
		if strings.HasPrefix(k, prefix) {
			result[k] = v
		}
	}
	return result, nil
}

// commit 把 overlay 写回 harness 的共享 KV
func (sv *harnessStateView) commit() {
	sv.h.mu.Lock()
	defer sv.h.mu.Unlock()
	for k := range sv.deleted {
		delete(sv.h.data, k)
	}
	for k, v := range sv.overlay {
		sv.h.data[k] = v
	}
}

// ---------------------------------------------------------------------------
// StateReader 实现（给 FROST Runtime 用）
// ---------------------------------------------------------------------------

type harnessStateReader struct{ h *Harness }

func (r *harnessStateReader) Get(key string) ([]byte, bool, error) {
	v, ok := r.h.rawGet(key)
	return v, ok, nil
}

func (r *harnessStateReader) Scan(prefix string, fn func(k string, v []byte) bool) error {
	r.h.mu.RLock()
	defer r.h.mu.RUnlock()
	for k, v := range r.h.data {
		if strings.HasPrefix(k, prefix) {
			if !fn(k, v) {
				break
			}
		}
	}
	return nil
}

// StateReader 返回一个实现 types.StateReader 的适配器
func (h *Harness) StateReader() types.StateReader {
	return &harnessStateReader{h: h}
}

// ---------------------------------------------------------------------------
// TxSubmitter 实现（提交 tx → 立即走 VM DryRun）
// ---------------------------------------------------------------------------

type harnessTxSubmitter struct {
	h *Harness
}

// TxSubmitter 返回一个实现 types.TxSubmitter 的适配器
func (h *Harness) TxSubmitter() types.TxSubmitter {
	return &harnessTxSubmitter{h: h}
}

func (s *harnessTxSubmitter) Submit(tx any) (string, error) {
	if anyTx, ok := tx.(*pb.AnyTx); ok {
		return anyTx.GetTxId(), s.h.executeTx(anyTx)
	}
	return "", fmt.Errorf("unsupported tx type")
}

func (s *harnessTxSubmitter) SubmitDkgCommitTx(_ context.Context, tx *pb.FrostVaultDkgCommitTx) error {
	if tx.Base != nil {
		tx.Base.Status = pb.Status_PENDING
	}
	return s.h.executeTx(&pb.AnyTx{Content: &pb.AnyTx_FrostVaultDkgCommitTx{FrostVaultDkgCommitTx: tx}})
}

func (s *harnessTxSubmitter) SubmitDkgShareTx(_ context.Context, tx *pb.FrostVaultDkgShareTx) error {
	if tx.Base != nil {
		tx.Base.Status = pb.Status_PENDING
	}
	return s.h.executeTx(&pb.AnyTx{Content: &pb.AnyTx_FrostVaultDkgShareTx{FrostVaultDkgShareTx: tx}})
}

func (s *harnessTxSubmitter) SubmitDkgValidationSignedTx(_ context.Context, tx *pb.FrostVaultDkgValidationSignedTx) error {
	if tx.Base != nil {
		tx.Base.Status = pb.Status_PENDING
	}
	return s.h.executeTx(&pb.AnyTx{Content: &pb.AnyTx_FrostVaultDkgValidationSignedTx{FrostVaultDkgValidationSignedTx: tx}})
}

func (s *harnessTxSubmitter) SubmitWithdrawSignedTx(_ context.Context, tx *pb.FrostWithdrawSignedTx) error {
	if tx.Base != nil {
		tx.Base.Status = pb.Status_PENDING
	}
	return s.h.executeTx(&pb.AnyTx{Content: &pb.AnyTx_FrostWithdrawSignedTx{FrostWithdrawSignedTx: tx}})
}

// ---------------------------------------------------------------------------
// TX 执行核心
// ---------------------------------------------------------------------------

func (h *Harness) executeTx(tx *pb.AnyTx) error {
	if tx == nil {
		return fmt.Errorf("nil tx")
	}
	kind, err := h.kindFn(tx)
	if err != nil {
		return fmt.Errorf("extract kind: %w", err)
	}
	handler, ok := h.reg.Get(kind)
	if !ok {
		return fmt.Errorf("no handler for kind=%s", kind)
	}

	// 注入当前高度
	if base := tx.GetBase(); base != nil {
		base.ExecutedHeight = atomic.LoadUint64(&h.height)
	}

	sv := h.newStateView()
	_, receipt, err := handler.DryRun(tx, sv)

	entry := txLogEntry{
		Height: atomic.LoadUint64(&h.height),
		TxID:   tx.GetTxId(),
		Kind:   kind,
	}
	if receipt != nil {
		entry.Status = receipt.Status
		entry.Error = receipt.Error
	}
	if err != nil {
		entry.Error = err.Error()
	}
	h.txLog = append(h.txLog, entry)

	// DryRun 成功则提交 overlay
	if err == nil {
		sv.commit()
	}
	return err
}

// SubmitTx 手动提交一笔 tx 并通过 VM 执行
func (h *Harness) SubmitTx(tx *pb.AnyTx) error {
	return h.executeTx(tx)
}

// SubmitTxExpectSuccess 提交 tx 并断言成功
func (h *Harness) SubmitTxExpectSuccess(tx *pb.AnyTx) {
	h.t.Helper()
	if err := h.executeTx(tx); err != nil {
		h.t.Fatalf("SubmitTxExpectSuccess: %v", err)
	}
}

// SubmitBlock 构造并执行一个包含多笔 tx 的 block
func (h *Harness) SubmitBlock(txs ...*pb.AnyTx) {
	h.t.Helper()
	for _, tx := range txs {
		if err := h.executeTx(tx); err != nil {
			h.t.Fatalf("SubmitBlock tx %s failed: %v", tx.GetTxId(), err)
		}
	}
	h.AdvanceHeight(1)
}

// ---------------------------------------------------------------------------
// 高度控制
// ---------------------------------------------------------------------------

// Height 获取当前高度
func (h *Harness) Height() uint64 {
	return atomic.LoadUint64(&h.height)
}

// AdvanceHeight 推进 n 个高度
func (h *Harness) AdvanceHeight(n uint64) {
	newH := atomic.AddUint64(&h.height, n)
	h.rawSet(keys.KeyLatestHeight(), []byte(strconv.FormatUint(newH, 10)))
}

// AutoAdvanceHeight 后台自动推高度，间隔 interval，返回 cancel
func (h *Harness) AutoAdvanceHeight(interval time.Duration) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.AdvanceHeight(1)
			}
		}
	}()
	return cancel
}

// ---------------------------------------------------------------------------
// 种子数据 Helpers
// ---------------------------------------------------------------------------

// SeedAccount 创建账户并设初始余额
func (h *Harness) SeedAccount(addr, token, balance string) {
	h.t.Helper()
	account := &pb.Account{Address: addr}
	data, _ := proto.Marshal(account)
	h.rawSet(keys.KeyAccount(addr), data)

	bal := &pb.TokenBalanceRecord{
		Balance: &pb.TokenBalance{Balance: balance, MinerLockedBalance: "0"},
	}
	bData, _ := proto.Marshal(bal)
	h.rawSet(keys.KeyBalance(addr, token), bData)
}

// SeedMinerAccount 创建矿工账户（含质押）
func (h *Harness) SeedMinerAccount(addr, balance, minerLocked string) {
	h.t.Helper()
	account := &pb.Account{Address: addr}
	data, _ := proto.Marshal(account)
	h.rawSet(keys.KeyAccount(addr), data)

	bal := &pb.TokenBalanceRecord{
		Balance: &pb.TokenBalance{Balance: balance, MinerLockedBalance: minerLocked},
	}
	bData, _ := proto.Marshal(bal)
	h.rawSet(keys.KeyBalance(addr, "FB"), bData)
}

// SeedVaultTransition 种一个 VaultTransitionState
func (h *Harness) SeedVaultTransition(chain string, vaultID uint32, epochID uint64, committee []string,
	triggerHeight, commitDeadline, sharingDeadline, disputeDeadline uint64, signAlgo pb.SignAlgo,
) {
	h.t.Helper()
	n := uint32(len(committee))
	threshold := uint32(float64(n)*0.67 + 0.5)
	if threshold < 1 {
		threshold = 1
	}
	transition := &pb.VaultTransitionState{
		Chain:               chain,
		VaultId:             vaultID,
		EpochId:             epochID,
		SignAlgo:            signAlgo,
		TriggerHeight:       triggerHeight,
		DkgCommitDeadline:   commitDeadline,
		DkgSharingDeadline:  sharingDeadline,
		DkgDisputeDeadline:  disputeDeadline,
		OldCommitteeMembers: committee,
		NewCommitteeMembers: committee,
		DkgStatus:           "NOT_STARTED",
		DkgThresholdT:       threshold,
		DkgN:                n,
		ValidationStatus:    "NOT_STARTED",
		Lifecycle:           "ACTIVE",
	}
	data, _ := proto.Marshal(transition)
	h.rawSet(keys.KeyFrostVaultTransition(chain, vaultID, epochID), data)
}

// SeedVaultConfig 种 VaultConfig
func (h *Harness) SeedVaultConfig(chain string, vaultID uint32, vaultCount uint32) {
	h.t.Helper()
	cfg := &pb.FrostVaultConfig{VaultCount: vaultCount, ThresholdRatio: 0.67}
	data, _ := proto.Marshal(cfg)
	h.rawSet(keys.KeyFrostVaultConfig(chain, vaultID), data)
}

// SeedVaultState 种 VaultState
func (h *Harness) SeedVaultState(chain string, vaultID uint32, status string, epochID uint64) {
	h.t.Helper()
	state := &pb.FrostVaultState{
		Chain:    chain,
		VaultId:  vaultID,
		Status:   status,
		KeyEpoch: epochID,
	}
	data, _ := proto.Marshal(state)
	h.rawSet(keys.KeyFrostVaultState(chain, vaultID), data)
}

// ---------------------------------------------------------------------------
// 断言 Helpers
// ---------------------------------------------------------------------------

// GetBalance 获取余额
func (h *Harness) GetBalance(addr, token string) string {
	h.t.Helper()
	raw, ok := h.rawGet(keys.KeyBalance(addr, token))
	if !ok {
		return "0"
	}
	var rec pb.TokenBalanceRecord
	if err := proto.Unmarshal(raw, &rec); err != nil || rec.Balance == nil {
		return "0"
	}
	return rec.Balance.Balance
}

// AssertBalance 断言余额
func (h *Harness) AssertBalance(addr, token, expected string) {
	h.t.Helper()
	got := h.GetBalance(addr, token)
	if got != expected {
		h.t.Fatalf("AssertBalance(%s, %s): want %s, got %s", addr, token, expected, got)
	}
}

// AssertBalanceGt 断言余额 > threshold
func (h *Harness) AssertBalanceGt(addr, token string, threshold int64) {
	h.t.Helper()
	got := h.GetBalance(addr, token)
	bal, ok := new(big.Int).SetString(got, 10)
	if !ok {
		h.t.Fatalf("AssertBalanceGt: invalid balance %s", got)
	}
	if bal.Int64() <= threshold {
		h.t.Fatalf("AssertBalanceGt(%s, %s): want > %d, got %s", addr, token, threshold, got)
	}
}

// GetVaultState 获取 VaultState
func (h *Harness) GetVaultState(chain string, vaultID uint32) *pb.FrostVaultState {
	h.t.Helper()
	raw, ok := h.rawGet(keys.KeyFrostVaultState(chain, vaultID))
	if !ok {
		return nil
	}
	var state pb.FrostVaultState
	if err := proto.Unmarshal(raw, &state); err != nil {
		return nil
	}
	return &state
}

// AssertVaultActive 断言 Vault 状态为 ACTIVE
func (h *Harness) AssertVaultActive(chain string, vaultID uint32) {
	h.t.Helper()
	state := h.GetVaultState(chain, vaultID)
	if state == nil {
		h.t.Fatalf("AssertVaultActive(%s, %d): vault state not found", chain, vaultID)
	}
	if state.Status != "ACTIVE" {
		h.t.Fatalf("AssertVaultActive(%s, %d): want ACTIVE, got %s", chain, vaultID, state.Status)
	}
}

// GetTransitionState 获取 VaultTransitionState
func (h *Harness) GetTransitionState(chain string, vaultID uint32, epochID uint64) *pb.VaultTransitionState {
	h.t.Helper()
	raw, ok := h.rawGet(keys.KeyFrostVaultTransition(chain, vaultID, epochID))
	if !ok {
		return nil
	}
	var s pb.VaultTransitionState
	if err := proto.Unmarshal(raw, &s); err != nil {
		return nil
	}
	return &s
}

// AssertDkgStatus 断言 DKG 状态
func (h *Harness) AssertDkgStatus(chain string, vaultID uint32, epochID uint64, expected string) {
	h.t.Helper()
	ts := h.GetTransitionState(chain, vaultID, epochID)
	if ts == nil {
		h.t.Fatalf("AssertDkgStatus: transition not found")
	}
	if ts.DkgStatus != expected {
		h.t.Fatalf("AssertDkgStatus(%s,%d,%d): want %s, got %s", chain, vaultID, epochID, expected, ts.DkgStatus)
	}
}

// GetRechargeRequest 获取入账请求
func (h *Harness) GetRechargeRequest(requestID string) *pb.RechargeRequest {
	h.t.Helper()
	raw, ok := h.rawGet(keys.KeyRechargeRequest(requestID))
	if !ok {
		return nil
	}
	var req pb.RechargeRequest
	if err := proto.Unmarshal(raw, &req); err != nil {
		return nil
	}
	return &req
}

// ---------------------------------------------------------------------------
// 等待 / 超时
// ---------------------------------------------------------------------------

// RunUntil 等到 cond 返回 true 或超时
func (h *Harness) RunUntil(cond func() bool, timeout time.Duration) bool {
	h.t.Helper()
	deadline := time.After(timeout)
	for {
		if cond() {
			return true
		}
		select {
		case <-deadline:
			h.t.Fatalf("RunUntil timeout after %v", timeout)
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// ---------------------------------------------------------------------------
// TX 构造 Helpers
// ---------------------------------------------------------------------------

var txCounter uint64

func nextTxID(prefix string) string {
	n := atomic.AddUint64(&txCounter, 1)
	return fmt.Sprintf("%s_%d", prefix, n)
}

// BuildTransferTx 构造转账 tx
func BuildTransferTx(from, to, token, amount string) *pb.AnyTx {
	return &pb.AnyTx{Content: &pb.AnyTx_Transaction{Transaction: &pb.Transaction{
		Base:         &pb.BaseMessage{TxId: nextTxID("transfer"), FromAddress: from, Status: pb.Status_PENDING, Fee: "1"},
		To:           to,
		TokenAddress: token,
		Amount:       amount,
	}}}
}

// BuildOrderTx 构造订单 tx
func BuildOrderTx(from, baseToken, quoteToken, amount, price string, side pb.OrderSide) *pb.AnyTx {
	return &pb.AnyTx{Content: &pb.AnyTx_OrderTx{OrderTx: &pb.OrderTx{
		Base:       &pb.BaseMessage{TxId: nextTxID("order"), FromAddress: from, Status: pb.Status_PENDING},
		BaseToken:  baseToken,
		QuoteToken: quoteToken,
		Amount:     amount,
		Price:      price,
		Side:       side,
	}}}
}

// BuildWitnessStakeTx 构造见证者质押 tx
func BuildWitnessStakeTx(addr string, op pb.OrderOp, amount string) *pb.AnyTx {
	return &pb.AnyTx{Content: &pb.AnyTx_WitnessStakeTx{WitnessStakeTx: &pb.WitnessStakeTx{
		Base:   &pb.BaseMessage{TxId: nextTxID("wstake"), FromAddress: addr, Status: pb.Status_PENDING},
		Op:     op,
		Amount: amount,
	}}}
}

// BuildDkgCommitTx 构造 DKG Commit tx
func BuildDkgCommitTx(sender, chain string, vaultID uint32, epochID uint64, signAlgo pb.SignAlgo, commitPoints [][]byte, ai0 []byte) *pb.AnyTx {
	return &pb.AnyTx{Content: &pb.AnyTx_FrostVaultDkgCommitTx{FrostVaultDkgCommitTx: &pb.FrostVaultDkgCommitTx{
		Base:             &pb.BaseMessage{TxId: nextTxID("dkg_commit"), FromAddress: sender, Status: pb.Status_PENDING},
		Chain:            chain,
		VaultId:          vaultID,
		EpochId:          epochID,
		SignAlgo:         signAlgo,
		CommitmentPoints: commitPoints,
		AI0:              ai0,
	}}}
}

// BuildDkgShareTx 构造 DKG Share tx
func BuildDkgShareTx(dealer, receiver, chain string, vaultID uint32, epochID uint64, ciphertext []byte) *pb.AnyTx {
	return &pb.AnyTx{Content: &pb.AnyTx_FrostVaultDkgShareTx{FrostVaultDkgShareTx: &pb.FrostVaultDkgShareTx{
		Base:       &pb.BaseMessage{TxId: nextTxID("dkg_share"), FromAddress: dealer, Status: pb.Status_PENDING},
		Chain:      chain,
		VaultId:    vaultID,
		EpochId:    epochID,
		DealerId:   dealer,
		ReceiverId: receiver,
		Ciphertext: ciphertext,
	}}}
}

// PrintTxLog 打印所有 tx 执行日志
func (h *Harness) PrintTxLog() {
	h.t.Helper()
	h.t.Log("=== TX Execution Log ===")
	for _, e := range h.txLog {
		h.t.Logf("  height=%d kind=%-30s txid=%-20s status=%s err=%s", e.Height, e.Kind, e.TxID, e.Status, e.Error)
	}
}

// TxLog 返回执行日志
func (h *Harness) TxLog() []txLogEntry { return h.txLog }

// RawSet 直接写 KV（用于种子）
func (h *Harness) RawSet(key string, val []byte) { h.rawSet(key, val) }

// RawGet 直接读 KV
func (h *Harness) RawGet(key string) ([]byte, bool) { return h.rawGet(key) }
