# FROST 实现缺口 TODO（对照 `frost/design.md` v1）

> 目的：把当前仓库里 FROST 相关实现与 `frost/design.md` 的目标/约束逐条对照，列出仍未实现、仍为占位或与设计不一致的缺口项。
>
> 范围：`frost/`、`vm/`、`pb/`、`handlers/`、`config/`、`cmd/main.go`。
>
> 优先级约定：
> - **P0**：资金安全/共识正确性/主流程阻塞（不补齐不能进入可用状态）
> - **P1**：主功能缺失（补齐后才算完成 v1 目标）
> - **P2**：工程化/可观测性/运维体验（建议补齐）

---

## P0：资金安全 & 共识正确性（必须补齐）

- [ ] **VM 端必须确定性重算 Withdraw “队首 job 窗口”，严格按 FIFO 验收 `FrostWithdrawSignedTx`**
  - 现状：
    - `vm/frost_withdraw_signed.go` 直接使用交易里自带的 `withdraw_ids` 批量置 `SIGNED`，并将 FIFO head 推到 `maxSeq+1`；没有复算“队首窗口”、没有校验“连续前缀”、也没有“只允许窗口内最靠前未签名 job 上链”的约束。
  - 设计要求（概要）：
    - VM 处理 `FrostWithdrawSignedTx` 时，必须基于链上状态+配置确定性重算窗口与模板（包括资金占用/UTXO 锁/模板 hash），不一致直接拒绝。
  - 需要补齐：
    - 定义/落库 `SigningJob`（或等价链上结构）及其 `template_hash`、`vault_id`、`key_epoch`、输入集合（BTC UTXO / 合约链的 nonce/withdraw_id 去重信息等）。
    - 复算窗口：对每个 `(chain, asset)` 独立，从 FIFO head 开始拿连续 `QUEUED` withdraw，按确定性规则规划最多 `maxInFlightPerChainAsset` 个 job；只接受“窗口中当前最靠前未签名 job”的提交。
  - 验收标准：
    - 任意节点对同一高度状态复算得到的窗口/模板 hash 一致。
    - 伪造/跳过队首/打乱顺序/塞入不连续 withdraw 的 `FrostWithdrawSignedTx` 必须被拒绝。

- [ ] **VM 端必须强制验签：不能在 VaultState 缺失/不完整时“跳过验签”**
  - 现状：
    - `vm/frost_withdraw_signed.go` 在读不到 VaultState 时会“跳过验签”（代码注释明确写了测试环境/生产应报错，但未落实）。
  - 需要补齐：
    - 明确 VM 对 `vault_state` 缺失、`group_pubkey` 长度错误、`sign_algo` 不匹配等情况的处理：一律拒绝（除非明确的 dev 模式/测试开关，并且不可在生产打开）。
  - 验收标准：
    - 没有合法 VaultState 的任何签名产物都无法上链推进状态。

- [ ] **BTC 签名验收必须绑定到“真实可广播交易/输入集合”，不能只验 `template_hash`**
  - 现状：
    - `vm/frost_withdraw_signed.go`（BTC 分支）假设 `signed_package_bytes` 前 64 字节是签名，并对 `template_hash` 做 BIP340 验签；不校验签名是否对应每个 input 的 sighash，也不校验 `utxo_inputs` 与实际签名内容一致。
  - 需要补齐：
    - VM 侧验收逻辑应至少做到：模板数据可复算（inputs/outputs/fee/change），并对每个 input 的 sighash 做验签（或对可解析的 raw tx/witness 做一致性验证）。
    - `utxo_inputs` 必须与模板一致，且 lock/consume 必须由 VM 复算的输入集合驱动，而不是相信交易入参。

- [ ] **提现资金占用/消耗必须落在链上状态（FundsLedger），并与签名 job 绑定**
  - 现状：
    - `vm/frost_withdraw_signed.go` 只做了 BTC 的 UTXO lock（且输入来源不可信），没有对账户链的余额/FIFO lot 做消耗与防重复。
    - 资金 FIFO（按 `vault_id` 分片的 lot/UTXO）虽然有 key 设计与 helper（`vm/frost_funds_ledger.go`），但提现 handler 未使用。
  - 需要补齐：
    - VM 在首次接受某 job 的 `FrostWithdrawSignedTx` 时：
      - 账户链：把对应 lot/FIFO 视为 consumed（并保证同一 lot 不能被两个 job 消耗）。
      - BTC：对 UTXO 做锁定并视为已花费（至少在链内标记 consumed），并能重放/幂等。

- [ ] **把 DKG/轮换/迁移相关交易纳入共识输入（`pb.AnyTx`）并接入 VM 处理链路**
  - 现状：
    - `pb/data.proto` 已定义 `FrostVaultDkgCommitTx / FrostVaultDkgShareTx / FrostVaultDkgValidationSignedTx / FrostVaultTransitionSignedTx` 等消息，但 `AnyTx` oneof 里没有它们（因此共识/txpool 无法按统一交易入口承载）。
    - VM 默认 handler 注册只包含 `FrostWithdrawRequestTx` 和 `FrostWithdrawSignedTx`：`vm/default_handlers.go`。
    - 现有 DKG 相关逻辑主要是 `Executor` 上的 `HandleFrostVault*` 方法（`vm/frost_vault_*_handler.go`），并非 `TxHandler` 风格，且 key 命名不统一。
  - 需要补齐：
    - 在 `pb.AnyTx` 中加入上述 DKG/迁移交易类型，并补齐 `vm/handlers.go` 的 kind 映射与 `vm/default_handlers.go` 注册。
    - 为这些交易实现 `TxHandler.DryRun(...)`（基于 `StateView` 写集/回执），或明确替代方案并打通 `Executor.PreExecuteBlock` 执行路径。

- [ ] **DKG 争议/裁决链上流程缺失（complaint/reveal）**
  - 现状：
    - `vm/frost_vault_dkg_complaint.go`、`vm/frost_vault_dkg_reveal.go` 为空文件。
    - `pb/data.proto` 也没有对应的 complaint/reveal 消息。
  - 需要补齐：
    - 按 `frost/requirements.md` 的“dealer 公开 share 明文与 r 以便链上验证裁决”的思路，补齐 complaint/reveal 交易、存储 key、状态机推进与裁决规则。

- [ ] **关键命名/Key 规则需要统一，否则同一功能读写会对不上**
  - 现状：
    - `keys/keys.go` 已定义 `KeyFrostVaultState(...)` 为 `v1_frost_vault_state_<chain>_<vault>`，且 epoch 使用 pad；但 VM 的 DKG handler 里又自行拼了 `v1_frost_vault_<chain>_<vault>`/`v1_frost_vault_transition_<chain>_<vault>_<epoch>` 等 key（与 `keys/keys.go` 不一致）。
  - 需要补齐：
    - 统一所有 Frost 状态 key 的来源（建议全部走 `keys/keys.go`），并加测试确保 VM/Runtime/Handler 读写一致。

---

## P1：v1 功能缺失（补齐后才算“按设计完成一版”）

### 1) Runtime：提现签名流水线（Scanner → Planner → ROAST/FROST → 回写 tx）

- [ ] **WithdrawWorker 目前是 dummy 签名，需要接入真实 ROAST/FROST 会话**
  - 现状：`frost/runtime/withdraw_worker.go` 把 `signed_package_bytes` 写成 `dummy+template_hash`，未做任何门限签名。
  - 需要补齐：
    - 从 `JobPlanner` 输出的 `TemplateResult.SigHashes`（或等价消息哈希集合）发起签名会话，产出真实 `SignedPackage`。
    - 填充 `FrostWithdrawSignedTx` 的关键字段：`vault_id/key_epoch/template_hash/utxo_inputs/chain/asset/withdraw_ids`（当前只填了 `job_id/signed_package_bytes/withdraw_ids`）。

- [ ] **ROAST 协议实现不完整：会话内缺少“聚合签名产物”逻辑**
  - 现状：`frost/runtime/session/roast.go` 在收集够 shares 后只把状态置 `COMPLETE`，标注 `// TODO: 聚合签名`，没有 `FinalSignature` 产出。
  - 需要补齐：
    - 定义 share 的格式（按 `pb.SignAlgo`/曲线区分），并在聚合端实现校验与聚合。
    - 失败重试：子集重试与“超时切换聚合者”需按设计具备确定性（所有节点能独立计算切换序列）。

- [ ] **Coordinator/Participant 角色尚未实现**
  - 现状：`frost/runtime/coordinator.go`、`frost/runtime/participant.go` 为空文件。
  - 需要补齐：
    - Coordinator：选子集、收集 nonce/share、聚合签名、超时换子集/换聚合者、提交回写 tx。
    - Participant：生成 nonce 承诺与签名份额、做 nonce 绑定、验证聚合者广播的 R 集合一致性等。

- [ ] **SessionStore 目前只做 nonce 绑定，缺少“会话持久化 + 重启恢复”**
  - 现状：
    - `frost/runtime/session/store.go` 只实现了 nonce→msgHash 绑定（且未与签名流程打通）。
    - `frost/runtime/session/recovery.go` 为空文件。
  - 需要补齐：
    - 会话级状态（当前 round、已收集 nonce/share、当前聚合者、重试次数、消息哈希等）落本地持久化（至少可选的落盘实现）。
    - 重启后能恢复进行中的 session 并继续（或明确回滚策略/重新发起策略）。

### 2) Runtime：模板规划（JobPlanner）

- [ ] **JobPlanner 仍是最小版：没有 batch、没有资金 FIFO 消耗、没有 Vault 选择**
  - 现状：`frost/runtime/job_planner.go` 仅支持“单 withdraw → 单 job”，且 `selectVault(...)` 固定返回 `(vault_id=0, key_epoch=1)`。
  - 设计要求（概要）：
    - 严格 FIFO（队首连续前缀）、资金按 lot/UTXO FIFO 消耗、尽量少 job（可 batch 的链应 batch）、并发窗口最多 `maxInFlightPerChainAsset`。
  - 需要补齐：
    - 读取/使用链上 `FundsLedger`（按 vault 分片）与 `VaultState/VaultConfig`，确定性选择可覆盖提现的 Vault。
    - BTC：确定性 UTXO 选择（按 confirmed_height + txid:vout），并计算 fee/change（来自配置）。
    - 账户链：lot FIFO 消耗与 batch 规则下沉到链策略/配置。

### 3) Chain Adapters：BTC/EVM/SOL/TRX（v1 链支持）

- [ ] **BTC ChainAdapter 未实现**
  - 现状：
    - `frost/chain/btc/template.go` 已有模板序列化与 BIP341 sighash 计算，但 `frost/chain/btc/adapter.go` 为空文件。
    - `frost/chain/btc/utxo.go` 为空文件（UTXO 管理/选择未落地）。
  - 需要补齐：
    - `BuildWithdrawTemplate(...)`：产出 `TemplateData`、`TemplateHash`、每个 input 的 `SigHashes`。
    - `PackageSigned(...)`：将 signatures 组装成可广播 raw tx（或规范化交易包），并能被 VM 复验。
    - `VerifySignature(...)`：VM 侧验签使用（至少支持 BIP340）。

- [ ] **EVM/SOL/TRX ChainAdapter 未实现**
  - 现状：`frost/chain/evm/{adapter.go,contract.go}`、`frost/chain/solana/adapter.go`、`frost/chain/tron/adapter.go` 均为空文件。
  - 需要补齐：
    - EVM：合约 call data 模板化、template_hash 规范化序列化、签名验证规则（bn128 Schnorr）与 `PackageSigned`。
    - SOL：交易模板与 ed25519 签名路径。
    - TRX：设计明确“非 FROST，需要 GG20/CGGMP，v1 可暂不支持”；至少需要在代码中明确返回 `unsupported` 并在配置/路由层禁用，避免误用。

### 4) Crypto：多曲线验签/签名支持不足

- [ ] **通用验签 `frost/core/frost/api.go` 未实现 bn128 与 ed25519**
  - 现状：`Verify(...)` 对 `SCHNORR_ALT_BN128` 与 `ED25519` 直接返回 `ErrUnsupportedSignAlgo`。
  - 影响：
    - VM 的 `FrostVaultDkgValidationSignedTx`/提现验签目前实际只能覆盖 BTC（BIP340），ETH/BNB/SOL 路径无法按设计“必须验签”。

### 5) Runtime 管理器与依赖注入未打通

- [ ] **`cmd/main.go` 启动 FROST Runtime 时依赖全是 nil，等于没接入系统**
  - 现状：`cmd/main.go` 创建 `frostrt.NewManager(...)` 时，`StateReader/TxSubmitter/Notifier/P2P/...` 全为 nil。
  - 需要补齐：
    - StateReader：基于 DB/StateDB 的只读最终化视图（可复用 `frost/runtime/adapters/state_reader.go`，但需要确认接口与 db.Manager 匹配）。
    - TxSubmitter：对接真实 `txpool.TxPool` 的提交接口（当前 `frost/runtime/adapters/tx_submitter.go` 定义的 TxPool 接口与实际 `txpool` 不匹配，需要重做/适配）。
    - Notifier：订阅 finalized 高度事件，用于唤醒扫描/推进。
    - P2P：把 `/frostmsg` 的收包链路路由到 `frost/runtime/net.Router`，并实现出站发送。

- [ ] **接口重复/不一致：`frost/runtime/deps.go` 与 `frost/chain/adapter.go` 存在两套 ChainAdapter 定义**
  - 现状：
    - JobPlanner 使用的是 `dex/frost/chain.ChainAdapterFactory`。
    - `frost/runtime/deps.go` 又定义了一套 `ChainAdapter/ChainAdapterFactory`（方法签名不同）。
  - 需要补齐：
    - 统一为单一接口来源，避免 runtime 与 chain 包长期分叉。

---

## P2：工程化/可观测性/运维体验（建议补齐）

### 1) P2P 消息安全与防重放

- [ ] **FrostEnvelope 的签名校验与防重放未落地**
  - 现状：
    - `handlers/handleFrostMsg.go` 明确“暂不做签名校验”。
    - `pb.FrostEnvelope` 有 `sig/seq` 字段，但缺少全链路校验/去重策略。
  - 需要补齐：
    - 根据链上/DB 可获得的 miner 公钥，校验 `sig`；引入 `seq/nonce` 去重与过期策略。

### 2) 对外查询/运维 API 仍是占位

- [ ] **`/frost/config` 返回硬编码**
  - 现状：`handlers/frost_query_handlers.go` 里写了 `// TODO: 从配置/DB 读取真实值`，返回固定数组与阈值。
  - 需要补齐：
    - 从 `config`/链上 `v1_frost_cfg`/VaultConfig 读取并返回真实配置与支持链列表。

- [ ] **`/frost/withdraws` 扫描前缀疑似不匹配实际 key**
  - 现状：使用 `v1_frost_lot_` 前缀扫描，但提现状态 key 实际是 `v1_frost_withdraw_...`（见 `keys/keys.go`）。
  - 需要补齐：
    - 统一 key 前缀与 API 查询逻辑，补齐分页/过滤能力，并提供签名产物 history 查询（按 `job_id`）。

- [ ] **`/frost/rescan` 未真正触发重扫**
  - 现状：`handlers/frost_admin_handlers.go` 只是返回 “rescan triggered”，没有与 runtime scanner/manager 交互。
  - 需要补齐：
    - 暴露可控的触发机制（如向 runtime 发信号/设置重扫高度/刷新窗口）。

### 3) 配置文件与默认值

- [ ] **`config/frost_default.json` 为空，未体现“按年均 300% 写死 gas/手续费”的 v1 约束**
  - 需要补齐：
    - 给出可落地的默认配置样例（BTC sat/vB、EVM gasPrice/gasLimit、SOL priority fee、TRX feeLimit），并在文档/代码里明确来源与更新策略。

### 4) 兼容性/一致性（链名大小写、序列化规范）

- [ ] **链名大小写在多个模块混用（例如 `"btc"` vs `"BTC"`）**
  - 影响：key 生成、条件分支（如 VM BTC 验签分支）与配置查找可能出现不一致。
  - 需要补齐：统一规范（建议统一小写或统一枚举），并在入口处标准化。

### 5) 测试补齐（建议）

- [ ] **增加 VM 侧“确定性复算 + 拒绝非法提交”的单元测试/集成测试**
  - 覆盖点：跳过队首、非连续前缀、伪造 withdraw_ids、伪造 utxo_inputs、错误 template_hash、VaultState 缺失等。

- [ ] **增加 ROAST session：重试/换聚合者/聚合签名产物的测试**

- [ ] **增加 ChainAdapter：template_hash 确定性、PackageSigned 可解析/可复验的测试**

---

## 备注（当前已存在但需要确认/补齐的实现点）

- `vm/witness_handler.go` 已实现 “H(request_id) % vault_count” 的 `vault_id` 确定性分配；需要与 Vault lifecycle（ACTIVE/DRAINING/RETIRED）联动，确保 DRAINING 不再分配新入账。
- `vm/frost_funds_ledger.go` 已提供 lot head/seq helper，但尚未被提现验收逻辑使用。
- `frost/runtime/vault_committee.go` 已实现 Top10000 → Vault 委员会的确定性分配（洗牌+切分），需要接入链上 Top10000 真实数据与 VaultConfig。

