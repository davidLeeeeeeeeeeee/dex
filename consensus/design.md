# Consensus 模块设计文档

## 📌 1. 模块概述

本模块实现了基于 **Snowman** 协议的共识机制，是 Avalanche 共识家族的一员，支持链式区块结构的 BFT 共识。

```mermaid
flowchart LR
    subgraph Consensus模块
        SE[SnowmanEngine<br>共识引擎]
        SB[Snowball<br>投票算法]
        PM[ProposalManager<br>提案管理]
        QM[QueryManager<br>查询管理]
        MH[MessageHandler<br>消息处理]
        GM[GossipManager<br>广播管理]
        SM[SyncManager<br>同步管理]
    end

    PM -->|提出区块| SE
    QM -->|发起查询| SE
    SE -->|投票统计| SB
    MH -->|处理消息| QM
    MH -->|处理消息| GM
    MH -->|处理消息| SM

    style SE fill:#dfefff,stroke:#6b8fd6
    style SB fill:#eaffea,stroke:#4f8f00
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
      超时处理
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
    SAMPLE --> QUERY[发送 Query 请求偏好]
    QUERY --> COLLECT[收集 Chits 响应]
    COLLECT --> CHECK{票数 >= α?}

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

    style FINALIZE fill:#eaffea,stroke:#4f8f00
```

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

```mermaid
sequenceDiagram
    participant A as Node A (提议者)
    participant B as Node B
    participant C as Node C
    participant D as Node D

    Note over A: 提议新区块 Block-X

    A->>B: PushQuery(Block-X) 携带完整区块
    A->>C: PushQuery(Block-X)
    A->>D: PushQuery(Block-X)

    B->>B: 存储区块
    C->>C: 存储区块
    D->>D: 存储区块

    B-->>A: Chits(preference=Block-X)
    C-->>A: Chits(preference=Block-X)
    D-->>A: Chits(preference=Block-X)

    A->>A: 统计投票 (3 >= α)
    A->>A: confidence++

    Note over A: 持续查询直到 confidence >= β
    A->>A: 区块最终化
```

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
| `MaxConcurrentQueries` | 最大并发查询数 | 4 |
| `ProposalInterval` | 提案检查间隔 | 100ms |
| `GossipInterval` | Gossip 间隔 | 500ms |
| `GossipFanout` | Gossip 扇出 | 8 |
| `SyncBehindThreshold` | 触发同步的落后高度 | 10 |
| `SnapshotThreshold` | 触发快照同步的落后高度 | 100 |
| `SyncBatchSize` | 同步批量大小 | 50 |

