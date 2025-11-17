# VM Module - Virtual Machine for Transaction Execution

## 概述

VM模块实现了"内存预执行，延迟落库"的交易执行引擎，专门用于区块链系统中的高性能交易处理。

### 核心特性

- **内存预执行**：交易先在内存中模拟执行，不直接写入数据库
- **延迟提交**：只有共识达成后才批量写入数据库
- **状态隔离**：使用Overlay模式实现读写隔离
- **快照回滚**：支持交易失败时的自动回滚
- **LRU缓存**：避免重复计算，提高性能
- **插件化Handler**：易于扩展新的交易类型

## 项目结构

```
vm/
├── go.mod                  # Go模块定义
├── README.md              # 本文档
├── types.go               # 基础类型定义
├── interfaces.go          # 接口定义
├── stateview.go          # StateView实现
├── handlers.go           # Handler注册表
├── spec_cache.go         # LRU缓存实现
├── executor.go           # 核心执行器
├── default_handlers.go   # 默认Handler实现
├── vm_test.go           # 测试文件
└── example/
    └── main.go          # 使用示例
```

## 文件说明

### types.go
定义了VM模块的基础类型：
- `WriteOp`: 写操作，包含以下字段：
  - `Key`: 完整的 key（包括命名空间前缀）
  - `Value`: 序列化后的值
  - `Del`: 是否删除操作
  - `SyncStateDB`: **是否同步到 StateDB**（用于账户、订单等关键数据）
  - `Category`: 数据分类（account, token, order, receipt, meta 等），便于追踪和调试
- `Receipt`: 执行收据
- `SpecResult`: 执行结果

**注意**: `Block` 和 `AnyTx` 类型现在使用 `pb` 包中的定义（`pb.Block` 和 `pb.AnyTx`），遵循单一事实来源原则。

### interfaces.go
定义了核心接口：
- `StateView`: 状态视图接口
- `TxHandler`: 交易处理器接口
- `SpecExecCache`: 缓存接口
- `DBManager`: 数据库管理器接口
  - `SyncToStateDB(height uint64, updates []interface{}) error`: **同步状态到 StateDB**（VM 统一提交入口调用）

### stateview.go
实现了内存Overlay状态视图：
- 读写隔离
- 快照和回滚功能
- 差异集合生成

### handlers.go
Handler注册表管理：
- 注册和获取Handler
- 交易类型提取函数

### spec_cache.go
LRU缓存实现：
- 缓存执行结果
- 自动淘汰策略
- 按高度清理

### executor.go
核心执行器：
- `PreExecuteBlock`: 预执行（内存）
- `CommitFinalizedBlock`: 最终提交（数据库）
- `applyResult`: **统一提交入口**，负责：
  - 幂等性检查（防止重复提交）
  - 应用所有 WriteOp 到 Badger
  - 同步 `SyncStateDB=true` 的数据到 StateDB
  - 写入交易收据和区块元数据
  - 原子提交
- 交易和区块状态查询

### default_handlers.go
内置的Handler实现：
- `OrderTxHandler`: 订单交易
- `TransferTxHandler`: 转账交易
- `MinerTxHandler`: 矿工奖励

## 快速开始

### 1. 安装

```bash
go get github.com/yourproject/vm
```

### 2. 基本使用

```go
package main

import (
    "dex/pb"
    "dex/vm"
)

func main() {
    // 1. 创建数据库管理器
    db := NewYourDBManager()

    // 2. 创建Handler注册表
    registry := vm.NewHandlerRegistry()

    // 3. 注册Handler
    vm.RegisterDefaultHandlers(registry)

    // 4. 创建缓存
    cache := vm.NewSpecExecLRU(1000)

    // 5. 创建执行器
    executor := vm.NewExecutor(db, registry, cache)

    // 6. 创建pb.Block区块（使用pb包的类型）
    block := &pb.Block{
        BlockHash:     "block_001",
        PrevBlockHash: "genesis",
        Height:        1,
        Body:          []*pb.AnyTx{...}, // pb.AnyTx交易列表
    }

    // 7. 预执行区块
    result, err := executor.PreExecuteBlock(block)
    if err != nil {
        // 处理错误
    }

    // 8. 共识后提交
    err = executor.CommitFinalizedBlock(block)
}
```

### 3. 自定义Handler

```go
import (
    "dex/pb"
    "dex/vm"
)

type MyHandler struct{}

func (h *MyHandler) Kind() string {
    return "my_tx_type"
}

func (h *MyHandler) DryRun(tx *pb.AnyTx, sv vm.StateView) ([]vm.WriteOp, *vm.Receipt, error) {
    // 1. 从pb.AnyTx中提取具体交易类型
    // 2. 读取状态
    // 3. 执行逻辑
    // 4. 生成变更
    // 5. 返回结果

    txID := tx.GetTxId()
    // ... 处理逻辑

    // 生成 WriteOp 时需要指定 SyncStateDB 和 Category
    ws := []vm.WriteOp{
        {
            Key:         "account:alice",
            Value:       accountData,
            Del:         false,
            SyncStateDB: true,      // 账户数据需要同步到 StateDB
            Category:    "account", // 数据分类，便于调试
        },
        {
            Key:         "order:12345",
            Value:       orderData,
            Del:         false,
            SyncStateDB: true,      // 订单数据也需要同步（支持轻节点）
            Category:    "order",
        },
    }

    return ws, &vm.Receipt{
        TxID:   txID,
        Status: "SUCCEED",
    }, nil
}

func (h *MyHandler) Apply(tx *pb.AnyTx) error {
    return vm.ErrNotImplemented
}
```

## 执行流程

### 预执行流程（内存 Overlay）

```
接收区块 -> 检查缓存 -> 创建StateView -> 遍历交易 -> 执行Handler.DryRun()
    |                                           |
    v                                           v
缓存命中则返回                        生成 WriteOp[] -> 应用到 StateView
                                              |
                                              v
                                        收集 Diff -> 缓存结果
```

### 提交流程（统一提交入口）

```
接收区块 -> 查找缓存 -> applyResult（统一提交入口）
    |           |              |
    v           v              ├─ 1. 幂等性检查
缓存缺失    重新执行            ├─ 2. 应用所有 WriteOp 到 Badger
                              ├─ 3. 同步 SyncStateDB=true 的数据到 StateDB
                              ├─ 4. 写入交易收据和元数据
                              └─ 5. 原子提交（ForceFlush）
```

**关键改进**：
- ✅ **统一提交路径**：所有状态变更都通过 `applyResult` 提交，避免散布的 `db.Save*` 调用
- ✅ **双层存储**：Badger（完整数据） + StateDB（账户/订单等关键数据的 Merkle 树）
- ✅ **幂等性保证**：通过 `KeyVMCommitHeight` 检查，防止重复提交
- ✅ **原子性保证**：所有写操作在 `ForceFlush` 时原子提交

## 性能优化建议

1. **缓存配置**
    - 根据区块生成速度调整缓存大小
    - 定期清理过期缓存

2. **并发处理**
    - 不同区块可并行预执行
    - 使用读写锁减少竞争

3. **内存管理**
    - 及时释放StateView
    - 控制overlay大小

4. **批处理**
    - 合并小交易
    - 批量数据库操作

## 测试

运行测试：
```bash
go test ./...
```

运行基准测试：
```bash
go test -bench=.
```

运行示例：
```bash
go run example/main.go
```

## 架构设计

### 双层存储架构

```
┌─────────────────────────────────────────────────────────────┐
│                         VM Layer                            │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐  │
│  │   Handler    │ -> │  StateView   │ -> │   WriteOp[]  │  │
│  │   DryRun()   │    │  (Overlay)   │    │              │  │
│  └──────────────┘    └──────────────┘    └──────────────┘  │
└─────────────────────────────────────────────────────────────┘
                              |
                              v
                      applyResult (统一提交)
                              |
                ┌─────────────┴─────────────┐
                v                           v
        ┌──────────────┐          ┌──────────────┐
        │    Badger    │          │   StateDB    │
        │ (完整数据)    │          │ (Merkle树)   │
        │              │          │              │
        │ - 所有交易    │          │ - 账户状态    │
        │ - 所有订单    │          │ - 订单状态    │
        │ - 索引数据    │          │ - Token状态   │
        │ - 元数据      │          │              │
        └──────────────┘          └──────────────┘
```

**设计要点**：
1. **Badger**：存储所有数据，作为完整的数据源
2. **StateDB**：只存储关键数据（账户、订单、Token），用于：
   - 快速生成 Merkle Root（用于共识）
   - 支持轻节点同步
   - 状态证明（State Proof）
3. **WriteOp.SyncStateDB**：控制哪些数据需要同步到 StateDB
   - `true`：账户、订单、Token 等关键状态
   - `false`：索引、元数据、临时数据

### 数据分类（Category）

| Category  | 说明           | SyncStateDB | 示例                    |
|-----------|----------------|-------------|-------------------------|
| account   | 账户数据       | true        | 余额、投票、锁定        |
| token     | Token 数据     | true        | 发行信息、总供应量      |
| order     | 订单数据       | true        | 订单状态、成交信息      |
| receipt   | 交易收据       | false       | 执行结果、错误信息      |
| index     | 索引数据       | false       | 价格索引、时间索引      |
| meta      | 元数据         | false       | 区块高度、提交标记      |

## 注意事项

1. **所有状态变更必须通过 VM 的 WriteOp 机制**，不要直接调用 `db.Save*` 方法
2. DBManager 需要实现 `SyncToStateDB` 方法以支持 StateDB 同步
3. Handler 的 DryRun 方法必须正确设置 `SyncStateDB` 和 `Category` 字段
4. 生产环境建议增加监控和日志，特别是 StateDB 同步失败的情况
5. 幂等性依赖 `KeyVMCommitHeight`，不要手动删除这些键

## 许可证

[您的许可证]

## 贡献

欢迎提交Issue和Pull Request！