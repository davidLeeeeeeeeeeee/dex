---
name: Transaction Pool
description: Pending transaction management, validation queue, and block proposal selection.
triggers:
  - 交易池
  - pending
  - 待处理交易
  - txpool
  - 交易队列
---

# TxPool (交易池) 模块指南

管理待处理交易，为区块提案提供交易选择。

## 目录结构

```
txpool/
├── txpool.go        # ⭐ 主逻辑
└── txpool_queue.go  # 队列实现
```

## 核心功能

```go
type TxPool struct {
    pending map[string]*pb.AnyTx  // 待处理交易
    mu      sync.RWMutex
}

// 主要方法
Add(tx *pb.AnyTx) error        // 添加交易
Remove(txID string)             // 移除交易
Get(txID string) *pb.AnyTx      // 获取交易
GetAll() []*pb.AnyTx            // 获取所有
SelectForBlock(limit int) []*pb.AnyTx  // 选择打包
```

## 交易生命周期

```
用户提交 → TxPool.Add()
    ↓
验证签名、格式
    ↓
加入 pending 池
    ↓
Proposer 选择 → SelectForBlock()
    ↓
打入区块
    ↓
区块确认 → TxPool.Remove()
```

## 验证规则

1. **签名验证**: 交易签名必须有效
2. **Nonce 检查**: 防止重放
3. **余额检查**: 发送者余额足够（可选）
4. **大小限制**: 单笔交易不超过限制

## 与其他模块的关系

```
handlers/handleTx.go
    ↓ 验证后
TxPool.Add()
    ↓
consensus/realProposer.go
    ↓ 出块时
TxPool.SelectForBlock()
    ↓
vm/executor.go 执行
    ↓ 区块确认后
TxPool.Remove()
```

## 开发规范

1. **线程安全**: 所有操作需加锁
2. **防重放**: 已执行的交易 ID 要记录
3. **内存限制**: 控制 pending 池大小
4. **日志前缀**: `[TxPool]`

## 调试

```bash
# 查看 pending 交易数
curl "https://localhost:8443/status" -k | jq .pending_count
```

