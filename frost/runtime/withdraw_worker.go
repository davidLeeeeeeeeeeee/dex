// frost/runtime/withdraw_worker.go
// 提现流程执行器（Scanner -> Planner -> 提交 FrostWithdrawSignedTx）
package runtime

import (
	"dex/frost/chain"
	"dex/pb"
	"encoding/hex"
)

// WithdrawWorker 提现流程执行器
type WithdrawWorker struct {
	scanner     *Scanner
	planner     *JobPlanner
	txSubmitter TxSubmitter
}

// NewWithdrawWorker 创建新的 WithdrawWorker
func NewWithdrawWorker(stateReader ChainStateReader, adapterFactory chain.ChainAdapterFactory, txSubmitter TxSubmitter) *WithdrawWorker {
	return &WithdrawWorker{
		scanner:     NewScanner(stateReader),
		planner:     NewJobPlanner(stateReader, adapterFactory),
		txSubmitter: txSubmitter,
	}
}

// ProcessOnce 处理一次提现流程
// 1. Scanner 扫描队首 QUEUED withdraw
// 2. JobPlanner 规划 job（确定性模板）
// 3. 生成 dummy 签名并提交 FrostWithdrawSignedTx
func (w *WithdrawWorker) ProcessOnce(chain, asset string) (*Job, error) {
	// 1. 扫描队首 QUEUED withdraw
	scanResult, err := w.scanner.ScanOnce(chain, asset)
	if err != nil {
		return nil, err
	}
	if scanResult == nil {
		// 没有待处理的 withdraw
		return nil, nil
	}

	// 2. 规划 job
	job, err := w.planner.PlanJob(scanResult)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, nil
	}

	// 3. 生成 dummy 签名（signed_package_bytes = "dummy" + template_hash）
	signedPackageBytes := w.generateDummySignature(job.TemplateHash)

	// 4. 提交 FrostWithdrawSignedTx
	tx := &pb.FrostWithdrawSignedTx{
		Base: &pb.BaseMessage{
			// BaseMessage 字段可以后续填充
		},
		JobId:              job.JobID,
		SignedPackageBytes: signedPackageBytes,
		WithdrawIds:        job.WithdrawIDs,
	}

	_, err = w.txSubmitter.Submit(tx)
	if err != nil {
		return nil, err
	}

	return job, nil
}

// generateDummySignature 生成 dummy 签名
// 格式：dummy + template_hash（hex）
func (w *WithdrawWorker) generateDummySignature(templateHash []byte) []byte {
	return []byte("dummy" + hex.EncodeToString(templateHash))
}
