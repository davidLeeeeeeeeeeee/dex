---
name: Consensus
description: Guide for understanding and modifying the Snowball consensus protocol and block management.
triggers:
  - 共识
  - Snowball
  - 区块同步
  - 查询管理
  - QueryManager
  - SyncManager
---

# Consensus (共识层) 模块指南

该模块实现了基于 Snowball 的概率性共识协议。

## 目录结构

```
consensus/
├── snowball.go           # ⭐ Snowball 算法核心
├── consensusEngine.go    # 共识循环驱动
├── node.go               # 节点入口
│
├── queryManager.go       # 查询管理器（发起投票查询）
├── gossipManager.go      # Gossip 消息传播
├── syncManager.go        # 区块同步（含快照同步）
├── snapshotManager.go    # 快照管理
│
├── realProposer.go       # ⭐ 区块提案（含缓存机制）
├── realBlockStore.go     # 区块持久化
├── realTransport.go      # 网络传输
│
├── pending_block_buffer.go  # 待处理区块缓冲
├── messageHandler.go     # 消息处理分发
├── adapter.go            # 类型转换适配器
└── config.go             # 共识配置
```

## 核心概念

| 概念 | 说明 |
|:---|:---|
| **Snowball** | 概率性共识算法，通过多轮采样达成共识 |
| **Query** | 向对等节点发起查询以决定区块偏好 |
| **Gossip** | 传播区块 Header 和投票信息 |
| **BlockStore** | 负责区块的持久化和高度索引 |

## 新增核心组件

### QueryManager
负责发起和管理共识查询：
```go
// 发起查询
qm.issueQuery()

// 处理投票响应
qm.HandleChit(msg)

// 请求缺失区块（带限流）
qm.RequestBlock(blockID, fromNode)
```

### RealProposer 区块缓存
提案的区块在最终化前缓存在内存中：
```go
// 缓存区块
consensus.CacheBlock(block)

// 获取缓存
block, ok := consensus.GetCachedBlock(blockID)

// 清理低于指定高度的缓存
consensus.CleanupBlockCacheBelowHeight(height)
```

### SyncManager 快照同步
支持快照快速同步落后节点：
```go
// 请求快照同步（大幅落后时使用）
sm.requestSnapshotSync(targetHeight)

// 常规区块同步
sm.requestSync(fromHeight, toHeight)
```

## Block Header/Body 分离

区块现在分为 Header 和 Body：
```go
type BlockHeader struct {
    Height      uint64
    ParentID    string
    StateRoot   string   // JMT 状态根
    TxHash      string   // 交易累积哈希
    Proposer    string
    Window      int
    VRFProof    []byte
}
```

## 代码约定

- **Interface Driven**: `Real` 前缀是生产实现，`Simulated` 前缀用于测试
- **Concurrency**: 广泛使用 `sync.RWMutex` 保护状态
- **日志前缀**: `[Snowball]`, `[QueryManager]`, `[SyncManager]`, `[RealProposer]`

## 调试说明

- 共识不推进 → 检查 `QueryManager` 采样是否正常
- 区块同步卡住 → 检查 `SyncManager` 日志
- 查看区块缓存大小: `consensus.GetBlockCacheSize()`
