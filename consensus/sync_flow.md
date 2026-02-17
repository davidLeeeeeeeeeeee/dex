# 共识同步流程详解

> 源码文件：`consensus/syncManager.go`、`consensus/queryManager.go`、`consensus/messageHandler.go`、`consensus/realBlockStore.go`

---

## 📌 目录

1. [整体架构](#1-整体架构)
2. [触发路径：两条入口](#2-触发路径两条入口)
3. [采样验证（Quorum）](#3-采样验证quorum)
4. [三种同步模式](#4-三种同步模式)
5. [同步响应处理：HandleSyncResponse](#5-同步响应处理handlesyncresponse)
6. [VRF 签名集合验证](#6-vrf-签名集合验证)
7. [超时与容错机制](#7-超时与容错机制)
8. [PendingBlockBuffer 补课机制](#8-pendingblockbuffer-补课机制)
9. [关键配置参数](#9-关键配置参数)
10. [完整流程总览图](#10-完整流程总览图)

---

## 1. 整体架构

同步管理器 `SyncManager` 负责让落后节点追赶网络最新状态。它有**两条触发路径**和**三种同步模式**：

```
触发路径:
  ├── 路径A: 定时轮询 (checkAndSync, 每 CheckInterval 触发)
  └── 路径B: Chit 事件驱动 (TriggerSyncFromChit, 实时触发)

同步模式:
  ├── 普通同步 (requestSync)          —— 单节点批量拉取
  ├── 分片并行同步 (requestSyncParallel) —— 多节点并发拉取
  └── 快照同步 (requestSnapshotSync)   —— 大幅度落后时使用
```

```mermaid
flowchart TB
    subgraph 触发层
        A["定时轮询<br>pollPeerHeights<br>+ checkAndSync"]
        B["Chit 事件驱动<br>HandleChit → TriggerSyncFromChit"]
    end

    subgraph 决策层
        C{"落后幅度判断"}
        D["采样验证<br>Quorum"]
    end

    subgraph 执行层
        E["普通同步<br>requestSync"]
        F["并行同步<br>requestSyncParallel"]
        G["快照同步<br>requestSnapshotSync"]
    end

    subgraph 处理层
        H["HandleSyncResponse<br>接收→存储→最终化→续传"]
        I["HandleSnapshotResponse<br>加载快照→续传"]
    end

    A --> D
    D --> C
    B --> C
    C -->|"差距 ≤ BatchSize"| E
    C -->|"差距适中"| F
    C -->|"差距 > SnapshotThreshold"| G
    E --> H
    F --> H
    G --> I
    I -->|"还有差距"| E

    style A fill:#fff3cd,stroke:#d6a735
    style B fill:#dfefff,stroke:#6b8fd6
    style D fill:#eaffea,stroke:#4f8f00
    style G fill:#ffe8d6,stroke:#d67f35
```

---

## 2. 触发路径：两条入口

### 2.1 路径 A：定时轮询（兜底机制）

`SyncManager.Start()` 启动 **3 个后台循环**：

| 循环 | 间隔 | 职责 |
|------|------|------|
| `checkAndSync` | `CheckInterval` (30s) | 检查是否落后，启动采样/同步 |
| `pollPeerHeights` | `CheckInterval` (30s)，落后时 500ms | 向随机节点询问高度 |
| `processTimeouts` | 1s | 清理超时请求、采样、处理中范围 |

**高度探测流程：**

```mermaid
sequenceDiagram
    participant A as 本节点
    participant B as Peer-1
    participant C as Peer-2

    Note over A: pollPeerHeights 触发

    A->>B: MsgHeightQuery
    A->>C: MsgHeightQuery
    B-->>A: MsgHeightResponse(height=1000)
    C-->>A: MsgHeightResponse(height=1002)

    Note over A: 更新 PeerHeights 映射表<br>PeerHeights[B]=1000, PeerHeights[C]=1002
```

**checkAndSync 决策流程：**

```mermaid
flowchart TD
    START["checkAndSync()"] --> SYNCING{正在同步?}
    SYNCING -->|是| RETURN1[直接返回]
    SYNCING -->|否| SAMPLING{正在采样?}

    SAMPLING -->|是| EVAL_QUORUM["评估 Quorum"]
    EVAL_QUORUM --> QUORUM_OK{Quorum 达成?}
    QUORUM_OK -->|否| RETURN2[等待更多响应]
    QUORUM_OK -->|是| DECIDE_MODE{"heightDiff ><br>SnapshotThreshold?"}

    SAMPLING -->|否| CALC_DIFF["计算高度差<br>maxPeerHeight - localAccepted"]
    CALC_DIFF --> BEHIND{heightDiff ><br>BehindThreshold?}
    BEHIND -->|否| RETURN3[不需要同步]
    BEHIND -->|是| START_SAMPLE["启动采样验证<br>startHeightSampling()"]

    DECIDE_MODE -->|是| SNAPSHOT["快照同步"]
    DECIDE_MODE -->|否| BLOCK_SYNC["区块同步<br>requestSync()"]

    style START fill:#fff3cd,stroke:#d6a735
    style SNAPSHOT fill:#ffe8d6,stroke:#d67f35
    style BLOCK_SYNC fill:#dfefff,stroke:#6b8fd6
    style START_SAMPLE fill:#eaffea,stroke:#4f8f00
```

### 2.2 路径 B：Chit 事件驱动（快速响应）

当 `QueryManager.HandleChit()` 收到投票响应时，如果对方的 `AcceptedHeight > localAccepted`，会立即调用 `TriggerSyncFromChit()`。

这条路径不依赖定时器，**实时感知**落后。

**Chit 触发的完整流程：**

```mermaid
flowchart TD
    CHIT["HandleChit()<br>收到投票响应"] --> CMP{"peerAcceptedHeight ><br>localAccepted?"}
    CMP -->|否| IGNORE[忽略]
    CMP -->|是| TRIGGER["TriggerSyncFromChit()"]

    TRIGGER --> HARD{"heightDiff >= chitHardGap?<br>(默认 3)"}
    HARD -->|是| HARD_PATH["硬触发路径"]
    HARD -->|否| SOFT{"heightDiff >= chitSoftGap?<br>(默认 1)"}
    SOFT -->|否| IGNORE2[差距太小，忽略]
    SOFT -->|是| SOFT_PATH["软触发路径<br>记录 pending + 等待"]

    subgraph 硬触发路径
        HARD_PATH --> STALE1{旧状态残留?}
        STALE1 -->|是| DEFER1["延迟 200ms 重评估"]
        STALE1 -->|否| COOLDOWN1{"冷却期未过?"}
        COOLDOWN1 -->|是| DEFER2["等待冷却结束"]
        COOLDOWN1 -->|否| EXECUTE1["立即执行同步"]
    end

    subgraph 软触发路径
        SOFT_PATH --> MARK["markPendingChit"]
        MARK --> SCHEDULE["scheduleChitEvaluation"]
        SCHEDULE --> EVAL["evaluatePendingChitTrigger()"]
        EVAL --> STALE2{旧状态残留?}
        STALE2 -->|是| DEFER3["延迟重评估"]
        STALE2 -->|否| COOLDOWN2{"冷却期内?"}
        COOLDOWN2 -->|是| DEFER4["等待冷却"]
        COOLDOWN2 -->|否| GRACE{"grace period 已过?"}
        GRACE -->|否| DEFER5["等待 grace period"]
        GRACE -->|是| CONFIRM{"minConfirmPeers<br>足够确认?"}
        CONFIRM -->|否| DEFER6["等待更多确认"]
        CONFIRM -->|是| EXECUTE2["执行同步"]
    end

    EXECUTE1 --> PERFORM["performTriggeredSync()"]
    EXECUTE2 --> PERFORM

    style CHIT fill:#dfefff,stroke:#6b8fd6
    style EXECUTE1 fill:#eaffea,stroke:#4f8f00
    style EXECUTE2 fill:#eaffea,stroke:#4f8f00
```

**Chit 触发的防抖参数：**

| 参数 | 含义 | 默认值 |
|------|------|--------|
| `ChitSoftGap` | 最小触发差距 | 1 |
| `ChitHardGap` | 立即触发差距 | 3 |
| `ChitGracePeriod` | 软触发等待期 | 1s |
| `ChitCooldown` | 两次触发间冷却 | 1.5s |
| `ChitMinConfirmPeers` | 最少确认节点数 | 2 |

---

## 3. 采样验证（Quorum）

在**路径 A**中，当检测到落后超过 `BehindThreshold` 后，不会直接同步，而是先**采样验证**——确认多数节点确实在该高度，避免被单个恶意节点误导。

```mermaid
sequenceDiagram
    participant A as 本节点
    participant S1 as 采样节点-1
    participant S2 as 采样节点-2
    participant S3 as 采样节点-3

    Note over A: startHeightSampling()<br>采样 SampleSize=15 个节点

    A->>S1: MsgHeightQuery
    A->>S2: MsgHeightQuery
    A->>S3: MsgHeightQuery

    S1-->>A: MsgHeightResponse(height=500)
    S2-->>A: MsgHeightResponse(height=502)
    S3-->>A: MsgHeightResponse(height=500)

    Note over A: 下一轮 checkAndSync 中<br>调用 evaluateSampleQuorum()

    Note over A: 计算 Quorum:<br>required = SampleSize × QuorumRatio<br>= 15 × 0.67 = 10<br>响应数 >= 10 且 67%+ 支持某高度<br>→ Quorum 达成
```

**Quorum 评估算法：**

```
对每个候选高度 H：
  统计 sampleResponses 中 height >= H 的节点数 → supportCount
  如果 supportCount >= required 且 H > maxQuorumHeight：
    maxQuorumHeight = H

返回 maxQuorumHeight（满足 Quorum 的最高高度）
```

---

## 4. 三种同步模式

### 4.1 普通同步 `requestSync(from, to)`

适用于小范围同步（≤5 个块或只有 1 个可用 Peer）。

```mermaid
sequenceDiagram
    participant A as 本节点
    participant B as 目标 Peer

    A->>A: 检查去重 (InFlightSyncRanges)
    A->>A: 标记 Syncing=true
    A->>B: MsgSyncRequest(from=101, to=150)
    B->>B: GetBlocksFromHeight(101, 150)
    B->>B: 附加 SignatureSets (VRF 证据)
    B-->>A: MsgSyncResponse(50个区块+签名集)
    A->>A: HandleSyncResponse()
```

### 4.2 分片并行同步 `requestSyncParallel(from, to)`

适用于中等范围同步，将高度范围分配给多个 Peer 并发拉取。

```mermaid
sequenceDiagram
    participant A as 本节点
    participant P1 as Peer-1
    participant P2 as Peer-2
    participant P3 as Peer-3

    Note over A: 高度范围 101-250<br>ParallelPeers=3

    A->>A: 计算分片<br>P1: 101-150<br>P2: 151-200<br>P3: 201-250

    par 并行请求
        A->>P1: MsgSyncRequest(101-150, syncID=1)
        A->>P2: MsgSyncRequest(151-200, syncID=2)
        A->>P3: MsgSyncRequest(201-250, syncID=3)
    end

    P1-->>A: MsgSyncResponse(50 blocks)
    P2-->>A: MsgSyncResponse(50 blocks)
    P3-->>A: MsgSyncResponse(50 blocks)

    Note over A: 分别处理每个响应
```

**ShortTxs 模式判断：**  
当 `totalBlocks <= ShortSyncThreshold`（默认 20）时，启用 ShortTxs 模式：
- 发送方附带 `ShortTxs`（交易短哈希）
- 接收方从本地 TxPool 还原完整交易
- 减少网络传输量

### 4.3 快照同步 `requestSnapshotSync(targetHeight)`

适用于大幅度落后（`heightDiff > SnapshotThreshold`，默认 100）。

```mermaid
sequenceDiagram
    participant A as 本节点（落后 500 块）
    participant B as 目标 Peer

    A->>A: 选择 height >= targetHeight 的 Peer
    A->>B: MsgSnapshotRequest(height=1000)
    B->>B: GetLatestSnapshot()
    B-->>A: MsgSnapshotResponse(快照数据)

    A->>A: LoadSnapshot(snapshot)
    A->>A: 发布 EventSnapshotLoaded

    Note over A: 快照加载后检查<br>是否还需补充区块

    alt maxPeerHeight > currentHeight + 1
        A->>B: MsgSyncRequest(继续同步剩余区块)
    end
```

---

## 5. 同步响应处理：HandleSyncResponse

这是同步流程中**最核心**的函数，处理从 Peer 返回的区块数据。

```mermaid
flowchart TD
    RECV["HandleSyncResponse()"] --> VALIDATE_ID{"SyncID 匹配?"}
    VALIDATE_ID -->|否| DROP[丢弃]
    VALIDATE_ID -->|是| PROCESS["遍历所有区块"]

    PROCESS --> SHORT{"ShortMode<br>+ 有 ShortTxs?"}
    SHORT -->|是| BUFFER["放入 PendingBlockBuffer<br>异步还原"]
    SHORT -->|否| TRY_ADD["store.Add(block)"]

    TRY_ADD --> ADD_OK{添加成功?}
    ADD_OK -->|是, isNew| COUNT_UP["added++"]
    ADD_OK -->|失败| ERROR_CHECK{"block data<br>incomplete?"}
    ERROR_CHECK -->|是| BUFFER2["放入 PendingBlockBuffer"]
    ERROR_CHECK -->|否| LOG_ERR["记录错误"]

    COUNT_UP --> FAST_FINALIZE
    BUFFER --> FAST_FINALIZE
    BUFFER2 --> FAST_FINALIZE
    LOG_ERR --> FAST_FINALIZE

    FAST_FINALIZE["加速最终化循环"] --> LOOP_START["从 acceptedHeight+1 开始"]
    LOOP_START --> FIND_NEXT{"在 sync 响应中<br>找到下一高度的区块?"}
    FIND_NEXT -->|否| LOOP_END["退出循环"]
    FIND_NEXT -->|是| PARENT_MATCH{"ParentID == acceptedID?"}
    PARENT_MATCH -->|是| CHOSEN["选为目标区块"]
    PARENT_MATCH -->|否| FALLBACK["退化选第一个候选"]
    CHOSEN --> VERIFY_SIG{"有 SignatureSet?<br>验证通过?"}
    FALLBACK --> VERIFY_SIG
    VERIFY_SIG -->|验证失败| LOOP_END
    VERIFY_SIG -->|通过或无签名| SET_FINAL["SetFinalized(height, blockID)"]
    SET_FINAL --> FINALIZED{成功?}
    FINALIZED -->|是| PUBLISH["发布 EventBlockFinalized<br>finalized++"]
    FINALIZED -->|否, ErrAlreadyFinalized| SKIP["跳过，继续"]
    FINALIZED -->|否, 其他错误| LOOP_END
    PUBLISH --> LOOP_START
    SKIP --> LOOP_START

    LOOP_END --> STALL_CHECK{"added=0 且<br>toHeight > accepted?"}
    STALL_CHECK -->|是| STALL_INC["consecutiveStallCount++"]
    STALL_INC --> STALL_THRESHOLD{"stalls >= 3?"}
    STALL_THRESHOLD -->|是| SWITCH_PEER["清理该 Peer<br>强制重新采样"]
    STALL_THRESHOLD -->|否| SIGNAL

    STALL_CHECK -->|否| SIGNAL["信号精准化"]
    SIGNAL --> CLOSE_GAP{"距最大 Peer<br>差距 < BatchSize?"}
    CLOSE_GAP -->|是| EMIT_COMPLETE["发布 EventSyncComplete"]
    CLOSE_GAP -->|否| SKIP_SIGNAL["跳过信号<br>避免唤醒无效查询"]

    EMIT_COMPLETE --> PIPELINE
    SKIP_SIGNAL --> PIPELINE
    SWITCH_PEER --> DONE

    PIPELINE["流水线续传"] --> STILL_BEHIND{"还落后?"}
    STILL_BEHIND -->|是| NEXT_SYNC["延迟 50ms<br>requestSync(下一批)"]
    STILL_BEHIND -->|否| DONE["完成"]

    style RECV fill:#dfefff,stroke:#6b8fd6
    style FAST_FINALIZE fill:#eaffea,stroke:#4f8f00
    style SWITCH_PEER fill:#ffe8d6,stroke:#d67f35
```

### 关键设计点

1. **加速最终化**：不依赖共识轮次，直接按父链关系推进 `lastAccepted`，解决"本地已有区块但共识迟迟无法收敛"的问题。

2. **只用 sync 响应中的区块**：不混入本地 store 的候选，因为本地可能有未被选中分支的区块（不同 Window/不同 parent），混入会导致父链不兼容。

3. **流水线续传**：每轮同步完成后，如果仍落后，延迟 50ms 后立即发起下一轮，实现高效追块。

4. **停滞保护**：连续 3 轮 added=0 后，清理当前 Peer 信息，强制重新采样，避免"死盯一个坏 Peer"。

---

## 6. VRF 签名集合验证

同步响应中附带 `SignatureSets`，在加速最终化前进行 **四步验证**：

```mermaid
flowchart TD
    INPUT["VerifySignatureSet()"] --> STEP1{"① 轮次完整性<br>len(rounds) >= beta?"}
    STEP1 -->|否| FAIL["❌ 验证失败"]
    STEP1 -->|是| STEP2{"② 每轮签名充足<br>len(sigs) >= alpha?"}
    STEP2 -->|否| FAIL
    STEP2 -->|是| STEP3{"③ 采样合法性<br>重演 VRF 确定性采样<br>确认签名者在合法采样集中?"}
    STEP3 -->|否| FAIL
    STEP3 -->|是| STEP4{"④ ECDSA 签名验证<br>重算 digest 并验签"}
    STEP4 -->|否| FAIL
    STEP4 -->|是| PASS["✅ 验证通过"]

    style PASS fill:#eaffea,stroke:#4f8f00
    style FAIL fill:#ffcccc,stroke:#cc0000
```

| 步骤 | 验证内容 | 防御目标 |
|------|---------|---------|
| ① 轮次完整性 | 至少 β 轮成功 | 防止伪造快速最终化 |
| ② 签名充足 | 每轮至少 α 个签名 | 防止少数节点串谋 |
| ③ 采样合法性 | 签名者在 VRF 确定性采样集中 | 防止选择性签名者 |
| ④ 密码学验签 | ECDSA 签名正确 | 防止签名伪造 |

---

## 7. 超时与容错机制

### 7.1 processTimeouts（每 1 秒执行）

```mermaid
flowchart TD
    TICK["processTimeouts()"] --> CHECK_SYNC{正在同步?}
    CHECK_SYNC -->|是| SCAN_REQ["扫描 SyncRequests"]
    SCAN_REQ --> REQ_TIMEOUT{"请求超过<br>Timeout(10s)?"}
    REQ_TIMEOUT -->|是| DEL_REQ["删除超时请求"]
    REQ_TIMEOUT -->|否| NEXT_REQ[继续]
    DEL_REQ --> ALL_GONE{"所有请求都超时?"}
    ALL_GONE -->|是| RESET_SYNC["Syncing=false<br>usingSnapshot=false"]

    CHECK_SYNC -->|否| CHECK_SAMPLE{正在采样?}
    CHECK_SAMPLE -->|是| SAMPLE_TIMEOUT{"超过<br>SampleTimeout(2s)?"}
    SAMPLE_TIMEOUT -->|是| RESET_SAMPLE["sampling=false"]

    TICK --> CLEAN_RANGE["清理过期 InFlightSyncRanges<br>(Timeout×2)"]

    style TICK fill:#fff3cd,stroke:#d6a735
    style RESET_SYNC fill:#ffe8d6,stroke:#d67f35
```

### 7.2 状态保护机制

| 机制 | 作用 |
|------|------|
| `InFlightSyncRanges` 去重 | 防止同一高度范围重复请求 |
| `consecutiveStallCount` | 检测停滞，3次后切换 Peer |
| `chitCooldown` | 两次 Chit 触发间最少 1.5s 间隔 |
| `chitGracePeriod` | 软触发需等待 1s，收集更多证据 |
| `resetStaleSyncState` | 清理残留的 Syncing/sampling 状态 |

---

## 8. PendingBlockBuffer 补课机制

当同步收到的区块**缺失交易数据**时，会进入 `PendingBlockBuffer` 进行异步补课：

```mermaid
flowchart TD
    TRIGGER["区块数据不完整<br>或 ShortTxs 模式"] --> ADD["AddPendingBlockForConsensus()"]
    ADD --> TRY_RESOLVE["尝试从 TxPool 还原"]
    TRY_RESOLVE --> RESOLVED{还原成功?}
    RESOLVED -->|是| SUCCESS["回调 onSuccess<br>注入共识"]
    RESOLVED -->|否| QUEUE["加入待处理队列"]
    QUEUE --> FETCH["fetchMissingTxs()<br>主动拉取缺失交易"]
    FETCH --> RETRY_LOOP["retryLoop<br>每 200ms~2s 重试"]
    RETRY_LOOP --> RETRY["retryResolve()"]
    RETRY --> RESOLVED2{还原成功?}
    RESOLVED2 -->|是| SUCCESS
    RESOLVED2 -->|否, 重试次数 < 5| RETRY_LOOP
    RESOLVED2 -->|否, 超过上限| DISCARD["丢弃"]

    style TRIGGER fill:#ffe8d6,stroke:#d67f35
    style SUCCESS fill:#eaffea,stroke:#4f8f00
```

---

## 9. 关键配置参数

| 参数 | 所属配置 | 含义 | 默认值 |
|------|---------|------|--------|
| `CheckInterval` | SyncConfig | 定时检查间隔 | 30s |
| `BehindThreshold` | SyncConfig | 触发采样的最小落后块数 | 2 |
| `BatchSize` | SyncConfig | 单次同步最大区块数 | 50 |
| `Timeout` | SyncConfig | 同步请求超时 | 10s |
| `SnapshotThreshold` | SyncConfig | 触发快照同步的落后块数 | 100 |
| `ShortSyncThreshold` | SyncConfig | 启用 ShortTxs 模式的阈值 | 20 |
| `ParallelPeers` | SyncConfig | 并行同步节点数 | 3 |
| `SampleSize` | SyncConfig | 采样验证节点数 | 15 |
| `QuorumRatio` | SyncConfig | Quorum 比例 | 0.67 |
| `SampleTimeout` | SyncConfig | 采样超时 | 2s |
| `SyncAlpha` | SyncConfig | 签名验证 α | 14 |
| `SyncBeta` | SyncConfig | 签名验证 β | 15 |
| `ChitSoftGap` | SyncConfig | Chit 软触发差距 | 1 |
| `ChitHardGap` | SyncConfig | Chit 硬触发差距 | 3 |
| `ChitGracePeriod` | SyncConfig | Chit 软触发等待期 | 1s |
| `ChitCooldown` | SyncConfig | Chit 触发冷却期 | 1.5s |
| `ChitMinConfirmPeers` | SyncConfig | Chit 最少确认节点 | 2 |

---

## 10. 完整流程总览图

```mermaid
flowchart TB
    %% ===== 触发层 =====
    subgraph 触发层["🔔 触发层"]
        A1["⏰ 定时轮询<br>pollPeerHeights()"]
        A2["📨 Chit 事件驱动<br>HandleChit()"]
    end

    %% ===== 发现差距 =====
    A1 --> POLL["向 10 个随机节点<br>发送 HeightQuery"]
    POLL --> RESP["收集 HeightResponse<br>更新 PeerHeights"]
    RESP --> CHECK_SYNC["checkAndSync()"]

    A2 --> CHIT_CMP{"peerAccepted ><br>localAccepted?"}
    CHIT_CMP -->|否| NOOP["忽略"]
    CHIT_CMP -->|是| TRIGGER_SYNC["TriggerSyncFromChit()"]

    %% ===== 采样验证 =====
    CHECK_SYNC --> BEHIND{落后 ><br>BehindThreshold?}
    BEHIND -->|否| NOOP2[不需要同步]
    BEHIND -->|是| SAMPLE["startHeightSampling()<br>采样 15 个节点"]
    SAMPLE --> QUORUM["下一轮 evaluateSampleQuorum()"]
    QUORUM --> QUORUM_OK{67%+ 确认?}
    QUORUM_OK -->|否| WAIT["等待更多响应"]
    QUORUM_OK -->|是| DECIDE

    %% ===== 防抖 =====
    TRIGGER_SYNC --> DEBOUNCE["防抖机制<br>soft/hard gap<br>grace period<br>cooldown<br>min confirm peers"]
    DEBOUNCE --> DECIDE

    %% ===== 决策 =====
    DECIDE{"heightDiff 判断"}
    DECIDE -->|"> SnapshotThreshold<br>(100)"| SNAP_SYNC["requestSnapshotSync()"]
    DECIDE -->|"适中 + 多 Peer"| PARA_SYNC["requestSyncParallel()"]
    DECIDE -->|"≤5 或 1 Peer"| NORM_SYNC["requestSync()"]

    %% ===== 执行同步 =====
    SNAP_SYNC --> SNAP_REQ["发送 MsgSnapshotRequest"]
    SNAP_REQ --> SNAP_RESP["HandleSnapshotResponse()<br>LoadSnapshot()"]
    SNAP_RESP --> SNAP_CONTINUE{"还有差距?"}
    SNAP_CONTINUE -->|是| NORM_SYNC

    PARA_SYNC --> SHARD["分片: 高度范围 ÷ 节点数"]
    SHARD --> PARALLEL["并行发送 MsgSyncRequest"]
    PARALLEL --> HANDLE_RESP

    NORM_SYNC --> SINGLE["发送 MsgSyncRequest"]
    SINGLE --> HANDLE_RESP

    %% ===== 响应处理 =====
    HANDLE_RESP["HandleSyncResponse()"]
    HANDLE_RESP --> ADD_BLOCKS["遍历区块:<br>store.Add() 或 PendingBuffer"]
    ADD_BLOCKS --> FAST_FIN["加速最终化循环:<br>按父链推进 lastAccepted"]
    FAST_FIN --> VERIFY["验证 SignatureSet<br>(4 步验证)"]
    VERIFY --> SET_FIN["SetFinalized()"]
    SET_FIN --> PUBLISH["发布 EventBlockFinalized"]
    PUBLISH --> STALL{"停滞检测<br>stalls >= 3?"}
    STALL -->|是| SWITCH["切换 Peer"]
    STALL -->|否| PIPELINE{"还落后?"}
    PIPELINE -->|是| NORM_SYNC
    PIPELINE -->|否| DONE["✅ 同步完成<br>发布 EventSyncComplete"]

    style A1 fill:#fff3cd,stroke:#d6a735
    style A2 fill:#dfefff,stroke:#6b8fd6
    style SAMPLE fill:#eaffea,stroke:#4f8f00
    style SNAP_SYNC fill:#ffe8d6,stroke:#d67f35
    style DONE fill:#eaffea,stroke:#4f8f00
    style SWITCH fill:#ffcccc,stroke:#cc0000
```

---

## 附录：消息类型一览

| 消息类型 | 方向 | 说明 |
|---------|------|------|
| `MsgHeightQuery` | 请求 | 询问 Peer 当前已最终化高度 |
| `MsgHeightResponse` | 响应 | 返回自己的已最终化高度 |
| `MsgSyncRequest` | 请求 | 请求指定高度范围的区块 |
| `MsgSyncResponse` | 响应 | 返回区块 + ShortTxs + SignatureSets |
| `MsgSnapshotRequest` | 请求 | 请求最新快照 |
| `MsgSnapshotResponse` | 响应 | 返回快照数据 |
