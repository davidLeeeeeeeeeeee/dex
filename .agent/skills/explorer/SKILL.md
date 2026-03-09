---
name: explorer
description: Explorer backend APIs plus Vue frontend dashboard for node health, tx/block search, trading, frost, and wallet proxy views.
---

# Explorer Skill

## Trigger Cues

- `explorer`
- `frontend`
- `dashboard`
- `vue`
- `indexdb`
- `wallet api`

Use this skill for UI behavior, explorer API responses, index sync issues, or wallet proxy endpoint behavior.

## Source Map

- Backend server: `cmd/explorer/main.go`
- Frost backend endpoints: `cmd/explorer/frost_handlers.go`
- Wallet proxy endpoints: `cmd/explorer/wallet_handlers.go`
- Index sync loop: `cmd/explorer/syncer/syncer.go`
- Indexed storage: `cmd/explorer/indexdb/`
- Local node DB read path: `cmd/explorer/local_db.go`
- Frontend shell: `explorer/src/App.vue`
- Frontend API client: `explorer/src/api.ts`
- Main components: `explorer/src/components/`

## API Layers

- Frontend calls `/api/*` endpoints on explorer backend.
- Explorer backend proxies node endpoints (`/status`, `/getblock`, `/frost/*`, `/witness/*`) and/or local index DB.
- Wallet-specific proxy endpoints are `/api/wallet/submittx`, `/api/wallet/nonce`, `/api/wallet/receipt`.

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
4. Fix wallet submit/nonce/receipt mismatch:
   - inspect request transform and fallback flow in `cmd/explorer/wallet_handlers.go`

## 已知经验（实战）

1. `txhistory` 状态看起来不连续:
   - 现象：低高度记录是 `PENDING`，高高度记录已经 `SUCCEED`。
   - 原因：`syncer` 某些异常路径会跳过中间高度，导致旧交易状态没有被回填。
   - 排查点：`cmd/explorer/syncer/syncer.go` 的增量同步循环和状态写入顺序。
2. 历史余额快照全部相同:
   - 现象：历史记录里的 `fb_balance_after` 近似同值。
   - 原因：读取了最新状态而非交易发生时的快照。
   - 处理：优先从 receipt 快照字段取值，或按高度读历史状态版本。
3. `--node-data-dir` 本地读库限制:
   - Explorer 用 `db.NewReadOnlyManager` 只读打开节点 Pebble DB。
   - 在 Windows/同机并发场景下可能受到文件锁限制，节点和 Explorer 同时访问同一目录会失败。
   - 参考：`cmd/explorer/main.go`、`cmd/explorer/local_db.go`、`db/readonly.go`。

## Quick Commands

```bash
rg "HandleFunc\\(|/api/" cmd/explorer
rg "wallet|frost|witness|sync" cmd/explorer/main.go cmd/explorer/wallet_handlers.go cmd/explorer/frost_handlers.go
rg "fetch[A-Z]" explorer/src/api.ts
rg "activeTab|navigationNodes|wallet" explorer/src/App.vue explorer/src/components
```
