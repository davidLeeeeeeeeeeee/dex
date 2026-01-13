# TODO 实现计划

> 基于 `frost/requirements.md` 中的 TODO 9-12 条目

---

## 目录

1. [Explorer 可视化增强](#1-explorer-可视化增强)
2. [上账挖矿奖励机制](#2-上账挖矿奖励机制)
3. [VM 金额计算安全检查](#3-vm-金额计算安全检查)
4. [转账 TX 逻辑完善](#4-转账-tx-逻辑完善)

---

## 1. Explorer 可视化增强

### 1.1 目标
在浏览器上可视化看到 DKG、ROAST、上账过程、各种 TX 的状态。

### 1.2 当前状态分析

**Explorer 架构**:
- 前端: Vue 3 (`explorer/src/`)
- 后端: Go HTTP 服务器 (`cmd/explorer/main.go`)
- API: `/api/nodes`, `/api/summary`, `/api/block`, `/api/tx`
- 当前已支持: 区块查询、交易列表、Frost metrics 基础展示

**已有数据源**:
- `frost_metrics`: `frost_jobs`, `frost_withdraws`, `api_call_stats`
- 交易类型: `TxSummary` 包含 `tx_type`, `status`, `summary`

### 1.3 实现方案

#### 阶段 1: 后端 API 扩展

| API 路径 | 功能 | 数据来源 |
|---------|------|---------|
| `/api/frost/dkg/{epoch_id}` | DKG 会话状态 | `v1_frost_vault_dkg_*` keys |
| `/api/frost/dkg/{epoch_id}/commits` | DKG 参与者承诺点列表 | `v1_frost_vault_dkg_commit_*` |
| `/api/frost/withdraw/queue/{chain}/{asset}` | 提现队列 | `v1_frost_withdraw_*` |
| `/api/frost/withdraw/{withdraw_id}` | 单笔提现详情 | `v1_frost_withdraw_{id}` |
| `/api/frost/jobs` | 当前活跃的签名任务 | SessionStore |
| `/api/frost/transition/{epoch_id}` | 权力交接状态 | `v1_frost_transition_*` |
| `/api/witness/requests` | 上账请求列表 | `v1_witness_request_*` |
| `/api/witness/{request_id}` | 单个上账请求详情 | 含投票、挑战状态 |

**实现文件**: `cmd/explorer/frost_handlers.go` (新建)

#### 阶段 2: 前端组件开发

| 组件 | 功能 |
|------|------|
| `FrostDashboard.vue` | Frost 总览页（DKG/提现/迁移状态聚合） |
| `DkgTimeline.vue` | DKG 流程时间线（Committing→Sharing→Resolving→KeyReady） |
| `WithdrawQueue.vue` | 提现队列列表，按 chain/asset 分组 |
| `RoastSessionView.vue` | ROAST 会话详情（参与者、进度、协调者轮换） |
| `WitnessFlow.vue` | 上账流程图（投票→公示→最终化） |
| `TxTypeRenderer.vue` | 增强 TX 详情页，按类型渲染不同字段 |

**文件位置**: `explorer/src/components/frost/`

#### 阶段 3: 状态机可视化

使用 Mermaid 或自定义 SVG 渲染:
- DKG 状态机: `NotStarted → Committing → Sharing → Resolving → KeyReady`
- Withdraw 状态: `QUEUED → SIGNED`
- Witness 状态: `PENDING → VOTING → CHALLENGED → FINALIZED/REJECTED`

### 1.4 所需后端修改

```
cmd/explorer/main.go          # 新增路由注册
cmd/explorer/frost_handlers.go # Frost 相关 API handlers (新建)
cmd/explorer/witness_handlers.go # Witness 相关 API handlers (新建)
```

### 1.5 工作量估算

| 阶段 | 预计时间 |
|------|---------|
| 后端 API (8个) | 2-3 天 |
| 前端组件 (6个) | 3-4 天 |
| 状态机可视化 | 1 天 |
| 测试与联调 | 1-2 天 |
| **总计** | **7-10 天** |

---

## 2. 上账挖矿奖励机制

### 2.1 目标
上账过程也可以挖矿，每个区块给固定奖励，按年递减，给区块矿工均分。

### 2.2 当前状态分析

**现有奖励机制**:
- Witness 模块有奖励分配: `StakeManager.DistributeReward()` (分配手续费给诚实见证者)
- 区块有 `accumulated_reward` 字段
- 无区块生产奖励（Coinbase）机制

### 2.3 实现方案

#### 2.3.1 奖励参数设计

```go
// 建议新增配置 config/reward.json
{
  "block_reward": {
    "initial_reward": "1000000000",      // 初始每区块奖励 (wei/sat 等最小单位)
    "halving_interval_years": 1,         // 递减周期(年)
    "decay_rate": 0.9,                   // 每年衰减到 90%
    "min_reward": "100000"               // 最小奖励（衰减下限）
  },
  "witness_reward_ratio": 0.3            // 见证者奖励占比 30%
}
```

#### 2.3.2 核心逻辑

**1. 区块奖励计算模块** (`vm/block_reward.go` 新建)

```go
type BlockRewardCalculator struct {
    initialReward   *big.Int
    decayRate       decimal.Decimal
    blocksPerYear   uint64    // 根据出块时间计算
    minReward       *big.Int
}

func (c *BlockRewardCalculator) Calculate(height uint64) *big.Int
func (c *BlockRewardCalculator) DistributeToMiners(miners []string, totalReward *big.Int) map[string]*big.Int
```

**2. VM 执行器扩展** (`vm/executor.go` 修改)

在 `CommitFinalizedBlock()` 中增加:
1. 计算当前区块奖励
2. 识别区块矿工（`block.Miner`）
3. 将奖励写入矿工账户余额

**3. 奖励记录**

新增 KV 存储:
- `v1_block_reward_{height}` → 该区块发放的奖励详情
- 账户余额变更通过现有的 Account 结构更新

#### 2.3.3 需修改的文件

| 文件 | 修改内容 |
|------|---------|
| `vm/block_reward.go` | 新建：奖励计算器 |
| `vm/executor.go` | 修改：`CommitFinalizedBlock` 增加奖励分发 |
| `keys/keys.go` | 新增：`KeyBlockReward` |
| `pb/data.proto` | 可选：新增 `BlockReward` 消息类型 |
| `config/` | 新增奖励配置 |

### 2.4 工作量估算

| 任务 | 预计时间 |
|------|---------|
| 奖励计算模块 | 1 天 |
| VM 集成 | 1 天 |
| 配置与 Keys | 0.5 天 |
| 单元测试 | 1 天 |
| **总计** | **3-4 天** |

---

## 3. VM 金额计算安全检查

### 3.1 目标
检查 VM 中所有涉及金额的计算，确保使用安全模块，无边界问题。

### 3.2 当前状态分析

**已有安全模块**: `vm/safe_math.go`
- `SafeAdd()`, `SafeSub()` - 溢出/下溢检查
- `ParseBalance()` - 余额字符串安全解析
- `MaxUint256` - 256 位上限

**已使用 SafeMath 的 Handler**:
- ✅ `transfer_handler.go` - 使用 `SafeAdd`, `SafeSub`
- ✅ `miner_handler.go` - 使用 `SafeAdd`, `SafeSub`

### 3.3 需检查的文件清单

| 文件 | 检查项 |
|------|-------|
| `vm/order_handler.go` | 撮合金额计算（使用 decimal，需评估） |
| `vm/witness_handler.go` | 质押/奖励金额 |
| `vm/frost_withdraw_request.go` | 提现金额校验 |
| `vm/frost_withdraw_signed.go` | 资金消耗标记 |
| `vm/frost_funds_ledger.go` | 账本余额操作 |
| `vm/frost_vault_*` | DKG/迁移相关金额（如有） |
| `vm/issue_token_handler.go` | Token 发行总量 |
| `vm/freeze_handler.go` | 无金额操作（低优先级） |

### 3.4 检查 Checklist

```markdown
对每个涉及金额的文件执行以下检查:

[ ] 1. 金额解析是否使用 ParseBalance() 或类似安全方法
[ ] 2. 加法是否使用 SafeAdd() 或 decimal.Add()
[ ] 3. 减法是否使用 SafeSub() 并检查下溢
[ ] 4. 是否检查金额为负数的情况
[ ] 5. 是否检查金额超过 MaxUint256
[ ] 6. decimal 类型是否有精度丢失风险
[ ] 7. 字符串与 big.Int 互转是否安全
```

### 3.5 实现步骤

1. **审计阶段** (1 天)
   - 逐个文件检查金额操作
   - 记录问题清单

2. **修复阶段** (1-2 天)
   - 替换不安全的操作为 SafeMath
   - 添加边界检查

3. **测试阶段** (1 天)
   - 添加边界条件测试用例
   - Fuzz testing（可选）

### 3.6 工作量估算: **3-4 天**

---

## 4. 转账 TX 逻辑完善

### 4.1 目标
完善真实的转账逻辑，Explorer 上也能看到转账详情。

### 4.2 当前状态分析

**现有转账实现** (`vm/transfer_handler.go`):
- ✅ 基本转账逻辑完整
- ✅ 使用 SafeAdd/SafeSub
- ✅ 检查余额不足
- ✅ 检查冻结状态
- ✅ 记录转账历史 (`KeyTransferHistory`)
- ⚠️ Explorer 未针对转账类型做特殊展示

**Proto 定义** (`pb/data.proto`):
```protobuf
message Transaction {
  BaseMessage base = 1;
  string to = 2;
  string token_address = 3;
  string amount = 4;
}
```

### 4.3 待完善项

#### 4.3.1 后端完善

| 项目 | 说明 |
|------|------|
| 转账手续费 | 当前只有 `base.fee` 字段，需实现扣除逻辑 |
| 手续费销毁/分配 | 手续费去向（销毁/矿工/国库） |

**实现要点**:

```go
// transfer_handler.go 修改

// 1. 扣除手续费
fee, _ := ParseBalance(transfer.Base.Fee)
totalDeduct, _ := SafeAdd(amount, fee)
if fromBalance.Cmp(totalDeduct) < 0 {
    return nil, receipt("insufficient balance for amount + fee"), err
}

// 2. 手续费处理（示例：销毁）
// 或: 转入矿工账户 / 国库账户
```

#### 4.3.2 Explorer 展示增强

**TxDetail.vue 修改**:

```vue
<!-- 转账类型特殊展示 -->
<div v-if="tx.tx_type === 'transfer'" class="transfer-detail">
  <div class="transfer-flow">
    <span class="from">{{ tx.from_address }}</span>
    <span class="arrow">→</span>
    <span class="to">{{ tx.to_address }}</span>
  </div>
  <div class="amount">{{ formatAmount(tx.details.amount) }} {{ tx.details.token_symbol }}</div>
  <div class="fee">Fee: {{ tx.fee }}</div>
</div>
```

**后端 API 增强** (`cmd/explorer/main.go`):
- 交易详情返回 `to_address`, `amount`, `token_symbol`

### 4.4 需修改的文件

| 文件 | 修改内容 |
|------|---------|
| `vm/transfer_handler.go` | 手续费扣除逻辑 |
| `cmd/explorer/main.go` | 交易详情增强 |
| `explorer/src/components/TxDetail.vue` | 转账类型渲染 |

### 4.5 工作量估算

| 任务 | 预计时间 |
|------|---------|
| 手续费逻辑 | 0.5 天 |
| Explorer 展示 | 0.5 天 |
| 测试 | 0.5 天 |
| **总计** | **1-1.5 天** |

---

## 总体优先级建议

| 优先级 | TODO | 工作量 | 理由 |
|--------|------|--------|------|
| **P0** | 3. VM 金额安全检查 | 3-4 天 | 涉及资金安全，必须优先 |
| **P1** | 4. 转账 TX 完善 | 1-1.5 天 | 基础功能，用户可见 |
| **P2** | 2. 上账挖矿奖励 | 3-4 天 | 经济模型核心 |
| **P3** | 1. Explorer 可视化 | 7-10 天 | 体验优化，可分阶段 |

**总工作量**: 约 14-19 天

---

## 附录: 相关代码位置

```
vm/
├── safe_math.go              # 安全数学运算
├── transfer_handler.go       # 转账处理器
├── miner_handler.go          # 矿工处理器
├── witness_handler.go        # 见证者处理器
├── frost_*.go                # Frost 相关 handlers
├── executor.go               # VM 执行器
└── ...

cmd/explorer/
├── main.go                   # Explorer 后端入口
└── (待新建) frost_handlers.go

explorer/src/
├── components/
│   ├── TxDetail.vue          # 交易详情
│   ├── BlockDetail.vue       # 区块详情
│   └── (待新建) frost/       # Frost 可视化组件
└── api.ts                    # API 调用
```

