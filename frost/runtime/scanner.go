// frost/runtime/scanner.go
// Frost FIFO 队列扫描器
package runtime

import (
	"dex/keys"
	"dex/pb"
	"strconv"

	"google.golang.org/protobuf/proto"
)

// ScanResult 扫描结果
type ScanResult struct {
	Chain      string
	Asset      string
	WithdrawID string
	Seq        uint64
}

// Scanner FIFO 队列扫描器
type Scanner struct {
	stateReader ChainStateReader
}

// NewScanner 创建新的 Scanner
func NewScanner(stateReader ChainStateReader) *Scanner {
	return &Scanner{
		stateReader: stateReader,
	}
}

// ScanOnce 扫描一次指定 (chain, asset) 的 FIFO 队列
// 返回队首 QUEUED 状态的 withdraw，如果没有返回 nil
func (s *Scanner) ScanOnce(chain, asset string) (*ScanResult, error) {
	// 1. 读取当前 head（下一个待处理的 seq）
	headKey := keys.KeyFrostWithdrawFIFOHead(chain, asset)
	headData, exists, err := s.stateReader.Get(headKey)
	if err != nil {
		return nil, err
	}

	var head uint64 = 1 // 默认从 seq=1 开始
	if exists && len(headData) > 0 {
		head, _ = strconv.ParseUint(string(headData), 10, 64)
		if head == 0 {
			head = 1
		}
	}

	// 2. 读取当前 seq（最大已分配的 seq）
	seqKey := keys.KeyFrostWithdrawFIFOSeq(chain, asset)
	seqData, exists, err := s.stateReader.Get(seqKey)
	if err != nil {
		return nil, err
	}

	if !exists || len(seqData) == 0 {
		// 没有任何 withdraw request
		return nil, nil
	}

	maxSeq, _ := strconv.ParseUint(string(seqData), 10, 64)
	if head > maxSeq {
		// 所有 withdraw 都已处理
		return nil, nil
	}

	// 3. 读取 head 位置的 withdraw_id
	indexKey := keys.KeyFrostWithdrawFIFOIndex(chain, asset, head)
	withdrawID, exists, err := s.stateReader.Get(indexKey)
	if err != nil {
		return nil, err
	}

	if !exists || len(withdrawID) == 0 {
		// index 不存在，可能是数据不一致
		return nil, nil
	}

	// 4. 读取 withdraw 详情并检查状态
	withdrawKey := keys.KeyFrostWithdraw(string(withdrawID))
	withdrawData, exists, err := s.stateReader.Get(withdrawKey)
	if err != nil {
		return nil, err
	}

	if !exists || len(withdrawData) == 0 {
		return nil, nil
	}

	// 解析 withdraw 状态
	state := &pb.FrostWithdrawState{}
	if err := proto.Unmarshal(withdrawData, state); err != nil {
		return nil, err
	}

	// 只返回 QUEUED 状态的 withdraw
	if state.Status != "QUEUED" {
		return nil, nil
	}

	return &ScanResult{
		Chain:      chain,
		Asset:      asset,
		WithdrawID: string(withdrawID),
		Seq:        head,
	}, nil
}
