# FROST 未实现逻辑需求清单（对照 `frost/design.md` v1）

> 目的：仅保留当前代码里仍未实现的逻辑需求。
> 范围：`frost/`、`vm/`、`pb/`、`handlers/`、`config/`、`cmd/main.go`。
> 优先级约定：
> - **P0**：资金安全/共识正确性/主流程阻塞（不补齐不能进入可用状态）
> - **P1**：主功能缺失（补齐后才算完成 v1 目标）
> - **P2**：工程化/可观测性/运维体验（建议补齐）

---

## P0：资金安全 & 共识正确性（必须补齐）

- [ ] **VM 端必须确定性重算 Withdraw “队首 job 窗口”并严格按 FIFO 验收**
  - 需求：复算窗口/模板/资金占用；只接受“窗口中最靠前未签名 job”；不能信任 tx 中的 `withdraw_ids`。
  - 出处：`frost/design.md` 5.2, 5.3.1, 8.3
  - 参考：`vm/frost_withdraw_signed.go`

- [ ] **VM 端必须强制验签，VaultState 缺失/不完整时直接拒绝**
  - 需求：校验 `vault_state/group_pubkey/sign_algo`，不允许“跳过验签”。
  - 出处：`frost/design.md` 5.5.1
  - 参考：`vm/frost_withdraw_signed.go`

- [ ] **BTC 验签必须绑定真实可广播交易/输入集合**
  - 需求：按模板复算 inputs/outputs/fee/change，并对每个 input 的 sighash 验签；UTXO lock/consume 必须来自 VM 复算的输入集合。
  - 出处：`frost/design.md` 5.5, 5.5.1
  - 参考：`vm/frost_withdraw_signed.go`、`frost/chain/btc/*`

- [ ] **提现资金占用/消耗必须落在链上状态（FundsLedger）并与 job 绑定**
  - 需求：账户链 lot FIFO 消耗、BTC UTXO consumed/lock，保证幂等与防重复消耗。
  - 出处：`frost/design.md` 4.3.2, 5.5
  - 参考：`vm/frost_funds_ledger.go`

- [ ] **DKG/轮换/迁移 tx 必须进入共识入口并走 VM TxHandler**
  - 需求：把 `FrostVaultDkgCommitTx / FrostVaultDkgShareTx / FrostVaultDkgValidationSignedTx / FrostVaultTransitionSignedTx` 纳入 `AnyTx` oneof，并补齐 `vm/handlers.go`/`vm/default_handlers.go`。
  - 出处：`frost/design.md` 4.4.2, 8.3
  - 参考：`pb/data.proto`、`vm/handlers.go`、`vm/default_handlers.go`

- [ ] **DKG complaint/reveal 交易与链上裁决流程缺失**
  - 需求：新增 complaint/reveal tx、存储 key 与裁决状态机。
  - 出处：`frost/design.md` 4.4.2, 6.3.2
  - 参考：`pb/data.proto`、`vm/frost_vault_dkg_complaint.go`、`vm/frost_vault_dkg_reveal.go`

- [ ] **Frost 状态 key 命名需统一到 `keys/keys.go`**
  - 需求：避免 VM/Runtime/DB 读写 key 不一致。
  - 出处：`frost/design.md` 4.2
  - 参考：`keys/keys.go`、`vm/frost_vault_dkg_commit_handler.go`

---

## P1：v1 功能缺失（补齐后才算“按设计完成一版”）

- [ ] **提现签名流水线仍是 dummy，需要接入 ROAST/FROST**
  - 需求：用 `TemplateResult.SigHashes` 发起签名会话，产出真实 `SignedPackage`，并完整填充 `FrostWithdrawSignedTx` 关键字段。
  - 出处：`frost/design.md` 5.2.2, 5.4, 7
  - 参考：`frost/runtime/withdraw_worker.go`

- [ ] **ROAST 会话缺少聚合签名与确定性重试/换聚合者**
  - 需求：聚合 `share` 形成 `FinalSignature`；子集重试与聚合者切换需确定性。
  - 出处：`frost/design.md` 5.4.2, 5.4.3, 7.3, 7.4
  - 参考：`frost/runtime/session/roast.go`

- [ ] **Coordinator/Participant 角色与消息路由未实现**
  - 需求：选子集、收集 nonce/share、聚合签名、超时换子集/换聚合者；参与者侧 nonce 绑定与份额产出。
  - 出处：`frost/design.md` 2, 7, 8.2
  - 参考：`frost/runtime/coordinator.go`、`frost/runtime/participant.go`、`frost/runtime/net/handlers.go`

- [ ] **SessionStore 缺少会话持久化与重启恢复**
  - 需求：会话状态落盘/恢复（round、收集进度、重试次数、聚合者索引等）。
  - 出处：`frost/design.md` 8.4
  - 参考：`frost/runtime/session/store.go`、`frost/runtime/session/recovery.go`

- [ ] **JobPlanner 仍是最小版**
  - 需求：严格 FIFO 前缀、Vault 选择、资金 FIFO/UTXO 消耗、batch 规则、fee/change 确定性计算、并发窗口 `maxInFlightPerChainAsset`。
  - 出处：`frost/design.md` 5.3.1, 5.3.2, 5.3.3
  - 参考：`frost/runtime/job_planner.go`

- [ ] **ChainAdapter 未实现（BTC/EVM/SOL/TRX）**
  - 需求：`BuildWithdrawTemplate` / `PackageSigned` / `VerifySignature` 落地；TRX 若 v1 不支持需显式拒绝。
  - 出处：`frost/design.md` 3, 5.3.2, 5.3.3
  - 参考：`frost/chain/btc/*`、`frost/chain/evm/*`、`frost/chain/solana/*`、`frost/chain/tron/*`

- [ ] **多曲线验签缺失（bn128/ed25519）**
  - 需求：补齐 `frost/core/frost/api.go` 的验签实现。
  - 出处：`frost/design.md` 0, 5.5.1

- [ ] **Power Transition/DKG Runtime 仍是 dummy**
  - 需求：真实 DKG commitment/share/resolve、validation 签名与迁移 job 规划。
  - 出处：`frost/design.md` 6, 6.3, 6.4
  - 参考：`frost/runtime/transition_worker.go`

- [ ] **ROAST Core 包为空**
  - 需求：补齐 core 侧 ROAST wrapper（与 runtime 协同）。
  - 出处：`frost/design.md` 2, 3, 7
  - 参考：`frost/core/roast/roast.go`

- [ ] **Runtime Manager 未驱动实际扫描/签名流程，依赖未接入**
  - 需求：Manager 主循环驱动 scanner/workers；`cmd/main.go` 注入真实 `StateReader/TxSubmitter/Notifier/P2P/...`。
  - 出处：`frost/design.md` 2, 8.1, 8.3
  - 参考：`frost/runtime/manager.go`、`cmd/main.go`

- [ ] **ChainAdapter 接口重复/不一致**
  - 需求：统一 `frost/runtime/deps.go` 与 `frost/chain/adapter.go` 的接口定义。
  - 出处：`frost/design.md` 8.1（接口定义的单一性）

---

## P2：工程化/可观测性/运维体验（建议补齐）

- [ ] **FrostEnvelope 签名校验与防重放未落地**
  - 需求：`/frostmsg` 收包与 P2P 侧做签名校验与 seq/nonce 去重。
  - 出处：`frost/design.md` 8.2, 12.1
  - 参考：`handlers/handleFrostMsg.go`

- [ ] **对外查询/运维 API 仍是占位**
  - 需求：`/frost/config` 读真实配置；`/frost/withdraws` 前缀修正；`/frost/rescan` 触发 runtime 重扫。
  - 出处：`frost/design.md` 9.1, 9.2
  - 参考：`handlers/frost_query_handlers.go`、`handlers/frost_admin_handlers.go`

- [ ] **`config/frost_default.json` 仍为空**
  - 需求：给出 v1 的 gas/fee 默认值（按年均 300% 规则）。
  - 出处：`frost/design.md` 10

- [ ] **链名大小写统一**
  - 需求：统一入口规范（建议小写），避免 key 与配置匹配不一致。
  - 出处：设计未明确，属于实现一致性约束。
