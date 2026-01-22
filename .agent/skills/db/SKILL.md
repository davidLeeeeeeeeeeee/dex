---
name: Database Layer
description: BadgerDB wrapper, key management, account/order/block persistence, and queue-based async writes.
triggers:
  - 数据库
  - 存储
  - BadgerDB
  - key 设计
  - 账户查询
  - 区块存储
  - 订单索引
---

# Database (数据库) 模块指南

负责所有持久化存储，使用 BadgerDB 作为底层 KV 存储。

## 目录结构

```
db/
├── db.go                    # Manager 主类，队列写入机制
├── keys.go                  # Key 命名导出（从 keys/ 重导出）
├── base.go                  # SaveTxRaw, SaveAnyTx 等基础方法
├── queue.go                 # 异步写入队列实现
│
├── manage_account.go        # 账户 CRUD
├── manage_frost.go          # FROST 状态存储
├── manage_tx_storage.go     # 交易存储
├── mange_block.go           # 区块存储（注意拼写）
├── mange_price.go           # 价格索引
├── miner_index_manager.go   # 矿工索引（bitmap 采样）
└── utils.go                 # Proto 序列化等工具

keys/
└── keys.go                  # ⭐ 所有 Key 命名规范
```

## Key 命名规范 (keys/keys.go)

所有 Key 都有版本前缀 `v1_`：

| 函数 | 格式 | 用途 |
|:---|:---|:---|
| `KeyAccount(addr)` | `v1_account_<addr>` | 账户数据 |
| `KeyOrderState(id)` | `v1_orderstate_<id>` | 订单状态 |
| `KeyTradeRecord(pair,ts,id)` | `v1_trade_<pair>_<inverted_ts>_<id>` | 成交记录 |
| `KeyBlockData(hash)` | `v1_block_data_<hash>` | 区块数据 |
| `KeyOrderPriceIndex(...)` | `v1_pair:<pair>\|is_filled:...\|price:...\|order_id:...` | 价格索引 |

**重要**: 成交记录使用**倒序时间戳** `^uint64(timestamp)`，让最新记录排在前面。

## 核心机制

### 队列写入
写操作通过 `EnqueueSet/EnqueueDelete` 进入队列，后台批量提交：
```go
manager.EnqueueSet(key, value)  // 异步写入
manager.ForceFlush()            // 强制刷盘
```

### 扫描方法
```go
ScanByPrefix(prefix, limit)         // 正向扫描
ScanPrefixReverse(prefix, limit)    // 反向扫描（IndexDB）
```

## 开发规范

1. **新 Key 必须在 keys/keys.go 定义**: 不要硬编码字符串
2. **批量写入**: 使用 Enqueue 系列方法，不要直接写
3. **索引一致性**: 主数据和索引要同时更新

## 常见调试

```bash
# 查看数据库中某前缀的所有 key
go test ./db/ -run TestScanPrefix -v

# 重建订单索引
go test ./db/ -run TestRebuildOrderIndex -v
```

