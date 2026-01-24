package main

import (
	"dex/pb"
	"fmt"
	"time"
)

// generateTxID 生成正确格式的十六进制 TxId (0x + 64位十六进制)
func generateTxID(counter uint64) string {
	return fmt.Sprintf("0x%016x%016x", time.Now().UnixNano(), counter)
}

// generateTransferTx 生成转账交易
func generateTransferTx(from, to, token, amount, fee string, nonce uint64) *pb.AnyTx {
	tx := &pb.Transaction{
		Base: &pb.BaseMessage{
			TxId:        generateTxID(nonce),
			FromAddress: from,
			Fee:         fee,
			Status:      pb.Status_PENDING,
			Nonce:       nonce,
		},
		To:           to,
		TokenAddress: token,
		Amount:       amount,
	}
	return &pb.AnyTx{
		Content: &pb.AnyTx_Transaction{Transaction: tx},
	}
}

// generateIssueTokenTx 生成发币交易
func generateIssueTokenTx(from, name, symbol, supply string, canMint bool, nonce uint64) *pb.AnyTx {
	tx := &pb.IssueTokenTx{
		Base: &pb.BaseMessage{
			TxId:        generateTxID(nonce),
			FromAddress: from,
			Status:      pb.Status_PENDING,
			Nonce:       nonce,
		},
		TokenName:   name,
		TokenSymbol: symbol,
		TotalSupply: supply,
		CanMint:     canMint,
	}
	return &pb.AnyTx{
		Content: &pb.AnyTx_IssueTokenTx{IssueTokenTx: tx},
	}
}

// generateMinerTx 生成矿工注册交易
func generateMinerTx(from string, op pb.OrderOp, amount string, nonce uint64) *pb.AnyTx {
	tx := &pb.MinerTx{
		Base: &pb.BaseMessage{
			TxId:        generateTxID(nonce),
			FromAddress: from,
			Status:      pb.Status_PENDING,
			Nonce:       nonce,
		},
		Op:     op,
		Amount: amount,
	}
	return &pb.AnyTx{
		Content: &pb.AnyTx_MinerTx{MinerTx: tx},
	}
}

// generateWitnessStakeTx 生成见证者质押交易
func generateWitnessStakeTx(from string, op pb.OrderOp, amount string, nonce uint64) *pb.AnyTx {
	tx := &pb.WitnessStakeTx{
		Base: &pb.BaseMessage{
			TxId:        generateTxID(nonce),
			FromAddress: from,
			Status:      pb.Status_PENDING,
			Nonce:       nonce,
		},
		Op:     op,
		Amount: amount,
	}
	return &pb.AnyTx{
		Content: &pb.AnyTx_WitnessStakeTx{WitnessStakeTx: tx},
	}
}

// generateWitnessRequestTx 生成上账请求交易
func generateWitnessRequestTx(from, chain, nativeHash, token, amount, receiver, fee string, nonce uint64) *pb.AnyTx {
	tx := &pb.WitnessRequestTx{
		Base: &pb.BaseMessage{
			TxId:        generateTxID(nonce),
			FromAddress: from,
			Status:      pb.Status_PENDING,
			Nonce:       nonce,
		},
		NativeChain:     chain,
		NativeTxHash:    nativeHash,
		TokenAddress:    token,
		Amount:          amount,
		ReceiverAddress: receiver,
		RechargeFee:     fee,
	}
	return &pb.AnyTx{
		Content: &pb.AnyTx_WitnessRequestTx{WitnessRequestTx: tx},
	}
}

// generateWitnessVoteTx 生成见证投票交易
func generateWitnessVoteTx(witness, requestID string, voteType pb.WitnessVoteType, nonce uint64) *pb.AnyTx {
	tx := &pb.WitnessVoteTx{
		Base: &pb.BaseMessage{
			TxId:        generateTxID(nonce),
			FromAddress: witness,
			Status:      pb.Status_PENDING,
			Nonce:       nonce,
		},
		Vote: &pb.WitnessVote{
			RequestId:      requestID,
			WitnessAddress: witness,
			VoteType:       voteType,
			Timestamp:      uint64(time.Now().Unix()),
		},
	}
	return &pb.AnyTx{
		Content: &pb.AnyTx_WitnessVoteTx{WitnessVoteTx: tx},
	}
}

// generateWithdrawRequestTx 生成提现请求交易
func generateWithdrawRequestTx(from, chain, asset, to, amount string, nonce uint64) *pb.AnyTx {
	tx := &pb.FrostWithdrawRequestTx{
		Base: &pb.BaseMessage{
			TxId:        generateTxID(nonce),
			FromAddress: from,
			Status:      pb.Status_PENDING,
			Nonce:       nonce,
		},
		Chain:  chain,
		Asset:  asset,
		To:     to,
		Amount: amount,
	}
	return &pb.AnyTx{
		Content: &pb.AnyTx_FrostWithdrawRequestTx{FrostWithdrawRequestTx: tx},
	}
}

// generateDkgCommitTx 生成 DKG 承诺交易
func generateDkgCommitTx(from, chain string, vaultID uint32, epochID uint64, algo pb.SignAlgo, nonce uint64) *pb.AnyTx {
	tx := &pb.FrostVaultDkgCommitTx{
		Base: &pb.BaseMessage{
			TxId:        generateTxID(nonce),
			FromAddress: from,
			Status:      pb.Status_PENDING,
			Nonce:       nonce,
		},
		Chain:            chain,
		VaultId:          vaultID,
		EpochId:          epochID,
		SignAlgo:         algo,
		CommitmentPoints: [][]byte{[]byte("point1"), []byte("point2")}, // 模拟数据
		AI0:              []byte("ai0"),
	}
	return &pb.AnyTx{
		Content: &pb.AnyTx_FrostVaultDkgCommitTx{FrostVaultDkgCommitTx: tx},
	}
}

// generateDkgShareTx 生成 DKG Share 交易
func generateDkgShareTx(from, to, chain string, vaultID uint32, epochID uint64, nonce uint64) *pb.AnyTx {
	tx := &pb.FrostVaultDkgShareTx{
		Base: &pb.BaseMessage{
			TxId:        generateTxID(nonce),
			FromAddress: from,
			Status:      pb.Status_PENDING,
			Nonce:       nonce,
		},
		Chain:      chain,
		VaultId:    vaultID,
		EpochId:    epochID,
		DealerId:   from,
		ReceiverId: to,
		Ciphertext: []byte("encrypted_share"),
	}
	return &pb.AnyTx{
		Content: &pb.AnyTx_FrostVaultDkgShareTx{FrostVaultDkgShareTx: tx},
	}
}

// generateOrderTx 生成订单交易
func generateOrderTx(from, baseToken, quoteToken, amount, price string, nonce uint64, side pb.OrderSide) *pb.AnyTx {
	tx := &pb.OrderTx{
		Base: &pb.BaseMessage{
			TxId:        generateTxID(nonce),
			FromAddress: from,
			Fee:         "1",
			Status:      pb.Status_PENDING,
			Nonce:       nonce,
		},
		BaseToken:  baseToken,
		QuoteToken: quoteToken,
		Op:         pb.OrderOp_ADD,
		Amount:     amount,
		Price:      price,
		Side:       side,
	}
	return &pb.AnyTx{
		Content: &pb.AnyTx_OrderTx{OrderTx: tx},
	}
}

func generateTransactions(nodes []*NodeInstance) {
	simulator := NewTxSimulator(nodes)
	simulator.Start()
}
