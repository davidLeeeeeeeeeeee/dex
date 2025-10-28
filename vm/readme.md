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
- `WriteOp`: 写操作
- `Receipt`: 执行收据
- `SpecResult`: 执行结果
- `Block`: 区块结构
- `AnyTx`: 通用交易结构

### interfaces.go
定义了核心接口：
- `StateView`: 状态视图接口
- `TxHandler`: 交易处理器接口
- `SpecExecCache`: 缓存接口
- `DBManager`: 数据库管理器接口

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
    
    // 6. 预执行区块
    result, err := executor.PreExecuteBlock(block)
    if err != nil {
        // 处理错误
    }
    
    // 7. 共识后提交
    err = executor.CommitFinalizedBlock(block)
}
```

### 3. 自定义Handler

```go
type MyHandler struct{}

func (h *MyHandler) Kind() string {
    return "my_tx_type"
}

func (h *MyHandler) DryRun(tx *vm.AnyTx, sv vm.StateView) ([]vm.WriteOp, *vm.Receipt, error) {
    // 1. 解析交易
    // 2. 读取状态
    // 3. 执行逻辑
    // 4. 生成变更
    // 5. 返回结果
}

func (h *MyHandler) Apply(tx *vm.AnyTx) error {
    return vm.ErrNotImplemented
}
```

## 执行流程

### 预执行流程

```
接收区块 -> 检查缓存 -> 创建StateView -> 遍历交易 -> 执行Handler
    |                                           |
    v                                           v
缓存命中则返回                              收集Diff -> 缓存结果
```

### 提交流程

```
接收区块 -> 查找缓存 -> 应用Diff -> 写入数据库
    |           |
    v           v
缓存缺失    重新执行
```

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

## 注意事项

1. DBManager接口需要根据实际数据库实现
2. Block和AnyTx结构可能需要根据项目调整
3. Handler的具体业务逻辑需要根据需求实现
4. 生产环境建议增加监控和日志

## 许可证

[您的许可证]

## 贡献

欢迎提交Issue和Pull Request！