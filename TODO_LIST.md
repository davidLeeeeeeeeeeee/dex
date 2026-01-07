# FROST 模块 TODO 清单

基于 `frost/design.md` 设计文档与现有代码对比，以下是未实现或不完善的功能清单。

---

## 📊 完成状态总览

### ✅ 已完成的高优先级任务（2025-01-05 更新）

1. **✅ VaultConfig/VaultState 链上初始化和管理**
   - 已创建 `vm/frost_vault_init.go`，包含 `InitVaultConfig`、`InitVaultStates`、`UpdateVaultStateAfterDKG`
   - 已在 `FrostVaultDkgValidationSignedTxHandler` 中实现 DKG 完成后的 VaultState 更新
   - Vault 委员会分组算法已在 `frost/runtime/committee/vault_committee.go` 中实现

2. **✅ FundsLedger 完整实现（按 Vault 分片）**
   - 已在 `vm/witness_events.go` 中实现账户链 lot 的按 Vault 分片写入
   - 已在 `vm/frost_withdraw_signed.go` 中实现资金消耗标记（账户链 lot 消耗和 BTC UTXO 锁定）
   - FundsLedger FIFO 头指针更新逻辑已在 `vm/frost_funds_ledger.go` 中实现

3. **✅ VM Handlers 签名验证（防止资金锁死）**
   - 已在 `vm/frost_withdraw_signed.go` 中完善多曲线签名验证（BIP340、BN128、Ed25519）
   - 已在 `vm/frost_vault_transition_signed_handler.go` 中添加多曲线签名验证
   - 已在 `vm/frost_vault_dkg_validation_signed_handler.go` 中完善多曲线签名验证支持

4. **✅ JobPlanner 完整实现（确定性规划）**
   - 已在 `frost/runtime/planning/job_planner.go` 和 `job_window_planner.go` 中实现 `selectVault` 逻辑（遍历 ACTIVE Vault，检查余额）
   - 已在 `frost/runtime/planning/job_window_planner.go` 中实现 BTC 多 input/output 规划算法（贪心装箱）
   - 已实现资金 FIFO 消耗逻辑和余额检查（`calculateVaultAvailableBalance`、`calculateBTCBalance`、`calculateAccountChainBalance`）

### ⚠️ 部分完成的任务

- **FundsLedger BTC UTXO 存储**：已区分 BTC 和账户链处理，但 BTC UTXO 写入逻辑需要从 RechargeRequest 解析 txid/vout（需要扩展数据结构或从外部获取）

---

## 一、核心功能缺失（高优先级）

### 1. VaultConfig/VaultState 链上初始化和管理 ⚠️

**设计文档要求**（§4.3.0）：
- 每条链一个 `VaultConfig`（vault_count、committee_size、threshold_ratio、sign_algo、deposit_allocation_rule 等）
- 每个 Vault 一个 `VaultState`（vault_ref、sign_algo、committee_members、key_epoch、group_pubkey、lifecycle）

**当前状态**：
- ✅ Proto 定义已存在（`pb.FrostVaultConfig`、`pb.FrostVaultState`）
- ✅ Key 函数已定义（`keys.KeyFrostVaultConfig`、`keys.KeyFrostVaultState`）
- ✅ **已完成**：VaultConfig 的链上初始化逻辑（`vm/frost_vault_init.go` 中的 `InitVaultConfig`）
- ✅ **已完成**：VaultState 的创建和更新逻辑（`vm/frost_vault_init.go` 中的 `InitVaultStates` 和 `UpdateVaultStateAfterDKG`）
- ✅ **已完成**：在 `FrostVaultDkgValidationSignedTxHandler` 中更新 VaultState（group_pubkey、key_epoch、committee_members）
- ✅ **已完成**：Vault 委员会分组算法（`frost/runtime/committee/vault_committee.go`）：`seed = H(epoch_id || chain)`，确定性洗牌并切分

**需要实现**：
- [x] 创建 `vm/frost_vault_init.go`：系统启动时或治理交易创建 VaultConfig ✅
- [x] 在 `FrostVaultDkgValidationSignedTxHandler` 中：DKG 完成后更新 VaultState（group_pubkey、key_epoch、lifecycle） ✅
- [x] 实现 Vault 委员会分组算法（`frost/runtime/committee/vault_committee.go`）：`seed = H(epoch_id || chain)`，确定性洗牌并切分 ✅

---

### 2. FundsLedger 完整实现（按 Vault 分片）⚠️

**设计文档要求**（§4.3.2）：
- BTC：每个 Vault 独立的 UTXO 集合（`v1_frost_btc_utxo_<vault_id>_<txid>_<vout>`）
- 账户链：每个 Vault 独立的 lot FIFO（`v1_frost_funds_lot_<chain>_<asset>_<vault_id>_<height>_<seq>`）
- 资金消耗标记（consumed/spent）

**当前状态**：
- ✅ Key 函数已定义（`keys.KeyFrostBtcUtxo`、`keys.KeyFrostFundsLotIndex` 等）
- ✅ **已完成**：账户链 lot 的按 Vault 分片 FIFO 实现（`vm/witness_events.go` 中的 `applyRechargeFinalized` 已按 vault_id 写入）
- ✅ **已完成**：资金消耗标记逻辑（`vm/frost_withdraw_signed.go` 中已实现账户链 lot 消耗和 BTC UTXO 锁定）
- ✅ **已完成**：FundsLedger 的 FIFO 头指针维护（`vm/frost_funds_ledger.go` 中已实现）
- ⚠️ **部分完成**：BTC UTXO 的按 Vault 分片存储（`vm/witness_events.go` 中已区分 BTC 和账户链处理，但 BTC UTXO 写入逻辑需要从 RechargeRequest 解析 txid/vout）

**需要实现**：
- [x] 在 `vm/witness_handler.go` 中：入账时按 vault_id 写入 UTXO 或 lot（使用 `KeyFrostBtcUtxo` 或 `KeyFrostFundsLotIndex`）✅（账户链已完成，BTC 需要完善）
- [x] 在 `vm/frost_withdraw_signed.go` 中：签名完成后标记资金为 consumed/spent（UTXO 锁定、lot 标记）✅
- [x] 实现 FundsLedger FIFO 头指针更新逻辑 ✅

---

### 3. VM Handlers 签名验证（防止资金锁死）⚠️

**设计文档要求**（§5.5.1）：
- `FrostWithdrawSignedTx`、`FrostVaultTransitionSignedTx`、`FrostVaultDkgValidationSignedTx` 必须验证聚合签名
- 按 `sign_algo` 分支验证：
  - `SCHNORR_SECP256K1_BIP340`（BTC）：验证每个 input 的签名
  - `SCHNORR_ALT_BN128`（ETH/BNB）：验证 bn128 Schnorr 签名
  - `ED25519`（SOL）：验证 ed25519 签名
  - `ECDSA_SECP256K1`（TRX）：验证 ECDSA 签名（需 GG20/CGGMP）

**当前状态**：
- ✅ Handler 框架已存在
- ✅ BTC 签名验证已实现（`vm/frost_withdraw_signed.go` 中支持 BIP340）
- ✅ **已完成**：ETH/BNB/SOL 的签名验证逻辑（BN128 和 Ed25519 已在所有 handler 中实现）
- ✅ **已完成**：`FrostVaultTransitionSignedTx` 的签名验证（支持 BIP340、BN128、Ed25519）
- ✅ **已完成**：`FrostVaultDkgValidationSignedTx` 的完整签名验证（支持 BIP340、BN128、Ed25519）

**需要实现**：
- [x] 在 `vm/frost_withdraw_signed.go` 中：完善合约链/账户链的签名验证（bn128、ed25519）✅
- [x] 在 `vm/frost_vault_transition_signed_handler.go` 中：添加签名验证逻辑 ✅
- [x] 在 `vm/frost_vault_dkg_validation_signed_handler.go` 中：完善多曲线签名验证支持 ✅

---

### 4. JobPlanner 完整实现（确定性规划）⚠️

**设计文档要求**（§5.3）：
- Vault 选择：按 vault_id 升序遍历 ACTIVE Vault，选出能覆盖提现的第一个
- BTC 规划：支持多 input、多 output（1 个 input 支付 N 个 withdraw，减少签名压力）
- 资金 FIFO 消耗：按 lot/UTXO 先入先出（`finalize_height + seq` 递增顺序）
- 确定性规划：所有节点计算结果必须一致

**当前状态**：
- ✅ 基础框架已实现（`frost/runtime/planning/job_planner.go`）
- ✅ **已完成**：Vault 选择逻辑（`selectVault` 已实现遍历 ACTIVE Vault 并检查余额）
- ✅ **已完成**：BTC 多 input/output 规划算法（`planBTCJobWindow` 和 `planBTCJob` 实现贪心装箱，1 个 input 支付 N 个 withdraw）
- ✅ **已完成**：资金 FIFO 消耗逻辑（`vm/frost_withdraw_signed.go` 中已实现账户链 lot 消耗，`calculateVaultAvailableBalance` 实现余额检查）
- ⚠️ **部分完成**：合约链 batch 规划（基础框架存在，可扩展支持批量）

**需要实现**：
- [x] 在 `frost/runtime/planning/job_planner.go` 中：实现 `selectVault` 逻辑（遍历 ACTIVE Vault，检查余额/UTXO）✅
- [x] 在 `frost/runtime/planning/job_window_planner.go` 中：实现 BTC 多 input/output 规划算法 ✅
- [x] 实现资金 FIFO 消耗逻辑（从 FundsLedger 按 Vault 分片读取并消耗）✅

---

### 5. CompositeJob（跨 Vault 组合支付）✅

**设计文档要求**（§5.1.2）：
- 当队首提现无法由单个 Vault 覆盖时，启用跨 Vault 组合模式（仅限合约链/账户链）
- CompositeJob 结构：包含多个 SubJob，每个对应一个 Vault 的部分支付
- BTC 不支持跨 Vault 组合（每个 Vault 是独立 Taproot 地址）

**当前状态**：
- ✅ **已完成**：CompositeJob 的实现

**需要实现**：
- [x] 在 `frost/runtime/planning/job_window_planner.go` 中：检测队首大额提现，触发 CompositeJob 规划 ✅
- [x] 定义 CompositeJob 结构（包含 sub_jobs[]）✅
- [x] 实现组合规划算法（按 vault_id 升序累加可用余额直至覆盖总额）✅
- [x] 在 `WithdrawWorker` 中：支持 CompositeJob 的并发签名（每个 SubJob 独立 ROAST）✅

---

## 二、轮换功能缺失（中优先级）

### 6. TransitionWorker DKG 触发检测 ✅

**设计文档要求**（§6.1、§6.3）：
- 按 Vault 独立检测触发条件（change_ratio >= transitionTriggerRatio，默认 0.2）
- 固定边界：轮换只在 `epochBlocks` 边界生效
- EWMA 加权平均：过滤短期波动
- VaultTransitionState 的创建和状态推进

**当前状态**：
- ✅ TransitionWorker 框架已实现（`frost/runtime/workers/transition_worker.go`）
- ✅ `CheckTriggerConditions` 方法框架存在
- ✅ **已完成**：完整的触发条件检测逻辑（EWMA 加权平均、固定边界检查）
- ✅ **已完成**：VaultTransitionState 的创建（触发时通过 VM 交易创建）
- ✅ **已完成**：DKG 剔除后的确定性规则（qualified_set、disqualified_set 维护）

**需要实现**：
- [x] 在 `frost/runtime/workers/transition_worker.go` 中：完善 `CheckTriggerConditions`（EWMA、固定边界）✅
- [x] 创建 VM 交易类型 `FrostVaultTransitionTriggerTx`（或通过现有机制创建 VaultTransitionState）✅
- [x] 在 `vm/frost_dkg_handlers.go` 中：实现 DKG 剔除后的状态更新（qualified_set、disqualified_set、n/t 更新）✅

---

### 7. DKG 投诉裁决完整流程 ✅

**设计文档要求**（§6.3.2）：
- 恶意投诉处理：投诉者被惩罚并剔除，dealer 的 share 已泄露，仅 dealer 重新生成多项式
- Dealer 作恶处理：dealer 被剔除，其他参与者直接排除该 dealer 的贡献，无需重新生成
- DKG 重启规则：当 `current_n < initial_t` 时，必须重启 DKG

**当前状态**：
- ✅ Handler 框架已存在（`FrostVaultDkgComplaintTxHandler`、`FrostVaultDkgRevealTxHandler`）
- ✅ **已完成**：完整的投诉裁决逻辑（恶意投诉 vs dealer 作恶的区分处理）
- ✅ **已完成**：DKG 剔除后的 qualified_set 维护
- ✅ **已完成**：DKG 重启规则（当 qualified_count < threshold 时）

**需要实现**：
- [x] 在 `vm/frost_dkg_handlers.go` 中：完善 `FrostVaultDkgRevealTxHandler` 的裁决逻辑 ✅
- [x] 实现恶意投诉处理（剔除投诉者，清空 dealer commitment，仅 dealer 重做）✅
- [x] 实现 dealer 作恶处理（剔除 dealer，其他参与者继续，无需重做）✅
- [x] 实现 DKG 重启规则（检查 `current_n >= initial_t`，否则标记 FAILED）✅

---

### 8. 迁移 Job 规划 ✅

**设计文档要求**（§6.4）：
- 合约链：生成 `updatePubkey(new_pubkey, vault_id, epoch_id)` 模板
- BTC：生成该 Vault 的 sweep 交易（多 UTXO → 新地址）
- 迁移完成判定：VM 依据 FundsLedger 判定该 Vault 旧 key 资产已全部覆盖/消耗

**当前状态**：
- ✅ TransitionWorker 框架已实现
- ✅ `PlanMigrationJobs` 方法框架存在（`frost/runtime/workers/transition_worker.go`）
- ✅ **已完成**：完整的迁移 Job 规划逻辑（扫描资金、生成模板、启动 ROAST）

**需要实现**：
- [x] 在 `frost/runtime/workers/transition_worker.go` 中：完善 `planBTCMigrationJobs`（扫描 UTXO，生成 sweep 模板）✅
- [x] 完善 `planContractMigrationJobs`（生成 updatePubkey 模板）✅
- [x] 在 VM 中：实现迁移完成判定逻辑（检查该 Vault 旧 key 资产是否全部消耗）✅

---

### 9. Vault 生命周期管理 ✅

**设计文档要求**（§4.3.0、§6.2.1）：
- `ACTIVE`：正常运行，可接收充值、可提现
- `DRAINING`：排空中，停止新充值（witness 不再分配该 Vault 的地址），现有资金继续提现或迁移
- `RETIRED`：已退役，资金全部迁移完成，该 Vault 不再使用

**当前状态**：
- ✅ 常量定义已存在（`vm/frost_vault_transition_signed.go`）
- ✅ **已完成**：lifecycle 状态转换逻辑（ACTIVE → DRAINING → RETIRED）
- ✅ **已完成**：witness 入账时检查 Vault lifecycle（DRAINING 时不再分配）

**需要实现**：
- [x] 在 `vm/frost_vault_transition_signed_handler.go` 中：实现 lifecycle 状态转换 ✅
- [x] 在 `vm/witness_handler.go` 中：入账时检查 Vault lifecycle，DRAINING 时拒绝分配 ✅

---

## 三、ROAST 功能完善（中优先级）

### 10. ROAST 聚合者切换（确定性 + 超时）✅

**设计文档要求**（§5.4.3、§7.4）：
- 确定性聚合者切换算法：`seed = H(session_id || key_epoch || "frost_agg")`，确定性排列委员会
- 超时自动切换：`agg_index = floor((now_height - session_start_height) / agg_timeout_blocks) % len(agg_candidates)`
- 参与者仅接受当前 `agg_index` 对应协调者的请求

**当前状态**：
- ✅ ROAST 框架已实现（`frost/runtime/roast/coordinator.go`）
- ✅ `computeCoordinatorIndex` 方法存在
- ✅ **已完成**：超时切换逻辑（基于区块高度）
- ✅ **已完成**：参与者端对聚合者切换的验证（仅接受当前聚合者的请求）

**需要实现**：
- [x] 在 `frost/runtime/roast/coordinator.go` 中：完善超时切换逻辑（基于区块高度）✅
- [x] 在 `frost/runtime/roast/participant.go` 中：添加聚合者验证（拒绝非当前聚合者的请求）✅

---

### 11. ROAST 子集重试和部分完成 ✅

**设计文档要求**（§5.4.2）：
- 允许"某些 task 已完成签名、少数 task 因掉线未完成"的情况
- 协调者可对未完成 task 继续向新子集收集 share
- 已完成 task 的签名保持不变（不需要推倒重来）

**当前状态**：
- ✅ ROAST 框架已实现
- ✅ **已完成**：task 级别的部分完成逻辑

**需要实现**：
- [x] 在 `frost/runtime/roast/coordinator.go` 中：实现 task 级别的状态跟踪（每个 task 的 need_shares/collected/done）✅
- [x] 实现部分完成逻辑（对未完成 task 继续收集 share）✅

---

### 12. Nonce 安全防护（防二次签名攻击）✅

**设计文档要求**（§12.2）：
- 同一 nonce commitment（R_i）只能用于一个 msg
- 防止二次签名攻击（恶意协调者诱导签名不同消息）
- `share_sent` 状态必须持久化

**当前状态**：
- ✅ SessionStore 框架已实现（`frost/runtime/session/store.go`）
- ✅ **已完成**：nonce 一次性绑定机制（msg_bound 检查）

**需要实现**：
- [x] 在 `frost/runtime/session/store.go` 中：添加 `NonceState` 结构（包含 `msg_bound`、`share_sent` 字段）✅
- [x] 在 `frost/runtime/roast/participant.go` 中：实现 `ProduceSigShare` 的 msg_bound 检查逻辑 ✅
- [x] 确保 `share_sent = true` 持久化后才发送 share ✅

---

## 四、签名算法支持（中优先级）

### 13. 多曲线签名算法支持 ⚠️

**设计文档要求**（§0、§7.1）：
- `SCHNORR_SECP256K1_BIP340`（BTC）：FROST-secp256k1
- `SCHNORR_ALT_BN128`（ETH/BNB）：FROST-bn128
- `ED25519`（SOL）：FROST-Ed25519
- `ECDSA_SECP256K1`（TRX）：GG20/CGGMP（非 FROST，v1 暂不支持）

**当前状态**：
- ✅ Core 层曲线适配器框架已存在（`frost/core/curve/`）
- ✅ secp256k1 实现已存在
- ⚠️ **不完善**：bn128、ed25519 的实现可能不完整
- ❌ **缺失**：TRX 的 ECDSA 门限方案（GG20/CGGMP）

**需要实现**：
- [ ] 检查并完善 `frost/core/curve/bn256.go`（ETH/BNB 支持）
- [ ] 检查并完善 `frost/core/curve/` 中的 ed25519 支持
- [ ] 在 DKG 和 ROAST 中：确保所有参与者使用相同的 `sign_algo`（校验逻辑）

---

## 五、VM Handlers 完善（中优先级）

### 14. FrostWithdrawSignedTx 确定性重算 Job 窗口 ⚠️

**设计文档要求**（§4.4.1、§5.2.2）：
- VM 必须基于链上状态 + 配置，确定性重算“队首 job 窗口”（最多 `maxInFlightPerChainAsset` 个）
- 若该 `job_id` 尚不存在：仅当 tx 的 `job_id` 等于窗口中**当前最靠前的未签名 job**才接受
- 若 job 已存在：只追加 receipt/history，不再改变状态

**当前状态**：
- ✅ Handler 框架已存在（`vm/frost_withdraw_signed.go`）
- ⚠️ **不完善**：确定性重算 Job 窗口的逻辑可能不完整

**需要实现**：
- [ ] 在 `vm/frost_withdraw_signed.go` 中：实现确定性重算 Job 窗口逻辑（与 Runtime JobPlanner 算法一致）
- [ ] 验证 job_id 是否等于窗口中最靠前的未签名 job

---

### 15. FrostVaultDkgValidationSignedTx 完整实现 ⚠️

**设计文档要求**（§4.4.2、§6.3.1）：
- VM 重算 `new_group_pubkey == Σ a_i0(qualified_dealers)`
- 验证 `validation_msg_hash == H("frost_vault_dkg_validation" || chain || vault_id || epoch_id || sign_algo || new_group_pubkey)`
- 验证签名有效后，写入 `new_group_pubkey`，并将 `validation_status=Passed`、`dkg_status=KeyReady`

**当前状态**：
- ✅ Handler 框架已存在（`vm/frost_vault_dkg_validation_signed_handler.go`）
- ⚠️ **不完善**：`new_group_pubkey` 的重算逻辑可能不完整（需要聚合所有 qualified_dealers 的 a_i0）

**需要实现**：
- [ ] 在 `vm/frost_vault_dkg_validation_signed_handler.go` 中：实现 `new_group_pubkey` 的重算逻辑（聚合所有 qualified_dealers 的 a_i0）
- [ ] 确保验证通过后更新 VaultState（group_pubkey、key_epoch 递增）

---

## 六、外部接口（低优先级）

### 16. HTTP RPC/API 完整实现 ⚠️

**设计文档要求**（§9.1、§9.2）：
- `GetVaultGroupPubKey(chain, vault_id, epoch_id)`
- `GetVaultTransitionStatus(chain, vault_id, epoch_id)`
- `GetVaultDkgCommitment(chain, vault_id, epoch_id, dealer_id)`
- `ListVaultDkgComplaints(chain, vault_id, epoch_id, from, limit)`
- `ListVaults(chain)`
- `GetHealth()`、`GetSession(job_id)`、`ForceRescan()`、`Metrics()`

**当前状态**：
- ✅ 部分查询接口已实现（`GetFrostConfig`、`GetWithdrawStatus`、`ListWithdraws`）
- ❌ **缺失**：Vault 相关查询接口
- ❌ **缺失**：运维/调试类接口

**需要实现**：
- [ ] 在 `handlers/` 中：实现所有 Vault 相关查询接口
- [ ] 实现运维/调试类接口（`GetHealth`、`GetSession`、`ForceRescan`、`Metrics`）

---

## 七、配置和初始化（低优先级）

### 17. 配置文件完整性和验证 ⚠️

**设计文档要求**（§10）：
- 配置文件应包含所有必需字段（committee、vault、timeouts、withdraw、transition、chains）
- 配置验证逻辑（确保参数合法性）

**当前状态**：
- ✅ 配置文件已存在（`config/frost_default.json`）
- ⚠️ **需要检查**：配置是否完整，是否有验证逻辑

**需要实现**：
- [ ] 检查配置文件是否包含所有必需字段
- [ ] 添加配置验证逻辑（参数合法性检查）

---

### 18. 系统启动时的 Vault 初始化 ⚠️

**设计文档要求**（§4.3.0）：
- 系统启动时，为每条链创建 VaultConfig 和初始 VaultState
- 初始 Vault 的 committee 从 Top10000 确定性分配

**当前状态**：
- ❌ **缺失**：系统启动时的 Vault 初始化逻辑

**需要实现**：
- [ ] 在系统启动流程中：读取配置文件，为每条链创建 VaultConfig
- [ ] 为每个 Vault 创建初始 VaultState（committee、初始 group_pubkey 等）

---

## 八、测试和文档（低优先级）

### 19. 单元测试和集成测试 ⚠️

**需要实现**：
- [ ] 为所有新增功能添加单元测试
- [ ] 添加集成测试（端到端提现流程、DKG 流程）
- [ ] 添加确定性测试（确保所有节点计算结果一致）

---

### 20. 文档完善 ⚠️

**需要实现**：
- [ ] 更新 `FROST_IMPLEMENTATION_STATUS.md`（反映当前实现状态）
- [ ] 添加 API 文档（所有 RPC 接口的详细说明）
- [ ] 添加运维文档（如何监控、如何排查问题）

---

## 九、优先级总结

### 高优先级（核心功能，必须实现）✅ 全部完成（2025-01-05）
1. ✅ **VaultConfig/VaultState 链上初始化和管理** - **已完成**（2025-01-05）
2. ✅ **FundsLedger 完整实现（按 Vault 分片）** - **已完成**（2025-01-05）
3. ✅ **VM Handlers 签名验证（防止资金锁死）** - **已完成**（2025-01-05）
4. ✅ **JobPlanner 完整实现（确定性规划）** - **已完成**（2025-01-05）

### 中优先级（功能完善，建议实现）
5. **CompositeJob（跨 Vault 组合支付）**
6. **TransitionWorker DKG 触发检测**
7. **DKG 投诉裁决完整流程**
8. **迁移 Job 规划**
9. **Vault 生命周期管理**
10. **ROAST 聚合者切换（确定性 + 超时）**
11. **ROAST 子集重试和部分完成**
12. **Nonce 安全防护（防二次签名攻击）**
13. **多曲线签名算法支持**

### 低优先级（辅助功能，可选实现）
14. **FrostWithdrawSignedTx 确定性重算 Job 窗口**
15. **FrostVaultDkgValidationSignedTx 完整实现**
16. **HTTP RPC/API 完整实现**
17. **配置文件完整性和验证**
18. **系统启动时的 Vault 初始化**
19. **单元测试和集成测试**
20. **文档完善**

---

## 十、实现建议

### 分阶段实现
- **Phase 1（核心提现）**：FundsLedger + JobPlanner + 签名验证 + Vault 初始化
- **Phase 2（轮换功能）**：DKG 触发 + 投诉裁决 + 迁移规划
- **Phase 3（安全加固）**：Nonce 防护 + 聚合者切换 + 多曲线支持
- **Phase 4（完善功能）**：CompositeJob + API + 测试

### 测试重点
- 签名验证的正确性（防止无效签名锁死资金）
- 资金分片的隔离性（跨 Vault 不混用）
- 确定性规划的一致性（所有节点计算结果相同）
- DKG 剔除后的状态一致性（qualified_set 维护）

---

## 参考文件

- 设计文档：`frost/design.md`
- 需求文档：`frost/requirements.md`
- 当前实现状态：`FROST_IMPLEMENTATION_STATUS.md`
- 代码位置：
  - Runtime：`frost/runtime/`
  - VM Handlers：`vm/frost_*.go`
  - Core：`frost/core/`
  - Chain Adapters：`frost/chain/`
