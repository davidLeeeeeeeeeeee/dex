# FROST 模块实现状态分析

根据 `frost/design.md` 设计文档，对比当前工程代码实现，以下是需要完成的工作清单。

## 一、已完成部分 ✅

### 1. 基础架构
- ✅ Runtime Manager、Scanner、WithdrawWorker、TransitionWorker 框架
- ✅ Coordinator/Participant（ROAST 协调者和参与者）
- ✅ SessionStore（会话持久化）
- ✅ Core 层：FROST 签名、DKG、ROAST wrapper、曲线适配器
- ✅ Chain Adapters：BTC/EVM/SOL/TRX 适配器框架
- ✅ 配置结构：FrostConfig 定义和默认值

### 2. VM Handlers 框架
- ✅ FrostWithdrawRequestTxHandler
- ✅ FrostWithdrawSignedTxHandler（框架）
- ✅ FrostVaultDkgCommitTxHandler
- ✅ FrostVaultDkgShareTxHandler
- ✅ FrostVaultDkgComplaintTxHandler
- ✅ FrostVaultDkgRevealTxHandler
- ✅ FrostVaultDkgValidationSignedTxHandler
- ✅ FrostVaultTransitionSignedTxHandler

### 3. HTTP API 与 UI 可视化
- ✅ GetFrostConfig
- ✅ GetWithdrawStatus
- ✅ ListWithdraws
- ✅ **Explorer 增强**: 
  - ✅ 新增 `/api/frost/withdraw/queue`
  - ✅ 新增 `/api/witness/requests`
  - ✅ 新增 `/api/frost/dkg/list`
  - ✅ 前端 `FrostDashboard` (提现队列、上账流、DKG 时间轴)
  - ✅ 交易详情增强渲染 (`TxTypeRenderer`)

---

## 二、待完成工作 🔨

### 1. VaultConfig/VaultState 链上初始化和管理

**设计要求**（§4.3.0）：
- 每条链一个 `VaultConfig`（vault_count、committee_size、threshold_ratio、sign_algo 等）
- 每个 Vault 一个 `VaultState`（vault_ref、group_pubkey、committee_members、key_epoch、lifecycle）

**当前状态**：
- ✅ Proto 定义已存在（`pb.FrostVaultConfig`、`pb.FrostVaultState`）
- ✅ Key 函数已定义（`keys.KeyFrostVaultConfig`、`keys.KeyFrostVaultState`）
- ❌ **缺失**：VaultConfig 的链上初始化逻辑（系统启动时或治理交易创建）
- ❌ **缺失**：VaultState 的创建和更新逻辑（DKG 完成后写入 group_pubkey、key_epoch 递增）

**需要实现**：
```go
// vm/frost_vault_init.go（新文件）
// 1. 系统启动时或治理交易创建 VaultConfig
// 2. DKG 完成后更新 VaultState（group_pubkey、key_epoch、lifecycle）
```

---

### 2. FundsLedger 完整实现（按 Vault 分片）

**设计要求**（§4.3.2）：
- BTC：每个 Vault 独立的 UTXO 集合（`v1_frost_btc_utxo_<vault_id>_<txid>_<vout>`）
- 账户链：每个 Vault 独立的 lot FIFO（`v1_frost_funds_lot_<chain>_<asset>_<vault_id>_<height>_<seq>`）
- 资金消耗标记（consumed/spent）

**当前状态**：
- ✅ Key 函数已定义（`keys.KeyFrostBtcUtxo`、`keys.KeyFrostFundsLotIndex` 等）
- ❌ **缺失**：BTC UTXO 的按 Vault 分片存储和管理
- ❌ **缺失**：账户链 lot 的按 Vault 分片 FIFO 实现
- ❌ **缺失**：资金消耗标记逻辑（FrostWithdrawSignedTx 接受时标记为 consumed/spent）

**需要实现**：
```go
// vm/frost_funds_ledger.go（新文件）
// 1. BTC UTXO 按 vault_id 隔离存储
// 2. 账户链 lot 按 vault_id 分片 FIFO
// 3. 资金消耗标记（withdraw 签名完成后）
```

---

### 3. VM Handlers 签名验证

**设计要求**（§5.5.1）：
- `FrostWithdrawSignedTx` 和 `FrostVaultTransitionSignedTx` 必须验证聚合签名
- 按 `sign_algo` 分支验证（BTC=BIP-340、ETH/BNB=bn128、SOL=ed25519）

**当前状态**：
- ✅ Handler 框架已存在
- ❌ **缺失**：签名验证逻辑（防止无效签名锁死资金）

**需要实现**：
```go
// vm/frost_withdraw_signed.go（补充）
func (h *FrostWithdrawSignedTxHandler) DryRun(...) {
    // 1. 从 VaultState 读取 group_pubkey、sign_algo
    // 2. 从 template_hash 派生 msg
    // 3. 按 sign_algo 分支验证签名
    // 4. 验证失败直接拒绝 tx
}
```

---

### 4. JobPlanner 完整实现

**设计要求**（§5.3）：
- Vault 选择：按 vault_id 升序遍历 ACTIVE Vault，选出能覆盖提现的第一个
- BTC 规划：支持多 input、多 output（1 个 input 支付 N 个 withdraw）
- 资金 FIFO 消耗：按 lot/UTXO 先入先出

**当前状态**：
- ✅ 基础框架已实现（`frost/runtime/job_planner.go`）
- ❌ **缺失**：Vault 选择逻辑（`selectVault` 目前返回固定值）
- ❌ **缺失**：BTC 多 input/output 规划算法
- ❌ **缺失**：资金 FIFO 消耗逻辑（从 FundsLedger 读取并消耗）

**需要实现**：
```go
// frost/runtime/job_planner.go（完善）
func (p *JobPlanner) selectVault(chain string, amount uint64) (vaultID uint32, keyEpoch uint64, err error) {
    // 1. 遍历所有 ACTIVE Vault（按 vault_id 升序）
    // 2. 检查该 Vault 的 available_balance 或 UTXO 集合
    // 3. 选出第一个能覆盖 amount 的 Vault
}

// frost/runtime/job_window_planner.go（完善 BTC 规划）
func (p *JobWindowPlanner) planBTCJob(...) {
    // 1. 从队首开始收集连续 withdraw（最多 max_outputs）
    // 2. 按 confirm_height 升序选择 UTXO（贪心装箱）
    // 3. 支持 1 个 input 支付 N 个 output
}
```

---

### 5. TransitionWorker DKG 触发检测

**设计要求**（§6.1、§6.3）：
- 按 Vault 独立检测触发条件（change_ratio >= threshold）
- VaultTransitionState 的创建和状态推进
- DKG 剔除后的确定性规则（n/t 更新、qualified_set 维护）

**当前状态**：
- ✅ TransitionWorker 框架已实现
- ❌ **缺失**：触发条件检测逻辑（扫描 Top10000 变化，计算 change_ratio）
- ❌ **缺失**：VaultTransitionState 的创建（触发时创建新的 transition state）
- ❌ **缺失**：DKG 剔除后的确定性规则（qualified_set、disqualified_set 维护）

**需要实现**：
```go
// frost/runtime/transition_worker.go（补充）
func (w *TransitionWorker) CheckTriggerConditions(ctx context.Context) {
    // 1. 扫描各 Vault 的委员会变化
    // 2. 计算 change_ratio（EWMA 加权平均）
    // 3. 达到阈值时创建 VaultTransitionState
}

// vm/frost_vault_dkg_reveal.go（补充）
// DKG 剔除后的状态更新：
// - qualified_set.remove(被剔除者)
// - disqualified_set.add(被剔除者)
// - 检查 current_n >= initial_t，否则标记 FAILED
```

---

### 6. 迁移 Job 规划

**设计要求**（§6.4）：
- 合约链：生成 `updatePubkey(new_pubkey, vault_id, epoch_id)` 模板
- BTC：生成该 Vault 的 sweep 交易（多 UTXO → 新地址）

**当前状态**：
- ✅ TransitionWorker 框架已实现
- ❌ **缺失**：迁移 Job 规划逻辑（扫描该 Vault 的资金，生成迁移模板）

**需要实现**：
```go
// frost/runtime/transition_worker.go（补充）
func (w *TransitionWorker) PlanMigrationJobs(ctx context.Context, chain string, vaultID uint32, epochID uint64) {
    // 1. 扫描该 Vault 的 FundsLedger（BTC UTXO 或账户链余额）
    // 2. 生成 MigrationJob 模板（合约链=updatePubkey，BTC=sweep）
    // 3. 启动 ROAST 签名会话
}
```

---

### 7. HTTP API 完整实现

**设计要求**（§9.1）：
- `GetVaultGroupPubKey(chain, vault_id, epoch_id)`
- `GetVaultTransitionStatus(chain, vault_id, epoch_id)`
- `GetVaultDkgCommitment(chain, vault_id, epoch_id, dealer_id)`
- `ListVaults(chain)`

**当前状态**：
- ✅ 部分查询接口已实现（GetFrostConfig、GetWithdrawStatus）
- ❌ **缺失**：Vault 相关查询接口

**需要实现**：
```go
// handlers/frost_query_handlers.go（补充）
func (hm *HandlerManager) HandleGetVaultGroupPubKey(...)
func (hm *HandlerManager) HandleGetVaultTransitionStatus(...)
func (hm *HandlerManager) HandleGetVaultDkgCommitment(...)
func (hm *HandlerManager) HandleListVaults(...)
```

---

### 8. ROAST 聚合者切换

**设计要求**（§5.4.3、§7.4）：
- 确定性聚合者切换算法（基于 session_id、key_epoch、区块高度）
- 超时自动切换（`aggregatorRotateBlocks` 超时窗口）

**当前状态**：
- ✅ ROAST 框架已实现
- ❌ **缺失**：聚合者切换逻辑（确定性序列计算、超时检测）

**需要实现**：
```go
// frost/runtime/coordinator.go（补充）
func (c *Coordinator) getCurrentAggregator(sessionID string, keyEpoch uint64, nowHeight uint64) NodeID {
    // 1. seed = H(session_id || key_epoch || "frost_agg")
    // 2. committee_list = BitmapToList(committee)
    // 3. agg_candidates = Permute(committee_list, seed)
    // 4. agg_index = floor((now_height - session_start_height) / agg_timeout_blocks) % len(agg_candidates)
    // 5. 返回 agg_candidates[agg_index]
}
```

---

### 9. Nonce 安全防护

**设计要求**（§12.2）：
- 同一 nonce commitment（R_i）只能用于一个 msg
- 防止二次签名攻击（恶意协调者诱导签名不同消息）
- `share_sent` 状态必须持久化

**当前状态**：
- ✅ SessionStore 框架已实现
- ❌ **缺失**：nonce 一次性绑定机制（msg_bound 检查）

**需要实现**：
```go
// frost/runtime/participant.go（补充）
func (p *Participant) ProduceSigShare(session_id, task_id string, R_agg Point, msg []byte) error {
    nonce := p.sessionStore.GetNonce(session_id, task_id)
    if nonce.share_sent {
        if !bytes.Equal(nonce.msg_bound, msg) {
            // 检测到二次签名攻击，拒绝
            return ErrDuplicateShareDifferentMsg
        }
    }
    // 首次产出 share
    nonce.msg_bound = msg
    nonce.share_sent = true
    p.sessionStore.SaveNonce(session_id, task_id, nonce) // 持久化
}
```

---

### 10. Witness 集成 Vault 分配

**设计要求**（§4.3.2）：
- `WitnessRequestTx` 必须包含 `vault_id`（或 `deposit_address`）
- 入账时按 Vault 分片写入 Pending Lot 和 Finalized Lot

**当前状态**：
- ✅ 设计文档提到已实现于 `vm/witness_handler.go`
- ⚠️ **需要确认**：WitnessRequestTx 是否包含 vault_id，入账逻辑是否按 Vault 分片

**需要检查**：
```go
// vm/witness_handler.go（检查）
// 1. WitnessRequestTx 是否包含 vault_id 字段
// 2. allocateVaultID 函数是否正确实现
// 3. Pending Lot 和 Finalized Lot 的 key 是否包含 vault_id
```

---

## 三、优先级建议

### 高优先级（核心功能）
1. **VM Handlers 签名验证**（防止资金锁死）
2. **FundsLedger 完整实现**（资金管理基础）
3. **JobPlanner 完整实现**（提现流程核心）
4. **VaultConfig/VaultState 初始化**（系统启动必需）

### 中优先级（功能完善）
5. **TransitionWorker DKG 触发检测**（轮换功能）
6. **ROAST 聚合者切换**（鲁棒性）
7. **Nonce 安全防护**（安全性）

### 低优先级（辅助功能）
8. **迁移 Job 规划**（轮换完成）
9. **HTTP API 完整实现**（查询便利）
10. **Witness 集成确认**（入账流程）

---

## 四、实现建议

### 1. 分阶段实现
- **Phase 1**：核心提现流程（FundsLedger + JobPlanner + 签名验证）
- **Phase 2**：Vault 初始化和管理（VaultConfig/VaultState）
- **Phase 3**：轮换功能（DKG 触发 + 迁移规划）
- **Phase 4**：安全加固（Nonce 防护 + 聚合者切换）
- **Phase 5**：API 完善（查询接口）

### 2. 测试重点
- 签名验证的正确性（防止无效签名）
- 资金分片的隔离性（跨 Vault 不混用）
- 确定性规划的一致性（所有节点计算结果相同）
- DKG 剔除后的状态一致性（qualified_set 维护）

---

## 五、参考文件

- 设计文档：`frost/design.md`
- 需求文档：`frost/requirements.md`
- 当前实现：
  - Runtime：`frost/runtime/`
  - VM Handlers：`vm/frost_*.go`
  - Core：`frost/core/`
  - Chain Adapters：`frost/chain/`
