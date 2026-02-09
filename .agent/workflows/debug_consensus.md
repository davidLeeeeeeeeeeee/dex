---
description: 排查共识分叉、同步卡住、投票异常等问题
---

# 排查共识问题

## 1. 确认各节点高度是否一致

检查日志中的 `lastAccepted` 和 `height` 字段，多节点应保持接近。

```bash
rg "lastAccepted|Finalized block" logs/
```

## 2. 检查 StateRoot 是否一致

多节点间的同高度 StateRoot 必须完全相同，不一致说明状态执行不确定。

```bash
rg "StateRoot|stateRoot|state_root" logs/
```

## 3. 排查分叉

查看是否存在同一高度多个候选块、或低 window 被高 window 覆盖的情况：

```bash
rg "selectBestCandidate|conflicting|fork|preference changed" consensus/
```

关键文件：
- `consensus/snowball.go` — `selectBestCandidate` 应优先低 window block
- `consensus/messageHandler.go` — 确认没有因 `GetCachedBlock` 跳过投票
- `consensus/syncManager.go` — Fast-Finalize 路径应合并本地和同步候选

## 4. 排查同步卡住

```bash
rg "checkAndSync|pollPeerHeights|processTimeouts|HandleSyncResponse|stall" consensus/syncManager.go
```

## 5. 排查查询风暴

```bash
rg "queryCooldown|issueQuery|queryTimeout|retryBackoff" consensus/queryManager.go
```
