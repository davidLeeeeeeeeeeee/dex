---
name: testharness
description: 通用集成测试框架，在单进程内模拟多节点 + VM + FROST Runtime 协作，跳过共识/网络/区块打包。
---

# Test Harness Skill

## Trigger Cues

- `testharness`
- `测试`
- `test`
- `harness`
- `integration test`
- `集成测试`

框架位于 `testharness/harness.go`，测试示例见 `testharness/example_test.go`。

## 原理

共享内存 KV (`map[string][]byte`) 模拟链上状态。tx 提交 → VM Handler DryRun → 写回 KV，跳过共识/网络。
- `harnessStateView` 实现 `vm.StateView`，给 DryRun 用
- `harnessStateReader` 实现 `types.StateReader`，给 FROST Runtime 用
- `harnessTxSubmitter` 实现 `types.TxSubmitter`，Submit*Tx 内部调 `executeTx` → DryRun
- `AutoAdvanceHeight` 后台推高度，驱动 Runtime 的 `waitForHeight` 循环

## API 分类

- **创建**: `New(t, committeeSize)` → 自动注册所有 Handler，生成 `h.Committee`
- **种子**: `SeedAccount` / `SeedMinerAccount` / `SeedVaultTransition` / `SeedVaultConfig` / `SeedVaultState` / `RawSet`
- **提交**: `SubmitTx` / `SubmitTxExpectSuccess` / `SubmitBlock`
- **高度**: `Height` / `AdvanceHeight(n)` / `AutoAdvanceHeight(interval)`
- **断言**: `AssertBalance` / `AssertBalanceGt` / `AssertDkgStatus` / `AssertVaultActive`
- **读取**: `GetBalance` / `GetVaultState` / `GetTransitionState` / `GetRechargeRequest`
- **Runtime 适配**: `StateReader()` / `TxSubmitter()` — 传给 `TransitionWorker`
- **构造 tx**: `BuildTransferTx` / `BuildOrderTx` / `BuildWitnessStakeTx` / `BuildDkgCommitTx` / `BuildDkgShareTx`
- **工具**: `RunUntil(cond, timeout)` / `PrintTxLog` / `TxLog`

## Quick Commands

```bash
go test -v ./testharness/...
rg "func.*Harness" testharness/harness.go
```

