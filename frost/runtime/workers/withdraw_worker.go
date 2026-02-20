// frost/runtime/workers/withdraw_worker.go
// 提现流程执行器（Scanner -> Planner -> SigningService -> 提交 FrostWithdrawSignedTx）
package workers

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"dex/frost/chain"
	"dex/frost/runtime/planning"
	"dex/logs"
	"dex/pb"
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
	scanner        *planning.Scanner
	planner        *planning.JobPlanner
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
		scanner:        planning.NewScanner(planningReader),
		planner:        planning.NewJobPlanner(planningReader, adapterFactory),
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
	for withdrawID, logs := range transientLogs {
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

	// 启动签名会话
	sessionParams := &SigningSessionParams{
		JobID:     job.JobID,
		Chain:     job.Chain,
		VaultID:   job.VaultID,
		KeyEpoch:  job.KeyEpoch,
		SignAlgo:  signAlgo,
		Messages:  messages,
		Threshold: threshold,
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
	signedPkg, err := w.signingService.WaitForCompletion(ctx, sessionID, timeout)
	if err != nil {
		w.Logger.Error("[WithdrawWorker] signing session %s failed: %v", sessionID, err)
		// 汇报失败
		w.reportPlanningLogs(ctx, &planning.Job{
			JobID:       job.JobID,
			WithdrawIDs: job.WithdrawIDs,
			Logs: []*pb.FrostPlanningLog{
				{Step: "Signing", Status: "FAILED", Message: fmt.Sprintf("Signing session failed: %v", err), Timestamp: uint64(time.Now().UnixMilli())},
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
		Chain:              job.Chain,
		Asset:              job.Asset,
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
	// 对于合约链/账户链：消息就是 template_hash
	// 对于 BTC：需要从模板中提取每个 input 的 sighash
	// 这里简化处理，返回 template_hash 作为单条消息
	// 实际实现需要根据链类型和模板数据提取
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
			signedPkg, err := w.signingService.WaitForCompletion(ctx, sessionID, timeout)
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
	// 构建完整的 SignedPackage
	// 包含：签名、模板数据、raw_tx（如果可用）
	// 这里简化处理，实际需要序列化为 protobuf
	// TODO: 实现完整的 SignedPackage 序列化
	return signedPkg.Signature, nil
}
