package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"dex/pb"
)

// FrostWithdrawQueueItem 提现队列项
type FrostWithdrawQueueItem struct {
	WithdrawID   string                 `json:"withdraw_id"`
	Chain        string                 `json:"chain"`
	Asset        string                 `json:"asset"`
	To           string                 `json:"to"`
	Amount       string                 `json:"amount"`
	Status       string                 `json:"status"`
	PlanningLogs []*pb.FrostPlanningLog `json:"planning_logs"`
}

// WitnessVoteItem 供前端使用的见证投票结构
type WitnessVoteItem struct {
	RequestId      string `json:"request_id"`
	WitnessAddress string `json:"witness_address"`
	VoteType       int32  `json:"vote_type"`
	Status         string `json:"status"` // 新增状态字段，对应 VoteType 的字符串表示
	Reason         string `json:"reason,omitempty"`
	Timestamp      uint64 `json:"timestamp"`
	TxId           string `json:"tx_id"`
}

func (s *server) handleFrostWithdrawQueue(w http.ResponseWriter, r *http.Request) {
	node := r.URL.Query().Get("node")
	if node == "" {
		node = s.defaultNodes[0]
	}

	chain := r.URL.Query().Get("chain")
	asset := r.URL.Query().Get("asset")

	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()

	// 这里我们需要从节点获取数据。目前可能没有直接的 /frost/withdraw/queue API
	// 我们尝试通过 /getdata 通用接口或者新增一个接口
	// 为了简化演示，我们实现一个从 GetData 路径获取特定 Key 的逻辑

	// 实际生产中，节点应该提供一个专门查询队列的接口
	// 我们暂时假设节点支持 /frost/withdraw/queue
	var resp pb.FrostWithdrawStateList
	if err := s.fetchProto(ctx, node, "/frost/withdraw/list", &pb.FrostWithdrawListRequest{Chain: chain, Asset: asset}, &resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	items := make([]FrostWithdrawQueueItem, 0, len(resp.States))
	for _, st := range resp.States {
		items = append(items, FrostWithdrawQueueItem{
			WithdrawID:   st.WithdrawId,
			Chain:        st.Chain,
			Asset:        st.Asset,
			To:           st.To,
			Amount:       st.Amount,
			Status:       st.Status,
			PlanningLogs: st.PlanningLogs,
		})
	}

	writeJSON(w, items)
}

// WitnessRequestItem 供前端使用的见证请求结构（snake_case）
type WitnessRequestItem struct {
	RequestID         string            `json:"request_id"`
	NativeChain       string            `json:"native_chain"`
	NativeTxHash      string            `json:"native_tx_hash"`
	TokenAddress      string            `json:"token_address"`
	Amount            string            `json:"amount"`
	ReceiverAddress   string            `json:"receiver_address"`
	RequesterAddress  string            `json:"requester_address"`
	Status            string            `json:"status"`
	CreateHeight      uint64            `json:"create_height"`
	DeadlineHeight    uint64            `json:"deadline_height"`
	FinalizeHeight    uint64            `json:"finalize_height"`
	Round             uint32            `json:"round"`
	PassCount         uint32            `json:"pass_count"`
	FailCount         uint32            `json:"fail_count"`
	AbstainCount      uint32            `json:"abstain_count"`
	VaultID           uint32            `json:"vault_id"`
	SelectedWitnesses []string          `json:"selected_witnesses"`
	Votes             []WitnessVoteItem `json:"votes"`
}

func (s *server) handleWitnessRequests(w http.ResponseWriter, r *http.Request) {
	node := r.URL.Query().Get("node")
	if node == "" {
		node = s.defaultNodes[0]
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()

	// 节点的 /witness/requests 直接使用 GET 查询参数，不需要 request body
	var resp pb.RechargeRequestList
	if err := s.fetchProto(ctx, node, "/witness/requests?limit=100", nil, &resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 转换为前端格式（snake_case）
	items := make([]WitnessRequestItem, 0, len(resp.Requests))
	for _, req := range resp.Requests {
		items = append(items, WitnessRequestItem{
			RequestID:         req.RequestId,
			NativeChain:       req.NativeChain,
			NativeTxHash:      req.NativeTxHash,
			TokenAddress:      req.TokenAddress,
			Amount:            req.Amount,
			ReceiverAddress:   req.ReceiverAddress,
			RequesterAddress:  req.RequesterAddress,
			Status:            req.Status.String(),
			CreateHeight:      req.CreateHeight,
			DeadlineHeight:    req.DeadlineHeight,
			FinalizeHeight:    req.FinalizeHeight,
			Round:             req.Round,
			PassCount:         req.PassCount,
			FailCount:         req.FailCount,
			AbstainCount:      req.AbstainCount,
			VaultID:           req.VaultId,
			SelectedWitnesses: req.SelectedWitnesses,
			Votes:             convertVotes(req.Votes),
		})
	}

	writeJSON(w, items)
}

func convertVotes(protoVotes []*pb.WitnessVote) []WitnessVoteItem {
	if protoVotes == nil {
		return nil
	}
	votes := make([]WitnessVoteItem, 0, len(protoVotes))
	for _, v := range protoVotes {
		votes = append(votes, WitnessVoteItem{
			RequestId:      v.RequestId,
			WitnessAddress: v.WitnessAddress,
			VoteType:       int32(v.VoteType),
			Status:         v.VoteType.String(),
			Reason:         v.Reason,
			Timestamp:      v.Timestamp,
			TxId:           v.TxId,
		})
	}
	return votes
}

// DKGSessionItem 供前端使用的 DKG 会话结构（snake_case）
type DKGSessionItem struct {
	Chain               string                    `json:"chain"`
	VaultID             uint32                    `json:"vault_id"`
	EpochID             uint64                    `json:"epoch_id"`
	SignAlgo            string                    `json:"sign_algo"`
	TriggerHeight       uint64                    `json:"trigger_height"`
	OldCommitteeMembers []string                  `json:"old_committee_members"`
	NewCommitteeMembers []string                  `json:"new_committee_members"`
	DkgStatus           string                    `json:"dkg_status"`
	DkgSessionID        string                    `json:"dkg_session_id"`
	DkgThresholdT       uint32                    `json:"dkg_threshold_t"`
	DkgN                uint32                    `json:"dkg_n"`
	DkgCommitDeadline   uint64                    `json:"dkg_commit_deadline"`
	DkgDisputeDeadline  uint64                    `json:"dkg_dispute_deadline"`
	ValidationStatus    string                    `json:"validation_status"`
	Lifecycle           string                    `json:"lifecycle"`
	NewGroupPubkey      string                    `json:"new_group_pubkey,omitempty"`
	ManagedAssets       []string                  `json:"managed_assets,omitempty"`
	AssetDeposits       []DKGAssetDepositsByAsset `json:"asset_deposits,omitempty"`
}

// DKGAssetDepositItem 单个资产入账请求（用于核对 request_id 与充值状态）
type DKGAssetDepositItem struct {
	RequestID      string `json:"request_id"`
	Status         string `json:"status"`
	Amount         string `json:"amount,omitempty"`
	FinalizeHeight uint64 `json:"finalize_height,omitempty"`
}

// DKGAssetDepositsByAsset 同一资产下的入账请求列表
type DKGAssetDepositsByAsset struct {
	Asset    string                `json:"asset"`
	Deposits []DKGAssetDepositItem `json:"deposits"`
}

func dkgVaultKey(chain string, vaultID uint32) string {
	return fmt.Sprintf("%s:%d", strings.ToUpper(strings.TrimSpace(chain)), vaultID)
}

func normalizeAsset(asset string) string {
	a := strings.TrimSpace(asset)
	if a == "" {
		return "native"
	}
	return a
}

func (s *server) handleFrostDKGSessions(w http.ResponseWriter, r *http.Request) {
	node := r.URL.Query().Get("node")
	if node == "" {
		node = s.defaultNodes[0]
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()

	// 假设节点支持 /frost/dkg/list
	var resp pb.VaultTransitionStateList
	if err := s.fetchProto(ctx, node, "/frost/dkg/list", nil, &resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 同步充值请求并按 (chain, vault_id, asset) 聚合，供前端展示每个 Vault 管理的资产和 request_id。
	vaultAssetDeposits := make(map[string]map[string][]DKGAssetDepositItem)
	var reqResp pb.RechargeRequestList
	if err := s.fetchProto(ctx, node, "/witness/requests?limit=500", nil, &reqResp); err == nil {
		for _, req := range reqResp.Requests {
			vKey := dkgVaultKey(req.NativeChain, req.VaultId)
			asset := normalizeAsset(req.TokenAddress)

			assetsByVault, ok := vaultAssetDeposits[vKey]
			if !ok {
				assetsByVault = make(map[string][]DKGAssetDepositItem)
				vaultAssetDeposits[vKey] = assetsByVault
			}

			assetsByVault[asset] = append(assetsByVault[asset], DKGAssetDepositItem{
				RequestID:      req.RequestId,
				Status:         req.Status.String(),
				Amount:         req.Amount,
				FinalizeHeight: req.FinalizeHeight,
			})
		}
	}

	// 转换为前端格式（snake_case）
	items := make([]DKGSessionItem, 0, len(resp.States))
	for _, st := range resp.States {
		item := DKGSessionItem{
			Chain:               st.Chain,
			VaultID:             st.VaultId,
			EpochID:             st.EpochId,
			SignAlgo:            st.SignAlgo.String(),
			TriggerHeight:       st.TriggerHeight,
			OldCommitteeMembers: st.OldCommitteeMembers,
			NewCommitteeMembers: st.NewCommitteeMembers,
			DkgStatus:           st.DkgStatus,
			DkgSessionID:        st.DkgSessionId,
			DkgThresholdT:       st.DkgThresholdT,
			DkgN:                st.DkgN,
			DkgCommitDeadline:   st.DkgCommitDeadline,
			DkgDisputeDeadline:  st.DkgDisputeDeadline,
			ValidationStatus:    st.ValidationStatus,
			Lifecycle:           st.Lifecycle,
		}
		// 如果有公钥，转换为 hex 字符串
		if len(st.NewGroupPubkey) > 0 {
			item.NewGroupPubkey = hex.EncodeToString(st.NewGroupPubkey)
		}

		if groupedByAsset, ok := vaultAssetDeposits[dkgVaultKey(st.Chain, st.VaultId)]; ok {
			assets := make([]string, 0, len(groupedByAsset))
			for asset, deposits := range groupedByAsset {
				assets = append(assets, asset)
				sort.Slice(deposits, func(i, j int) bool {
					if deposits[i].FinalizeHeight == deposits[j].FinalizeHeight {
						return deposits[i].RequestID > deposits[j].RequestID
					}
					return deposits[i].FinalizeHeight > deposits[j].FinalizeHeight
				})
				groupedByAsset[asset] = deposits
			}
			sort.Strings(assets)

			item.ManagedAssets = assets
			item.AssetDeposits = make([]DKGAssetDepositsByAsset, 0, len(assets))
			for _, asset := range assets {
				item.AssetDeposits = append(item.AssetDeposits, DKGAssetDepositsByAsset{
					Asset:    asset,
					Deposits: groupedByAsset[asset],
				})
			}
		}

		items = append(items, item)
	}

	writeJSON(w, items)
}

// WitnessInfoItem 供前端使用的见证者信息结构（snake_case）
type WitnessInfoItem struct {
	Address           string   `json:"address"`
	StakeAmount       string   `json:"stake_amount"`
	PendingReward     string   `json:"pending_reward"`
	Status            string   `json:"status"`
	UnstakeHeight     uint64   `json:"unstake_height"`
	TotalWitnessCount uint64   `json:"total_witness_count"`
	PassCount         uint64   `json:"pass_count"`
	FailCount         uint64   `json:"fail_count"`
	AbstainCount      uint64   `json:"abstain_count"`
	SlashedCount      uint64   `json:"slashed_count"`
	PendingTasks      []string `json:"pending_tasks"`
	TotalReward       string   `json:"total_reward"`
}

func (s *server) handleWitnessList(w http.ResponseWriter, r *http.Request) {
	node := r.URL.Query().Get("node")
	if node == "" {
		node = s.defaultNodes[0]
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()

	// 从节点获取见证者列表
	var resp pb.WitnessInfoList
	if err := s.fetchProto(ctx, node, "/witness/list", nil, &resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 转换为前端格式（snake_case）
	items := make([]WitnessInfoItem, 0, len(resp.Witnesses))
	for _, info := range resp.Witnesses {
		items = append(items, WitnessInfoItem{
			Address:           info.Address,
			StakeAmount:       info.StakeAmount,
			PendingReward:     info.PendingReward,
			Status:            info.Status.String(),
			UnstakeHeight:     info.UnstakeHeight,
			TotalWitnessCount: info.TotalWitnessCount,
			PassCount:         info.PassCount,
			FailCount:         info.FailCount,
			AbstainCount:      info.AbstainCount,
			SlashedCount:      info.SlashedCount,
			PendingTasks:      info.PendingTasks,
			TotalReward:       info.TotalReward,
		})
	}

	writeJSON(w, items)
}
