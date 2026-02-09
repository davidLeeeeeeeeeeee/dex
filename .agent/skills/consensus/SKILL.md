---
name: Consensus
description: Snowman/Snowball consensus, query polling, gossip handling, and block sync management.
triggers:
  - consensus
  - snowball
  - query manager
  - sync manager
  - finalization
---

# Consensus Skill

Use this skill for block preference, chit voting, query lifecycle, or sync behavior.

## Source Map

- Engine and vote processing: `consensus/consensusEngine.go`
- Snowball state machine: `consensus/snowball.go`
- Query issue/retry logic: `consensus/queryManager.go`
- Sync and height polling: `consensus/syncManager.go`
- Message dispatch: `consensus/messageHandler.go`
- Pending block tracking: `consensus/pending_block_buffer.go`
- Runtime wiring: `consensus/node.go`, `consensus/realManager.go`

## Key Behaviors

- Query flow:
  1. QueryManager picks preference/candidate.
  2. Registers query in engine.
  3. Broadcasts `PushQuery` or `PullQuery`.
  4. Engine collects chits and updates Snowball preference.
- Sync flow:
  - Height polling + targeted sync requests + timeout cleanup.
  - Includes stall protection and in-flight range dedupe.

## Typical Tasks

1. Fix query storm or retry behavior:
   - inspect `queryCooldown`, retry backoff, and timeout handlers in `consensus/queryManager.go`
2. Fix finalization inconsistency:
   - inspect candidate filtering and vote recording in `consensus/consensusEngine.go`
3. Fix stuck syncing:
   - inspect `checkAndSync`, `processTimeouts`, and height response handling in `consensus/syncManager.go`

## 已知陷阱（实战经验）

1. **共识分叉**（已修复）：
   - **根因**：`selectBestCandidate` 在 `snowball.go` 中未优先选择低 window block，导致高CPU负载下不同节点偏好不同候选。
   - **修复**：`selectBestCandidate` 优先低 window block；移除 `messageHandler.go` 中的 `GetCachedBlock` 前置检查（该检查在高负载下可能跳过投票，导致投票不一致）；`syncManager.go` 的 Fast-Finalize 路径合并本地和同步候选，优先低 window block。
   - **排查流程**：参考 `/.agent/workflows/debug_consensus.md`

2. **同步卡住**：
   - 关注 `syncManager.go` 中 stall protection 和 in-flight range dedupe 逻辑
   - 丢包环境下 `HandleSyncResponse` 超时可能导致 range 永不释放

3. **查询风暴**：
   - `queryManager.go` 中 `queryCooldown` 不够时可能导致指数级重试
   - 注意区分 `PushQuery` 和 `PullQuery` 的使用场景

## Quick Commands

```bash
rg "issueQuery|RegisterQuery|SubmitChit|processVotes" consensus
rg "checkAndSync|pollPeerHeights|processTimeouts|HandleHeightResponse" consensus
rg "selectBestCandidate|GetCachedBlock|FastFinalize" consensus
```

