# PROMPT_PLAN.md - 面向 AI Agent 的分步提示词计划（实现 frost/design.md）

本文件是“提示词驱动”的执行计划。每一项都是一条完整提示词，
你可以直接复制给 AI Agent 使用。步骤按“先主体框架、后细节”排序，
且每步都有简单验证方式。

使用方法：
- 每次只投喂一个步骤的提示词
- 不要跳步
- 每步都执行验证

---

## 统一提示词模板（建议每步都使用）

```
你是 Go 项目的 coding agent。只完成【本步骤】的内容，不做后续步骤。
设计依据：frost/design.md（只读相关章节）。保持工程边界：
- VM handler 放 vm/
- HTTP API 放 handlers/
- 配置放 config/
约束：
- 不新增第三方依赖
- 不做无关重构
- 变更尽量小
交付：
- 列出改动文件
- 运行并贴出验证命令结果（通过/失败）
```

---

## 计划步骤（每一项都是完整提示词）

### P00 基线（当前测试状态）

```
按模板执行。
目标：跑一遍 go test 记录当前基线失败，不改代码。
验证命令：go test ./...
输出：失败包/用例名/错误摘要。
```

---

### P01 主体框架：迁移已有 frost 代码到 core（仅重构）

```
按模板执行。
目标：把 frost/ 下的纯算法代码迁到 frost/core（不改行为）。
任务：
1) 新建目录：
   - frost/core/curve
   - frost/core/dkg
   - frost/core/frost
   - frost/core/roast
2) 迁移：
   - frost/dkg.go
   - frost/group.go
   - frost/utils.go
   - frost/bn256_adapter.go
   - frost/secp2561_adapter.go
   - frost/dkg_test.go
3) 修正 package/import，保证能编译测试。
4) 如果 frost/sign/main.go 是 demo，移到 cmd/ 下或明确标注 demo。
验证命令：go test ./...
```

---

### P02 主体框架：Runtime 接口 + 空壳 Manager

```
按模板执行。
目标：补 runtime 接口与最小 manager（不做业务逻辑）。
任务：
1) 新增 frost/runtime/deps.go（按 design.md §8.1）：
   ChainStateReader, TxSubmitter, FinalityNotifier, P2P, SignerSetProvider,
   VaultCommitteeProvider, ChainAdapterFactory。
2) 新增 frost/runtime/manager.go：Start/Stop，订阅 finalized 事件并记录/打印高度。
3) 单测：fake notifier 触发 finalized，manager 能接收。
验证命令：go test ./frost/runtime -run Test
```

---

### P03 主体框架：ChainAdapter 接口 + BTC 模板 hash（最小可测）

```
按模板执行。
目标：建立 frost/chain 骨架与 BTC 的确定性 template_hash。
任务：
1) 新增 frost/chain/adapter.go：定义 ChainAdapter 接口签名
   (BuildWithdrawTemplate / TemplateHash / PackageSigned 等)。
2) 新增 frost/chain/btc/template.go：实现规范化序列化后 sha256 的 template_hash。
3) 单测：相同逻辑输入在不同构造顺序下 hash 一致。
验证命令：go test ./frost/chain/btc -run Test
```

---

### P04 主体框架：FrostConfig 并入 config（只默认值）

```
按模板执行。
目标：按 design.md §10 把 FrostConfig 落地到 config/。
任务：
1) 新增 config/frost.go：定义 FrostConfig（committee/vault/timeouts/withdraw/transition/chains）。
2) config.Config 增加 Frost FrostConfig。
3) 在 DefaultConfig() 填默认值。
4) 单测：DefaultConfig().Frost 关键字段有默认值。
验证命令：go test ./config -run Test
```

---

### P05 主体框架：Runtime 挂到节点启动（默认关闭）

```
按模板执行。
目标：把 frost/runtime 接到 cmd/main.go 生命周期里，但默认不启用。
任务：
1) NodeInstance 增加 FrostRuntime 字段（或等效）。
2) 配置开启时创建并 Start；退出时 Stop。
验证命令：go test ./...
```

---

## 链上协议与 VM（先让状态机能跑通）

### P06 Protobuf：最小 Frost tx/state

```
按模板执行。
目标：新增最小 Frost protobuf，让编译通过。
任务：
1) pb/data.proto：新增 FrostWithdrawRequestTx、FrostWithdrawSignedTx。
2) 加入 AnyTx.oneof（新 field number，不能改旧编号）。
3) 运行 protoc 重新生成 pb/data.pb.go。
4) 更新 pb/anytx_ext.go：GetBase() 覆盖新增 tx。
验证命令：go test ./pb
```

---

### P07 VM 路由：识别 Frost tx（占位 handler）

```
按模板执行。
目标：VM 能识别 Frost tx kind，并注册占位 handler。
任务：
1) 修改 vm/handlers.go 的 DefaultKindFn 识别 Frost tx。
2) vm/default_handlers.go 注册占位 handler（返回 ErrNotImplemented）。
3) 单测：AnyTx -> kind 字符串正确。
验证命令：go test ./vm -run FrostKind
```

---

### P08 Keys：补齐 Frost keyspace（字符串即可）

```
按模板执行。
目标：在 keys/keys.go 增加缺失的 frost key 生成函数。
任务：
1) 增加：
   - KeyFrostConfig
   - KeyFrostTop10000
   - KeyFrostVaultConfig
   - KeyFrostVaultState
   - KeyFrostVaultTransition
   - KeyFrostVaultDkgCommit
   - KeyFrostSignedPackage
2) 单测：对每个 key 做字符串断言。
验证命令：go test ./keys -run Frost
```

---

## Withdraw（队列 -> 上链回写）

### P09 VM：FrostWithdrawRequestTx handler（FIFO 入队）

```
按模板执行。
目标：实现 WithdrawRequest 入队（QUEUED + FIFO seq）。
任务：
1) 新增 vm/frost_withdraw_request_handler.go。
2) 写 WithdrawRequest{status=QUEUED, seq, request_height} + FIFO index。
3) 幂等：同 tx_id 重放不递增 seq。
4) 单测：两笔不同 tx_id seq 递增；重复 tx_id 不变。
验证命令：go test ./vm -run FrostWithdrawRequest
```

---

### P10 VM：FrostWithdrawSignedTx handler（最小回写）

```
按模板执行。
目标：最小签名产物上链回写（先不做 template 复算/验签）。
任务：
1) 新增 vm/frost_withdraw_signed_handler.go。
2) QUEUED -> SIGNED，追加 signed_package receipt/history。
3) 幂等：重复提交只追加 receipt。
4) 单测：状态变更 + 重复提交行为。
验证命令：go test ./vm -run FrostWithdrawSigned
```

---

### P11 VM：FundsLedger helper（FIFO head）

```
按模板执行。
目标：增加 vm/frost_funds_ledger.go，支持 lot FIFO head/seq 操作。
任务：
1) 实现 KeyFrostFundsLotHead/Seq/Index 读写。
2) 单测：消费后 head 正确推进。
验证命令：go test ./vm -run FrostFundsLedger
```

---

## Runtime（扫描 -> 规划 -> 回写，先用 dummy 签名）

### P12 Runtime：StateReader + TxSubmitter 适配

```
按模板执行。
目标：runtime 能读链上状态并提交 tx。
任务：
1) 新增 frost/runtime/adapters/state_reader.go（ChainStateReader 基于 StateDB/DB）。
2) 新增 frost/runtime/adapters/tx_submitter.go（TxSubmitter 基于 txpool）。
3) 单测：fake 适配器可用。
验证命令：go test ./frost/runtime/... -run Adapter
```

---

### P13 Runtime：Scanner（找到队首 withdraw）

```
按模板执行。
目标：scanner 发现队首 QUEUED withdraw 并触发 worker。
任务：
1) 实现 frost/runtime/scanner.go 的 ScanOnce。
2) 读 FIFO head + index，遇到 QUEUED 触发 worker。
3) 单测：fake state -> scanner 触发一次。
验证命令：go test ./frost/runtime -run Scanner
```

---

### P14 Runtime：JobPlanner（单 vault / 单 withdraw）

```
按模板执行。
目标：最小确定性规划（单 vault、单 withdraw）。
任务：
1) 新增 frost/runtime/job_planner.go。
2) 读取 withdraw，调用 ChainAdapter(BTC) 得 template_hash。
3) 生成 job_id（按 design.md 规则）。
4) 单测：同状态多次规划结果一致。
验证命令：go test ./frost/runtime -run JobPlanner
```

---

### P15 Runtime：WithdrawWorker（dummy 签名回写）

```
按模板执行。
目标：Scanner -> Planner -> 提交 FrostWithdrawSignedTx（signed_package_bytes 用 dummy）。
任务：
1) 新增 frost/runtime/withdraw_worker.go。
2) signed_package_bytes = "dummy" + template_hash。
3) 单测：submitter 收到正确 tx；VM handler 后 withdraw=SIGNED。
验证命令：go test ./frost/runtime -run WithdrawWorker
```

---

## 真实签名路径（先 BTC）

### P16 Core：SignAlgo 抽象 + BTC BIP340 verify

```
按模板执行。
目标：添加 Verify API，并实现 BTC BIP340 验签。
任务：
1) 新增 frost/core/frost/api.go：Verify(SignAlgo,...)。
2) 实现 BTC BIP340 verify。
3) 单测：正确签名通过，改 msg 失败。
验证命令：go test ./frost/core/... -run BIP340
```

---

### P17 Chain/BTC：使用真实 Taproot sighash 作为 template_hash

```
按模板执行。
目标：BTC template_hash 与真实 Taproot sighash 对齐。
任务：
1) 实现 sighash 计算（基于模板数据）。
2) 固定向量单测：hash 稳定。
验证命令：go test ./frost/chain/btc -run Sighash
```

---

### P18 VM：BTC 验签 + UTXO lock

```
按模板执行。
目标：VM 对 BTC SignedPackage 验签并锁定 UTXO。
任务：
1) FrostWithdrawSignedTx handler 增加 BTC 验签逻辑。
2) 成功后写 KeyFrostBtcLockedUtxo(utxo)->job_id。
3) 单测：验签通过 -> SIGNED + lock；失败 -> 拒绝；重复不双锁。
验证命令：go test ./vm -run FrostWithdrawSignedBTC
```

---

## P2P 与 ROAST（鲁棒异步）

### P19 P2P：MsgFrost + FrostEnvelope 发送/接收

```
按模板执行。
目标：增加 MsgFrost 与 /frostmsg 接收端点（不做签名校验）。
任务：
1) types/message.go：新增 MsgFrost 与 payload 字段。
2) handlers：新增 /frostmsg 接收并入队 MsgFrost。
3) sender：新增 doSendFrost.go（POST /frostmsg）。
4) realTransport：增加 MsgFrost 发送分支。
验证：httptest server 收到 payload 返回 200。
验证命令：go test ./handlers -run FrostMsg
```

---

### P20 Runtime/net：FrostEnvelope 路由（不做加密）

```
按模板执行。
目标：runtime/net 能按 Kind 分发 FrostEnvelope。
任务：
1) 新增 frost/runtime/net/msg.go 定义 FrostEnvelope。
2) 新增 frost/runtime/net/handlers.go 实现 HandleEnvelope switch。
3) 单测：NonceCommit/SigShare 进入正确分支。
验证命令：go test ./frost/runtime/... -run FrostNet
```

---

### P21 SessionStore：nonce 落盘与防二次签名

```
按模板执行。
目标：nonce 状态持久化并强制“同一 nonce 只能绑定一个 msg”。
任务：
1) 实现 frost/runtime/session/store.go（NonceState）。
2) 单测：同 nonce + 不同 msg 必须失败；同 msg 幂等允许。
验证命令：go test ./frost/runtime/... -run NonceSafety
```

---

### P22 ROAST：子集重试 + 聚合者切换（fake P2P）

```
按模板执行。
目标：实现最小 ROAST 会话（超时重试 + 聚合者切换）。
任务：
1) frost/core/roast：会话状态机。
2) runtime/coordinator.go：Round1/2 收集 + 超时切换。
3) 单测：部分参与者不响应仍能完成。
验证命令：go test ./frost/... -run ROAST
```

---

## Power Transition / DKG（按 Vault 分片轮换）

### P23 Vault 委员会：Top10000 -> VaultCommittee

```
按模板执行。
目标：确定性将 Top10000 分配到 Vault 委员会。
任务：
1) 用 db.IndexMgr 获取矿工 index 列表（排序）作为 Top10000。
2) 实现 Permute(list, seed) + 按 K 切分。
3) 单测：相同 epoch 输出一致；不同 seed 输出变化。
验证命令：go test ./frost/... -run VaultCommittee
```

---

### P24 VM：DKG Commit/Share handler

```
按模板执行。
目标：实现 DKG commit/share 上链存储与基本校验。
任务：
1) vm/frost_vault_dkg_commit_handler.go：写 commitment，校验 sign_algo 一致。
2) vm/frost_vault_dkg_share_handler.go：写加密 share。
3) 单测：重复 commit 幂等；sign_algo 不一致拒绝。
验证命令：go test ./vm -run FrostDKG
```

---

### P25 VM：DKG ValidationSigned（KeyReady）

```
按模板执行。
目标：实现 ValidationSignedTx handler 推进到 KeyReady。
任务：
1) vm/frost_vault_dkg_validation_signed_handler.go：校验 msg_hash，写 receipt。
2) 单测：不匹配拒绝；匹配 -> KeyReady。
验证命令：go test ./vm -run FrostDKGValidation
```

---

### P26 Runtime：TransitionWorker 最小闭环

```
按模板执行。
目标：最小轮换闭环：提交 commit/share/validation。
任务：
1) 实现 frost/runtime/transition_worker.go（手动或 epoch 触发）。
2) 单测：按顺序提交三类 tx。
验证命令：go test ./frost/runtime -run TransitionWorker
```

---

### P27 VM：TransitionSignedTx handler（迁移产物）

```
按模板执行。
目标：实现迁移签名产物上链（最小消费规则）。
任务：
1) vm/frost_vault_transition_signed_handler.go：append receipt/history。
2) 最小迁移完成规则（例如余额为 0）。
3) 单测：重复提交只追加，不重复消耗。
验证命令：go test ./vm -run FrostTransitionSigned
```

---

## 外部 API 与最终加固

### P28 HTTP：只读 Frost API（先 3 个）

```
按模板执行。
目标：实现 3 个只读接口：GetFrostConfig、GetWithdrawStatus、ListWithdraws。
任务：
1) handlers/ 下增加路由与 handler。
2) httptest 单测 200 + 返回正确结构。
验证命令：go test ./handlers -run FrostAPI
```

---

### P29 管理接口：GetHealth/ForceRescan/Metrics

```
按模板执行。
目标：实现运维接口最小集合。
任务：
1) handlers/ 下增加 GetHealth/ForceRescan/Metrics。
2) httptest 单测。
验证命令：go test ./handlers -run FrostAdmin
```

---

### P30 最终加固：关键安全与幂等

```
按模板执行。
目标：补齐必须安全项（不扩需求）：
1) VM 强校验：SignedTx 绑定 template_hash；receipt 幂等。
2) BTC：UTXO lock 防双花。
3) Runtime：nonce 落盘后才发 commitment；一 nonce 一 msg。
4) FrostEnvelope：消息签名与 anti-replay。
验证命令：go test ./...
```

