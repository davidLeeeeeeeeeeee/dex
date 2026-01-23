---
name: FROST & DKG
description: FROST 门限签名与分布式密钥生成模块的完整开发指南，包含架构设计、流程说明、接口规范与调试方法。
---

# FROST & DKG 模块开发指南

该模块负责系统的核心安全：**门限签名 (FROST/ROAST)** 和 **分布式密钥生成 (DKG)**。

---

## 1. 模块架构概览

FROST 采用三层架构设计：

```
┌─────────────────────────────────────────────────────────────────┐
│                    VM 层（链上状态/共识）                         │
│  frost_withdraw_*.go | frost_dkg_handlers.go | keys/keys.go     │
├─────────────────────────────────────────────────────────────────┤
│                    Runtime 层（链下执行）                         │
│  manager.go | workers/ | roast/ | planning/ | session/          │
├─────────────────────────────────────────────────────────────────┤
│                    Core 层（纯算法）                              │
│  curve/ | dkg/ | frost/ | roast/                                │
└─────────────────────────────────────────────────────────────────┘
```

**核心原则**：
- Core 层：纯数学/算法，**不引入任何外部依赖**
- Runtime 层：链下执行，读链上状态 + 执行动作 + 提交 tx 回写结果
- VM 层：链上状态机，全网一致、可验证

---

## 2. 目录结构详解

```
frost/
├── design.md           # v1 设计文档（架构、流程、数据模型）
├── dkg.md              # DKG 流程图解
├── code_guide.md       # 代码阅读指南
├── requirements.md     # 需求与约束清单
│
├── core/               # 【纯算法层】不要引入外部依赖！
│   ├── curve/          # 椭圆曲线实现
│   │   ├── group.go        # 曲线群接口定义
│   │   ├── secp256k1.go    # BTC 用（BIP-340 Schnorr）
│   │   ├── bn256.go        # ETH/BNB 用（alt_bn128）
│   │   └── ed25519.go      # SOL 用
│   ├── dkg/            # DKG 数学工具
│   │   ├── polynomial.go   # 随机多项式生成
│   │   └── lagrange.go     # 拉格朗日插值
│   ├── frost/          # FROST 签名算法
│   │   ├── api.go          # 统一验签 API（按 SignAlgo 路由）
│   │   ├── challenge.go    # BIP340/Keccak challenge
│   │   ├── schnorr.go      # Schnorr 签名/验签
│   │   └── threshold.go    # 门限签名聚合
│   └── roast/          # ROAST 子集重试算法
│       └── roast.go        # 子集选择、绑定系数
│
├── runtime/            # 【运行时层】链下执行引擎
│   ├── manager.go          # Runtime 主循环与生命周期
│   ├── deps.go             # 依赖接口定义
│   ├── interfaces.go       # 内部接口定义
│   │
│   ├── workers/        # 后台工作者
│   │   ├── withdraw_worker.go     # 提现签名流程
│   │   └── transition_worker.go   # DKG 轮换流程
│   │
│   ├── planning/       # 任务规划
│   │   ├── scanner.go         # FIFO 队列扫描
│   │   ├── job_planner.go     # 签名任务生成
│   │   └── job_window_planner.go  # 并发窗口规划
│   │
│   ├── roast/          # ROAST 会话管理
│   │   ├── coordinator.go     # 协调者（收集 nonce/share）
│   │   ├── participant.go     # 参与者（生成 share）
│   │   ├── signer.go          # 签名服务封装
│   │   └── roast_dispatcher.go # 消息分发
│   │
│   ├── session/        # 会话状态管理
│   │   ├── store.go       # nonce 持久化（防二次签名攻击！）
│   │   ├── roast.go       # ROAST 会话状态机
│   │   ├── dkg.go         # DKG 会话状态
│   │   └── recovery.go    # 重启恢复逻辑
│   │
│   ├── committee/      # 委员会管理
│   │   ├── vault_committee.go          # Top10000 → Vault 分配算法
│   │   └── vault_committee_provider.go # 从链上读取委员会信息
│   │
│   ├── adapters/       # 外部适配器
│   │   ├── state_reader.go     # StateDB 读取适配
│   │   ├── tx_submitter.go     # TxPool 提交适配
│   │   ├── p2p.go              # P2P 消息发送
│   │   └── finality_notifier.go # 区块最终化通知
│   │
│   ├── net/            # P2P 消息路由
│   │   ├── msg.go          # FrostEnvelope 路由器
│   │   └── handlers.go     # 消息处理器注册
│   │
│   └── services/       # 高层服务
│       ├── signing_service.go  # 签名服务
│       └── dkg_service.go      # DKG 服务
│
├── chain/              # 【链适配器】交易模板构建
│   ├── adapter.go          # 适配器接口与工厂
│   ├── btc/            # BTC 适配器（已实现）
│   │   ├── adapter.go      # Taproot 提现模板
│   │   └── template.go     # sighash 计算
│   ├── evm/            # EVM 适配器（ETH/BNB）
│   │   └── adapter.go      # 合约调用模板
│   ├── solana/         # SOL 适配器（待实现）
│   └── tron/           # TRX 适配器（待实现，需 GG20/CGGMP）
│
└── security/           # 安全工具
    ├── ecies.go            # share 加密（DKG 用）
    ├── signing.go          # 消息签名/验签
    └── idempotency.go      # 幂等检查
```

---

## 3. 关键数据结构与状态键

### 3.1 链上状态键（`keys/keys.go`）

| 类别 | Key 格式 | 用途 |
|------|----------|------|
| **Vault 配置** | `v1_frost_vault_cfg_<chain>` | 每链的 Vault 分片策略 |
| **Vault 状态** | `v1_frost_vault_<chain>_<vault_id>` | 单个 Vault 运行时状态 |
| **DKG 轮换** | `v1_frost_vault_transition_<chain>_<vault_id>_<epoch_id>` | DKG 状态机 |
| **DKG 承诺** | `v1_frost_vault_dkg_commit_<chain>_<vault_id>_<epoch_id>_<miner>` | 参与者承诺点 |
| **提现队列** | `v1_frost_withdraw_<withdraw_id>` | 提现请求状态 |
| **FIFO 索引** | `v1_frost_withdraw_q_<chain>_<asset>_<seq>` | 队列顺序 |
| **签名产物** | `v1_frost_signed_pkg_<job_id>_<idx>` | 已签名包（可追加） |
| **资金账本** | `v1_frost_funds_lot_<chain>_<asset>_<vault_id>_<height>_<seq>` | 入账 lot 索引 |
| **BTC UTXO** | `v1_frost_btc_utxo_<vault_id>_<txid>_<vout>` | BTC UTXO 管理 |

### 3.2 签名算法与曲线映射

| 链 | SignAlgo | 曲线 | FROST 变体 |
|----|----------|------|-----------|
| BTC | `SCHNORR_SECP256K1_BIP340` | secp256k1 | FROST-secp256k1 |
| ETH/BNB | `SCHNORR_ALT_BN128` | alt_bn128 | FROST-bn128 |
| SOL | `ED25519` | ed25519 | FROST-Ed25519 |
| TRX | `ECDSA_SECP256K1` | secp256k1 | **不使用 FROST**（需 GG20） |

---

## 4. 核心流程

### 4.1 提现流程（Withdraw Pipeline）

```
用户请求 → VM 入队(QUEUED) → Runtime Scanner → JobPlanner → ROAST 签名 → VM 确认(SIGNED)
```

**关键文件**：
- `vm/frost_withdraw_request.go`: 创建 WithdrawRequest
- `vm/frost_withdraw_signed.go`: 验签并标记完成
- `frost/runtime/planning/scanner.go`: 扫描 FIFO 队列
- `frost/runtime/planning/job_planner.go`: 生成签名任务
- `frost/runtime/workers/withdraw_worker.go`: 提交签名交易

**确定性规划规则**：
1. 从 FIFO 队首 (`plan_head_seq`) 开始收集连续 QUEUED withdraw
2. 资金按 `finalize_height + seq` 递增顺序消耗（先入先出）
3. `template_hash` 由 `ChainAdapter.BuildTemplate()` 确定性生成
4. VM 重算校验，不一致直接拒绝

### 4.2 DKG 流程（Vault 级别独立轮换）

```
触发 → Committing → Sharing → Resolving → KeyReady → 迁移 → Active
```

**阶段详解**：

| 阶段 | 链上状态 | 参与者动作 | Worker 动作 |
|------|----------|-----------|-------------|
| **Committing** | 等待承诺 | 提交 `FrostVaultDkgCommitTx` | 广播承诺点 |
| **Sharing** | 等待份额 | 提交 `FrostVaultDkgShareTx`（加密） | 分发加密 share |
| **Resolving** | 裁决窗口 | 可投诉/揭示 | 监听并处理争议 |
| **KeyReady** | 密钥就绪 | 验证签名 | 提交 `ValidationSignedTx` |
| **迁移** | 资金转移 | 签名迁移交易 | 提交 `TransitionSignedTx` |

**关键文件**：
- `frost/runtime/workers/transition_worker.go`: DKG 状态机驱动
- `vm/frost_dkg_handlers.go`: 链上 DKG 交易处理
- `frost/runtime/session/dkg.go`: DKG 会话状态

**高度敏感检查**：
```go
// transition_worker.go 中的关键检查
func (w *TransitionWorker) isHeightInWindow(height, deadline uint64) bool {
    return height <= deadline
}
```

### 4.3 ROAST 签名流程

```
协调者选定 → Round1(nonce commitment) → Round2(sig share) → 聚合签名
     ↓ 超时
换协调者/换子集 → 重试
```

**聚合者确定性选举**：
```go
seed = H(session_id || key_epoch || "frost_agg")
agg_candidates = Permute(committee_list, seed)
agg_index = (now_height - start_height) / agg_timeout_blocks % len(agg_candidates)
```

**关键文件**：
- `frost/runtime/roast/coordinator.go`: 协调者逻辑
- `frost/runtime/roast/participant.go`: 参与者逻辑
- `frost/runtime/session/store.go`: Nonce 持久化

---

## 5. 安全约束（必须遵守！）

### 5.1 Nonce 安全（防二次签名攻击）

**攻击场景**：恶意协调者用相同 `R_i` 诱导产出两份 share，可反算私钥份额。

**强制规则**：
```go
// session/store.go 中的核心逻辑
type NonceState struct {
    R_i        Point   // 已发送的 commitment
    msg_bound  []byte  // 已绑定的 msg（首次产出 share 时绑定）
    share_sent bool    // 是否已产出 share
}

func ProduceSigShare(session_id, task_id string, msg []byte) error {
    nonce := store.GetNonce(session_id, task_id)
    if nonce.share_sent && !bytes.Equal(nonce.msg_bound, msg) {
        return ErrDuplicateShareDifferentMsg  // 拒绝！
    }
    // 先落盘 share_sent=true，再发送
}
```

### 5.2 模板绑定

**参与者在产生 `R_i` 或 `z_i` 前必须校验**：
- `job_id` 匹配链上队首 job 或已存在的 SigningJob
- `template_hash` 与链上一致
- `key_epoch` 与链上一致
- `sign_algo` 与 Vault 配置一致

### 5.3 DKG Share 安全

- Dealer 上链 `commitment_points[]` 与加密 share
- Receiver 解密后验证：`f_i(x_j) · G == Σ x_j^k · A_{ik}`
- 争议时 dealer 公开 `(share, enc_rand)`，VM 裁决

---

## 6. 开发规范

### 6.1 Core 层规范

```go
// ✅ 正确：纯计算
func ComputeChallenge(R, P []byte, msg []byte) *big.Int { ... }

// ❌ 错误：引入外部依赖
func ComputeChallenge(ctx context.Context, db StateDB, ...) { ... }
```

### 6.2 日志前缀

| 模块 | 前缀 |
|------|------|
| DKG | `[DKG]` |
| TransitionWorker | `[TransitionWorker]` |
| WithdrawWorker | `[WithdrawWorker]` |
| Coordinator | `[ROAST-Coord]` |
| Participant | `[ROAST-Part]` |
| Scanner | `[Scanner]` |

### 6.3 错误处理

```go
// 链上校验失败：直接返回错误，拒绝 tx
if !verifySignature(pkg) {
    return nil, fmt.Errorf("invalid signature: %w", ErrInvalidSig)
}

// Runtime 暂时失败：记录并重试
if err := submitTx(...); err != nil {
    log.Warn("[Worker] submit failed, will retry", "err", err)
    return ErrRetryable
}
```

---

## 7. 常见调试任务

### 7.1 DKG 不提交交易

**检查点**：
1. `transition_worker.go` 的 `isHeightInWindow()` 返回值
2. 当前高度是否在 `dkg_commit_deadline` / `dkg_dispute_deadline` 内
3. 节点是否在 `new_committee_members[]` 中

```go
// 调试日志
log.Debug("[TransitionWorker] height check",
    "current", height,
    "deadline", deadline,
    "inWindow", inWindow)
```

### 7.2 签名失败

**检查点**：
1. `participant.go` 的 `ProcessSignRequest()` 是否正确收到请求
2. Nonce 是否已持久化
3. `template_hash` 是否匹配

### 7.3 提现卡住

**检查点**：
1. `scanner.go` 是否扫描到任务
2. FIFO 队首的 withdraw 状态是否为 `QUEUED`
3. Vault 余额/UTXO 是否足够

### 7.4 验证 share 有效性

```go
// core/dkg/polynomial.go
// 验证 share: f_i(x_j) * G == Σ x_j^k * A_ik
func VerifyShare(share *big.Int, x *big.Int, commitments []*Point) bool {
    left := G.ScalarMult(share)  // share * G
    right := evaluateCommitments(x, commitments)
    return left.Equal(right)
}
```

---

## 8. 扩展指南

### 8.1 添加新链支持

1. 在 `frost/chain/` 下创建新目录
2. 实现 `ChainAdapter` 接口：
   ```go
   type ChainAdapter interface {
       BuildWithdrawTemplate(job *SigningJob) (*Template, error)
       BuildMigrationTemplate(job *MigrationJob) (*Template, error)
       PackageSignature(template *Template, sigs [][]byte) ([]byte, error)
   }
   ```
3. 在 `adapter.go` 的 `DefaultAdapterFactory` 中注册
4. 在 `config/frost.go` 中添加链配置

### 8.2 修改 DKG 参数

配置文件位置：`config/frost.go`

```go
type TransitionConfig struct {
    TriggerChangeRatio   float64  // 触发轮换的变更比例
    DkgCommitWindowBlocks uint64  // commit 窗口
    DkgDisputeWindowBlocks uint64 // 裁决窗口
}
```

### 8.3 调整 ROAST 超时

```go
type TimeoutsConfig struct {
    NonceCommitBlocks     uint64  // Round1 超时
    SigShareBlocks        uint64  // Round2 超时
    AggregatorRotateBlocks uint64 // 协调者轮换间隔
    SessionMaxBlocks      uint64  // 会话最大时长
}
```

---

## 9. 相关 VM Handler 一览

| Handler | 文件 | 用途 |
|---------|------|------|
| `FrostWithdrawRequestTxHandler` | `vm/frost_withdraw_request.go` | 创建提现请求 |
| `FrostWithdrawSignedTxHandler` | `vm/frost_withdraw_signed.go` | 确认签名完成 |
| `FrostVaultDkgCommitTxHandler` | `vm/frost_dkg_handlers.go` | 登记 DKG 承诺 |
| `FrostVaultDkgShareTxHandler` | `vm/frost_dkg_handlers.go` | 登记加密 share |
| `FrostVaultDkgComplaintTxHandler` | `vm/frost_dkg_handlers.go` | 投诉处理 |
| `FrostVaultDkgRevealTxHandler` | `vm/frost_dkg_handlers.go` | 揭示裁决 |
| `FrostVaultDkgValidationSignedTxHandler` | `vm/frost_dkg_handlers.go` | 确认新 key |
| `FrostVaultTransitionSignedTxHandler` | `vm/frost_dkg_handlers.go` | 迁移签名 |

---

## 10. 测试指南

### 10.1 单元测试位置

- `frost/core/frost/schnorr_test.go`: Schnorr 签名测试
- `frost/core/roast/roast_test.go`: ROAST 算法测试
- `frost/runtime/roast/signer_test.go`: 签名服务测试
- `frost/runtime/session/store_test.go`: Nonce 存储测试
- `frost/runtime/committee/vault_committee_test.go`: 委员会分配测试

### 10.2 集成测试建议

1. **错误 share 测试**：构造无效 share 验证举报流程
2. **超时测试**：模拟节点掉线触发协调者切换
3. **并发测试**：多节点同时启动验证鲁棒性
