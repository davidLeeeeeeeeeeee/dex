// frost/runtime/planning/btc_job.go
// BTC 单/批 job 构建

package planning

import (
	"fmt"

	chainpkg "dex/frost/chain"
	"dex/keys"
	"dex/pb"

	"google.golang.org/protobuf/proto"
)

// planBTCJob 规划 BTC job（单 UTXO 保守策略）
// 找到第一个可用 UTXO，再贪心选取能覆盖的 withdraw outputs。
func (p *JobWindowPlanner) planBTCJob(chain, asset string, vaultID uint32, keyEpoch uint64, withdraws []*ScanResult) (*Job, error) {
	// 1. 选择单个 UTXO
	utxos, err := p.selectBTCUTXOs(chain, vaultID)
	if err != nil {
		return nil, err
	}
	if len(utxos) == 0 {
		return nil, fmt.Errorf("no available UTXOs for vault %d", vaultID)
	}
	utxo := utxos[0]
	totalInput := utxo.Amount

	// 2. 读取 withdraw 详情，贪心选取能覆盖的 outputs
	outputs := make([]chainpkg.WithdrawOutput, 0, len(withdraws))
	withdrawIDs := make([]string, 0, len(withdraws))
	var firstSeq uint64
	var totalAmount uint64

	fee := p.estimateBTCFee(1, 1) // 初始：1 input, 1 output (最少输出)
	const btcDustLimit uint64 = 546

	for i, wd := range withdraws {
		wKey := keys.KeyFrostWithdraw(wd.WithdrawID)
		wData, exists, err := p.stateReader.Get(wKey)
		if err != nil || !exists {
			continue
		}
		state := &pb.FrostWithdrawState{}
		if err := proto.Unmarshal(wData, state); err != nil {
			continue
		}
		wAmount := parseAmount(state.Amount)
		curFee := p.estimateBTCFee(1, len(outputs)+1+1) // +1 for this output, +1 for possible change
		if totalAmount+wAmount+curFee > totalInput {
			break // 超出这个 UTXO 的承载能力
		}
		if i == 0 {
			firstSeq = wd.Seq
		}
		outputs = append(outputs, chainpkg.WithdrawOutput{
			WithdrawID: wd.WithdrawID,
			To:         state.To,
			Amount:     wAmount,
		})
		withdrawIDs = append(withdrawIDs, wd.WithdrawID)
		totalAmount += wAmount
	}
	if len(outputs) == 0 {
		return nil, fmt.Errorf("UTXO amount %d cannot cover smallest withdraw for vault %d", totalInput, vaultID)
	}

	// 3. 计算实际手续费和找零
	fee = p.estimateBTCFee(1, len(outputs))
	changeAmount := uint64(0)
	changeAddress := ""

	if totalInput > totalAmount+fee {
		changeAmount = totalInput - totalAmount - fee
		if changeAmount >= btcDustLimit {
			feeWithChange := p.estimateBTCFee(1, len(outputs)+1)
			if totalInput > totalAmount+feeWithChange {
				changeAmount = totalInput - totalAmount - feeWithChange
				fee = feeWithChange
				if changeAmount >= btcDustLimit {
					// 找零回 UTXO 原地址（同 tweak）
					changeAddress = btcAddressFromScriptPubKey(utxo.ScriptPubKey, chain)
					if changeAddress == "" {
						fee = totalInput - totalAmount
						changeAmount = 0
					}
				} else {
					fee = totalInput - totalAmount
					changeAmount = 0
				}
			} else {
				fee = totalInput - totalAmount
				changeAmount = 0
			}
		} else {
			fee = totalInput - totalAmount
			changeAmount = 0
		}
	}

	// 4. 构建模板
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

// btcAddressFromScriptPubKey 从 Taproot scriptPubKey 反推 bech32m 地址
func btcAddressFromScriptPubKey(scriptPubKey []byte, chain string) string {
	if len(scriptPubKey) != 34 || scriptPubKey[0] != 0x51 || scriptPubKey[1] != 0x20 {
		return ""
	}
	addr, err := btcTaprootAddressFromXOnly(scriptPubKey[2:], btcNetParamsForChain(chain))
	if err != nil {
		return ""
	}
	return addr
}
