# Consensus 模块设计文档

## 📌 1. 模块概述

本模块实现了基于 **Snowman** 协议的共识机制，是 Avalanche 共识家族的一员，支持链式区块结构的 BFT 共识。

```mermaid
flowchart LR
    subgraph Consensus
        SE["SnowmanEngine<br/>共识引擎<br/>-----<br/>1s: checkTimeouts"]
        SB[Snowball<br/>投票算法]
        PM["ProposalManager<br/>提案管理<br/>-----<br/>100ms: proposeBlock"]
        QM["QueryManager<br/>查询管理<br/>-----<br/>107ms: tryIssueQuery"]
        MH[MessageHandler<br/>消息处理]
        GM["GossipManager<br/>广播管理<br/>-----<br/>500ms: gossipNewBlocks"]
        SM["SyncManager<br/>同步管理<br/>-----<br/>1s: pollPeerHeights<br/>checkAndSync"]
    end

    PM -->|提出区块| SE
    QM -->|发起查询| SE
    SE -->|投票统计| SB
    MH -->|处理消息| QM
    MH -->|处理消息| GM
    MH -->|处理消息| SM

    style SE fill:#dfefff,stroke:#6b8fd6
    style SB fill:#eaffea,stroke:#4f8f00
    style PM fill:#fff3cd,stroke:#d6a735
    style QM fill:#fff3cd,stroke:#d6a735
    style GM fill:#fff3cd,stroke:#d6a735
    style SM fill:#fff3cd,stroke:#d6a735
```

---

## 📌 2. 核心组件

```mermaid
mindmap
  root((Consensus))
    SnowmanEngine
      管理 Snowball 实例
      处理投票结果
      区块最终化
      查询超时检测 checkTimeouts
    Snowball
      偏好追踪
      置信度累积
      最终化判断
    ProposalManager
      VRF 出块资格
      Window 机制
      区块提案
    QueryManager
      PushQuery/PullQuery
      Chits 投票收集
      响应超时事件
    MessageHandler
      消息路由
      区块缓存
      待处理查询
    GossipManager
      区块广播
      去重机制
    SyncManager
      高度同步
      快照同步
      批量区块同步
```

---

## 📌 3. Snowball 共识算法

### 3.1 核心参数

| 参数 | 含义 | 典型值 |
|------|------|--------|
| **K** | 每轮采样节点数 | 20 |
| **α (Alpha)** | 达成共识所需最小票数 | 15 |
| **β (Beta)** | 最终化所需连续成功轮数 | 20 |

### 3.2 算法流程

```mermaid
flowchart TD
    START[开始] --> SAMPLE[随机采样 K 个节点]
    SAMPLE --> TYPE{查询类型}

    TYPE -->|PushQuery<br>提议者使用| PUSH[发送 PushQuery<br>携带完整区块]
    TYPE -->|PullQuery<br>非提议者使用| PULL[发送 PullQuery<br>仅携带区块ID]

    PUSH --> PEER_STORE[对方存储区块]
    PEER_STORE --> PEER_VOTE[对方投票]

    PULL --> PEER_CHECK{对方有区块?}
    PEER_CHECK -->|是| PEER_VOTE
    PEER_CHECK -->|否| PEER_GET[对方发送 Get 请求]
    PEER_GET --> SEND_PUT[返回 Put 区块数据]
    SEND_PUT --> PEER_STORE2[对方存储区块]
    PEER_STORE2 --> PEER_VOTE

    PEER_VOTE --> COLLECT[收集 Chits 响应]

    COLLECT --> TIMEOUT{超时检查<br>checkTimeouts}
    TIMEOUT -->|超时| EXPIRE[移除过期查询<br>发布 QueryComplete]
    TIMEOUT -->|未超时| CHECK{票数 >= α?}
    EXPIRE --> SAMPLE

    CHECK -->|是| SAME{与当前偏好相同?}
    CHECK -->|否| FALLBACK[选择字典序最大区块]

    SAME -->|是| INCR[confidence++]
    SAME -->|否| SWITCH[切换偏好<br>confidence = 1]

    FALLBACK --> RESET[confidence = 0]

    INCR --> FINAL{confidence >= β?}
    SWITCH --> FINAL
    RESET --> FINAL

    FINAL -->|是| FINALIZE[区块最终化 ✓]
    FINAL -->|否| SAMPLE

    style PUSH fill:#dfefff,stroke:#6b8fd6
    style PULL fill:#fff3cd,stroke:#d6a735
    style PEER_GET fill:#ffe8d6,stroke:#d67f35
    style SEND_PUT fill:#ffe8d6,stroke:#d67f35
    style FINALIZE fill:#eaffea,stroke:#4f8f00
    style TIMEOUT fill:#fff3cd,stroke:#d6a735
    style EXPIRE fill:#ffe8d6,stroke:#d67f35
```

#### PushQuery vs PullQuery 对比

| 特性 | PushQuery | PullQuery |
|------|-----------|-----------|
| **使用者** | 区块提议者 | 非提议者（收到 Gossip 后） |
| **携带数据** | 完整区块 | 仅区块ID |
| **网络开销** | 较大（每次传输区块） | 较小（仅ID） |
| **延迟** | 低（对方直接投票） | 可能高（需额外 Get/Put） |
| **适用场景** | 首次广播新区块 | 后续查询或同步后查询 |

---

## 📌 4. 区块提案流程

### 4.1 Window 机制

```mermaid
flowchart LR
    subgraph Window时间窗口
        W0[Window 0<br>概率 5%]
        W1[Window 1<br>概率 15%]
        W2[Window 2<br>概率 30%]
        W3[Window 3<br>概率 100%]
    end

    W0 -->|超时| W1
    W1 -->|超时| W2
    W2 -->|超时| W3

    VRF[VRF 随机数] --> CHECK{VRF < 阈值?}
    CHECK -->|是| PROPOSE[允许提案]
    CHECK -->|否| WAIT[等待下一窗口]
```

### 4.2 提案时序

```mermaid
sequenceDiagram
    participant P as Proposer
    participant TX as TxPool
    participant S as BlockStore
    participant E as EventBus

    P->>P: 检查 Window 和 VRF
    P->>TX: GetPendingTxs()
    TX-->>P: 返回待打包交易
    P->>P: 排序交易 (按 FB 余额)
    P->>P: 生成 VRF 证明
    P->>P: 构造区块
    P->>S: Add(block)
    S-->>P: 添加成功
    P->>E: Publish(EventNewBlock)
```

---

## 📌 5. 查询与投票流程

### 5.1 消息类型

| 消息类型 | 发送者 | 用途 |
|----------|--------|------|
| **PushQuery** | 区块提议者 | 携带完整区块，请求投票 |
| **PullQuery** | 非提议者 | 仅携带区块ID，请求投票 |
| **Chits** | 被查询节点 | 返回偏好投票 |
| **Get** | 缺失区块的节点 | 请求区块数据 |
| **Put** | 持有区块的节点 | 响应区块数据 |
| **Gossip** | 任意节点 | 主动广播新区块 |

### 5.2 查询时序图

**PushQuery 只发给 K 个随机采样节点，不是所有矿工。** 未收到 PushQuery 的节点通过 Gossip 或 PullQuery 获取区块。

```mermaid
sequenceDiagram
    participant A as Node A (提议者)
    participant B as Node B (被采样)
    participant C as Node C (被采样)
    participant D as Node D (未被采样)
    participant E as Node E (未被采样)

    Note over A: 提议新区块 Block-X<br>随机采样 K=2 个节点 (B, C)

    par 并行: PushQuery 给采样节点
        A->>B: PushQuery(Block-X) 携带完整区块
        A->>C: PushQuery(Block-X)
    and 并行: Gossip 给 Fanout 个节点
        A->>D: Gossip(Block-X)
    end

    B->>B: 存储区块
    C->>C: 存储区块
    D->>D: 存储区块

    B-->>A: Chits(preference=Block-X)
    C-->>A: Chits(preference=Block-X)

    A->>A: 统计投票 (2 >= α)
    A->>A: confidence++

    Note over A: 持续查询直到 confidence >= β
    A->>A: 区块最终化

    Note over E: 未收到任何消息的节点<br>后续通过 PullQuery 获取区块

    Note over B: B 本地已有区块 X<br>开始自己的查询轮次
    B->>E: PullQuery(BlockID=X) 仅携带ID
    E->>E: 检查本地: 无区块 X
    E->>B: Get(BlockID=X)
    Note over B: B 本地有区块，可以响应
    B-->>E: Put(Block-X)
    E->>E: 存储区块
    E-->>B: Chits(preference=X)
```

> **注意**：发送 PullQuery 的节点**必须本地已有区块**。因为接收方可能发送 Get 请求，发送方需要能够响应并返回完整区块。

#### 区块传播路径总结

| 传播方式 | 发起者 | 接收者 | 携带数据 | 说明 |
|---------|--------|--------|---------|------|
| **PushQuery** | 提议者 | K 个采样节点 | 完整区块 | 首次查询，请求投票 |
| **Gossip** | 提议者 | Fanout 个节点 | 完整区块 | 主动广播，加速传播 |
| **PullQuery + Get/Put** | 任意节点 | 任意节点 | 仅ID → 按需获取 | 后续轮次或补漏 |

### 5.3 PullQuery 流程（非提议者）

```mermaid
sequenceDiagram
    participant A as Node A
    participant B as Node B (无区块)
    participant C as Node C

    A->>B: PullQuery(BlockID=X)
    B->>B: 检查本地: 无区块 X

    B->>A: Get(BlockID=X)
    A-->>B: Put(Block-X)

    B->>B: 存储区块
    B-->>A: Chits(preference=X)
```

### 5.4 查询超时处理 (checkTimeouts)

`SnowmanEngine.checkTimeouts()` 是共识引擎的**超时监控机制**，确保查询不会无限等待。

#### 工作原理

```mermaid
flowchart TD
    subgraph SnowmanEngine.Start
        TICKER[定时器<br>每 1 秒触发] --> CHECK[checkTimeouts]
    end

    subgraph checkTimeouts
        CHECK --> SCAN[扫描 activeQueries]
        SCAN --> COMPARE{now - startTime<br>> QueryTimeout?}
        COMPARE -->|是| EXPIRE[移除过期查询<br>加入 expired 列表]
        COMPARE -->|否| NEXT[继续下一个]
        EXPIRE --> NEXT
        NEXT --> DONE{扫描完成?}
        DONE -->|否| COMPARE
        DONE -->|是| PUBLISH{有过期查询?}
        PUBLISH -->|是| EVENT[发布 EventQueryComplete<br>Reason: timeout]
        PUBLISH -->|否| END[结束]
    end

    style TICKER fill:#fff3cd,stroke:#d6a735
    style EXPIRE fill:#ffe8d6,stroke:#d67f35
    style EVENT fill:#dfefff,stroke:#6b8fd6
```

#### 超时时序图

```mermaid
sequenceDiagram
    participant E as SnowmanEngine
    participant Q as activeQueries
    participant EB as EventBus
    participant QM as QueryManager

    Note over E: 每秒执行 checkTimeouts()

    E->>Q: 遍历所有活跃查询

    loop 对每个查询
        E->>E: 检查 now - startTime > QueryTimeout
        alt 已超时
            E->>Q: 删除该查询
            E->>E: 加入 expired 列表
        end
    end

    alt 有过期查询
        E->>EB: Publish(EventQueryComplete, timeout)
        EB-->>QM: 通知查询结束
        QM->>QM: 发起新一轮查询
    end
```

#### 为什么需要超时处理

| 场景 | 问题 | 超时处理的作用 |
|------|------|----------------|
| 网络分区 | 部分节点无法响应 Chits | 释放查询资源，允许重试 |
| 节点宕机 | 被查询节点不再响应 | 避免无限等待，继续共识 |
| 高负载 | 响应延迟超过阈值 | 防止查询堆积 |
| 恶意节点 | 故意不响应 | 限制 DoS 攻击影响 |

---

## 📌 6. 同步机制

### 6.1 同步策略

```mermaid
flowchart TD
    START[检测高度差] --> CHECK{差距大小?}

    CHECK -->|差距 > SnapshotThreshold| SNAPSHOT[快照同步]
    CHECK -->|差距 > BehindThreshold| BLOCK[区块同步]
    CHECK -->|差距较小| NORMAL[正常共识]

    SNAPSHOT --> LOAD[加载快照状态]
    LOAD --> CONTINUE[继续区块同步]

    BLOCK --> BATCH[批量请求区块]
    BATCH --> APPLY[应用区块]

    CONTINUE --> NORMAL
    APPLY --> NORMAL

    style SNAPSHOT fill:#ffe8d6,stroke:#d67f35
    style BLOCK fill:#dfefff,stroke:#6b8fd6
```

### 6.2 同步时序图

```mermaid
sequenceDiagram
    participant A as Node A (落后)
    participant B as Node B (领先)

    Note over A: 定期轮询节点高度

    A->>B: HeightQuery
    B-->>A: HeightResponse(height=1000)

    A->>A: 本地高度=900, 差距=100

    alt 差距 > SnapshotThreshold
        A->>B: SnapshotRequest
        B-->>A: SnapshotResponse(快照数据)
        A->>A: LoadSnapshot()
    else 差距 > BehindThreshold
        A->>B: SyncRequest(from=901, to=950)
        B-->>A: SyncResponse(50个区块)
        A->>A: 应用区块
    end

    A->>A: 发布 SyncComplete 事件
```

---

## 📌 7. Gossip 广播

```mermaid
flowchart TD
    subgraph 发送方
        NEW[新区块产生] --> CHECK{已广播过?}
        CHECK -->|否| SAMPLE[采样 Fanout 个节点]
        CHECK -->|是| SKIP[跳过]
        SAMPLE --> SEND[发送 Gossip 消息]
        SEND --> MARK[标记已广播]
    end

    subgraph 接收方
        RECV[收到 Gossip] --> DUP{已见过?}
        DUP -->|是| DROP[丢弃]
        DUP -->|否| STORE[存储区块]
        STORE --> FORWARD[延迟转发]
        FORWARD --> EVENT[发布 BlockReceived]
    end

    SEND -.-> RECV
```

---

## 📌 8. 消息处理流程

```mermaid
flowchart TD
    MSG[收到消息] --> TYPE{消息类型}

    TYPE -->|PullQuery| PQ[handlePullQuery]
    TYPE -->|PushQuery| PSQ[handlePushQuery]
    TYPE -->|Chits| CHIT[QueryManager.HandleChit]
    TYPE -->|Get| GET[handleGet]
    TYPE -->|Put| PUT[handlePut]
    TYPE -->|Gossip| GOS[GossipManager.HandleGossip]
    TYPE -->|SyncRequest| SR[SyncManager.HandleSyncRequest]
    TYPE -->|SyncResponse| SRS[SyncManager.HandleSyncResponse]

    PQ --> HAS{有区块?}
    HAS -->|是| SEND_CHIT[发送 Chits]
    HAS -->|否| REQ_BLOCK[发送 Get 请求]
    REQ_BLOCK --> PENDING[存入待处理队列]

    PSQ --> CACHE{Window 检查}
    CACHE -->|未来窗口| CACHE_BLOCK[缓存区块]
    CACHE -->|当前窗口| STORE_BLOCK[存储区块]
    STORE_BLOCK --> SEND_CHIT
```

---

## 📌 9. 区块最终化

```mermaid
sequenceDiagram
    participant E as SnowmanEngine
    participant SB as Snowball
    participant S as BlockStore
    participant EB as EventBus

    E->>SB: RecordVote(candidates, votes)
    SB->>SB: 统计投票，更新 preference
    SB->>SB: 更新 confidence

    alt confidence >= β
        SB-->>E: CanFinalize() = true
        E->>E: finalizeBlock(height, blockID)
        E->>S: SetLastAccepted(blockID)
        E->>EB: Publish(EventBlockFinalized)
    end
```

---

## 📌 10. 系统架构总览

```mermaid
flowchart TB
    subgraph Node
        T[Transport] --> MH[MessageHandler]
        MH --> QM[QueryManager]
        MH --> GM[GossipManager]
        MH --> SM[SyncManager]

        PM[ProposalManager] --> BS[BlockStore]
        QM --> SE[SnowmanEngine]
        SE --> SB[Snowball]
        SE --> BS

        EB[EventBus] -.-> PM
        EB -.-> QM
        EB -.-> GM
        EB -.-> SM
    end

    subgraph 外部
        TX[TxPool] --> PM
        DB[(Database)] --> BS
    end

    style SE fill:#dfefff,stroke:#6b8fd6
    style SB fill:#eaffea,stroke:#4f8f00
    style EB fill:#ffe8d6,stroke:#d67f35
```

---

## 📌 11. 关键配置参数

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `K` | 每轮采样节点数 | 20 |
| `Alpha` | 共识阈值 | 15 |
| `Beta` | 最终化阈值 | 20 |
| `QueryTimeout` | 查询超时时间 (checkTimeouts 检查间隔 1s) | 2s |
| `MaxConcurrentQueries` | 最大并发查询数 | 4 |
| `ProposalInterval` | 提案检查间隔 | 100ms |
| `GossipInterval` | Gossip 间隔 | 500ms |
| `GossipFanout` | Gossip 扇出 | 8 |
| `SyncBehindThreshold` | 触发同步的落后高度 | 10 |
| `SnapshotThreshold` | 触发快照同步的落后高度 | 100 |
| `SyncBatchSize` | 同步批量大小 | 50 |

