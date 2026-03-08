// frost/runtime/workers/withdraw_worker.go
// 提现流程执行器（Scanner -> Planner -> SigningService -> 提交 FrostWithdrawSignedTx）
package workers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"dex/frost/chain"
	"dex/frost/chain/btc"
	"dex/frost/core/frost"
	"dex/frost/runtime/planning"
	"dex/keys"
	"dex/logs"
	"dex/pb"

	"google.golang.org/protobuf/proto"
)

// SigningService 签名服务接口（本地定义，避免import cycle）
type SigningService interface {
	StartSigningSession(ctx context.Context, params *SigningSessionParams) (sessionID string, err error)
	GetSessionStatus(sessionID string) (*SessionStatus, error)
	CancelSession(sessionID string) error
	WaitForCompletion(ctx context.Context, sessionID string, timeout time.Duration) (*SignedPackage, error)
}

// SigningSessionParams 签名会话参数
type SigningSessionParams struct {
	JobID     string
	Chain     string
	VaultID   uint32
	KeyEpoch  uint64
	SignAlgo  pb.SignAlgo
	Messages  [][]byte
	Threshold int
	Tweaks    [][]byte // Taproot tweaks (per input)
}

// SessionStatus 会话状态
type SessionStatus struct {
	SessionID   string
	JobID       string
	State       string
	Progress    float64
	StartedAt   time.Time
	CompletedAt *time.Time
	Error       error
}

// SignedPackage 签名包
type SignedPackage struct {
	SessionID    string
	JobID        string
	Signature    []byte
	Signatures   [][]byte
	RawTx        []byte
	TemplateHash []byte
}

// 适配器：将StateReader转换为planning.ChainStateReader
type chainStateReaderAdapter struct {
	reader StateReader
}

func (a *chainStateReaderAdapter) Get(key string) ([]byte, bool, error) {
	return a.reader.Get(key)
}

func (a *chainStateReaderAdapter) Scan(prefix string, fn func(k string, v []byte) bool) error {
	return a.reader.Scan(prefix, fn)
}

func (a *chainStateReaderAdapter) GetMany(keys []string) (map[string][]byte, error) {
	if bulkReader, ok := a.reader.(interface {
		GetMany(keys []string) (map[string][]byte, error)
	}); ok {
		return bulkReader.GetMany(keys)
	}

	out := make(map[string][]byte, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		v, exists, err := a.reader.Get(key)
		if err != nil {
			return nil, err
		}
		if exists {
			out[key] = v
		}
	}
	return out, nil
}

// WithdrawWorker 提现流程执行器
type WithdrawWorker struct {
	stateReader    StateReader
	windowPlanner  *planning.JobWindowPlanner // Job 窗口规划器
	txSubmitter    TxSubmitter
	logReporter    LogReporter
	signingService SigningService
	vaultProvider  VaultCommitteeProvider
	maxInFlight    int // 最多并发 job 数
	localAddress   string
	Logger         logs.Logger
	logCounter     atomic.Uint64
}

// NewWithdrawWorker 创建新的 WithdrawWorker
func NewWithdrawWorker(
	stateReader StateReader,
	adapterFactory chain.ChainAdapterFactory,
	txSubmitter TxSubmitter,
	logReporter LogReporter,
	signingService SigningService,
	vaultProvider VaultCommitteeProvider,
	maxInFlight int,
	localAddress string,
	logger logs.Logger,
) *WithdrawWorker {
	if maxInFlight <= 0 {
		maxInFlight = 1 // 默认值
	}
	// 适配StateReader到planning.ChainStateReader
	planningReader := &chainStateReaderAdapter{reader: stateReader}
	return &WithdrawWorker{
		stateReader:    stateReader,
		windowPlanner:  planning.NewJobWindowPlanner(planningReader, adapterFactory, maxInFlight),
		txSubmitter:    txSubmitter,
		logReporter:    logReporter,
		signingService: signingService,
		vaultProvider:  vaultProvider,
		maxInFlight:    maxInFlight,
		localAddress:   localAddress,
		Logger:         logger,
	}
}

// ProcessOnce 处理一次提现流程（单 job 模式，向后兼容）
func (w *WithdrawWorker) ProcessOnce(ctx context.Context, chain, asset string) (*planning.Job, error) {
	jobs, err := w.ProcessWindow(ctx, chain, asset)
	if err != nil {
		return nil, err
	}
	if len(jobs) == 0 {
		return nil, nil
	}
	return jobs[0], nil // 返回第一个 job
}

// ProcessWindow 处理 Job 窗口（批量规划）
// 1. 扫描连续的 QUEUED withdraw（最多 maxInFlight 个）
// 2. 为每个 withdraw 规划 job
// 3. 并发启动 ROAST 会话
// 4. 等待完成并提交
func (w *WithdrawWorker) ProcessWindow(ctx context.Context, chain, asset string) ([]*planning.Job, error) {
	// 1. 规划 Job 窗口
	jobs, transientLogs, err := w.windowPlanner.PlanJobWindow(chain, asset)
	if err != nil {
		return nil, err
	}

	// 汇报产生的临时日志（针对未成功规划 job 的 withdraw）
	withdrawIDs := make([]string, 0, len(transientLogs))
	for withdrawID := range transientLogs {
		withdrawIDs = append(withdrawIDs, withdrawID)
	}
	sort.Strings(withdrawIDs)
	for _, withdrawID := range withdrawIDs {
		logs := transientLogs[withdrawID]
		w.reportPlanningLogs(ctx, &planning.Job{
			JobID:       "planning_failed",
			WithdrawIDs: []string{withdrawID},
			Logs:        logs,
		})
	}

	if len(jobs) == 0 {
		return nil, nil
	}

	w.Logger.Info("[WithdrawWorker] planned %d jobs for %s/%s", len(jobs), chain, asset)

	// 2. 汇报规划日志（汇报给每一项对应的 withdraw）
	for _, job := range jobs {
		w.reportPlanningLogs(ctx, job)
	}

	// 3. 为每个 job 启动签名会话（并发）
	// 如果是 CompositeJob，需要为每个 SubJob 启动独立的 ROAST 会话
	for _, job := range jobs {
		job := job // 修复变量捕获
		if job.IsComposite && len(job.SubJobs) > 0 {
			// CompositeJob：为每个 SubJob 启动独立的签名会话
			go func() {
				// logs.SetThreadNodeContext(w.localAddress) removed
				w.processCompositeJobAsync(ctx, job)
			}()
		} else {
			// 普通 Job：直接处理
			go func() {
				// logs.SetThreadNodeContext(w.localAddress) removed
				w.processJobAsync(ctx, job)
			}()
		}
	}

	return jobs, nil
}

// reportPlanningLogs 将规划日志异步汇报（直写本地 DB，不参与共识）
func (w *WithdrawWorker) reportPlanningLogs(ctx context.Context, job *planning.Job) {
	if len(job.Logs) == 0 {
		return
	}

	for _, withdrawID := range job.WithdrawIDs {
		planningLog := &pb.FrostWithdrawPlanningLogTx{
			Reporter:   w.localAddress,
			WithdrawId: withdrawID,
			Logs:       job.Logs,
		}

		err := w.logReporter.ReportWithdrawPlanningLog(ctx, planningLog)
		if err != nil {
			w.Logger.Warn("[WithdrawWorker] failed to report planning logs for %s: %v", withdrawID, err)
		}
	}
}

// processJobAsync 异步处理单个 job
func (w *WithdrawWorker) processJobAsync(ctx context.Context, job *planning.Job) {
	if skip, reason, err := w.shouldSkipJob(job); err != nil {
		w.Logger.Warn("[WithdrawWorker] skip-check failed for job %s: %v", job.JobID, err)
		return
	} else if skip {
		w.Logger.Trace("[WithdrawWorker] skip job %s: %s", job.JobID, reason)
		return
	}

	isCoordinator, coordinatorAddr, err := w.isJobCoordinator(job)
	if err != nil {
		w.Logger.Warn("[WithdrawWorker] failed to resolve coordinator for job %s: %v", job.JobID, err)
		return
	}
	if !isCoordinator {
		w.Logger.Trace("[WithdrawWorker] skip job %s: local=%s coordinator=%s", job.JobID, w.localAddress, coordinatorAddr)
		return
	}

	// 计算门限
	threshold, err := w.calculateThreshold(job.Chain, job.VaultID)
	if err != nil {
		w.Logger.Error("[WithdrawWorker] failed to calculate threshold: %v", err)
		return
	}

	// 获取签名算法
	signAlgo, err := w.getSignAlgo(job.Chain, job.VaultID)
	if err != nil {
		w.Logger.Error("[WithdrawWorker] failed to get sign algo: %v", err)
		return
	}

	// 构建待签名消息
	messages, err := w.extractMessages(job)
	if err != nil {
		w.Logger.Error("[WithdrawWorker] failed to extract messages: %v", err)
		return
	}
	w.logSigningContext(job, threshold, signAlgo, messages)

	// 加载 BTC UTXO tweaks
	var tweaks [][]byte
	if isBTCChain(job.Chain) {
		tweaks = w.loadBTCInputTweaks(job)
	}

	// 启动签名会话
	sessionParams := &SigningSessionParams{
		JobID:     job.JobID,
		Chain:     job.Chain,
		VaultID:   job.VaultID,
		KeyEpoch:  job.KeyEpoch,
		SignAlgo:  signAlgo,
		Messages:  messages,
		Threshold: threshold,
		Tweaks:    tweaks,
	}

	sessionID, err := w.signingService.StartSigningSession(ctx, sessionParams)
	if err != nil {
		w.Logger.Error("[WithdrawWorker] failed to start signing session: %v", err)
		return
	}

	w.Logger.Info("[WithdrawWorker] started signing session %s for job %s", sessionID, job.JobID)

	// 汇报会话已启动
	w.reportPlanningLogs(ctx, &planning.Job{
		JobID:       job.JobID,
		WithdrawIDs: job.WithdrawIDs,
		Logs: []*pb.FrostPlanningLog{
			{Step: "StartSigning", Status: "OK", Message: fmt.Sprintf("Signing session %s started", sessionID), Timestamp: uint64(time.Now().UnixMilli())},
		},
	})

	// 等待完成并提交
	w.waitAndSubmit(ctx, job, sessionID)
}

// waitAndSubmit 等待签名会话完成并提交交易
func (w *WithdrawWorker) waitAndSubmit(ctx context.Context, job *planning.Job, sessionID string) {
	// 等待会话完成（超时 5 分钟）
	timeout := 5 * time.Minute
	waitCtx := context.Background()
	if ctx != nil {
		// Ignore parent deadline/cancel while waiting for a long-running signing session.
		waitCtx = context.WithoutCancel(ctx)
	}
	signedPkg, err := w.signingService.WaitForCompletion(waitCtx, sessionID, timeout)
	if err != nil {
		reason := err.Error()
		if status, statusErr := w.signingService.GetSessionStatus(sessionID); statusErr == nil && status != nil {
			if status.Error != nil {
				reason = fmt.Sprintf("%s (state=%s, progress=%.2f, session_error=%v)", reason, status.State, status.Progress, status.Error)
			} else {
				reason = fmt.Sprintf("%s (state=%s, progress=%.2f)", reason, status.State, status.Progress)
			}
		}
		w.Logger.Error("[WithdrawWorker] signing session %s failed: %s", sessionID, reason)
		// 汇报失败
		w.reportPlanningLogs(ctx, &planning.Job{
			JobID:       job.JobID,
			WithdrawIDs: job.WithdrawIDs,
			Logs: []*pb.FrostPlanningLog{
				{Step: "Signing", Status: "FAILED", Message: fmt.Sprintf("Signing session failed: %s", reason), Timestamp: uint64(time.Now().UnixMilli())},
			},
		})
		return
	}

	// 汇报签名成功
	w.reportPlanningLogs(ctx, &planning.Job{
		JobID:       job.JobID,
		WithdrawIDs: job.WithdrawIDs,
		Logs: []*pb.FrostPlanningLog{
			{Step: "Signing", Status: "OK", Message: "Aggregation signature completed", Timestamp: uint64(time.Now().UnixMilli())},
		},
	})

	// 构建 SignedPackage bytes（需要包含签名和模板数据）
	signedPackageBytes, err := w.buildSignedPackageBytes(signedPkg, job)
	if err != nil {
		w.Logger.Error("[WithdrawWorker] failed to build signed package: %v", err)
		return
	}

	// 提交 FrostWithdrawSignedTx
	withdrawSignedTx := &pb.FrostWithdrawSignedTx{
		Base: &pb.BaseMessage{
			FromAddress:    w.localAddress,
			ExecutedHeight: 0, // 由 VM 填充
		},
		JobId:              job.JobID,
		SignedPackageBytes: signedPackageBytes,
		WithdrawIds:        job.WithdrawIDs,
		VaultId:            job.VaultID,
		KeyEpoch:           job.KeyEpoch,
		TemplateHash:       job.TemplateHash,
		Chain:              job.Chain,
		Asset:              job.Asset,
	}

	if isBTCChain(job.Chain) {
		template, parseErr := w.parseBTCTemplate(job)
		if parseErr != nil {
			w.Logger.Error("[WithdrawWorker] failed to parse BTC template for submit: %v", parseErr)
			return
		}

		inputSigs, sigErr := w.resolveBTCInputSignatures(signedPkg, len(template.Inputs))
		if sigErr != nil {
			w.Logger.Error("[WithdrawWorker] failed to resolve BTC input signatures: %v", sigErr)
			return
		}

		scriptPubkeys, spkErr := w.loadBTCScriptPubkeys(job.Chain, job.VaultID, template)
		if spkErr != nil {
			w.Logger.Error("[WithdrawWorker] failed to load BTC input scriptPubkeys: %v", spkErr)
			return
		}
		w.panicOnInvalidBTCSignature(job, template, scriptPubkeys, inputSigs)

		withdrawSignedTx.TemplateData = job.TemplateData
		withdrawSignedTx.InputSigs = inputSigs
		withdrawSignedTx.ScriptPubkeys = scriptPubkeys
	}

	// 生成提现交易 ID（基于内容的哈希）
	withdrawSignedTx.Base.TxId = fmt.Sprintf("tx_withdraw_%s", job.JobID)

	err = w.txSubmitter.SubmitWithdrawSignedTx(ctx, withdrawSignedTx)
	if err != nil {
		w.Logger.Error("[WithdrawWorker] failed to submit signed tx: %v", err)
		return
	}

	w.Logger.Info("[WithdrawWorker] successfully submitted signed tx for job %s", job.JobID)
}

// calculateThreshold 计算门限值
func (w *WithdrawWorker) calculateThreshold(chain string, vaultID uint32) (int, error) {
	return w.vaultProvider.CalculateThreshold(chain, vaultID)
}

// getSignAlgo 获取签名算法
func (w *WithdrawWorker) getSignAlgo(chain string, vaultID uint32) (pb.SignAlgo, error) {
	// 从 VaultState 读取 sign_algo
	// 简化处理：根据链返回默认算法
	switch chain {
	case "btc":
		return pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340, nil
	case "eth", "bnb":
		return pb.SignAlgo_SIGN_ALGO_SCHNORR_ALT_BN128, nil
	case "sol":
		return pb.SignAlgo_SIGN_ALGO_ED25519, nil
	case "trx":
		return pb.SignAlgo_SIGN_ALGO_ECDSA_SECP256K1, nil
	default:
		return pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340, nil
	}
}

// extractMessages 从 Job 中提取待签名消息
func (w *WithdrawWorker) extractMessages(job *planning.Job) ([][]byte, error) {
	if isBTCChain(job.Chain) {
		template, err := w.parseBTCTemplate(job)
		if err != nil {
			return nil, err
		}

		scriptPubkeys, err := w.loadBTCScriptPubkeys(job.Chain, job.VaultID, template)
		if err != nil {
			return nil, err
		}

		sighashes, err := template.ComputeTaprootSighash(scriptPubkeys, btc.SighashDefault)
		if err != nil {
			return nil, fmt.Errorf("compute BTC sighashes: %w", err)
		}
		if len(sighashes) == 0 {
			return nil, fmt.Errorf("empty BTC sighashes for job %s", job.JobID)
		}
		return sighashes, nil
	}

	if len(job.TemplateHash) == 0 {
		return nil, fmt.Errorf("empty template hash for job %s", job.JobID)
	}
	return [][]byte{job.TemplateHash}, nil
}

// processCompositeJobAsync 异步处理 CompositeJob
// 为每个 SubJob 启动独立的 ROAST 会话，等待所有 SubJob 完成后提交
func (w *WithdrawWorker) processCompositeJobAsync(ctx context.Context, compositeJob *planning.Job) {
	w.Logger.Info("[WithdrawWorker] processing composite job %s with %d sub jobs", compositeJob.JobID, len(compositeJob.SubJobs))

	// 为每个 SubJob 启动独立的签名会话（并发）
	subJobResults := make(chan *SignedPackage, len(compositeJob.SubJobs))
	subJobErrors := make(chan error, len(compositeJob.SubJobs))

	for _, subJob := range compositeJob.SubJobs {
		subJob := subJob // 修复变量捕获
		go func(subJob *planning.Job) {
			// logs.SetThreadNodeContext(w.localAddress) removed
			// 计算门限
			threshold, err := w.calculateThreshold(subJob.Chain, subJob.VaultID)
			if err != nil {
				subJobErrors <- fmt.Errorf("failed to calculate threshold: %w", err)
				return
			}

			// 获取签名算法
			signAlgo, err := w.getSignAlgo(subJob.Chain, subJob.VaultID)
			if err != nil {
				subJobErrors <- fmt.Errorf("failed to get sign algo: %w", err)
				return
			}

			// 构建待签名消息
			messages, err := w.extractMessages(subJob)
			if err != nil {
				subJobErrors <- fmt.Errorf("failed to extract messages: %w", err)
				return
			}
			w.logSigningContext(subJob, threshold, signAlgo, messages)

			// 启动签名会话
			sessionParams := &SigningSessionParams{
				JobID:     subJob.JobID,
				Chain:     subJob.Chain,
				VaultID:   subJob.VaultID,
				KeyEpoch:  subJob.KeyEpoch,
				SignAlgo:  signAlgo,
				Messages:  messages,
				Threshold: threshold,
			}

			sessionID, err := w.signingService.StartSigningSession(ctx, sessionParams)
			if err != nil {
				subJobErrors <- fmt.Errorf("failed to start signing session: %w", err)
				return
			}

			w.Logger.Info("[WithdrawWorker] started signing session %s for sub job %s", sessionID, subJob.JobID)

			// 等待完成
			timeout := 5 * time.Minute
			waitCtx := context.Background()
			if ctx != nil {
				waitCtx = context.WithoutCancel(ctx)
			}
			signedPkg, err := w.signingService.WaitForCompletion(waitCtx, sessionID, timeout)
			if err != nil {
				subJobErrors <- fmt.Errorf("signing session %s failed: %w", sessionID, err)
				return
			}

			subJobResults <- signedPkg
		}(subJob)
	}

	// 等待所有 SubJob 完成
	completedSubJobs := 0
	var firstError error

	for completedSubJobs < len(compositeJob.SubJobs) {
		select {
		case signedPkg := <-subJobResults:
			completedSubJobs++
			w.Logger.Info("[WithdrawWorker] sub job %s completed (%d/%d)", signedPkg.JobID, completedSubJobs, len(compositeJob.SubJobs))
		case err := <-subJobErrors:
			if firstError == nil {
				firstError = err
			}
			completedSubJobs++
			w.Logger.Error("[WithdrawWorker] sub job failed: %v", err)
		case <-ctx.Done():
			w.Logger.Error("[WithdrawWorker] composite job %s cancelled", compositeJob.JobID)
			return
		}
	}

	// 如果有错误，记录但不阻止（部分完成也是可接受的）
	if firstError != nil {
		w.Logger.Warn("[WithdrawWorker] composite job %s completed with errors: %v", compositeJob.JobID, firstError)
	}

	// 所有 SubJob 完成后，提交 CompositeJob
	// 注意：实际实现中，可能需要为每个 SubJob 分别提交交易，或者合并为一个 batch 交易
	w.Logger.Info("[WithdrawWorker] all sub jobs completed for composite job %s", compositeJob.JobID)
	// TODO: 实现 CompositeJob 的提交逻辑（可能需要多个交易或 batch 交易）
}

// buildSignedPackageBytes 构建 SignedPackage bytes
func (w *WithdrawWorker) buildSignedPackageBytes(signedPkg *SignedPackage, job *planning.Job) ([]byte, error) {
	if signedPkg == nil {
		return nil, fmt.Errorf("signed package is nil")
	}
	if len(signedPkg.Signature) > 0 {
		return append([]byte(nil), signedPkg.Signature...), nil
	}
	if len(signedPkg.Signatures) > 0 {
		total := 0
		for _, sig := range signedPkg.Signatures {
			total += len(sig)
		}
		combined := make([]byte, 0, total)
		for _, sig := range signedPkg.Signatures {
			combined = append(combined, sig...)
		}
		if len(combined) == 0 {
			return nil, fmt.Errorf("signed package signatures are empty")
		}
		return combined, nil
	}
	return nil, fmt.Errorf("signed package has no signature bytes")
}

func (w *WithdrawWorker) logSigningContext(job *planning.Job, threshold int, signAlgo pb.SignAlgo, messages [][]byte) {
	if job == nil {
		return
	}

	w.Logger.Info("[WithdrawWorker] signing context job=%s chain=%s asset=%s vault=%d epoch=%d threshold=%d sign_algo=%s withdraws=%v",
		job.JobID, job.Chain, job.Asset, job.VaultID, job.KeyEpoch, threshold, signAlgo.String(), job.WithdrawIDs)

	if len(messages) == 0 {
		w.Logger.Warn("[WithdrawWorker] signing context has no message to sign: job=%s", job.JobID)
	} else {
		for i, msg := range messages {
			w.Logger.Info("[WithdrawWorker] signing message job=%s chain=%s vault=%d task=%d payload=%s",
				job.JobID, job.Chain, job.VaultID, i, hex.EncodeToString(msg))
		}
	}

	if w.vaultProvider == nil {
		w.Logger.Warn("[WithdrawWorker] vault provider is nil, skip vault debug context for job=%s", job.JobID)
		return
	}

	committee, err := w.vaultProvider.VaultCommittee(job.Chain, job.VaultID, job.KeyEpoch)
	if err != nil {
		w.Logger.Warn("[WithdrawWorker] failed to load vault voter committee for job=%s chain=%s vault=%d epoch=%d: %v",
			job.JobID, job.Chain, job.VaultID, job.KeyEpoch, err)
	} else {
		w.Logger.Info("[WithdrawWorker] vault voter committee job=%s chain=%s vault=%d epoch=%d size=%d members=%s",
			job.JobID, job.Chain, job.VaultID, job.KeyEpoch, len(committee), formatSignerCommitteeForLog(committee))
	}

	groupPubkey, err := w.vaultProvider.VaultGroupPubkey(job.Chain, job.VaultID, job.KeyEpoch)
	if err != nil {
		w.Logger.Warn("[WithdrawWorker] failed to load aggregated public key for job=%s chain=%s vault=%d epoch=%d: %v",
			job.JobID, job.Chain, job.VaultID, job.KeyEpoch, err)
		return
	}
	w.Logger.Info("[WithdrawWorker] aggregated public key job=%s chain=%s vault=%d epoch=%d pubkey=%s",
		job.JobID, job.Chain, job.VaultID, job.KeyEpoch, hex.EncodeToString(groupPubkey))
}

func formatSignerCommitteeForLog(committee []SignerInfo) string {
	if len(committee) == 0 {
		return "[]"
	}
	members := make([]string, 0, len(committee))
	for i, member := range committee {
		memberID := string(member.ID)
		if memberID == "" {
			memberID = "unknown"
		}
		members = append(members, fmt.Sprintf("%d:%s", i+1, memberID))
	}
	return "[" + strings.Join(members, ",") + "]"
}

func isBTCChain(chainName string) bool {
	c := strings.ToLower(strings.TrimSpace(chainName))
	return c == "btc" || strings.HasPrefix(c, "btc_") || strings.HasPrefix(c, "btc-")
}

func (w *WithdrawWorker) parseBTCTemplate(job *planning.Job) (*btc.BTCTemplate, error) {
	if job == nil {
		return nil, fmt.Errorf("job is nil")
	}
	if len(job.TemplateData) == 0 {
		return nil, fmt.Errorf("missing template_data for BTC job %s", job.JobID)
	}
	template, err := btc.FromJSON(job.TemplateData)
	if err != nil {
		return nil, fmt.Errorf("decode BTC template_data: %w", err)
	}
	if len(template.Inputs) == 0 {
		return nil, fmt.Errorf("BTC template has no inputs for job %s", job.JobID)
	}
	return template, nil
}

func (w *WithdrawWorker) loadBTCScriptPubkeys(chainName string, vaultID uint32, template *btc.BTCTemplate) ([][]byte, error) {
	if template == nil {
		return nil, fmt.Errorf("template is nil")
	}

	scriptPubkeys := make([][]byte, 0, len(template.Inputs))
	var fallback []byte

	for _, in := range template.Inputs {
		scriptPubkey, err := w.loadUTXOScriptPubkey(vaultID, in.TxID, in.Vout)
		if err != nil {
			return nil, err
		}
		if len(scriptPubkey) == 0 {
			if len(fallback) == 0 {
				fallback, err = w.loadVaultTaprootScriptPubkey(chainName, vaultID)
				if err != nil {
					return nil, fmt.Errorf("missing scriptPubKey for input %s:%d and fallback failed: %w", in.TxID, in.Vout, err)
				}
			}
			scriptPubkey = fallback
		}
		scriptPubkeys = append(scriptPubkeys, append([]byte(nil), scriptPubkey...))
	}

	return scriptPubkeys, nil
}

func (w *WithdrawWorker) loadUTXOScriptPubkey(vaultID uint32, txid string, vout uint32) ([]byte, error) {
	utxoKey := keys.KeyFrostBtcUtxo(vaultID, txid, vout)
	utxoData, exists, err := w.stateReader.Get(utxoKey)
	if err != nil {
		return nil, fmt.Errorf("read BTC utxo %s failed: %w", utxoKey, err)
	}
	if !exists || len(utxoData) == 0 {
		return nil, nil
	}

	var utxo pb.FrostUtxo
	if err := proto.Unmarshal(utxoData, &utxo); err != nil {
		return nil, fmt.Errorf("decode BTC utxo %s failed: %w", utxoKey, err)
	}
	if len(utxo.ScriptPubkey) == 0 {
		return nil, nil
	}
	return append([]byte(nil), utxo.ScriptPubkey...), nil
}

// loadBTCInputTweaks 从 BTC template inputs 加载每个 UTXO 的 tweak
func (w *WithdrawWorker) loadBTCInputTweaks(job *planning.Job) [][]byte {
	template, err := w.parseBTCTemplate(job)
	if err != nil || len(template.Inputs) == 0 {
		return nil
	}
	tweaks := make([][]byte, 0, len(template.Inputs))
	for _, in := range template.Inputs {
		utxoKey := keys.KeyFrostBtcUtxo(job.VaultID, in.TxID, in.Vout)
		utxoData, exists, err := w.stateReader.Get(utxoKey)
		if err != nil || !exists || len(utxoData) == 0 {
			tweaks = append(tweaks, nil)
			continue
		}
		var utxo pb.FrostUtxo
		if proto.Unmarshal(utxoData, &utxo) != nil || len(utxo.Tweak) != 32 {
			tweaks = append(tweaks, nil)
			continue
		}
		tweaks = append(tweaks, append([]byte(nil), utxo.Tweak...))
	}
	return tweaks
}

func (w *WithdrawWorker) loadVaultTaprootScriptPubkey(chainName string, vaultID uint32) ([]byte, error) {
	vaultKey := keys.KeyFrostVaultState(chainName, vaultID)
	vaultData, exists, err := w.stateReader.Get(vaultKey)
	if err != nil {
		return nil, fmt.Errorf("read vault state %s failed: %w", vaultKey, err)
	}
	if !exists || len(vaultData) == 0 {
		return nil, fmt.Errorf("vault state missing: key=%s", vaultKey)
	}

	var vault pb.FrostVaultState
	if err := proto.Unmarshal(vaultData, &vault); err != nil {
		return nil, fmt.Errorf("decode vault state %s failed: %w", vaultKey, err)
	}

	xOnly, err := normalizeXOnlyPubKey(vault.GroupPubkey)
	if err != nil {
		return nil, err
	}

	scriptPubkey := make([]byte, 34)
	scriptPubkey[0] = 0x51
	scriptPubkey[1] = 0x20
	copy(scriptPubkey[2:], xOnly)
	return scriptPubkey, nil
}

func normalizeXOnlyPubKey(groupPubkey []byte) ([]byte, error) {
	switch len(groupPubkey) {
	case 32:
		return append([]byte(nil), groupPubkey...), nil
	case 33:
		if groupPubkey[0] != 0x02 && groupPubkey[0] != 0x03 {
			return nil, fmt.Errorf("invalid compressed pubkey prefix: 0x%x", groupPubkey[0])
		}
		return append([]byte(nil), groupPubkey[1:]...), nil
	default:
		return nil, fmt.Errorf("unsupported group pubkey length: %d", len(groupPubkey))
	}
}

func (w *WithdrawWorker) resolveBTCInputSignatures(signedPkg *SignedPackage, inputCount int) ([][]byte, error) {
	if signedPkg == nil {
		return nil, fmt.Errorf("signed package is nil")
	}
	if inputCount <= 0 {
		return nil, fmt.Errorf("invalid BTC input count: %d", inputCount)
	}

	if len(signedPkg.Signatures) > 0 {
		if len(signedPkg.Signatures) != inputCount {
			return nil, fmt.Errorf("btc signature count mismatch: got=%d want=%d", len(signedPkg.Signatures), inputCount)
		}
		result := make([][]byte, inputCount)
		for i, sig := range signedPkg.Signatures {
			if len(sig) != 64 {
				return nil, fmt.Errorf("invalid btc signature length for input %d: %d", i, len(sig))
			}
			result[i] = append([]byte(nil), sig...)
		}
		return result, nil
	}

	if len(signedPkg.Signature) == 0 {
		return nil, fmt.Errorf("missing BTC signatures")
	}

	if inputCount == 1 {
		if len(signedPkg.Signature) != 64 {
			return nil, fmt.Errorf("invalid btc signature length: %d", len(signedPkg.Signature))
		}
		return [][]byte{append([]byte(nil), signedPkg.Signature...)}, nil
	}

	if len(signedPkg.Signature) != inputCount*64 {
		return nil, fmt.Errorf("invalid combined BTC signature length: got=%d want=%d", len(signedPkg.Signature), inputCount*64)
	}

	result := make([][]byte, inputCount)
	for i := 0; i < inputCount; i++ {
		start := i * 64
		result[i] = append([]byte(nil), signedPkg.Signature[start:start+64]...)
	}
	return result, nil
}

func isTaprootScriptPubKey(scriptPubkey []byte) bool {
	return len(scriptPubkey) == 34 && scriptPubkey[0] == 0x51 && scriptPubkey[1] == 0x20
}

func shortHexPanic(data []byte, n int) string {
	if len(data) == 0 {
		return ""
	}
	if n <= 0 || len(data) <= n {
		return hex.EncodeToString(data)
	}
	return hex.EncodeToString(data[:n])
}

func (w *WithdrawWorker) panicOnInvalidBTCSignature(job *planning.Job, template *btc.BTCTemplate, scriptPubkeys, inputSigs [][]byte) {
	if job == nil {
		panic("frost-sign-debug: nil job in BTC signature precheck")
	}
	if template == nil {
		panic(fmt.Sprintf("frost-sign-debug: nil template in BTC signature precheck job=%s", job.JobID))
	}
	if len(scriptPubkeys) != len(template.Inputs) {
		panic(fmt.Sprintf("frost-sign-debug: script_pubkeys/input mismatch job=%s got_script_pubkeys=%d inputs=%d", job.JobID, len(scriptPubkeys), len(template.Inputs)))
	}
	if len(inputSigs) != len(template.Inputs) {
		panic(fmt.Sprintf("frost-sign-debug: input_sigs/input mismatch job=%s got_input_sigs=%d inputs=%d", job.JobID, len(inputSigs), len(template.Inputs)))
	}

	signAlgo, err := w.getSignAlgo(job.Chain, job.VaultID)
	if err != nil {
		panic(fmt.Sprintf("frost-sign-debug: getSignAlgo failed job=%s chain=%s vault=%d: %v", job.JobID, job.Chain, job.VaultID, err))
	}

	groupPubkey, err := w.vaultProvider.VaultGroupPubkey(job.Chain, job.VaultID, job.KeyEpoch)
	if err != nil {
		panic(fmt.Sprintf("frost-sign-debug: VaultGroupPubkey failed job=%s chain=%s vault=%d epoch=%d: %v", job.JobID, job.Chain, job.VaultID, job.KeyEpoch, err))
	}

	for i, spk := range scriptPubkeys {
		if !isTaprootScriptPubKey(spk) {
			panic(fmt.Sprintf("frost-sign-debug: non-taproot script_pubkey before submit job=%s input=%d script_pubkey=%s len=%d group_pubkey=%s",
				job.JobID, i, hex.EncodeToString(spk), len(spk), hex.EncodeToString(groupPubkey)))
		}
	}

	verifyPubkey := groupPubkey
	if signAlgo == pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340 {
		xOnly, err := normalizeXOnlyPubKey(groupPubkey)
		if err != nil {
			panic(fmt.Sprintf("frost-sign-debug: normalize group pubkey failed job=%s group_pubkey=%s: %v",
				job.JobID, hex.EncodeToString(groupPubkey), err))
		}
		verifyPubkey = xOnly
	}
	scriptXOnly := make([][]byte, len(scriptPubkeys))
	for i, spk := range scriptPubkeys {
		spkXOnly := append([]byte(nil), spk[2:]...)
		scriptXOnly[i] = spkXOnly
		spkMatch := bytes.Equal(spkXOnly, verifyPubkey)
		w.Logger.Info("frost-sign-debug: pre-submit script/pubkey check job=%s input=%d spk_xonly=%s verify_pubkey=%s spk_match=%t",
			job.JobID, i, hex.EncodeToString(spkXOnly), hex.EncodeToString(verifyPubkey), spkMatch)
		if !spkMatch {
			msg := fmt.Sprintf("frost-sign-debug: script_pubkey x-only mismatch job=%s input=%d sign_algo=%s spk_xonly=%s verify_pubkey=%s group_pubkey=%s script_pubkey=%s",
				job.JobID, i, signAlgo.String(),
				hex.EncodeToString(spkXOnly),
				hex.EncodeToString(verifyPubkey),
				hex.EncodeToString(groupPubkey),
				hex.EncodeToString(spk))
			w.Logger.Error("%s", msg)
			panic(msg)
		}
	}

	sighashes, err := template.ComputeTaprootSighash(scriptPubkeys, btc.SighashDefault)
	if err != nil {
		panic(fmt.Sprintf("frost-sign-debug: ComputeTaprootSighash failed job=%s: %v", job.JobID, err))
	}
	for i := range inputSigs {
		spkXOnlyHex := ""
		spkMatch := false
		if i < len(scriptXOnly) && len(scriptXOnly[i]) > 0 {
			spkXOnlyHex = hex.EncodeToString(scriptXOnly[i])
			spkMatch = bytes.Equal(scriptXOnly[i], verifyPubkey)
		}
		valid, verifyErr, panicVal := verifySignatureWithRecover(signAlgo, verifyPubkey, sighashes[i], inputSigs[i])
		if panicVal != nil {
			msg := fmt.Sprintf("frost-sign-debug: pre-submit verify panic job=%s input=%d sign_algo=%s sighash=%s sig=%s group_pubkey=%s verify_pubkey=%s spk_xonly=%s spk_match=%t template_hash=%s script_pubkey=%s panic=%v",
				job.JobID, i, signAlgo.String(),
				hex.EncodeToString(sighashes[i]),
				hex.EncodeToString(inputSigs[i]),
				hex.EncodeToString(groupPubkey),
				hex.EncodeToString(verifyPubkey),
				spkXOnlyHex,
				spkMatch,
				shortHexPanic(job.TemplateHash, 32),
				hex.EncodeToString(scriptPubkeys[i]),
				panicVal)
			w.Logger.Error("%s", msg)
			panic(msg)
		}
		if verifyErr != nil {
			msg := fmt.Sprintf("frost-sign-debug: pre-submit verify error job=%s input=%d sign_algo=%s sighash=%s sig=%s group_pubkey=%s verify_pubkey=%s spk_xonly=%s spk_match=%t template_hash=%s script_pubkey=%s err=%v",
				job.JobID, i, signAlgo.String(),
				hex.EncodeToString(sighashes[i]),
				hex.EncodeToString(inputSigs[i]),
				hex.EncodeToString(groupPubkey),
				hex.EncodeToString(verifyPubkey),
				spkXOnlyHex,
				spkMatch,
				shortHexPanic(job.TemplateHash, 32),
				hex.EncodeToString(scriptPubkeys[i]),
				verifyErr)
			w.Logger.Error("%s", msg)
			panic(msg)
		}
		if !valid {
			msg := fmt.Sprintf("frost-sign-debug: pre-submit verify invalid job=%s input=%d sign_algo=%s sighash=%s sig=%s group_pubkey=%s verify_pubkey=%s spk_xonly=%s spk_match=%t template_hash=%s script_pubkey=%s",
				job.JobID, i, signAlgo.String(),
				hex.EncodeToString(sighashes[i]),
				hex.EncodeToString(inputSigs[i]),
				hex.EncodeToString(groupPubkey),
				hex.EncodeToString(verifyPubkey),
				spkXOnlyHex,
				spkMatch,
				shortHexPanic(job.TemplateHash, 32),
				hex.EncodeToString(scriptPubkeys[i]))
			w.Logger.Error("%s", msg)
			panic(msg)
		}
	}
}

func verifySignatureWithRecover(signAlgo pb.SignAlgo, pubkey, msg, sig []byte) (valid bool, err error, panicVal any) {
	defer func() {
		if r := recover(); r != nil {
			panicVal = r
		}
	}()
	valid, err = frost.Verify(signAlgo, pubkey, msg, sig)
	return valid, err, nil
}

func (w *WithdrawWorker) shouldSkipJob(job *planning.Job) (bool, string, error) {
	if w.stateReader == nil || job == nil {
		return false, "", nil
	}

	txID := fmt.Sprintf("tx_withdraw_%s", job.JobID)
	if _, exists, err := w.stateReader.Get(keys.KeyPendingAnyTx(txID)); err != nil {
		return false, "", err
	} else if exists {
		return true, "withdraw tx already pending", nil
	}
	if _, exists, err := w.stateReader.Get(keys.KeyVMAppliedTx(txID)); err != nil {
		return false, "", err
	} else if exists {
		return true, "withdraw tx already applied", nil
	}
	if countData, exists, err := w.stateReader.Get(keys.KeyFrostSignedPackageCount(job.JobID)); err != nil {
		return false, "", err
	} else if exists && len(countData) > 0 {
		if count, parseErr := strconv.ParseUint(string(countData), 10, 64); parseErr == nil && count > 0 {
			return true, "signed package already persisted", nil
		}
	}

	for _, withdrawID := range job.WithdrawIDs {
		withdrawData, exists, err := w.stateReader.Get(keys.KeyFrostWithdraw(withdrawID))
		if err != nil {
			return false, "", err
		}
		if !exists || len(withdrawData) == 0 {
			return true, fmt.Sprintf("withdraw %s not found", withdrawID), nil
		}

		var state pb.FrostWithdrawState
		if err := proto.Unmarshal(withdrawData, &state); err != nil {
			return false, "", err
		}
		if state.Status != "QUEUED" {
			return true, fmt.Sprintf("withdraw %s status=%s", withdrawID, state.Status), nil
		}
	}

	return false, "", nil
}

func (w *WithdrawWorker) isJobCoordinator(job *planning.Job) (bool, string, error) {
	committee, err := w.vaultProvider.VaultCommittee(job.Chain, job.VaultID, job.KeyEpoch)
	if err != nil {
		return false, "", err
	}
	if len(committee) == 0 {
		return false, "", fmt.Errorf("empty committee for %s/%d/%d", job.Chain, job.VaultID, job.KeyEpoch)
	}

	coordIndex := computeJobCoordinatorIndex(job.JobID, job.KeyEpoch, committee)
	if coordIndex < 0 || coordIndex >= len(committee) {
		return false, "", fmt.Errorf("invalid coordinator index=%d", coordIndex)
	}

	coordAddr := string(committee[coordIndex].ID)
	return w.localAddress == coordAddr, coordAddr, nil
}

func computeJobCoordinatorIndex(jobID string, keyEpoch uint64, committee []SignerInfo) int {
	if len(committee) == 0 {
		return 0
	}
	seed := computeJobCoordinatorSeed(jobID, keyEpoch)
	permuted := permuteSignerCommittee(committee, seed)
	target := permuted[0].ID
	for i, member := range committee {
		if member.ID == target {
			return i
		}
	}
	return 0
}

func computeJobCoordinatorSeed(jobID string, keyEpoch uint64) []byte {
	// Keep this consistent with roast/coordinator.go.
	data := jobID + "|" + string(rune(keyEpoch)) + "|frost_agg"
	hash := sha256.Sum256([]byte(data))
	return hash[:]
}

func permuteSignerCommittee(committee []SignerInfo, seed []byte) []SignerInfo {
	result := make([]SignerInfo, len(committee))
	copy(result, committee)
	for i := len(result) - 1; i > 0; i-- {
		indexSeed := sha256.Sum256(append(seed, byte(i)))
		j := int(indexSeed[0]) % (i + 1)
		result[i], result[j] = result[j], result[i]
	}
	return result
}
