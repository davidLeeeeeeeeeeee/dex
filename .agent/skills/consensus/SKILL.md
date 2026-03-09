---
name: consensus
description: Snowman/Snowball consensus, query polling, gossip handling, and block sync management.
---

# Consensus Skill

## Trigger Cues

- `consensus`
- `snowball`
- `query manager`
- `sync manager`
- `finalization`

Use this skill for block preference, chit voting, query lifecycle, or sync behavior.

## Source Map

- Engine and vote processing: `consensus/consensusEngine.go`
- Snowball state machine: `consensus/snowball.go`
- Proposal path: `consensus/proposalManager.go`, `consensus/realProposer.go`
- Query issue/retry logic: `consensus/queryManager.go`
- Sync and height polling: `consensus/syncManager.go`
- Message dispatch: `consensus/messageHandler.go`
- Pending block tracking: `consensus/pending_block_buffer.go`
- Runtime wiring: `consensus/node.go`, `consensus/realManager.go`, `consensus/realTransport.go`

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

## 已知经验（实战）

1. 共识分叉（已修复）:
   - 根因：`snowball.go` 的 `selectBestCandidate` 在高负载下没有优先 window block，不同节点可能选出不同候选。
   - 修复：优先选择 window block；移除 `messageHandler.go` 中会跳过投票的过早缓存拦截；`syncManager.go` 的 fast-finalize 统一本地候选和同步候选优先级。
   - 排查流程：参考 `/.agent/workflows/debug_consensus.md`。
2. 同步卡住:
   - 重点检查 `syncManager.go` 的 stall protection 与 in-flight range dedupe。
   - 关注 `HandleSyncResponse` 超时后 range 是否被正确释放。
3. 查询风暴:
   - 重点检查 `queryManager.go` 的 `queryCooldown` 与重试上限。
   - 区分 `PushQuery` 和 `PullQuery` 的适用场景，避免重复广播。

## Quick Commands

```bash
rg "issueQuery|RegisterQuery|SubmitChit|processVotes" consensus
rg "checkAndSync|pollPeerHeights|processTimeouts|HandleHeightResponse" consensus
rg "selectBestCandidate|FastFinalize|pending block|proposal" consensus
```
