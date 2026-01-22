---
name: Network Sender
description: HTTP/3 client for P2P communication, handles tx broadcasting, block gossip, consensus queries, and FROST messages.
triggers:
  - 网络发送
  - P2P
  - HTTP/3
  - QUIC
  - 广播
  - gossip
  - 共识消息
---

# Sender (网络发送) 模块指南

负责节点间 P2P 通信，使用 HTTP/3 (QUIC) 协议。

## 目录结构

```
sender/
├── manager.go           # SenderManager 主类
├── http3_client.go      # HTTP/3 客户端封装
├── queue.go             # 发送队列
├── validate.go          # 请求验证
│
├── doSendTx.go          # 交易广播
├── doSendBlock.go       # 区块广播
├── doSendChits.go       # Snowball 投票
├── doSendPushQuery.go   # Push Query
├── doSendPullQuery.go   # Pull Query
├── doSendHeightQuery.go # 高度查询
├── doSendSyncRequest.go # 区块同步请求
├── doSendFrost.go       # FROST 消息
└── doSendBatchGetTxs.go # 批量交易获取

network/
└── network.go           # 网络层抽象
```

## 发送方法

| 方法 | 用途 | 目标 |
|:---|:---|:---|
| `SendTx()` | 广播交易 | 全网 |
| `SendBlock()` | 广播区块 | 全网 |
| `SendChits()` | 发送投票 | 单节点 |
| `SendPushQuery()` | 推送查询 | 采样节点 |
| `SendPullQuery()` | 拉取查询 | 采样节点 |
| `SendFrostMsg()` | FROST 消息 | 参与者 |

## HTTP/3 客户端

```go
// 创建客户端（跳过 TLS 验证用于开发）
client := &http.Client{
    Transport: &http3.RoundTripper{
        TLSClientConfig: &tls.Config{
            InsecureSkipVerify: true,
        },
    },
}
```

## 消息类型

```go
// types/message.go
type Message struct {
    Type    MessageType
    From    string
    To      string
    Payload []byte
}

type MessageType int
const (
    MsgTypeTx MessageType = iota
    MsgTypeBlock
    MsgTypeChit
    MsgTypeFrost
    // ...
)
```

## 开发规范

1. **超时控制**: 所有请求必须设置 Context 超时
2. **重试机制**: 关键消息需要重试
3. **并发安全**: SenderManager 是线程安全的
4. **日志前缀**: `[Sender]`, `[HTTP3]`

## 调试

```bash
# 查看发送日志
grep "\[Sender\]" logs/node.log

# 测试连接
curl "https://localhost:8443/status" -k
```

