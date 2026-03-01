// frost/runtime/planning/btc_job.go
// BTC 单/批 job 构建

package planning

import (
	"fmt"
	"strings"

	chainpkg "dex/frost/chain"
	"dex/keys"
	"dex/logs"
	"dex/pb"

	"google.golang.org/protobuf/proto"
)

// planBTCJob 规划 BTC job（批量：多个 withdraw，选择 UTXO）
func (p *JobWindowPlanner) planBTCJob(chain, asset string, vaultID uint32, keyEpoch uint64, withdraws []*ScanResult, totalAmount uint64) (*Job, error) {
	// 1. 读取所有 withdraw 详情
	outputs := make([]chainpkg.WithdrawOutput, 0, len(withdraws))
	withdrawIDs := make([]string, 0, len(withdraws))
	var firstSeq uint64

	for i, wd := range withdraws {
		withdrawKey := keys.KeyFrostWithdraw(wd.WithdrawID)
		withdrawData, exists, err := p.stateReader.Get(withdrawKey)
		if err != nil || !exists {
			return nil, err
		}

		state := &pb.FrostWithdrawState{}
		if err := proto.Unmarshal(withdrawData, state); err != nil {
			return nil, err
		}

		if i == 0 {
			firstSeq = wd.Seq
		}

		outputs = append(outputs, chainpkg.WithdrawOutput{
			WithdrawID: wd.WithdrawID,
			To:         state.To,
			Amount:     parseAmount(state.Amount),
		})
		withdrawIDs = append(withdrawIDs, wd.WithdrawID)
	}

	// 2. 选择 UTXO（按 confirm_height 升序，FIFO）
	utxos, err := p.selectBTCUTXOs(chain, vaultID, totalAmount)
	if err != nil {
		return nil, err
	}
	if len(utxos) == 0 {
		return nil, fmt.Errorf("no available UTXOs for vault %d", vaultID)
	}

	var fee uint64
	var changeAmount uint64
	changeAddress := ""
	const btcDustLimit uint64 = 546

	// Close the fee/input loop until totalIn can satisfy totalOut+fee(+change).
	for iter := 0; iter < 6; iter++ {
		totalInput := sumUTXOAmount(utxos)

		feeNoChange := p.estimateBTCFee(len(utxos), len(outputs))
		needNoChange := totalAmount + feeNoChange
		if totalInput < needNoChange {
			utxos, err = p.selectBTCUTXOs(chain, vaultID, needNoChange)
			if err != nil {
				return nil, err
			}
			continue
		}

		fee = feeNoChange
		changeAmount = totalInput - totalAmount - feeNoChange

		if changeAmount >= btcDustLimit {
			feeWithChange := p.estimateBTCFee(len(utxos), len(outputs)+1)
			needWithChange := totalAmount + feeWithChange
			if totalInput < needWithChange {
				utxos, err = p.selectBTCUTXOs(chain, vaultID, needWithChange)
				if err != nil {
					return nil, err
				}
				continue
			}

			fee = feeWithChange
			changeAmount = totalInput - totalAmount - feeWithChange

			if changeAmount >= btcDustLimit {
				changeAddress, err = p.getYoungestVaultTreasuryAddress(chain)
				if err != nil || strings.TrimSpace(changeAddress) == "" {
					logs.Warn("[JobWindowPlanner] fallback to no-change because youngest treasury address unavailable: %v", err)
					fee = totalInput - totalAmount
					changeAmount = 0
					changeAddress = ""
				}
			} else {
				// After accounting for an extra change output, change becomes dust.
				fee = totalInput - totalAmount
				changeAmount = 0
			}
		} else {
			// Dust-level remainder is absorbed into fee.
			fee = totalInput - totalAmount
			changeAmount = 0
		}

		break
	}

	totalInputFinal := sumUTXOAmount(utxos)
	if totalInputFinal < totalAmount+fee {
		return nil, fmt.Errorf("insufficient UTXOs after fee planning: need %d, got %d", totalAmount+fee, totalInputFinal)
	}

	// 4. Get chain adapter and build template.
	adapter, err := p.adapterFactory.Adapter(chain)
	if err != nil {
		return nil, err
	}

	params := chainpkg.WithdrawTemplateParams{
		Chain:         chain,
		Asset:         asset,
		VaultID:       vaultID,
		KeyEpoch:      keyEpoch,
		WithdrawIDs:   withdrawIDs,
		Outputs:       outputs,
		Inputs:        utxos,
		Fee:           fee,
		ChangeAmount:  changeAmount,
		ChangeAddress: changeAddress,
	}

	result, err := adapter.BuildWithdrawTemplate(params)
	if err != nil {
		return nil, err
	}

	// 5. 生成 job_id
	jobID := generateJobID(chain, asset, vaultID, firstSeq, result.TemplateHash, keyEpoch)

	return &Job{
		JobID:        jobID,
		Chain:        chain,
		Asset:        asset,
		VaultID:      vaultID,
		KeyEpoch:     keyEpoch,
		WithdrawIDs:  withdrawIDs,
		TemplateHash: result.TemplateHash,
		TemplateData: result.TemplateData,
		FirstSeq:     firstSeq,
	}, nil
}
