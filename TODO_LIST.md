# TODO_LIST — 落地 `frost/design.md`（从整体到细节）

> 目标：按 `frost/design.md` 的设计，把 FROST 做成“独立于共识”的跨链资产执行引擎，严格按**链上最终化队列**执行：
> 1) Withdraw Queue（提现）
> 2) Power Transition / Key Rotation Queue（权力交接/密钥轮换）
>
> 使用方式：按顺序逐项勾选 `[ ] → [x]`；建议每一大步做成一个小 PR（更容易 review/回滚/定位问题）。
>
> 目录边界（与现有工程对齐，避免循环依赖）：
> - 纯密码学：`frost/core/...`
> - Runtime：`frost/runtime/...`
> - 链适配器：`frost/chain/...`
> - VM TxHandlers：放在 `vm/...`（不要新建 `frost/vmhandler`）
> - HTTP API：放在 `handlers/...`（复用现有路由/Server）
> - 配置：放在 `config/...`（并入现有 `config.Config`）

---

## 里程碑（建议）

- **M0**：目录与编译通过（不改变行为）
- **M1**：Frost 相关 protobuf/keys/VM handler（WithdrawRequest）就绪
- **M2**：Withdraw（单链 + 单 Vault）端到端：入账 → 队列 → 模板 → 签名产物上链
- **M3**：ROAST（子集重试 + 聚合者切换 + 会话恢复）可用
- **M4**：多链适配（BTC + EVM + SOL），TRX 标记为 v1 not supported
- **M5**：Vault 轮换：DKG commit/share/裁决/validation 上链闭环
- **M6**：Vault 迁移 job + 签名产物上链，VM 判定迁移完成并切换 `key_epoch`
- **M7**：外部查询/运维 API、指标、关键安全约束全部落地

---

## Phase 0：基线与一致性（低风险）

- [ ] 0.1 跑一次全量测试：`go test ./...`（记录当前失败用例，避免后续“误修”）
- [ ] 0.2 统一术语/命名：确定 `chain` 值（`btc|eth|bnb|sol|trx`）与 `asset` 表达（native vs token address）
- [ ] 0.3（可选）把 `frost/design.md` 中仍指向 `frost/vmhandler` 的内容同步到实际目录（只改文档）
- [ ] 0.4 明确 v1 范围：TRX 仅保留配置与接口，门限签名实现先返回 `ErrNotSupported`

验收：
- [ ] Phase 0 完成后：不改任何业务行为，测试结果与 0.1 一致

---

## Phase 1：目录落地与现有 `frost/` 代码迁移（仅重构）

目标：把现有 `frost/` 下的 DKG/曲线/阈值签名实验代码迁到 `frost/core`，保持“纯算法不依赖网络/DB”。

- [ ] 1.1 新建目录骨架：
  - `frost/core/{curve,dkg,frost,roast}`
  - `frost/runtime/{session,net}`
  - `frost/chain/{btc,evm,solana,tron}`
- [ ] 1.2 迁移现有代码到 core：
  - `frost/dkg.go` → `frost/core/dkg/`（或拆成 `dkg/` + `schnorr_threshold/`）
  - `frost/group.go`、`frost/utils.go` → `frost/core/curve/`（或 `frost/core/`）
  - `frost/bn256_adapter.go`、`frost/secp2561_adapter.go` → `frost/core/curve/`
  - `frost/dkg_test.go` → 对应 core 子目录测试
- [ ] 1.3 处理 package 命名与 import（例如从 `package dkg` 变成 `package dkg` 但路径变了）
- [ ] 1.4 保留/移动 `frost/sign/main.go`：
  - 作为调试工具：建议移到 `cmd/frost_sign_demo/`（避免污染 `frost` 库包）
  - 或至少在 TODO 中标记为 “demo code”

验收：
- [ ] `go test ./...` 通过（或至少不比 Phase 0 更差）

---

## Phase 2：Protobuf（链上 tx 与状态对象的最小集合）

目标：让 “Withdraw / VaultTransition / SignedPackage” 这些对象能通过 `pb.AnyTx` 进入 VM 并落 StateDB。

- [ ] 2.1 在 `pb/data.proto` 增加 Frost 相关消息（先最小闭环，后续扩展）：
  - `FrostWithdrawRequestTx`
  - `FrostWithdrawSignedTx`
  - `FrostVaultDkgCommitTx`
  - `FrostVaultDkgShareTx`
  - `FrostVaultDkgComplaintTx`
  - `FrostVaultDkgRevealTx`
  - `FrostVaultDkgValidationSignedTx`
  - `FrostVaultTransitionSignedTx`
- [ ] 2.2 在 `pb/data.proto` 增加 Frost 相关状态对象（用于 StateDB value）：
  - `FrostWithdrawRequest`、`FrostSigningJob`
  - `FrostSignedPackage`（或复用 bytes + metadata）
  - `FrostVaultConfig`、`FrostVaultState`
  - `FrostVaultTransitionState`、`FrostVaultDkgCommitment`
- [ ] 2.3 把新增 tx 加进 `AnyTx.oneof`（分配新的字段编号，避免破坏已有编号）
- [ ] 2.4 更新 `pb/anytx_ext.go`：让 `AnyTx.GetBase()` 覆盖新增 tx（否则 txpool/vm 无法取 tx_id）
- [ ] 2.5 重新生成 `pb/data.pb.go`：
  - 运行：`protoc --go_out=. --go_opt=paths=source_relative pb/data.proto`
  - 确认生成文件可编译

验收：
- [ ] `go test ./pb -run TestNonExistent`（至少 `go test ./...` 能编译通过）

---

## Phase 3：Keys（补齐 `v1_frost_*` keyspace）

目标：所有 Frost 链上状态都能通过 `keys/keys.go` 统一生成 key（保持你工程既有风格）。

- [ ] 3.1 在 `keys/keys.go` 增加全局配置相关 key：
  - `v1_frost_cfg`
  - `v1_frost_top10000_<height>`（bitmap snapshot）
- [ ] 3.2 增加 Vault 配置与状态 key：
  - `v1_frost_vault_cfg_<chain>`
  - `v1_frost_vault_<chain>_<vault_id>`
  - `v1_frost_vault_transition_<chain>_<vault_id>_<epoch_id>`
- [ ] 3.3 增加 DKG 存储 key（按参与者拆分）：
  - `v1_frost_vault_dkg_commit_<chain>_<vault_id>_<epoch_id>_<dealer_id>`
  - `v1_frost_vault_dkg_share_<chain>_<vault_id>_<epoch_id>_<dealer_id>_<receiver_id>`
  - `v1_frost_vault_dkg_complaint_<chain>_<vault_id>_<epoch_id>_<idx>`
- [ ] 3.4 增加签名产物 receipt/history key（append-only）：
  - `v1_frost_signed_pkg_<job_id>_<idx>`
  -（如果你需要按 vault 分片索引：增加 `v1_frost_signed_pkg_<vault_id>_<job_id>_<idx>`，二选一但要全局统一）
- [ ] 3.5（可选）为 key 生成函数补单测（纯字符串断言，成本低、收益高）

验收：
- [ ] `go test ./keys`

---

## Phase 4：VM 集成（先做 Withdraw，再做 Transition）

目标：VM 只负责“验证 + 写状态机”，不跑签名；Runtime 只负责离链签名协作。

### 4A. VM 路由支持（基础设施）

- [ ] 4.1 更新 `vm/handlers.go` 的 `DefaultKindFn`：识别新增 Frost tx 类型并返回 kind 字符串
- [ ] 4.2 更新 `vm/default_handlers.go`：注册 Frost TxHandlers（顺序无关，但建议按业务分组）

验收：
- [ ] `go test ./vm -run TestNonExistent`（至少能编译）

### 4B. Withdraw（链上状态机）

- [ ] 4.3 新增 `vm/frost_withdraw_request_handler.go`
  - 行为：创建 `WithdrawRequest{status=QUEUED}`，分配 `(chain,asset)` 队列 `seq`，写 FIFO index
  - 要求：幂等（重复 tx_id 不应造成 seq 重复增长）
- [ ] 4.4 新增 `vm/frost_withdraw_signed_handler.go`（先做最小校验版本）
  - 行为：校验 `job_id/template_hash`，把 `signed_package_bytes` 追加到 receipt/history，并把 withdraw 标记为 `SIGNED`
  - 先不做复杂的“VM 重算模板”，先把接口与状态流跑通（后续 Phase 8/9 再把验证补齐）
- [ ] 4.5 为 4.3/4.4 各写 1~2 个单测（核心是：FIFO/幂等/状态转移）

验收：
- [ ] `go test ./vm -run FrostWithdraw`

### 4C. FundsLedger（链上资金账本校验 helper）

你目前已经在 `vm/witness_handler.go` 与 `vm/witness_events.go` 写入了：
- pending lot：`KeyFrostFundsPendingLot*`
- finalized lot：`KeyFrostFundsLotIndex` + `KeyFrostFundsLotSeq`

接下来补齐“消费/锁/头指针/UTXO（BTC）”：

- [ ] 4.6 新增 `vm/frost_funds_ledger.go`（helper + 小工具函数）
- [ ] 4.7 增加并使用 `KeyFrostFundsLotHead`：提现/迁移消费成功后推进 head
- [ ] 4.8（BTC）补齐 UTXO key 写入与锁：
  - 入账时（witness finalized）如果 `chain==btc`，写 `KeyFrostBtcUtxo`
  - 在 `FrostWithdrawSignedTx` 通过时写 `KeyFrostBtcLockedUtxo`（lock(utxo)->job_id）

验收：
- [ ] `go test ./vm -run FrostFunds`

---

## Phase 5：Runtime 依赖注入与生命周期（先跑起来）

目标：先把 runtime “能启动/能扫描/能提交 tx” 搭好，再逐步补签名逻辑。

- [ ] 5.1 新增 `frost/runtime/deps.go`：按 `frost/design.md §8.1` 定义接口
- [ ] 5.2 写一层适配器把现有工程对象接进来：
  - `ChainStateReader`：基于 `db.Manager.StateDB`（或 VM 提供的只读接口）
  - `FinalityNotifier`：基于 `consensus.Node.events` 订阅 `types.EventBlockFinalized`
  - `P2P`：基于 `interfaces.Transport`（先只定义 send/receive 的最小封装）
  - `TxSubmitter`：基于 `txpool.TxPool.SubmitTx + senderManager.BroadcastTx`（或复用 `/tx` handler）
- [ ] 5.3 新增 `frost/runtime/manager.go`：Start/Stop + goroutine 管理
- [ ] 5.4 新增 `frost/runtime/scanner.go`：收到 finalized 事件/定时触发时扫描任务
- [ ] 5.5 新增 `frost/runtime/session/store.go`：本地会话存储（先实现 in-memory，下一步再落盘）
- [ ] 5.6 在 `cmd/main.go` 的 NodeInstance 启动流程中挂载 runtime（可配置开关）

验收：
- [ ] 本地启动节点后，runtime 能打印“收到 finalized height / scanner tick”日志（不要求签名）

---

## Phase 6：P2P 消息（FrostEnvelope + MsgFrost）

目标：复用现有 `Transport`，新增一种消息类型用于 FROST/ROAST 的两轮交互。

- [ ] 6.1 在 `types/message.go` 增加 `MsgFrost`
- [ ] 6.2 定义承载结构（二选一，推荐 protobuf）：
  - A) 在 `pb/data.proto` 增加 `FrostEnvelope` 消息；网络层直接传 protobuf bytes
  - B) 在 `frost/runtime/net/msg.go` 定义 Go struct + json（简单但跨语言/版本管理更差）
- [ ] 6.3 RealTransport 支持 MsgFrost：
  - 发送侧：`consensus/realTransport.go` 增加 case 分支，走 senderManager 新增的 `SendFrost(...)`
  - 接收侧：在 `handlers/` 增加一个 HTTP endpoint（例如 `/frostmsg`），把消息解包后 `EnqueueReceivedMessage`
- [ ] 6.4 sender 层补齐 `doSendFrost.go`（对端 endpoint + Content-Type）
- [ ] 6.5 在 runtime 中实现 `P2P.Receive()` 的监听循环，把 `MsgFrost` 分发到 `runtime/net/handlers.go`
- [ ] 6.6 加入防重放字段校验：`session_id / vault_id / sign_algo / epoch / round`（先校验基本字段，签名校验放到 Phase 12）

验收：
- [ ] 两个节点之间能互发一条 `MsgFrost` 并被 runtime 收到（日志可见）

---

## Phase 7：ChainAdapter（模板构建 + template_hash + SignedPackage 格式）

目标：把“签什么”变成可验证、可复算的模板哈希；产物落链的是 `SignedPackage`。

- [ ] 7.1 定义 `frost/chain/adapter.go`：
  - `BuildWithdrawTemplate(...) -> (template_bytes, template_hash, metadata)`
  - `BuildTransitionTemplate(...) -> (...)`
  - `PackageSignedResult(...) -> FrostSignedPackage`
- [ ] 7.2 先实现 BTC（最优先，且你的工程已有 btcd 依赖）：
  - `frost/chain/btc/template.go`：固定序列化规则（inputs/outputs/fee/change）
  - `frost/chain/btc/utxo.go`：UTXO 选择（确定性：按 txid:vout 排序 + FIFO）
  - `template_hash`：对“模板的规范化 bytes”做 `sha256`
- [ ] 7.3 再实现 EVM（ETH/BNB 共用）：
  - `frost/chain/evm/contract.go`：withdraw calldata 模板（链上不广播，只落签名产物）
  - `template_hash`：对 calldata/nonce/gas/chain_id 等规范化后 hash
- [ ] 7.4 SOL：先只实现模板结构与 hash，签名产物后续补齐
- [ ] 7.5 TRX：Adapter 先返回 `ErrNotSupported`（与设计一致）

验收：
- [ ] `go test ./frost/chain/...`（至少能编译；BTC 模板 hash 单测建议必做）

---

## Phase 8：Withdraw JobPlanner（确定性规划）

目标：从链上 FIFO withdraw 队首生成“可签名的 job”，并且任何节点都能复算同一个 `template_hash`。

- [ ] 8.1 新增 `frost/runtime/job_planner.go`：
  - 输入：`(chain, asset)` 队列 head、FundsLedger、VaultState
  - 输出：`job_id / vault_id / withdraw_ids[] / template_hash / key_epoch / sign_algo`
- [ ] 8.2 实现 Vault 选择（设计：按 `vault_id` 升序找第一个余额/UTXO 足够的 ACTIVE vault）
- [ ] 8.3 实现 “队首连续 QUEUED 前缀” 收集（受 `maxInFlightPerChainAsset` 限制）
- [ ] 8.4 BTC：实现“一个大 UTXO 支付多笔提现”的合并策略（减少 input 数）
- [ ] 8.5（可选）EVM：实现 CompositeJob（跨 Vault 组合支付），BTC 明确不支持
- [ ] 8.6 为 JobPlanner 写单测：相同链上状态 → 任意节点规划结果一致

验收：
- [ ] `go test ./frost/runtime -run JobPlanner`

---

## Phase 9：ROAST + 会话恢复（先“能签”，再“鲁棒”）

目标：实现 `frost/design.md §7`：两轮签名会话、子集重试、超时切换聚合者、重启恢复。

### 9A. Core：先把“可用的签名原语”固化

- [ ] 9.1 把现有 `frost` 里的阈值 Schnorr 逻辑整理成可复用的 `frost/core/frost` API
  - 先支持 `SCHNORR_SECP256K1_BIP340`（BTC）
  - 逐步支持 `SCHNORR_ALT_BN128`（ETH/BNB）、`ED25519`（SOL）
- [ ] 9.2 明确 nonce 生成/commit/响应的数据结构（与 SessionStore 一一对应）
- [ ] 9.3 为每种曲线增加 verify（VM/离链都要用）

### 9B. Runtime：Coordinator / Participant

- [ ] 9.4 实现 `frost/runtime/coordinator.go`（聚合者）
  - Round1：收集 nonce commitments（支持 batch task_ids）
  - Round2：广播 challenge/R_agg 并收集 sig shares
  - 聚合输出 `SignedPackage`
- [ ] 9.5 实现 `frost/runtime/participant.go`（参与者）
  - nonce 先落盘再发送 commitment
  - 同一 nonce 只允许绑定一个 msg（防二次签名攻击，见设计 §12）
- [ ] 9.6 实现超时/切换聚合者：
  - `nonceCommitBlocks / sigShareBlocks / aggregatorRotateBlocks / sessionMaxBlocks`
  - 聚合者切换后会话继续（不重复使用 nonce）
- [ ] 9.7 实现 SessionStore 的落盘版本（Badger 或本地文件）
  - 关键：nonce 状态必须持久化，重启后可恢复未完成 session
- [ ] 9.8 增加最小 metrics：会话耗时、重试次数、聚合者切换次数、失败原因

验收：
- [ ] 单机（模拟 3/5）能完成一次 ROAST 会话并产出签名
- [ ] kill -9 重启后，能从 SessionStore 恢复并继续（至少不复用 nonce）

---

## Phase 10：Withdraw 端到端闭环（上链回写）

目标：Scanner → JobPlanner → ROAST → Submit `FrostWithdrawSignedTx` → VM 落 job + receipt/history。

- [ ] 10.1 `frost/runtime/withdraw_worker.go`：把 Phase 8/9 串起来
- [ ] 10.2 `TxSubmitter` 真正提交 `pb.AnyTx{FrostWithdrawSignedTx...}` 进入 txpool
- [ ] 10.3 VM `FrostWithdrawSignedTx` 补齐强校验：
  - VM 复算 job 窗口（withdraw_ids + template_hash）
  - 校验 signed_package_bytes 绑定同一 template_hash
  - BTC：校验签名对模板有效，并锁定/消耗对应 UTXO（防双花）
- [ ] 10.4 增加一条集成测试（最小闭环）：
  - 构造入账 lot/UTXO
  - 创建 withdraw 请求
  - 运行 runtime 一轮，最终链上出现 signed package 与 withdraw=SIGNED

验收：
- [ ] 端到端测试可重复跑且幂等（重复提交 SignedTx 只会追加 receipt，不会二次消耗）

---

## Phase 11：Power Transition（DKG + validation + migration）

目标：按 Vault 分片完成轮换闭环（设计 §6），并且 VM 能最终判定 “新 key 生效”。

### 11A. 触发与状态机

- [ ] 11.1 定义/实现 `VaultTransitionState` 的存取与状态推进（VM 侧）
- [ ] 11.2 实现触发检测（TransitionWorker 扫描）：
  - `epochBlocks` 边界
  - `triggerChangeRatio`（change_ratio 计算规则先定一个可实现版本）
- [ ] 11.3 新增/更新 `VaultState`：`lifecycle/key_epoch/group_pubkey/committee_ref/committee_members`

### 11B. DKG 上链（commit/share/complaint/reveal）

- [ ] 11.4 VM handler：`FrostVaultDkgCommitTx`（校验 sign_algo 一致，写 commitment）
- [ ] 11.5 VM handler：`FrostVaultDkgShareTx`（写加密 share，receiver 可读）
- [ ] 11.6 VM handler：`FrostVaultDkgComplaintTx`（写投诉记录，锁/罚没 bond 的最小实现）
- [ ] 11.7 VM handler：`FrostVaultDkgRevealTx`（复算 ciphertext，裁决 dealer/complainer）
- [ ] 11.8 TransitionWorker：在 commit 窗口结束后统计 qualified 集合并计算 `new_group_pubkey`

### 11C. validation 签名（KeyReady）

- [ ] 11.9 定义 validation 消息 hash：`H("frost_vault_dkg_validation" || chain || vault_id || epoch_id || sign_algo || new_group_pubkey)`
- [ ] 11.10 TransitionWorker 发起一次 ROAST validation 签名会话
- [ ] 11.11 VM handler：`FrostVaultDkgValidationSignedTx`：
  - 校验 msg_hash 与当前 state 匹配
  - 校验签名产物有效
  - 状态推进到 `KeyReady`

### 11D. migration（迁移 job + SignedPackage）

- [ ] 11.12 迁移 job 模板规划（按 FundsLedger/UTXO）
- [ ] 11.13 TransitionWorker 执行迁移签名并回写 `FrostVaultTransitionSignedTx`
- [ ] 11.14 VM handler：`FrostVaultTransitionSignedTx`：
  - VM 复算当前迁移模板并校验签名
  - 消耗/锁定迁移资产，append receipt/history
- [ ] 11.15 VM 判定迁移完成：
  - 该 vault 旧 key 资产全部覆盖/消耗后，切换 `VaultState.key_epoch/group_pubkey`
  - `lifecycle` 从 DRAINING→ACTIVE（或 RETIRED）

验收：
- [ ] 单 vault 的 DKG → KeyReady → migration → key_epoch 切换，全流程可跑通

---

## Phase 12：外部 API（只读 + 运维）与安全加固

### 12A. HTTP API（复用 `handlers`）

- [ ] 12.1 在 `handlers/` 增加 Frost 路由（如 `/frost/...`）并挂到 `HandlerManager.RegisterRoutes`
- [ ] 12.2 实现查询接口（对应设计 §9.1）：
  - `GetFrostConfig`
  - `GetVaultGroupPubKey`
  - `GetWithdrawStatus` / `ListWithdraws`
  - `GetVaultTransitionStatus` / `GetVaultDkgCommitment`
  - `ListVaults`
- [ ] 12.3 实现运维接口（对应设计 §9.2）：
  - `GetHealth`、`ForceRescan`、`Metrics`、`GetSession`

### 12B. 安全（对应设计 §12）

- [ ] 12.4 FrostEnvelope 消息签名与验签（使用 miner 节点身份签名）
- [ ] 12.5 anti-replay：拒绝过期 epoch/round/session_id，不接受重复无意义消息
- [ ] 12.6 Nonce 安全：
  - nonce 落盘后才允许发送 commitment
  - 同一 nonce 只允许绑定一个 msg（允许重发同 msg 的 share 幂等）
- [ ] 12.7 “多份签名产物”安全：
  - VM 允许追加 receipt/history
  - 但必须保证不会导致双花（UTXO lock / withdraw_id→template_hash 绑定）

验收：
- [ ] 关键攻击面有单测/集成测试覆盖（至少覆盖 nonce 复用与重复提交）

---

## Phase 13：配置（并入现有 `config`）

目标：把 `frost/design.md §10` 的配置落到 `config.Config`，并提供默认值与加载方式。

- [ ] 13.1 新增 `config/frost.go`：定义 `FrostConfig`（timeouts/vault/chains/withdraw/transition）
- [ ] 13.2 在 `config/config.go` 的 `Config` 结构体里挂 `Frost FrostConfig`
- [ ] 13.3 增加加载方式（JSON/YAML 任选一种；或先只用默认配置）
- [ ] 13.4 让 runtime 读取配置并驱动：
  - 超时窗口（blocks）
  - `vaultsPerChain / thresholdRatio`
  - 手续费/gas 参数（写死年均 300% 的默认值）

验收：
- [ ] 不改配置时使用默认值，改配置后能影响 runtime 行为（例如超时/聚合者轮换）

---

## Phase 14：补齐“最终一致 + 幂等”与可观测性

目标：在异步冗余下不多签发、能恢复、能审计。

- [ ] 14.1 全链路幂等：
  - 重复 `WithdrawRequestTx` 不重复入队
  - 重复 `WithdrawSignedTx/TransitionSignedTx` 只追加 receipt，不重复消耗资金
- [ ] 14.2 scanner/job planner 的确定性：
  - 同一 finalized state，所有节点规划出同一 job/template_hash
- [ ] 14.3 指标补齐：
  - 会话耗时分布、失败原因、聚合者切换次数、子集重试次数
- [ ] 14.4 文档补齐：
  - “如何下载 SignedPackage 并手动广播”
  - “如何排查卡住的队列/迁移”

验收：
- [ ] 在多节点环境反复重启/重复提交下，状态仍保持一致，不出现 double spend

