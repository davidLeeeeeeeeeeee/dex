package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"dex/pb"
)

// FrostWithdrawQueueItem 提现队列项
type FrostWithdrawQueueItem struct {
	WithdrawID          string                 `json:"withdraw_id"`
	Chain               string                 `json:"chain"`
	Asset               string                 `json:"asset"`
	To                  string                 `json:"to"`
	Amount              string                 `json:"amount"`
	Status              string                 `json:"status"`
	TxID                string                 `json:"tx_id,omitempty"`
	JobID               string                 `json:"job_id,omitempty"`
	VaultID             uint32                 `json:"vault_id,omitempty"`
	RequestHeight       uint64                 `json:"request_height,omitempty"`
	PlanningLogs        []*pb.FrostPlanningLog `json:"planning_logs"`
	DerivedStatus       string                 `json:"derived_status,omitempty"`
	DerivedReason       string                 `json:"derived_reason,omitempty"`
	LastPlanningStep    string                 `json:"last_planning_step,omitempty"`
	LastPlanningStatus  string                 `json:"last_planning_status,omitempty"`
	LastPlanningMessage string                 `json:"last_planning_message,omitempty"`
	LastPlanningAt      uint64                 `json:"last_planning_at,omitempty"`
	FailureReason       string                 `json:"failure_reason,omitempty"`
	FailureAt           uint64                 `json:"failure_at,omitempty"`
}

type FrostWithdrawJobItem struct {
	JobID              string   `json:"job_id"`
	Chain              string   `json:"chain"`
	Asset              string   `json:"asset"`
	VaultID            uint32   `json:"vault_id"`
	Status             string   `json:"status"`
	WithdrawCount      int      `json:"withdraw_count"`
	WithdrawIDs        []string `json:"withdraw_ids"`
	TotalAmount        string   `json:"total_amount"`
	LastPlanningStep   string   `json:"last_planning_step,omitempty"`
	LastPlanningStatus string   `json:"last_planning_status,omitempty"`
	LastPlanningAt     uint64   `json:"last_planning_at,omitempty"`
	FailureReason      string   `json:"failure_reason,omitempty"`
	FailureAt          uint64   `json:"failure_at,omitempty"`
	DerivedStatus      string   `json:"derived_status,omitempty"`
	DerivedReason      string   `json:"derived_reason,omitempty"`
	Synthetic          bool     `json:"synthetic,omitempty"`
}

type planningSummary struct {
	LastStep    string
	LastStatus  string
	LastMessage string
	LastAt      uint64

	FailureReason string
	FailureAt     uint64
}

func summarizePlanningLogs(logs []*pb.FrostPlanningLog) planningSummary {
	var out planningSummary
	for _, log := range logs {
		if log == nil {
			continue
		}
		if log.Timestamp >= out.LastAt {
			out.LastAt = log.Timestamp
			out.LastStep = strings.TrimSpace(log.Step)
			out.LastStatus = strings.TrimSpace(log.Status)
			out.LastMessage = strings.TrimSpace(log.Message)
		}
		if strings.EqualFold(strings.TrimSpace(log.Status), "FAILED") && strings.TrimSpace(log.Message) != "" {
			if log.Timestamp >= out.FailureAt {
				out.FailureAt = log.Timestamp
				out.FailureReason = strings.TrimSpace(log.Message)
			}
		}
	}
	return out
}

func containsAny(s string, subs ...string) bool {
	if s == "" {
		return false
	}
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func deriveWithdrawDebugStatus(chainStatus string, logs []*pb.FrostPlanningLog) (string, string) {
	if strings.EqualFold(strings.TrimSpace(chainStatus), "SIGNED") {
		return "SIGNED", ""
	}

	summary := summarizePlanningLogs(logs)
	if summary.LastStep == "" {
		return "QUEUED", ""
	}

	step := strings.ToLower(summary.LastStep)
	status := strings.ToLower(summary.LastStatus)
	msgLower := strings.ToLower(summary.LastMessage)

	switch {
	case step == "signing" && status == "failed":
		return "SIGN_FAILED", summary.LastMessage
	case step == "signing" && status == "ok":
		return "SIGN_DONE", summary.LastMessage
	case step == "signing" && status == "waiting":
		return "SIGNING", summary.LastMessage
	case step == "startsigning" && status == "ok":
		return "SIGNING", summary.LastMessage
	case step == "finishplanning" && status == "ok":
		return "PLANNED", summary.LastMessage
	case step == "buildtemplate" && status == "ok":
		return "PLANNED", summary.LastMessage
	case step == "planbtcjob" && status == "failed":
		if containsAny(msgLower, "insufficient utxo", "no available utxo", "utxo") {
			return "WAIT_UTXO", summary.LastMessage
		}
		if containsAny(msgLower, "youngest treasury address unavailable", "no active vault") {
			return "WAIT_ACTIVE_VAULT", summary.LastMessage
		}
		return "PLAN_FAILED", summary.LastMessage
	case step == "selectvault" && status == "failed":
		if containsAny(msgLower, "no active vault", "active vault", "status=active") {
			return "WAIT_ACTIVE_VAULT", summary.LastMessage
		}
		if containsAny(msgLower, "insufficient", "balance", "available") {
			return "WAIT_VAULT_BALANCE", summary.LastMessage
		}
		return "WAIT_SELECT_VAULT", summary.LastMessage
	case step == "compositeplan" && status == "waiting":
		return "WAIT_VAULT_BALANCE", summary.LastMessage
	case status == "waiting":
		return "PLANNING_WAITING", summary.LastMessage
	case status == "failed":
		return "PLANNING_FAILED", summary.LastMessage
	default:
		return "QUEUED", summary.LastMessage
	}
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
		summary := summarizePlanningLogs(st.PlanningLogs)
		derivedStatus, derivedReason := deriveWithdrawDebugStatus(st.Status, st.PlanningLogs)
		items = append(items, FrostWithdrawQueueItem{
			WithdrawID:          st.WithdrawId,
			Chain:               st.Chain,
			Asset:               st.Asset,
			To:                  st.To,
			Amount:              st.Amount,
			Status:              st.Status,
			TxID:                st.SignedTxId,
			JobID:               st.JobId,
			VaultID:             st.VaultId,
			RequestHeight:       st.RequestHeight,
			PlanningLogs:        st.PlanningLogs,
			DerivedStatus:       derivedStatus,
			DerivedReason:       derivedReason,
			LastPlanningStep:    summary.LastStep,
			LastPlanningStatus:  summary.LastStatus,
			LastPlanningMessage: summary.LastMessage,
			LastPlanningAt:      summary.LastAt,
			FailureReason:       summary.FailureReason,
			FailureAt:           summary.FailureAt,
		})
	}

	writeJSON(w, items)
}

func inferJobIDFromPlanningLogs(logs []*pb.FrostPlanningLog) string {
	for _, log := range logs {
		if log == nil {
			continue
		}
		msg := strings.TrimSpace(log.Message)
		if !strings.HasPrefix(msg, "Job ") || !strings.HasSuffix(msg, " created") {
			if strings.HasPrefix(msg, "Signing session ") && strings.HasSuffix(msg, " started") {
				rest := strings.TrimPrefix(msg, "Signing session ")
				parts := strings.Fields(rest)
				if len(parts) > 0 {
					jobID := strings.TrimSpace(parts[0])
					if jobID != "" {
						return jobID
					}
				}
			}
			continue
		}
		jobID := strings.TrimSuffix(strings.TrimPrefix(msg, "Job "), " created")
		jobID = strings.TrimSpace(jobID)
		if jobID != "" {
			return jobID
		}
	}
	return ""
}

func resolveJobStatus(statuses map[string]struct{}, logs []*pb.FrostPlanningLog) string {
	if _, ok := statuses["SIGNED"]; ok {
		return "SIGNED"
	}
	logHas := func(step, status string) bool {
		for _, log := range logs {
			if log == nil {
				continue
			}
			if log.Step == step && log.Status == status {
				return true
			}
		}
		return false
	}
	if logHas("Signing", "FAILED") {
		return "FAILED"
	}
	if logHas("Signing", "OK") {
		return "SIGN_DONE"
	}
	if logHas("StartSigning", "OK") || logHas("Signing", "WAITING") {
		return "SIGNING"
	}
	if logHas("FinishPlanning", "OK") {
		return "PLANNED"
	}
	return "QUEUED"
}

func (s *server) handleFrostWithdrawJobs(w http.ResponseWriter, r *http.Request) {
	node := r.URL.Query().Get("node")
	if node == "" {
		node = s.defaultNodes[0]
	}

	chain := r.URL.Query().Get("chain")
	asset := r.URL.Query().Get("asset")

	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()

	var resp pb.FrostWithdrawStateList
	if err := s.fetchProto(ctx, node, "/frost/withdraw/list", &pb.FrostWithdrawListRequest{Chain: chain, Asset: asset}, &resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type agg struct {
		item      FrostWithdrawJobItem
		statuses  map[string]struct{}
		total     uint64
		allLogs   []*pb.FrostPlanningLog
		bestLogAt uint64
		failLogAt uint64
	}
	byID := make(map[string]*agg)
	syntheticByID := make(map[string]FrostWithdrawJobItem)

	for _, st := range resp.States {
		if st == nil {
			continue
		}
		summary := summarizePlanningLogs(st.PlanningLogs)
		derivedStatus, derivedReason := deriveWithdrawDebugStatus(st.Status, st.PlanningLogs)

		jobID := strings.TrimSpace(st.JobId)
		if jobID == "" {
			jobID = inferJobIDFromPlanningLogs(st.PlanningLogs)
		}
		if jobID == "" {
			if derivedStatus == "QUEUED" && summary.LastStep == "" {
				continue
			}
			withdrawID := strings.TrimSpace(st.WithdrawId)
			if withdrawID == "" {
				continue
			}
			synthID := "planning:" + withdrawID
			synth := FrostWithdrawJobItem{
				JobID:              synthID,
				Chain:              st.Chain,
				Asset:              st.Asset,
				VaultID:            st.VaultId,
				Status:             derivedStatus,
				WithdrawCount:      1,
				WithdrawIDs:        []string{withdrawID},
				TotalAmount:        st.Amount,
				LastPlanningStep:   summary.LastStep,
				LastPlanningStatus: summary.LastStatus,
				LastPlanningAt:     summary.LastAt,
				FailureReason:      summary.FailureReason,
				FailureAt:          summary.FailureAt,
				DerivedStatus:      derivedStatus,
				DerivedReason:      derivedReason,
				Synthetic:          true,
			}
			if synth.FailureReason == "" && strings.HasSuffix(strings.ToUpper(derivedStatus), "FAILED") {
				synth.FailureReason = derivedReason
				synth.FailureAt = summary.LastAt
			}
			syntheticByID[synthID] = synth
			continue
		}

		cur, ok := byID[jobID]
		if !ok {
			cur = &agg{
				item: FrostWithdrawJobItem{
					JobID:       jobID,
					Chain:       st.Chain,
					Asset:       st.Asset,
					VaultID:     st.VaultId,
					WithdrawIDs: make([]string, 0),
				},
				statuses: make(map[string]struct{}),
				allLogs:  make([]*pb.FrostPlanningLog, 0),
			}
			byID[jobID] = cur
		}
		if cur.item.Chain == "" {
			cur.item.Chain = st.Chain
		}
		if cur.item.Asset == "" {
			cur.item.Asset = st.Asset
		}
		if cur.item.VaultID == 0 {
			cur.item.VaultID = st.VaultId
		}
		if cur.item.DerivedStatus == "" {
			cur.item.DerivedStatus = derivedStatus
			cur.item.DerivedReason = derivedReason
		}

		cur.statuses[st.Status] = struct{}{}
		cur.item.WithdrawIDs = append(cur.item.WithdrawIDs, st.WithdrawId)

		if amount, err := strconv.ParseUint(st.Amount, 10, 64); err == nil {
			cur.total += amount
		}

		for _, log := range st.PlanningLogs {
			if log == nil {
				continue
			}
			cur.allLogs = append(cur.allLogs, log)
			if strings.EqualFold(strings.TrimSpace(log.Status), "FAILED") && strings.TrimSpace(log.Message) != "" {
				if log.Timestamp >= cur.failLogAt {
					cur.failLogAt = log.Timestamp
					cur.item.FailureReason = strings.TrimSpace(log.Message)
					cur.item.FailureAt = log.Timestamp
				}
			}
			if log.Timestamp >= cur.bestLogAt {
				cur.bestLogAt = log.Timestamp
				cur.item.LastPlanningStep = log.Step
				cur.item.LastPlanningStatus = log.Status
				cur.item.LastPlanningAt = log.Timestamp
			}
		}
	}

	items := make([]FrostWithdrawJobItem, 0, len(byID))
	for _, cur := range byID {
		sort.Strings(cur.item.WithdrawIDs)
		cur.item.WithdrawCount = len(cur.item.WithdrawIDs)
		cur.item.TotalAmount = strconv.FormatUint(cur.total, 10)
		cur.item.Status = resolveJobStatus(cur.statuses, cur.allLogs)
		derivedStatus, derivedReason := deriveWithdrawDebugStatus(cur.item.Status, cur.allLogs)
		cur.item.DerivedStatus = derivedStatus
		if cur.item.DerivedReason == "" {
			cur.item.DerivedReason = derivedReason
		}
		items = append(items, cur.item)
	}
	for _, synth := range syntheticByID {
		items = append(items, synth)
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].LastPlanningAt == items[j].LastPlanningAt {
			return items[i].JobID < items[j].JobID
		}
		return items[i].LastPlanningAt > items[j].LastPlanningAt
	})

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
