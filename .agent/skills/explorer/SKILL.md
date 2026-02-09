---
name: Explorer
description: Explorer backend APIs plus Vue frontend dashboard for node health, tx/block search, trading, and frost views.
triggers:
  - explorer
  - frontend
  - dashboard
  - vue
  - indexdb
---

# Explorer Skill

Use this skill for UI behavior, explorer API responses, or index sync issues.

## Source Map

- Backend server: `cmd/explorer/main.go`
- Frost backend endpoints: `cmd/explorer/frost_handlers.go`
- Index sync loop: `cmd/explorer/syncer/syncer.go`
- Indexed storage: `cmd/explorer/indexdb/`
- Frontend shell: `explorer/src/App.vue`
- Frontend API client: `explorer/src/api.ts`
- Main components: `explorer/src/components/`

## API Layers

- Frontend calls `/api/*` endpoints on explorer backend.
- Explorer backend proxies to node endpoints (`/status`, `/getblock`, `/frost/*`, `/witness/*`) and/or local index DB.

## Typical Tasks

1. Add a new panel or API card:
   - add client call in `explorer/src/api.ts`
   - add component in `explorer/src/components/`
   - add backend route/handler in `cmd/explorer/main.go`
2. Fix stale block/tx search:
   - inspect sync status in `cmd/explorer/syncer/syncer.go`
   - inspect index extraction in `cmd/explorer/indexdb/`
3. Fix frost/witness explorer data:
   - inspect mapping logic in `cmd/explorer/frost_handlers.go`

## 已知陷阱（实战经验）

1. **txhistory 状态非线性**：
   - 现象：`height:3` 为 PENDING 而 `height:19` 为 SUCCEED
   - 原因：`syncer` 同步逻辑可能跳过中间高度，导致旧交易状态未更新
   - 排查点：`cmd/explorer/syncer/syncer.go` 的同步循环和状态写入逻辑

2. **余额快照全部相同**：
   - 现象：所有历史 `fb_balance_after` 显示同一个值
   - 原因：`syncer` 使用最新 nodeDB 状态获取余额，而非历史高度的快照
   - 应改为从 Receipt 中的余额快照获取，或查询历史版本状态

3. **local_db 模式在 Windows 上的限制**：
   - BadgerDB 在 Windows 上不支持 Read-only 模式
   - Explorer 如果指向节点的数据目录，需要用 `--node-data-dir` 参数且节点不能同时运行
   - 参考 `cmd/explorer/local_db.go`

## Quick Commands

```bash
rg "HandleFunc\\(|/api/" cmd/explorer
rg "fetch[A-Z]" explorer/src/api.ts
rg "activeTab|navigationNodes" explorer/src/App.vue
```

