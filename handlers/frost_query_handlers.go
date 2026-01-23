// handlers/frost_query_handlers.go
// Frost 只读查询 API

package handlers

import (
	"dex/config"
	"dex/keys"
	"dex/pb"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"google.golang.org/protobuf/proto"
)

// HandleGetFrostConfig 获取 Frost 配置
func (hm *HandlerManager) HandleGetFrostConfig(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("GetFrostConfig")

	// 先尝试从 DB 读取配置
	var frostCfg config.FrostConfig
	cfgKey := keys.KeyFrostConfig()
	data, err := hm.dbManager.Get(cfgKey)
	if err != nil || data == nil {
		// DB 没有配置，使用默认配置
		frostCfg = config.DefaultFrostConfig()
	} else {
		// 尝试反序列化
		if err := json.Unmarshal(data, &frostCfg); err != nil {
			frostCfg = config.DefaultFrostConfig()
		}
	}

	// 构建支持的链列表
	supportedChains := make([]string, 0, len(frostCfg.Chains))
	chainCfgsPb := make(map[string]*pb.ChainCfg)
	for chainName, chainCfg := range frostCfg.Chains {
		supportedChains = append(supportedChains, chainName)
		chainCfgsPb[chainName] = &pb.ChainCfg{
			SignAlgo:       chainCfg.SignAlgo,
			FrostVariant:   chainCfg.FrostVariant,
			VaultsPerChain: int32(chainCfg.VaultsPerChain),
		}
	}

	// 计算总 vault 数量
	totalVaults := 0
	for _, c := range frostCfg.Chains {
		totalVaults += c.VaultsPerChain
	}

	resp := &pb.GetFrostConfigResponse{
		Enabled:          frostCfg.Enabled,
		SupportedChains:  supportedChains,
		DefaultThreshold: frostCfg.Vault.ThresholdRatio,
		VaultCount:       int32(totalVaults),
		CommitteeSize:    int32(frostCfg.Vault.DefaultK),
		MinWithdraw:      "0.001", // TODO: 从配置读取
		Chains:           chainCfgsPb,
	}

	// 序列化为 protobuf
	respData, err := proto.Marshal(resp)
	if err != nil {
		http.Error(w, "failed to marshal response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(respData)
}

// HandleGetWithdrawStatus 获取提现状态
func (hm *HandlerManager) HandleGetWithdrawStatus(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("GetWithdrawStatus")

	withdrawID := r.URL.Query().Get("withdraw_id")
	if withdrawID == "" {
		// 兼容旧参数名
		withdrawID = r.URL.Query().Get("lot_id")
	}
	if withdrawID == "" {
		http.Error(w, "missing withdraw_id", http.StatusBadRequest)
		return
	}

	// 从 DB 读取 WithdrawState（使用统一的 key 格式）
	key := keys.KeyFrostWithdraw(withdrawID)
	data, err := hm.dbManager.Get(key)
	if err != nil || data == nil {
		http.Error(w, "withdraw not found", http.StatusNotFound)
		return
	}

	var state pb.FrostWithdrawState
	if err := proto.Unmarshal(data, &state); err != nil {
		http.Error(w, "invalid state data", http.StatusInternalServerError)
		return
	}

	resp := &pb.GetWithdrawStatusResponse{
		LotId:        state.WithdrawId,
		Chain:        state.Chain,
		Status:       state.Status,
		Amount:       state.Amount,
		Receiver:     state.To,
		Height:       state.RequestHeight,
		TxHash:       state.JobId, // 使用 job_id 作为标识
		PlanningLogs: state.PlanningLogs,
	}

	// 序列化为 protobuf
	respData, err := proto.Marshal(resp)
	if err != nil {
		http.Error(w, "failed to marshal response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(respData)
}

// HandleListWithdraws 列出提现
func (hm *HandlerManager) HandleListWithdraws(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("ListWithdraws")

	chain := r.URL.Query().Get("chain")
	status := r.URL.Query().Get("status")
	limitStr := r.URL.Query().Get("limit")

	limit := 20
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}

	// 扫描所有提现（使用统一的 key 前缀）
	// v1_frost_withdraw_ 前缀
	prefix := "v1_frost_withdraw_"

	results, err := hm.dbManager.Scan(prefix)
	if err != nil {
		http.Error(w, "scan failed", http.StatusInternalServerError)
		return
	}

	var withdraws []*pb.GetWithdrawStatusResponse
	for _, data := range results {
		var state pb.FrostWithdrawState
		if err := proto.Unmarshal(data, &state); err != nil {
			continue
		}

		// 过滤 chain
		if chain != "" && state.Chain != chain {
			continue
		}

		// 过滤 status
		if status != "" && state.Status != status {
			continue
		}

		withdraws = append(withdraws, &pb.GetWithdrawStatusResponse{
			LotId:        state.WithdrawId,
			Chain:        state.Chain,
			Status:       state.Status,
			Amount:       state.Amount,
			Receiver:     state.To,
			Height:       state.RequestHeight,
			PlanningLogs: state.PlanningLogs,
		})

		if len(withdraws) >= limit {
			break
		}
	}

	resp := &pb.ListWithdrawsResponse{
		Withdraws: withdraws,
		Total:     int32(len(withdraws)),
	}

	// 序列化为 protobuf
	respData, err := proto.Marshal(resp)
	if err != nil {
		http.Error(w, "failed to marshal response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(respData)
}

// HandleGetVaultGroupPubKey 获取 Vault 的 Group PubKey
func (hm *HandlerManager) HandleGetVaultGroupPubKey(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("GetVaultGroupPubKey")

	chain := r.URL.Query().Get("chain")
	vaultIDStr := r.URL.Query().Get("vault_id")

	if chain == "" {
		http.Error(w, "missing chain parameter", http.StatusBadRequest)
		return
	}

	vaultID, err := strconv.ParseUint(vaultIDStr, 10, 32)
	if err != nil {
		http.Error(w, "invalid vault_id", http.StatusBadRequest)
		return
	}

	// 读取 VaultState
	vaultKey := keys.KeyFrostVaultState(chain, uint32(vaultID))
	data, err := hm.dbManager.Get(vaultKey)
	if err != nil || data == nil {
		http.Error(w, "vault not found", http.StatusNotFound)
		return
	}

	var vaultState pb.FrostVaultState
	if err := proto.Unmarshal(data, &vaultState); err != nil {
		http.Error(w, "invalid vault state data", http.StatusInternalServerError)
		return
	}

	// 将 group_pubkey 转换为 hex 字符串
	groupPubkeyHex := ""
	if len(vaultState.GroupPubkey) > 0 {
		groupPubkeyHex = hex.EncodeToString(vaultState.GroupPubkey)
	}

	resp := &pb.GetVaultGroupPubKeyResponse{
		Chain:       vaultState.Chain,
		VaultId:     vaultState.VaultId,
		KeyEpoch:    vaultState.KeyEpoch,
		GroupPubkey: groupPubkeyHex,
		SignAlgo:    vaultState.SignAlgo.String(),
		Status:      vaultState.Status,
	}

	// 序列化为 protobuf
	respData, err := proto.Marshal(resp)
	if err != nil {
		http.Error(w, "failed to marshal response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(respData)
}

// HandleGetVaultTransitionStatus 获取 Vault Transition 状态
func (hm *HandlerManager) HandleGetVaultTransitionStatus(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("GetVaultTransitionStatus")

	chain := r.URL.Query().Get("chain")
	vaultIDStr := r.URL.Query().Get("vault_id")
	epochIDStr := r.URL.Query().Get("epoch_id")

	if chain == "" {
		http.Error(w, "missing chain parameter", http.StatusBadRequest)
		return
	}

	vaultID, err := strconv.ParseUint(vaultIDStr, 10, 32)
	if err != nil {
		http.Error(w, "invalid vault_id", http.StatusBadRequest)
		return
	}

	// epoch_id 可选，如果不提供则尝试获取最新的
	var epochID uint64
	if epochIDStr != "" {
		epochID, err = strconv.ParseUint(epochIDStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid epoch_id", http.StatusBadRequest)
			return
		}
	} else {
		// 如果没有提供 epoch_id，尝试从 VaultState 获取当前 key_epoch
		vaultKey := keys.KeyFrostVaultState(chain, uint32(vaultID))
		data, err := hm.dbManager.Get(vaultKey)
		if err == nil && data != nil {
			var vaultState pb.FrostVaultState
			if err := proto.Unmarshal(data, &vaultState); err == nil {
				epochID = vaultState.KeyEpoch
			}
		}
		// 如果还是 0，使用默认值 1
		if epochID == 0 {
			epochID = 1
		}
	}

	// 读取 TransitionState
	transitionKey := keys.KeyFrostVaultTransition(chain, uint32(vaultID), epochID)
	data, err := hm.dbManager.Get(transitionKey)
	if err != nil || data == nil {
		http.Error(w, "transition not found", http.StatusNotFound)
		return
	}

	var transition pb.VaultTransitionState
	if err := proto.Unmarshal(data, &transition); err != nil {
		http.Error(w, "invalid transition state data", http.StatusInternalServerError)
		return
	}

	// 转换 group_pubkey 为 hex
	oldGroupPubkeyHex := ""
	if len(transition.OldGroupPubkey) > 0 {
		oldGroupPubkeyHex = hex.EncodeToString(transition.OldGroupPubkey)
	}
	newGroupPubkeyHex := ""
	if len(transition.NewGroupPubkey) > 0 {
		newGroupPubkeyHex = hex.EncodeToString(transition.NewGroupPubkey)
	}

	resp := &pb.GetVaultTransitionStatusResponse{
		Chain:               transition.Chain,
		VaultId:             transition.VaultId,
		EpochId:             transition.EpochId,
		SignAlgo:            transition.SignAlgo.String(),
		TriggerHeight:       transition.TriggerHeight,
		OldCommitteeMembers: transition.OldCommitteeMembers,
		NewCommitteeMembers: transition.NewCommitteeMembers,
		DkgStatus:           transition.DkgStatus,
		DkgSessionId:        transition.DkgSessionId,
		DkgThresholdT:       transition.DkgThresholdT,
		DkgN:                transition.DkgN,
		DkgCommitDeadline:   transition.DkgCommitDeadline,
		DkgDisputeDeadline:  transition.DkgDisputeDeadline,
		OldGroupPubkey:      oldGroupPubkeyHex,
		NewGroupPubkey:      newGroupPubkeyHex,
		ValidationStatus:    transition.ValidationStatus,
		Lifecycle:           transition.Lifecycle,
	}

	// 序列化为 protobuf
	respData, err := proto.Marshal(resp)
	if err != nil {
		http.Error(w, "failed to marshal response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(respData)
}

// HandleGetVaultDkgCommitments 获取 Vault DKG Commitments
func (hm *HandlerManager) HandleGetVaultDkgCommitments(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("GetVaultDkgCommitments")

	chain := r.URL.Query().Get("chain")
	vaultIDStr := r.URL.Query().Get("vault_id")
	epochIDStr := r.URL.Query().Get("epoch_id")

	if chain == "" {
		http.Error(w, "missing chain parameter", http.StatusBadRequest)
		return
	}

	vaultID, err := strconv.ParseUint(vaultIDStr, 10, 32)
	if err != nil {
		http.Error(w, "invalid vault_id", http.StatusBadRequest)
		return
	}

	epochID, err := strconv.ParseUint(epochIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid epoch_id", http.StatusBadRequest)
		return
	}

	// 扫描所有 DKG commitments
	// key 格式：v1_frost_vault_dkg_commit_<chain>_<vault_id>_<epoch_id>_<participant>
	// 使用 padUint 格式化 epoch_id（20 位）
	epochIDPadded := fmt.Sprintf("%020d", epochID)
	prefix := fmt.Sprintf("v1_frost_vault_dkg_commit_%s_%d_%s_", chain, vaultID, epochIDPadded)

	results, err := hm.dbManager.Scan(prefix)
	if err != nil {
		http.Error(w, "scan failed", http.StatusInternalServerError)
		return
	}

	var commitments []*pb.DkgCommitmentInfo
	for _, data := range results {
		var commit pb.FrostVaultDkgCommitment
		if err := proto.Unmarshal(data, &commit); err != nil {
			continue
		}

		// 转换 commitment_points 为 hex 字符串数组
		pointsHex := make([]string, len(commit.CommitmentPoints))
		for i, point := range commit.CommitmentPoints {
			pointsHex[i] = hex.EncodeToString(point)
		}

		commitments = append(commitments, &pb.DkgCommitmentInfo{
			Participant:      commit.MinerAddress,
			CommitmentPoints: pointsHex,
			CommitHeight:     commit.CommitHeight,
		})
	}

	resp := &pb.GetVaultDkgCommitmentsResponse{
		Chain:       chain,
		VaultId:     uint32(vaultID),
		EpochId:     epochID,
		Commitments: commitments,
		Total:       int32(len(commitments)),
	}

	// 序列化为 protobuf
	respData, err := proto.Marshal(resp)
	if err != nil {
		http.Error(w, "failed to marshal response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(respData)
}

// HandleFrostWithdrawList 获取提现列表（供 Explorer 使用）
// 路由: /frost/withdraw/list
func (hm *HandlerManager) HandleFrostWithdrawList(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("FrostWithdrawList")

	chain := r.URL.Query().Get("chain")
	asset := r.URL.Query().Get("asset")

	// 扫描所有提现（使用统一的 key 前缀）
	prefix := "v1_frost_withdraw_"

	results, err := hm.dbManager.Scan(prefix)
	if err != nil {
		http.Error(w, "scan failed", http.StatusInternalServerError)
		return
	}

	var states []*pb.FrostWithdrawState
	for _, data := range results {
		var state pb.FrostWithdrawState
		if err := proto.Unmarshal(data, &state); err != nil {
			continue
		}

		// 过滤 chain
		if chain != "" && state.Chain != chain {
			continue
		}

		// 过滤 asset
		if asset != "" && state.Asset != asset {
			continue
		}

		states = append(states, &state)
	}

	resp := &pb.FrostWithdrawStateList{
		States: states,
	}

	// 序列化为 protobuf
	respData, err := proto.Marshal(resp)
	if err != nil {
		http.Error(w, "failed to marshal response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(respData)
}

// HandleWitnessRequests 获取上账请求列表（供 Explorer 使用）
// 路由: /witness/requests
func (hm *HandlerManager) HandleWitnessRequests(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("WitnessRequests")

	limitStr := r.URL.Query().Get("limit")
	limit := 100
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 500 {
			limit = l
		}
	}

	// 扫描所有入账请求
	// key 前缀：v1_recharge_request_
	prefix := keys.KeyRechargeRequestPrefix()

	results, err := hm.dbManager.Scan(prefix)
	if err != nil {
		http.Error(w, "scan failed", http.StatusInternalServerError)
		return
	}

	var requests []*pb.RechargeRequest
	for _, data := range results {
		var req pb.RechargeRequest
		if err := proto.Unmarshal(data, &req); err != nil {
			continue
		}

		requests = append(requests, &req)

		if len(requests) >= limit {
			break
		}
	}

	resp := &pb.RechargeRequestList{
		Requests: requests,
	}

	// 序列化为 protobuf
	respData, err := proto.Marshal(resp)
	if err != nil {
		http.Error(w, "failed to marshal response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(respData)
}

// HandleFrostDkgList 获取 DKG 会话列表（供 Explorer 使用）
// 路由: /frost/dkg/list
func (hm *HandlerManager) HandleFrostDkgList(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("FrostDkgList")

	// 扫描所有 Vault Transition 状态
	// key 前缀：v1_frost_vault_transition_
	prefix := "v1_frost_vault_transition_"

	results, err := hm.dbManager.Scan(prefix)
	if err != nil {
		http.Error(w, "scan failed", http.StatusInternalServerError)
		return
	}

	var states []*pb.VaultTransitionState
	for _, data := range results {
		var state pb.VaultTransitionState
		if err := proto.Unmarshal(data, &state); err != nil {
			continue
		}

		states = append(states, &state)
	}

	resp := &pb.VaultTransitionStateList{
		States: states,
	}

	// 序列化为 protobuf
	respData, err := proto.Marshal(resp)
	if err != nil {
		http.Error(w, "failed to marshal response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(respData)
}

// HandleDownloadSignedPackage 下载签名包
func (hm *HandlerManager) HandleDownloadSignedPackage(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("DownloadSignedPackage")

	jobID := r.URL.Query().Get("job_id")
	idxStr := r.URL.Query().Get("idx")

	if jobID == "" {
		http.Error(w, "missing job_id parameter", http.StatusBadRequest)
		return
	}

	idx := uint64(0)
	if idxStr != "" {
		var err error
		idx, err = strconv.ParseUint(idxStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid idx", http.StatusBadRequest)
			return
		}
	}

	// 读取 SignedPackage
	pkgKey := keys.KeyFrostSignedPackage(jobID, idx)
	data, err := hm.dbManager.Get(pkgKey)
	if err != nil || data == nil {
		http.Error(w, "signed package not found", http.StatusNotFound)
		return
	}

	var signedPkg pb.FrostSignedPackage
	if err := proto.Unmarshal(data, &signedPkg); err != nil {
		http.Error(w, "invalid signed package data", http.StatusInternalServerError)
		return
	}

	// 将 signed_package_bytes 转换为 hex 字符串
	signedPkgBytesHex := ""
	if len(signedPkg.SignedPackageBytes) > 0 {
		signedPkgBytesHex = hex.EncodeToString(signedPkg.SignedPackageBytes)
	}

	resp := &pb.DownloadSignedPackageResponse{
		JobId:              signedPkg.JobId,
		Idx:                signedPkg.Idx,
		SignedPackageBytes: signedPkgBytesHex,
		SubmitHeight:       signedPkg.SubmitHeight,
		Submitter:          signedPkg.Submitter,
	}

	// 序列化为 protobuf
	respData, err := proto.Marshal(resp)
	if err != nil {
		http.Error(w, "failed to marshal response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(respData)
}
