---
name: Witness System
description: Cross-chain witness mechanism for deposit verification, including stake management, BLS voting, and challenge/arbitration.
triggers:
  - 见证者
  - 跨链
  - 入账
  - 充值验证
  - BLS 签名
  - 质押
  - 挑战
  - 仲裁
---

# Witness (见证者) 模块指南

实现跨链资产入账的见证机制，使用 BLS 聚合签名进行多方验证。

## 目录结构

```
witness/
├── witness.go           # 见证者主服务
├── service.go           # 服务生命周期管理
├── types.go             # 核心类型定义
│
├── selector.go          # 见证者选择算法
├── stake_manager.go     # 质押管理
├── vote_manager.go      # 投票聚合
├── challenge_manager.go # 挑战/仲裁处理
│
├── witness_design.md    # 设计文档
└── witness_code.md      # 代码说明

vm/
├── witness_handler.go   # 见证相关交易处理
└── witness_events.go    # 见证事件定义
```

## 核心流程

```
外链交易检测
    ↓
Witness 节点提交 RechargeRequest
    ↓
多个见证者投票 (BLS 签名)
    ↓
达到阈值 → 入账确认
    ↓
[可选] 挑战期 → 仲裁投票
```

## 关键概念

| 概念 | 说明 |
|:---|:---|
| **Witness** | 见证者节点，质押后可参与验证 |
| **RechargeRequest** | 入账请求，包含外链交易证明 |
| **Vote** | BLS 签名投票 |
| **Challenge** | 对可疑请求发起挑战 |
| **Arbitration** | 仲裁投票，解决争议 |

## BLS 签名

见证者使用 BLS 签名进行投票，支持聚合验证：
```go
// utils/bls.go
GenerateBLSKeyPair()
SignBLS(privateKey, message)
VerifyBLS(publicKey, message, signature)
AggregateBLSSignatures(signatures)
```

## 开发规范

1. **阈值控制**: 需要 2/3+ 见证者投票
2. **质押要求**: 见证者必须质押足够代币
3. **惩罚机制**: 恶意见证者会被 slash
4. **日志前缀**: `[Witness]`, `[VoteManager]`

## 配置

```json
// config/witness_default.json
{
  "min_stake": "10000",
  "vote_threshold": 0.67,
  "challenge_period": 100,
  "arbitration_threshold": 0.51
}
```

## 测试

```bash
go test ./witness/... -v
go test ./vm/ -run TestWitness -v
```

