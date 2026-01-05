# FROST 代码阅读指南（基于当前实现）

这份指南基于现有代码行为总结，重点描述运行生命周期、调用路径和关键模块。内容不以设计稿为前提。

## 0. Frost 目录责任总览（逐路径/文件）
- `frost/`：FROST 模块根目录，包含设计文档、核心算法、运行时与链适配器实现。
  - `code_guide.md`：当前阅读指南，汇总模块地图与关键调用路径。
  - `design.md`：FROST v1 设计文档，说明目标、架构、流程与数据模型。
  - `different.md`：FROST 与 ROAST 的概念差异与流程对比。
  - `dkg.md`：DKG（分布式密钥生成）流程与图示说明。
  - `requirements.md`：需求、风险点与设计约束的汇总清单。
  - `chain/`：链适配器层，负责交易模板构建、哈希绑定与签名打包。
    - `adapter.go`：定义链标识、模板结构、适配器接口与默认工厂。
    - `btc/`：BTC 适配器与模板实现。
      - `adapter.go`：构建 Taproot 提现模板、计算 sighash 并组装 raw tx。
      - `template.go`：BTC 模板结构、确定性序列化、哈希与 Taproot sighash 计算。
      - `template_test.go`：BTC 模板排序与哈希确定性的单元测试。
      - `utxo.go`：BTC UTXO 管理的占位文件（待实现）。
    - `evm/`：EVM 链（ETH/BNB）适配器实现。
      - `adapter.go`：构建合约调用模板并打包签名 calldata。
      - `contract.go`：EVM 合约交互的占位文件（待实现）。
    - `solana/`：Solana 适配器占位目录。
      - `adapter.go`：Solana 适配器占位文件（待实现）。
    - `tron/`：TRON 适配器占位目录。
      - `adapter.go`：TRON 适配器占位文件（待实现）。
  - `core/`：纯算法层，提供曲线、DKG、FROST 与 ROAST 的基础实现。
    - `curve/`：椭圆曲线抽象与实现，供签名算法复用。
      - `group.go`：定义曲线群接口与通用 Point 结构。
      - `secp256k1.go`：secp256k1 曲线实现，包含点运算与压缩点解码。
      - `bn256.go`：alt_bn128 曲线实现，封装 G1 运算与点转换。
      - `utils.go`：Taproot tweak 与大整数转换等通用工具。
    - `dkg/`：DKG 数学工具，包含多项式与拉格朗日计算。
      - `polynomial.go`：生成随机多项式并计算份额。
      - `lagrange.go`：拉格朗日插值与系数计算工具。
    - `frost/`：FROST/Schnorr 核心算法实现。
      - `api.go`：统一验签 API，按算法路由到具体实现。
      - `challenge.go`：BIP340 与 Keccak 的 challenge 计算实现。
      - `schnorr.go`：BIP340 Schnorr 签名与验签实现。
      - `threshold.go`：门限 Schnorr 部分签名与聚合签名流程。
      - `schnorr_test.go`：Schnorr 与统一验签 API 的单元测试。
    - `roast/`：ROAST 鲁棒签名算法实现。
      - `roast.go`：ROAST 子集选择、绑定系数与签名聚合逻辑。
      - `roast_test.go`：ROAST 核心算法的单元测试。
  - `runtime/`：运行时层，扫描链上队列并驱动签名/DKG 流程。
    - `deps.go`：Runtime 依赖接口与基础类型定义。
    - `manager.go`：Runtime 生命周期管理与主循环调度。
    - `manager_test.go`：Manager 与 Scanner 的单元测试。
    - `scanner.go`：扫描链上 FIFO 提现队列并定位待处理任务。
    - `job_planner.go`：生成确定性签名任务与模板参数。
    - `withdraw_worker.go`：提现流程执行器，提交 FrostWithdrawSignedTx。
    - `transition_worker.go`：DKG 轮换流程执行器，提交 commit/share/validation 交易。
    - `coordinator.go`：ROAST 协调者会话管理与签名聚合。
    - `participant.go`：签名参与者处理 nonce/份额并回复协调者。
    - `signer.go`：签名服务封装，驱动协调者并等待结果。
    - `signer_test.go`：签名服务与 ROAST 签名逻辑测试。
    - `vault_committee.go`：Top10000 矿工到 Vault 委员会的确定性分配算法。
    - `vault_committee_test.go`：委员会分配算法的单元测试。
    - `vault_committee_provider.go`：从链上读取配置并生成委员会信息。
    - `vault_committee_provider_test.go`：Vault 委员会 Provider 的单元测试。
    - `adapters/`：运行时与外部 DB/TxPool 的适配器实现。
      - `state_reader.go`：StateDB/DB 读取接口适配为 ChainStateReader。
      - `tx_submitter.go`：TxPool 提交器与测试用 Fake 实现。
      - `adapters_test.go`：StateReader 与 TxSubmitter 适配器测试。
    - `net/`：P2P FrostEnvelope 路由与消息处理。
      - `msg.go`：FrostEnvelope 路由器与反序列化分发。
      - `handlers.go`：默认消息处理器注册（占位转发）。
      - `msg_test.go`：P2P 路由器的单元测试。
    - `session/`：本地会话状态与 nonce 持久化管理。
      - `types.go`：会话状态、参与者与事件类型定义。
      - `store.go`：nonce 绑定与 SessionStore 实现。
      - `store_test.go`：SessionStore 相关单元测试。
      - `roast.go`：运行时 ROAST 会话状态机实现。
      - `roast_test.go`：运行时 ROAST 会话与恢复逻辑测试。
      - `recovery.go`：会话持久化与重启恢复逻辑。
  - `security/`：安全与防重放工具。
    - `ecies.go`：secp256k1 ECIES 加解密与 share 校验工具。
    - `ecies_test.go`：ECIES 加解密与 share 校验的单元测试。
    - `idempotency.go`：幂等检查、ID 生成与序列防重放工具。
    - `signing.go`：FrostEnvelope 签名/验签与模板哈希绑定工具。
  - `sign/`：独立实验工具目录。
    - `main.go`：BTC Taproot 交易构建与签名示例程序。

## 1. 入口与生命周期
- 实际入口：`cmd/main.go`
- 启动阶段（按代码顺序）：
  - Phase 1：创建节点、清理数据目录、初始化 DB / TxPool / Sender / Consensus / Handlers
  - Phase 2：将节点注册到各自 DB（使节点互相可发现）
  - Phase 3：启动 HTTP/3 服务（quic-go，自签证书）
  - Phase 3.5：启动共识引擎
  - Phase 3.6：若 `cfg.Frost.Enabled` 为 true，启动 FROST Runtime
  - Phase 4：启动共识查询、监控指标与进度
- 退出：SIGINT/SIGTERM 触发优雅关闭，依次停止 FROST Runtime、共识、Handler、Sender、TxPool、HTTP 服务、DB

调用图（启动生命周期）：
```mermaid
flowchart TD
  A[cmd/main.go: main] --> B[load config]
  B --> C[init nodes: DB/TxPool/Sender/Consensus/Handlers]
  C --> D[register nodes in DB]
  D --> E[start HTTP/3 servers]
  E --> F[start consensus engines]
  F --> G{cfg.Frost.Enabled}
  G -- yes --> H[start FROST runtime]
  G -- no --> I[skip FROST]
  F --> J[start consensus queries]
  J --> K[monitor metrics/progress]
  K --> L[wait SIGINT/SIGTERM]
  L --> M[shutdown all components]
```

相关文件：
- `cmd/main.go`
- `config/config.go`
- `config/frost.go`

## 2. 模块地图（定位入口）
- Runtime 编排：`frost/runtime/*`
  - `manager.go`（Runtime 主循环）
  - `scanner.go`（FIFO 队列扫描）
  - `job_planner.go`（确定性模板规划）
  - `withdraw_worker.go`（提交签名提现交易）
  - `transition_worker.go`（DKG 与 Vault 轮换）
  - `coordinator.go` / `participant.go`（ROAST 会话）
  - `session/*`（nonce 绑定与内存存储）
- 链适配器：`frost/chain/*`
  - `adapter.go`（接口 + 工厂）
  - `btc/*`（已实现）
  - `evm/*`（部分实现）
  - `tron/*`、`solana/*`（空壳）
- VM 状态机：`vm/frost_*`、`vm/frost_dkg_handlers.go`
- HTTP & P2P：`handlers/frost_*`、`handlers/handleFrostMsg.go`
- P2P 发送：`sender/doSendFrost.go`
- 状态 Key：`keys/keys.go`（FROST 区块）

## 3. FROST Runtime 生命周期（当前实现行为）
在 `cmd/main.go` 中创建 `Manager` 时只传了 `ManagerConfig{NodeID: ...}`，因此：
- `SupportedChains` 为空 => 扫描循环不执行任何链
- `Notifier` 为空 => finalized 事件回调不触发
- `TxSubmitter` / `P2P` / `SignerProvider` / `VaultProvider` 均为空

Runtime 主循环（`frost/runtime/manager.go`）：
- `Start()` 启动 `runLoop()`
- 默认每 5 秒执行 `scanAndProcess()`
- `scanAndProcess()` 依赖 `SupportedChains` 列表逐链处理

相关文件：
- `frost/runtime/manager.go`
- `frost/runtime/scanner.go`
- `frost/runtime/job_planner.go`
- `frost/runtime/withdraw_worker.go`
- `frost/runtime/adapters/state_reader.go`

## 4. 提现流程（VM + Runtime）
VM 侧：
- `FrostWithdrawRequestTxHandler` 写入 FIFO + `pb.FrostWithdrawState`
  - FIFO Keys：`KeyFrostWithdrawFIFOSeq` / `KeyFrostWithdrawFIFOHead` / `KeyFrostWithdrawFIFOIndex`
  - State Key：`KeyFrostWithdraw(withdraw_id)`
- `FrostWithdrawSignedTxHandler`：
  - 首次提交强制 FIFO 顺序
  - BTC 验签与 UTXO lock
  - 更新 Withdraw 状态为 SIGNED
  - 写入签名产物并推进 FIFO head

Runtime 侧（期望行为）：
- `Scanner.ScanOnce()` 读取 FIFO head 与 queued withdraw
- `JobPlanner.PlanJob()` 调链适配器构建模板与 job_id
- `WithdrawWorker.ProcessOnce()` 提交 `pb.FrostWithdrawSignedTx`

调用图（提现路径）：
```mermaid
flowchart LR
  W1[FrostWithdrawRequestTx] --> V1[VM: FrostWithdrawRequestTxHandler]
  V1 --> K1[StateDB: withdraw + FIFO index]
  K1 --> R1[Runtime: Scanner]
  R1 --> R2[JobPlanner -> BuildWithdrawTemplate]
  R2 --> R3[WithdrawWorker -> Submit FrostWithdrawSignedTx]
  R3 --> V2[VM: FrostWithdrawSignedTxHandler]
  V2 --> K2[StateDB: signed pkg + status + FIFO head]
```

相关文件：
- `vm/frost_withdraw_request.go`
- `vm/frost_withdraw_signed.go`
- `frost/runtime/scanner.go`
- `frost/runtime/job_planner.go`
- `frost/runtime/withdraw_worker.go`
- `keys/keys.go`

## 5. DKG 与 Vault 轮换流程
VM 侧（状态写入与校验）：
- `FrostVaultDkgCommitTxHandler`
- `FrostVaultDkgShareTxHandler`
- `FrostVaultDkgComplaintTxHandler`
- `FrostVaultDkgRevealTxHandler`
- `FrostVaultDkgValidationSignedTxHandler`
- `FrostVaultTransitionSignedTxHandler`

Runtime 侧（期望行为）：
- `TransitionWorker` 驱动 DKG：commit -> share -> key -> validation
- `DefaultVaultCommitteeProvider` 从 StateDB 读 Top10000、VaultConfig、VaultState

调用图（DKG 与切换）：
```mermaid
flowchart TD
  T1[TransitionWorker StartSession] --> T2[Submit DKG Commit Tx]
  T2 --> V1[VM: DkgCommitTxHandler]
  V1 --> T3[Submit DKG Share Tx]
  T3 --> V2[VM: DkgShareTxHandler]
  V2 --> T4[GenerateKey + Submit ValidationSigned Tx]
  T4 --> V3[VM: DkgValidationSignedTxHandler]
  V3 --> T5[VaultState KEY_READY]
  T5 --> V4[VM: VaultTransitionSignedTxHandler]
  V4 --> T6[Vault old->DRAINING, new->ACTIVE]
```

相关文件：
- `frost/runtime/transition_worker.go`
- `frost/runtime/vault_committee_provider.go`
- `vm/frost_dkg_handlers.go`
- `db/manage_frost.go`

## 6. P2P 消息路径（HTTP 入口）
实际 HTTP 路径：
- `sender.SendFrost` -> POST `https://<peer>/frostmsg`
- `handlers.HandleFrostMsg` 进行可选签名/重放校验，然后调用 `hm.frostMsgHandler`

调用图（P2P 入口）：
```mermaid
sequenceDiagram
  participant S as SenderManager
  participant H as /frostmsg handler
  participant R as FrostMsgHandler
  S->>H: POST /frostmsg (pb.FrostEnvelope)
  H->>H: optional signature/replay check
  H->>R: hm.frostMsgHandler(msg)
```

注意点：
- Runtime 内部使用 `runtime.FrostEnvelope`，HTTP 侧使用 `pb.FrostEnvelope`，两者未打通。
- `HandlerManager.SetFrostMsgHandler` 在 `cmd/main.go` 中没有被调用。

相关文件：
- `sender/doSendFrost.go`
- `handlers/handleFrostMsg.go`
- `frost/runtime/coordinator.go`
- `frost/runtime/participant.go`
- `frost/runtime/net/msg.go`

## 7. 配置与状态键
- `config/frost.go` 定义 FrostConfig，但运行时没有显式加载与注入。
- Key 定义集中在 `keys/keys.go` 的 FROST 部分，VM 与 Runtime 共享。

相关文件：
- `config/frost.go`
- `keys/keys.go`

## 8. 现有集成缺口（阅读时要特别注意）
- `cmd/main.go` 未使用 `DefaultManagerConfig`，导致 `SupportedChains` 为空。
- `TxSubmitter` 为 nil，`WithdrawWorker` 无法提交交易。
- `TxPoolSubmitter` 需要 `AddTx` / `Broadcast` 接口，但 `txpool.TxPool` 未实现这些方法。
- 只注册了 BTC 适配器，EVM 未注册，TRON/SOL 是空实现。
- P2P 处理链路未打通（runtime handler 未接入）。

## 9. 建议阅读顺序
1) `cmd/main.go`（全局生命周期）
2) `handlers/manager.go`、`handlers/handleFrostMsg.go`（HTTP 入口）
3) `vm/default_handlers.go` + `vm/frost_*`（链上状态变化）
4) `frost/runtime/manager.go` -> `scanner.go` -> `job_planner.go` -> `withdraw_worker.go`
5) `frost/chain/*`（模板构建）
6) `frost/runtime/coordinator.go`、`participant.go`（ROAST 会话）
7) `frost/runtime/transition_worker.go` + `vault_committee_provider.go`
8) `keys/keys.go` + `db/manage_frost.go`
