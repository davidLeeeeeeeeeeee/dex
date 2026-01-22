---
name: VM (Virtual Machine)
description: Core transaction execution layer, handles all tx types, state transitions, and order matching integration.
triggers:
  - 交易处理
  - 订单执行
  - 状态转换
  - 撮合引擎
  - order handler
  - transfer
  - miner
  - withdraw
  - DKG handler
---

# VM (虚拟机) 模块指南

VM 是整个 DEX 的核心执行层，负责所有交易的验证、执行和状态转换。

## 目录结构

```
vm/
├── executor.go              # 核心执行器，区块执行入口
├── handlers.go              # Handler 注册和分发
├── types.go                 # WriteOp, ExecuteResult 等核心类型
├── state.go                 # 状态管理
├── stateview.go             # 只读状态视图
│
├── order_handler.go         # ⭐ 订单处理（最复杂）
├── transfer_handler.go      # 转账处理
├── miner_handler.go         # 矿工注册/注销
├── freeze_handler.go        # 冻结/解冻
├── issue_token_handler.go   # Token 发行
├── block_reward.go          # 区块奖励
│
├── frost_*.go               # FROST/DKG 相关 Handler（10+个文件）
├── witness_*.go             # 见证者相关 Handler
└── safe_math.go             # 安全数学运算
```

## 核心概念

### WriteOp 机制
所有状态修改通过 `WriteOp` 结构返回，由 executor 统一写入：
```go
type WriteOp struct {
    Key         string
    Value       []byte
    Del         bool
    SyncStateDB bool  // 是否同步到 StateDB
    Category    string // "account", "order", "trade" 等
}
```

### Handler 接口
```go
type TxHandler interface {
    Handle(ctx *ExecuteContext, anyTx *pb.AnyTx) (*ExecuteResult, error)
}
```

## 关键文件详解

| 文件 | 职责 | 复杂度 |
|:---|:---|:---:|
| `order_handler.go` | 订单创建、撮合、成交记录生成 | ⭐⭐⭐⭐⭐ |
| `executor.go` | 区块执行、Diff 计算、回滚 | ⭐⭐⭐⭐ |
| `frost_dkg_handlers.go` | DKG 状态机转换 | ⭐⭐⭐⭐ |
| `transfer_handler.go` | 余额转账 | ⭐⭐ |
| `miner_handler.go` | 矿工质押/解押 | ⭐⭐ |

## 开发规范

1. **不直接写数据库**: 所有修改通过返回 `WriteOp` 切片
2. **幂等性**: 相同交易多次执行结果必须一致
3. **错误处理**: 验证失败返回 error，不 panic
4. **日志前缀**: `[OrderHandler]`, `[VMExecutor]` 等

## 常见任务

### 添加新交易类型
1. 在 `pb/data.proto` 添加消息定义
2. 创建 `xxx_handler.go` 实现 `TxHandler`
3. 在 `handlers.go` 注册 handler

### 调试订单撮合问题
1. 检查 `order_handler.go` 的 `executeOrderTx`
2. 查看 `matching/` 模块的撮合逻辑
3. 日志关键字: `[OrderHandler]`, `[Matching]`

## 测试

```bash
go test ./vm/... -v
go test ./vm/ -run TestOrderHandler -v
```

