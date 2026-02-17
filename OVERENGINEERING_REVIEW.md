# 🔬 过度设计 & 瘦身机会清单

> 更新时间：2026-02-17（数据库已从 BadgerDB 迁移至 PebbleDB）
> ⚠️ 本文档只列出问题，不做任何修改。按影响等级排序。

---

## ✅ 已完成：BadgerDB → PebbleDB 迁移

数据库引擎已切换到 PebbleDB。`db/db.go` 从 1594 行缩减至 **1342 行**，`import` 中无 badger 依赖。
但迁移遗留了若干待清理项目，见下方 🔴 一、二。

---

## ✅ ~~一、BadgerDB 残留注释 & 变量名（跨模块）~~ — 已完成

迁移到 Pebble 后，代码中仍散落着大量 Badger 相关的字面量：

| 文件 | 行号 | 内容 |
|------|------|------|
| `db/db.go` | 87 | 注释 `// 自增发号器 (替代 Badger Sequence)` |
| `config/config.go` | 47 | 注释 `// BadgerDB配置`（实际已是 Pebble 参数） |
| `config/config.go` | 48-49 | `ValueLogFileSize`, `BaseTableSize`（BadgerDB 独有概念，Pebble 不使用） |
| `config/config.go` | 59-61 | `WriteBatchSoftLimit`, `MaxCountPerTxn`, `PerEntryOverhead`（BadgerDB 事务限制，Pebble 无此约束） |
| `config/config.go` | 65 | `SequenceBandwidth`（Badger Sequence API，已被自增发号器替代） |
| `consensus/realBlockStore.go` | 407 | 注释 `避免并发 SetFinalized 触发 Badger 事务冲突` |
| `vm/executor_integration_test.go` | 152-184 | 多处变量名 `badgerBalData`, `badgerBal` 及相关注释 |
| `vm/vm_matching_statedb_e2e_test.go` | 33,40,53,415 | 多处注释 `创建真实的 Badger + StateDB` 等 |
| `vm/state.go` | 17 | 注释中大段描述 Badger Prefix 遍历策略 |

**瘦身方案**：
- 注释统一改为 Pebble 或通用表述
- 测试中 `badgerXxx` 变量名重命名为 `dbXxx`/`kvXxx`
- `config/config.go` 中删除 `ValueLogFileSize`, `BaseTableSize`, `WriteBatchSoftLimit`, `MaxCountPerTxn`, `PerEntryOverhead`, `SequenceBandwidth` 等 6 个已废弃字段及其默认值
- **预估：删除 6 个废弃 config 字段 + ~20 处注释修正**

> ✅ **状态：已完成。** `db/db.go`、`config/config.go`、`consensus/realBlockStore.go`、`vm/state.go`、`vm/executor_integration_test.go` 中已无任何 Badger 相关注释或变量名。6 个废弃 config 字段已删除。

---

## ✅ 二、StateDB / Verkle 死代码层（db + vm 跨模块） — 已完成

`Manager.StateDB` 始终为 `nil`（构造函数写死 `StateDB: nil`），但代码中仍保留了完整的 StateDB 抽象层：

### db/db.go 中的死代码（~120 行）
- `stateDB` 接口定义（L44-52）
- `stateDBSession` 接口定义（L34-42）
- `dbSession.verkleSess` 字段及所有 `if s.verkleSess == nil` 分支（L614-697）
- `CommitRoot()` 方法（L626-630）
- `SyncToStateDB()` 空实现（L1325-1329）
- `GetStateRoot()` 返回 `nil` 的空实现（L1331）
- `Read()`, `Get()` 中的 `IsStatefulKey` + `StateDB.Get` 分支（L1132-1136, L1154-1159）

### vm/ 中的死代码
- `WriteOp.SyncStateDB` 字段 — 贯穿 **14 个文件**，但 executor `applyResult` 中仅在 `StateDB != nil` 时才使用
- `stateview.go` 中 `ovVal.syncStateDB` 字段及 `SetWithMeta` 方法
- `witness_events.go` 中 `setWithMeta` 辅助函数

### config/config.go 中的残留
- `VerkleKVLogEnabled`（L72）— Verkle 已移除
- `VerkleDisableRootCommit`（L73）— 同上

**瘦身方案**：
- 若确认 StateDB 功能已弃用：删除 `stateDB`/`stateDBSession` 接口、`verkleSess` 字段、所有 `nil` 守卫分支、`SyncToStateDB`、`GetStateRoot`
- 删除 `WriteOp.SyncStateDB` 字段，简化 `SetWithMeta` → `Set`
- 删除 config 中 `VerkleKVLogEnabled`, `VerkleDisableRootCommit`, `IndexCacheSize`（BadgerDB Ristretto 缓存参数）
- **预估：删除 ~120 行（db）+ 简化 14 个 vm 文件 + 删除 3 个 config 字段**

> ✅ **状态：已完成。**
> - ✅ `db/db.go`：`stateDB`/`stateDBSession` 接口、`verkleSess` 字段、`CommitRoot()`、`SyncToStateDB()`、`GetStateRoot()`、`IsStatefulKey` 分支已全部删除
> - ✅ `config/config.go`：`VerkleKVLogEnabled`、`VerkleDisableRootCommit`、`IndexCacheSize` 已删除
> - ✅ `vm/types.go`：`WriteOp.SyncStateDB` 字段已删除
> - ✅ `vm/stateview.go`：`SetWithMeta` 路径已删除，统一为 `Set`
> - ✅ `vm/witness_events.go`：`setWithMeta` 辅助函数已删除，统一为 `Set`

---

## ✅ 三、`db/keys.go` — 纯转发层（241 行） — 已完成

`db/keys.go` 是一个 **241 行的纯代理文件**，每个函数只做一件事：调用 `keys.Xxx()` 并返回。
文件头注释写明 `新代码应该直接使用 "dex/keys" 包`。

**瘦身方案**：
- 全局搜索 `db.KeyXxx` 调用，改为直接 `keys.KeyXxx`
- 删除整个 `db/keys.go`
- **预估：删除 241 行 / 1 文件**

> ✅ **状态：已完成。** `db/keys.go` 已删除；`db` 包内部调用已全部改为直接使用 `dex/keys`；全仓无 `db.KeyXxx` 调用。

---

## ✅ 四、文件粒度过细（sender/ 模块） — 已完成

### 问题：每个 HTTP 发送函数独占一个文件，高度雷同

`sender/` 目录有 **13 个 `doSend*.go` 文件**，每个只有 30~40 行，且模式几乎一模一样：

| 文件 | 行数 | 差异点 |
|------|------|--------|
| `doSendTx.go` | 36 | URL=`/tx`, Content-Type=protobuf |
| `doSendBlock.go` | 40 | URL=`/put`, Content-Type=protobuf |
| `doSendChits.go` | 40 | URL=`/chits`, Content-Type=protobuf |
| `doSendPushQuery.go` | 37 | URL=`/pushquery`, Content-Type=protobuf |
| `doSendPullQuery.go` | 37 | URL=`/pullquery`, Content-Type=protobuf |
| `doSendHeightQuery.go` | ~40 | URL=`/heightquery`, 有回调 |
| `doSendSyncRequest.go` | ~70 | URL=`/syncrequest`, 有回调 |
| `doSendGetBlock.go` | ~60 | URL=`/getblock`, 有回调 |
| `doSendGetBlockByID.go` | ~50 | URL=`/get`, 有回调 |
| `doSendBatchGetTxs.go` | ~50 | URL=`/batchgettxs`, 有回调 |
| `gossip_sender.go` | ~40 | URL=`/gossipAnyMsg` |
| `doSendFrost.go` | ~80 | URL=FROST路由 |

**瘦身方案**：
- 无回调的简单发送（Tx/Block/Chits/Push/Pull）可以合并为一个通用的 `doSendSimple(url, data)` 函数
- 有回调的可以合并为 `doSendWithCallback(url, data, decoder, onSuccess)` 
- 每个消息类型只需定义路由路径常量，不需要独立文件
- **预估可从 13 个文件 → 2 个文件**

> ✅ **状态：已完成。** `doSendTx/doSendBlock/doSendChits/doSendPushQuery/doSendPullQuery/doSendHeightQuery/doSendSyncRequest/doSendGetBlock/doSendGetBlockByID/doSendBatchGetTxs` 与 `gossip_sender.go` 已合并为 `sender/do_send_simple.go` 与 `sender/do_send_callbacks.go`（`doSendFrost.go` 保持独立）。

### 同一问题：消息类型定义分散

`sender/consensus_types.go` 定义了 `chitsMessage`, `blockMessage`, `heightQueryMessage`, `syncRequestMessage`，
但 `pullQueryMsg`, `pushQueryMsg` 在各自的 `doSend*.go` 文件中定义。
**已统一到 `consensus_types.go`**。

---

## ✅ 五、Frost DKG 类型文件仅含常量定义 — 已完成

VM 中有 3 个文件**只是放常量**，而 Handler 实现在对应的 `_handler.go` 文件中：

| 文件 | 内容 | 行数 |
|------|------|------|
| `frost_vault_dkg_commit.go` | 2 个常量 `COMMITTED`, `DISQUALIFIED` | 15 行 |
| `frost_vault_dkg_share.go` | 3 个常量 `PENDING`, `VERIFIED`, `DISPUTED` | 17 行 |
| `frost_vault_transition_signed.go` | 6 个常量 `ACTIVE`, `DRAINING` 等 | 19 行 |

**瘦身方案**：将这些常量直接放入对应的 `_handler.go` 文件的顶部，消灭 3 个文件。

> ✅ **状态：已完成。** 常量已并入对应 handler 文件，原 3 个仅常量文件已删除。

---

## ✅ 六、`handlers/frost_routes.go` — 空文件 — 已完成

```go
package handlers
```

只有 `package handlers` 声明，2 行，0 功能。直接删除。

> ✅ **状态：已完成。** 空文件已删除。

---

## ✅ 七、`db/db.go` 巨型文件（~~1342~~ ~~1224~~ 389 行，主职责已拆分） — 已完成

迁移 Pebble 后从 1594 行缩减至 1342 行，随后到 1224 行；本轮继续拆分后，`db/db.go` 已降到 **389 行**，核心多职责已迁出：

1. **接口定义**：`stateDB`、`stateDBSession` 接口（可删，见上方 🔴 二）
2. **Manager 结构和构造器**
3. **写队列**（`InitWriteQueue`, `runWriteQueue`, `ForceFlush`, `drainWriteQueue`, `runWriteQueueWatchdog`）— ~200 行
4. **写队列 Metrics**（`writeQueueMetricsSnapshot`, 各种 counter/snapshot/log 函数）— ~250 行
5. **Session 管理**（`dbSession`, CRUD）
6. **扫描操作**（`Scan`, `ScanKVWithLimit`, `ScanKVWithLimitReverse`, `ScanByPrefix`）
7. **订单价格索引扫描**（`ScanOrderPriceIndexRange`, `ScanOrderPriceIndexRangeOrdered`, `ScanOrdersByPairs`）— ~200 行
8. **索引重建**（`RebuildOrderPriceIndexes`）

**瘦身方案**：
- 写队列 + Metrics（~450行）→ 抽取到 `db/write_queue.go`
- 订单价格索引扫描（~200行）→ 归入 `db/manage_tx_storage.go` 或独立 `db/scan_order.go`
- 删除 StateDB 死代码后可再减 ~120 行
- 不改任何逻辑，只拆文件

> ✅ **状态：已完成。**
> - ✅ 写队列 + Metrics 已抽取到 `db/write_queue.go`（495 行）
> - ✅ 订单价格索引扫描 + 索引重建已抽取到 `db/scan_order.go`（253 行）
> - ✅ `db/db.go` 当前 389 行，保留 Manager 构造、基础读写与会话等主干职责

---

## ✅ 八、`vm/executor.go` 巨型文件（~~1728~~ ~~1686~~ 1087 行） — 已完成（首轮拆分）

| 功能块 | 预估行数 | 建议 |
|--------|---------|------|
| Probe 监控统计（结构体 + 6 个函数） | ~100 行 | ✅ 已迁移到 `vm/executor_probe.go` |
| 失败原因分类&格式化 | ~50 行 | ✅ 已迁移到 `vm/executor_probe.go` |
| 订单簿重建 (`rebuildOrderBooksForPairs`, `scanOrderIndexesForSide`, `batchLoadOrderStates`, `loadOrderToBook`, `appendOrderCandidates`) | ~200 行 | ✅ 已迁移到 `vm/orderbook_rebuild.go` |
| `preExecuteBlock` 函数本身 | 410 行 | 🔶 仍偏长，后续可拆子步骤 |
| `applyResult` 函数本身 | 255 行 | 🔶 仍偏长，后续可拆子步骤 |

> ✅ **状态：已完成（首轮拆分）。** 监控探针、失败分类和订单簿重建逻辑已拆出，`vm/executor.go` 体量显著下降。

---

## ✅ 九、`vm/order_handler.go` 注释乱码 + 冗余（~~1235~~ 1121 行） — 已完成

该文件中曾存在大量乱码注释，并有两个 trade record 生成函数。

**已完成工作**：
- 修复了乱码注释。
- 删除了冗余的 `generateTradeRecords` 函数，统一使用 `generateWriteOpsFromTrades`。
- 保留 `handleRemoveOrderLegacy` 以兼容（虽然可能不再需要，但保留更安全）。

> ✅ **状态：已完成。**

---

## 🟡 十、Consensus 模块 Simulated vs Real 双实现

consensus 目录包含两套完整实现：

| Simulated（测试用） | Real（生产用） | 行数 |
|---------------------|---------------|------|
| `simulatedTransport.go` | `realTransport.go` (666 行) | 98 vs 666 |

| `simulatedBlockStore.go` | `realBlockStore.go` (1249 行) | 319 vs 1249 |
| `simulatedProposer.go` | `proposalManager.go` (271 行) | 82 vs 271 |
| `simulatedManager.go` | — | 308 行 |

Simulated 系列是内存模拟器的组件，通过接口对接。这是合理的**策略模式**，不算严重的过度设计。
但 `simulatedManager.go (NetworkManager)` 中的 `PrintFinalResults`, `PrintQueryStatistics`, `PrintStatus` 合计 **~160 行**打印逻辑，可以精简。

---

## 🟡 十一、`sender/queue.go` 三级优先队列（560 行）

SendQueue 实现了三个优先级的 channel 分流：

```
PriorityImmediate → immediateChan (紧急)
PriorityControl   → controlChan  (共识控制)
PriorityData      → dataChan     (普通数据)
```

包含了复杂的：
- Per-target inflight 限流
- Requeue + 指数退避
- 延迟任务定时器
- 13 项运行时统计（`SendQueueRuntimeStats`）
- Latency 统计

这些都有用，但实际场景中节点数 < 100，是否需要这么精细的任务队列调度可以评估。

---

## 🟢 十二、`cmd/main/metrics.go` 大量监控打印逻辑（474 行）

该文件有 6 个 monitor 函数，每个都启动一个 goroutine 定期打印：

| 函数 | 行数 | 功能 |
|------|------|------|
| `monitorMetrics` | 86 | API 调用统计 |
| `monitorQueueStats` | 111 | 队列状态打印 |
| `monitorGCStats` | 45 | GC 压力监控 |
| `monitorProgress` | 17 | 进度打印 |
| `monitorMinerParticipantsByEpoch` | 17 | Epoch 矿工刷新 |
| `printAPICallStatistics` | 47 | API 统计格式化 |

打印逻辑很多只是 `fmt.Sprintf` 拼接字符串。可考虑接入 structured logging 或 Prometheus 替代手写打印。

---

## 🟢 十三、`consensus/realBlockStore.go` 中的快照管理逻辑

`RealBlockStore` 同时承担：
1. 区块存储 & CRUD
2. 区块最终化 (VM 提交)
3. 快照创建/加载
4. VRF 验证
5. 签名集管理
6. FinalizationChits 管理
7. 旧数据清理

**1249 行，35 个方法**。建议将快照管理和 VRF 验证逻辑拆出。

---

## 📊 瘦身优先级总结

| 优先级 | 项目 | 预估节省 | 难度 |
|--------|------|---------|------|
| ✅ 完成 | BadgerDB 残留注释 & 废弃 config 字段清理 | 删除 6 个字段 + ~20 处修正 | 低 |
| ✅ 完成 | StateDB / Verkle 死代码删除（db + vm） | 删除 db 死代码 + vm `SetWithMeta` 路径 + 3 个 config 字段 | 中 |
| ✅ 完成 | `db/keys.go` 转发层删除 | 删除 241 行 / 1 文件 | 低 |
| ✅ 完成 | sender/ doSend 合并 | 11 文件 + gossip 合并为 2 文件 | 低 |
| ✅ 完成 | 空文件 `frost_routes.go` 删除 | 删除 2 行 / 1 文件 | 极低 |
| ✅ 完成 | Frost DKG 常量文件合并 | 删除 51 行 / 3 文件 | 极低 |
| ✅ 完成 | `db/db.go` 写队列 + 订单扫描拆分 | `db/db.go` 1224→389；新增 `db/write_queue.go` + `db/scan_order.go` | 低 |
| ✅ 完成 | `order_handler.go` 乱码注释清理 | N/A | 低 |
| ❌ 未做 | `order_handler.go` Legacy 函数评估 | ~170 行 | 中 |
| ✅ 完成 | `executor.go` 首轮拆分 | `vm/executor.go` 1686→1087；新增 `vm/executor_probe.go` + `vm/orderbook_rebuild.go` | 低 |
| ❌ 未做 | `realBlockStore.go` 拆分 | 0（纯重组） | 中 |
| ❌ 未做 | `metrics.go` 结构化 | N/A | 中 |
| ❌ 未做 | `SendQueue` 统计简化 | ~50 行 | 低 |
