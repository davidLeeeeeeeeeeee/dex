---
name: Consensus
description: Guide for understanding and modifying the Snowball consensus protocol and block management.
---

# Consensus (共识层) 模块指南

该模块实现了基于 Snowball 的概率性共识协议。

## 核心概念

-   **Snowball**: 用于节点间达成共识的算法。
-   **Query**: 向对等节点发起查询以决定区块偏好。
-   **Gossip**: 传播区块和投票信息。
-   **BlockStore**: 负责区块的持久化和高度索引。

## 关键文件

-   `consensus/snowball.go`: Snowball 算法核心实现。
-   `consensus/consensusEngine.go`: 驱动共识循环 (`loop.go`)。
-   `consensus/syncManager.go`: 处理区块高度同步和数据补全。

## 代码约定

-   **Interface Driven**: `Real` 前缀的类是实际运行时的实现，`Simulated` 前缀用于测试。
-   **Concurrency**: 广泛使用 `sync.RWMutex` 保护状态，修改共识状态时务必加锁。
-   **Errors**: 共识相关的错误需要传播到 `main.go` 触发节点重启或状态回滚。

## 调试说明

-   如果共识不推进，检查 `peer sampling` 逻辑是否能连通其他节点。
-   日志关键字: `[Snowball]`, `[ConsensusEngine]`, `[Sync]`.
