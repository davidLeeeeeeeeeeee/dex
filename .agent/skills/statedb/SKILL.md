---
name: StateDB
description: High-performance state database with sharding, WAL, epoch-based snapshots, and MPT-compatible queries.
triggers:
  - StateDB
  - 状态数据库
  - 分片
  - WAL
  - 快照
  - epoch
  - MPT
---

# StateDB (状态数据库) 模块指南

高性能状态存储，支持分片、WAL、快照和高效查询。

## 目录结构

```
stateDB/
├── db.go              # ⭐ 主入口，DB 结构体
├── types.go           # 配置和类型定义
├── keys.go            # Key 生成规则
│
├── shard.go           # 分片管理
├── mem_window.go      # 内存窗口（热数据）
├── snapshot.go        # 快照管理
├── wal.go             # Write-Ahead Log
│
├── query.go           # 查询接口
├── update.go          # 更新接口
├── utils.go           # 工具函数
│
├── README.md          # 设计说明
├── WAL_INTEGRATION.md # WAL 集成文档
└── QA.md              # 常见问题
```

## 核心特性

| 特性 | 说明 |
|:---|:---|
| **分片** | 按地址前缀分片，提高并发性能 |
| **内存窗口** | 最近 N 个 epoch 的数据保留在内存 |
| **WAL** | 写前日志，保证崩溃恢复 |
| **快照** | 按 epoch 生成快照，支持历史查询 |

## 配置

```go
type Config struct {
    DataDir         string
    EpochSize       uint64  // 每个 epoch 的区块数
    ShardHexWidth   int     // 分片位数
    PageSize        int     // 分页大小
    UseWAL          bool    // 是否启用 WAL
    VersionsToKeep  int     // 保留版本数
    AccountNSPrefix string  // 账户命名空间前缀
}
```

## 使用方式

```go
// 初始化
cfg := statedb.Config{
    DataDir:   "./data/state",
    EpochSize: 40000,
    UseWAL:    true,
}
db, _ := statedb.New(cfg)

// 读取账户
acc, _ := db.GetAccount(height, address)

// 批量更新（在 VM 中使用）
db.ApplyUpdates(height, updates)

// 创建快照
db.CreateSnapshot(epochID)
```

## 与其他模块的关系

```
                    ┌─────────────┐
                    │   VM 执行   │
                    └──────┬──────┘
                           │ WriteOps
              ┌────────────┼────────────┐
              ▼            ▼            ▼
        ┌─────────┐  ┌──────────┐  ┌─────────┐
        │ BadgerDB│  │  StateDB │  │   JMT   │
        │(交易/块)│  │ (窗口缓存)│  │(状态根) │
        └─────────┘  └──────────┘  └─────────┘
```

| 模块 | 职责 |
|:---|:---|
| **BadgerDB** (`db/`) | 存储交易、区块、索引等 |
| **StateDB** (`stateDB/`) | 内存窗口 + WAL，热数据快速查询 |
| **JMT** (`jmt/`) | 16 叉 Merkle 树，计算 StateRoot |

### JMT 集成

StateDB 现在通过 `jmt/jmt_statedb.go` 适配 JMT：
```go
// 区块执行后更新 JMT
jmtStateDB.ApplyUpdates(height, ops)

// 获取状态根
stateRoot := jmtStateDB.GetRoot()
```

VM 的 WriteOp 通过 `SyncStateDB: true` 触发同步更新。

## 开发规范

1. **高度一致性**: 所有查询需指定区块高度
2. **原子更新**: 同一区块的更新必须原子提交
3. **日志前缀**: `[StateDB]`, `[WAL]`

## 测试

```bash
go test ./stateDB/... -v
go test ./stateDB/ -run TestMultiPrefix -v
```

