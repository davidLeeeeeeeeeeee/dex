---
name: HTTP Handlers
description: HTTP/3 (QUIC) API layer, handles all external requests including tx submission, block queries, orderbook, and FROST messages.
triggers:
  - HTTP 接口
  - API
  - 请求处理
  - orderbook 查询
  - 交易提交
  - FROST 消息
  - 区块查询
---

# Handlers (HTTP 接口) 模块指南

处理所有 HTTP/3 请求，是节点对外通信的主要入口。

## 目录结构

```
handlers/
├── manager.go               # HandlerManager 主类
├── types.go                 # 请求/响应类型定义
├── tx_utils.go              # 交易验证工具
│
├── handleOrderBook.go       # ⭐ 订单簿 + 成交记录 API
├── handleTx.go              # 交易提交 /put
├── handleGetData.go         # 交易/订单查询 /getdata
├── handleGetBlock.go        # 区块查询
├── handleGetAccount.go      # 账户查询
├── handleStatus.go          # 节点状态
├── handleRecentBlocks.go    # 最近区块列表
│
├── handleChits.go           # Snowball 投票
├── handlePush.go            # Push Query
├── handlePull.go            # Pull Query
├── handleBlockGossip.go     # 区块 Gossip
│
├── frost_*.go               # FROST 相关接口
└── handleFrostMsg.go        # FROST P2P 消息
```

## 主要 API 端点

| 端点 | 方法 | 处理函数 | 用途 |
|:---|:---|:---|:---|
| `/put` | POST | `HandleTx` | 提交交易 |
| `/getdata` | POST | `HandleGetData` | 查询交易/订单 |
| `/orderbook` | GET | `HandleOrderBook` | 订单簿快照 |
| `/trades` | GET | `HandleTrades` | 成交记录 |
| `/account` | GET | `HandleGetAccount` | 账户余额 |
| `/status` | GET | `HandleStatus` | 节点状态 |
| `/chits` | POST | `HandleChits` | Snowball 投票 |

## 开发规范

1. **统计记录**: 每个 handler 开头调用 `hm.Stats.RecordAPICall("xxx")`
2. **错误返回**: 使用 `http.Error()` 或 JSON 格式的错误响应
3. **CORS**: 已在 middleware 统一处理
4. **日志**: 使用 `logs.Debug/Info/Error`

## 添加新 API

```go
// 1. 在 handleXxx.go 中添加处理函数
func (hm *HandlerManager) HandleNewAPI(w http.ResponseWriter, r *http.Request) {
    hm.Stats.RecordAPICall("HandleNewAPI")
    // ... 处理逻辑
}

// 2. 在 cmd/main/node.go 中注册路由 (startHTTPServerWithSignal)
mux.HandleFunc("/newapi", node.HandlerManager.HandleNewAPI)
```

## 常见调试

```bash
# 测试订单簿接口
curl "https://localhost:8443/orderbook?pair=FB_USDT" -k

# 测试成交记录
curl "https://localhost:8443/trades?pair=FB_USDT&limit=10" -k

# 查看节点状态
curl "https://localhost:8443/status" -k
```

