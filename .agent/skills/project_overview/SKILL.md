---
name: Project Overview
description: High-level architecture map of the DEX project with module index and quick navigation guide.
triggers:
  - 项目概览
  - 架构
  - 模块索引
  - 入门
  - overview
  - architecture
---

# DEX 项目概览 (Project Overview)

区块链去中心化交易所，集成 FROST 门限签名、Snowball 共识、订单撮合、跨链见证。

## 架构层次

```
┌─────────────────────────────────────────────────────────────┐
│                    cmd/main/ (项目入口)                      │
├─────────────────────────────────────────────────────────────┤
│  handlers/          │  explorer/         │  network/         │
│  HTTP API 层        │  前端 Vue + TS     │  P2P 通信         │
├─────────────────────────────────────────────────────────────┤
│  consensus/         │  txpool/           │  sender/          │
│  Snowball 共识      │  交易池            │  消息发送         │
├─────────────────────────────────────────────────────────────┤
│  vm/                │  matching/         │  witness/         │
│  交易执行           │  订单撮合          │  跨链见证         │
├─────────────────────────────────────────────────────────────┤
│  frost/             │  db/               │  stateDB/         │
│  门限签名 + DKG     │  BadgerDB 存储     │  状态数据库       │
└─────────────────────────────────────────────────────────────┘
```

## Skills 索引

| Skill | 目录 | 职责 | 复杂度 |
|:---|:---|:---|:---:|
| **vm** | `vm/` | 交易执行、订单处理、状态转换 | ⭐⭐⭐⭐⭐ |
| **frost** | `frost/` | DKG、门限签名、运行时 | ⭐⭐⭐⭐⭐ |
| **consensus** | `consensus/` | Snowball 共识、区块同步 | ⭐⭐⭐⭐ |
| **matching** | `matching/` | 订单撮合引擎 | ⭐⭐⭐ |
| **handlers** | `handlers/` | HTTP API 接口 | ⭐⭐⭐ |
| **db** | `db/`, `keys/` | 数据库、Key 规范 | ⭐⭐⭐ |
| **statedb** | `stateDB/` | 状态存储、快照 | ⭐⭐⭐ |
| **witness** | `witness/` | 跨链见证、BLS | ⭐⭐⭐ |
| **explorer** | `explorer/` | 前端界面 | ⭐⭐ |
| **sender** | `sender/` | P2P 发送 | ⭐⭐ |
| **txpool** | `txpool/` | 交易池 | ⭐⭐ |
| **config** | `config/` | 配置管理 | ⭐ |

## 任务路由

根据任务关键词选择 Skill：

| 关键词 | 推荐 Skill |
|:---|:---|
| 订单、撮合、成交 | `vm` + `matching` |
| DKG、签名、Vault | `frost` |
| 共识、区块、同步 | `consensus` |
| API、接口、查询 | `handlers` |
| 前端、UI、显示 | `explorer` |
| 数据库、Key、存储 | `db` |
| 入账、见证、BLS | `witness` |

## 技术栈

- **Backend**: Go 1.20+, HTTP/3 (QUIC)
- **Frontend**: Vue 3, TypeScript, Vite
- **Protocol**: Protobuf (`pb/data.proto`)
- **Database**: BadgerDB + StateDB

## 常用命令

```bash
# 启动节点
go run ./cmd/main

# 启动前端
cd explorer && npm run dev

# 编译 protobuf
protoc --go_out=. pb/data.proto

# 运行测试
go test ./vm/... -v
go test ./matching/... -v
```
