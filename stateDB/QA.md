# StateDB 轻节点同步 Q&A

## 问题

**如果节点只同步 StateDB 数据 + 后续区块数据，不同步过去历史，就能维持后续的共识和 VM 产出与其他全节点保持一致，还需要添加哪些数据进 StateDB 管理？**

---

## 当前状态分析

### 目前 StateDB 管理的数据

StateDB 目前**只管理账户数据（Account）**，其他所有数据都只存储在 Badger 中。

| 数据类型 | SyncStateDB | Key 格式 | 说明 |
|---------|-------------|----------|------|
| **Account** | ✅ true | `v1_account_{address}` | 账户状态（余额、nonce、订单列表等） |
| Transfer History | ❌ false | `v1_transfer_history_{txId}` | 转账历史记录 |
| Order Data | ❌ false | `v1_order_{orderId}` | 订单详情 |
| Order Index | ❌ false | `v1_order_index_*` | 订单索引 |
| Freeze Mark | ❌ false | `v1_freeze_{addr}_{token}` | 冻结标记 |
| Freeze History | ❌ false | `v1_freeze_history_{txId}` | 冻结历史 |
| Token Data | ❌ false | `v1_token_{tokenAddr}` | Token 元数据 |
| Token Registry | ❌ false | `v1_token_registry` | Token 注册表 |
| Recharge Record | ❌ false | `v1_recharge_record_{addr}_{tweak}` | 充值记录 |
| Miner History | ❌ false | `v1_miner_history_{txId}` | 挖矿历史 |
| Candidate History | ❌ false | `v1_candidate_history_{txId}` | 投票历史 |

### 当前问题

如果节点只同步 **StateDB（仅账户数据）+ 后续区块**，会导致以下 Handler 执行失败：

| Handler | 缺失数据 | 影响 |
|---------|---------|------|
| **TransferTxHandler** | ❌ 冻结标记 (`v1_freeze_{addr}_{token}`) | 无法检查账户是否被冻结 |
| **OrderTxHandler** | ❌ 订单数据 (`v1_order_{orderId}`) | 无法撤单（找不到订单） |
| **RechargeTxHandler** | ❌ Token 数据 (`v1_token_{tokenAddr}`)<br>❌ 充值记录 (`v1_recharge_record_{addr}_{tweak}`) | 无法验证 Token 存在<br>无法防止重复充值 |
| **IssueTokenTxHandler** | ❌ Token 数据 (`v1_token_{tokenAddr}`)<br>❌ Token 注册表 (`v1_token_registry`) | 无法检查 Token 是否已存在<br>无法维护 Token 列表 |

---

## 解决方案

### 需要添加到 StateDB 的数据

为了支持轻节点同步，需要将以下数据添加到 StateDB：

#### 1. 冻结标记（Freeze Mark）- 高优先级 ⭐⭐⭐

**依赖场景**：
- `vm/transfer_handler.go` - 转账前必须检查账户是否被冻结

**代码位置**：
```go
// vm/transfer_handler.go:49-57
freezeKey := keys.KeyFreeze(transfer.Base.FromAddress, transfer.TokenAddress)
freezeData, isFrozen, _ := sv.Get(freezeKey)
if isFrozen && string(freezeData) == "true" {
    return nil, &Receipt{
        Status: "FAILED",
        Error:  "sender account is frozen for this token",
    }, fmt.Errorf("sender account is frozen")
}
```

**修改方案**：
```go
// vm/freeze_handler.go:116, 125
ws = append(ws, WriteOp{
    Key:         freezeKey,
    Value:       []byte("true"),
    Del:         false,
    SyncStateDB: true,  // ✨ 改为 true（原来是 false）
    Category:    "freeze",
})
```

#### 2. 订单数据（Order Data）- 高优先级 ⭐⭐⭐

**依赖场景**：
- `vm/order_handler.go` - 撤单时必须读取订单数据验证所有权

**代码位置**：
```go
// vm/order_handler.go:180-187
targetOrderKey := keys.KeyOrder(ord.OpTargetId)
targetOrderData, exists, err := sv.Get(targetOrderKey)
if err != nil || !exists {
    return nil, &Receipt{
        Status: "FAILED",
        Error:  "target order not found",
    }, fmt.Errorf("target order not found")
}
```

**修改方案**：
```go
// vm/order_handler.go:155 (创建订单)
ws = append(ws, WriteOp{
    Key:         orderKey,
    Value:       orderData,
    Del:         false,
    SyncStateDB: true,  // ✨ 改为 true（原来是 false）
    Category:    "order",
})

// vm/order_handler.go:236 (删除订单)
ws = append(ws, WriteOp{
    Key:         targetOrderKey,
    Value:       nil,
    Del:         true,
    SyncStateDB: true,  // ✨ 改为 true（原来是 false）
    Category:    "order",
})
```

#### 3. Token 数据（Token Data）- 中优先级 ⭐⭐

**依赖场景**：
- `vm/recharge_handler.go` - 充值前必须验证 Token 是否存在
- `vm/issue_token_handler.go` - 发币前必须检查 Token 是否已存在

**代码位置**：
```go
// vm/recharge_handler.go:56-71
tokenKey := keys.KeyToken(recharge.TokenAddress)
_, tokenExists, err := sv.Get(tokenKey)
if !tokenExists {
    return nil, &Receipt{
        Status: "FAILED",
        Error:  "token not found",
    }, fmt.Errorf("token not found")
}

// vm/issue_token_handler.go:70-76
tokenKey := keys.KeyToken(tokenAddress)
_, tokenExists, _ := sv.Get(tokenKey)
if tokenExists {
    return nil, &Receipt{
        Status: "FAILED",
        Error:  "token already exists",
    }, fmt.Errorf("token already exists")
}
```

**修改方案**：
```go
// vm/issue_token_handler.go:127
ws = append(ws, WriteOp{
    Key:         tokenKey,
    Value:       tokenData,
    Del:         false,
    SyncStateDB: true,  // ✨ 改为 true（原来是 false）
    Category:    "token",
})
```

#### 4. 充值记录（Recharge Record）- 中优先级 ⭐⭐

**依赖场景**：
- `vm/recharge_handler.go` - 防止使用相同 tweak 重复充值

**代码位置**：
```go
// vm/recharge_handler.go:76-83
rechargeRecordKey := keys.KeyRechargeRecord(recharge.Base.FromAddress, recharge.Tweak)
_, recordExists, _ := sv.Get(rechargeRecordKey)
if recordExists {
    return nil, &Receipt{
        Status: "FAILED",
        Error:  "recharge address already used",
    }, fmt.Errorf("recharge address already used")
}
```

**修改方案**：
```go
// vm/recharge_handler.go:182
ws = append(ws, WriteOp{
    Key:         rechargeRecordKey,
    Value:       []byte("used"),
    Del:         false,
    SyncStateDB: true,  // ✨ 改为 true（原来是 false）
    Category:    "record",
})
```

#### 5. Token 注册表（Token Registry）- 低优先级 ⭐

**依赖场景**：
- `vm/issue_token_handler.go` - 维护全局 Token 列表

**代码位置**：
```go
// vm/issue_token_handler.go:149-156
registryKey := keys.KeyTokenRegistry()
registryData, _, _ := sv.Get(registryKey)

var registry pb.TokenRegistry
if registryData != nil {
    json.Unmarshal(registryData, &registry)
}
```

**修改方案**：
```go
// vm/issue_token_handler.go:173
ws = append(ws, WriteOp{
    Key:         registryKey,
    Value:       registryData,
    Del:         false,
    SyncStateDB: true,  // ✨ 改为 true（原来是 false）
    Category:    "registry",
})
```

---

## 完整修改清单

### 需要修改的文件

| 文件 | 行号 | 修改内容 | 优先级 |
|------|------|---------|--------|
| `vm/freeze_handler.go` | 116, 125 | `SyncStateDB: false` → `true` | ⭐⭐⭐ |
| `vm/order_handler.go` | 155, 236 | `SyncStateDB: false` → `true` | ⭐⭐⭐ |
| `vm/issue_token_handler.go` | 127, 173 | `SyncStateDB: false` → `true` | ⭐⭐ |
| `vm/recharge_handler.go` | 182, 195 | `SyncStateDB: false` → `true` | ⭐⭐ |
| `stateDB/update.go` | - | 支持多前缀过滤 | ⭐⭐⭐ |

### 修改后的数据分布

| 数据类型 | SyncStateDB | 说明 |
|---------|-------------|------|
| **Account** | ✅ true | 账户状态（已有） |
| **Freeze** | ✅ true | 冻结标记（新增） |
| **Order** | ✅ true | 订单数据（新增） |
| **Token** | ✅ true | Token 元数据（新增） |
| **Recharge Record** | ✅ true | 充值记录（新增） |
| **Token Registry** | ✅ true | Token 注册表（新增） |
| **History** | ❌ false | 历史记录（不需要） |
| **Index** | ❌ false | 索引数据（可重建） |

---

## StateDB 配置修改

### 当前实现

```go
// stateDB/update.go
func (s *DB) ApplyAccountUpdate(height uint64, kvs ...KVUpdate) error {
    // ...
    for _, kv := range kvs {
        // 只接管 account 命名空间
        if !bytes.HasPrefix([]byte(kv.Key), []byte(s.conf.AccountNSPrefix)) {
            continue // 跳过非账户数据
        }
        sh := shardOf(kv.Key, s.conf.ShardHexWidth)
        s.mem.apply(height, kv.Key, kv.Value, kv.Deleted, sh)
    }
    // ...
}
```

### 修改方案

```go
// stateDB/update.go
func (s *DB) ApplyAccountUpdate(height uint64, kvs ...KVUpdate) error {
    // ...
    for _, kv := range kvs {
        // 支持多个命名空间
        if !s.isStatefulKey(kv.Key) {
            continue
        }
        sh := shardOf(kv.Key, s.conf.ShardHexWidth)
        s.mem.apply(height, kv.Key, kv.Value, kv.Deleted, sh)
    }
    // ...
}

// 新增方法：判断 key 是否需要同步到 StateDB
func (s *DB) isStatefulKey(key string) bool {
    prefixes := []string{
        "v1_account_",          // 账户数据
        "v1_freeze_",           // 冻结标记
        "v1_order_",            // 订单数据
        "v1_token_",            // Token 数据
        "v1_recharge_record_",  // 充值记录
        "v1_token_registry",    // Token 注册表
    }
    for _, prefix := range prefixes {
        if bytes.HasPrefix([]byte(key), []byte(prefix)) {
            return true
        }
    }
    return false
}
```

---

## 数据量估算

假设有 100 万用户，10 万个 Token，50 万个活跃订单：

| 数据类型 | 数量 | 单条大小 | 总大小 |
|---------|------|---------|--------|
| Account | 1,000,000 | ~500 bytes | ~500 MB |
| Freeze | 100,000 | ~50 bytes | ~5 MB |
| Order | 500,000 | ~200 bytes | ~100 MB |
| Token | 100,000 | ~300 bytes | ~30 MB |
| Recharge Record | 1,000,000 | ~50 bytes | ~50 MB |
| Token Registry | 1 | ~30 KB | ~30 KB |
| **总计** | - | - | **~685 MB** |

**对比**：
- **当前方案**（仅账户）：~500 MB
- **完整方案**（所有状态）：~685 MB
- **增加**：~185 MB（+37%）

---

## 轻节点同步流程

```
┌─────────────────────────────────────────────────────────┐
│  步骤 1: 同步 StateDB 快照（最近的 Epoch）               │
│  包含：                                                  │
│  • v1_account_*          (账户)                         │
│  • v1_freeze_*           (冻结标记)                     │
│  • v1_order_*            (订单)                         │
│  • v1_token_*            (Token)                        │
│  • v1_recharge_record_*  (充值记录)                     │
│  • v1_token_registry     (Token 注册表)                 │
└─────────────────────────────────────────────────────────┘
                          ↓
┌─────────────────────────────────────────────────────────┐
│  步骤 2: 同步后续区块（从 Epoch 结束到当前高度）        │
│  应用每个区块的交易，更新 StateDB                       │
└─────────────────────────────────────────────────────────┘
                          ↓
┌─────────────────────────────────────────────────────────┐
│  步骤 3: 开始参与共识                                    │
│  ✅ 所有 Handler 都能正常执行                           │
│  ✅ VM 产出与全节点一致                                 │
└─────────────────────────────────────────────────────────┘
```

---

## 注意事项

### 1. 索引数据不需要同步

以下索引可以从主数据重建，不需要同步到 StateDB：

- `v1_order_index_{address}_{orderId}` - 可从 Account.Orders 重建
- `v1_order_price_index_*` - 可从订单数据重建
- `v1_candidate_index_*` - 可从 Account.Candidate 重建

### 2. 历史数据不需要同步

历史记录只用于查询，不影响 VM 执行：

- `v1_transfer_history_{txId}`
- `v1_miner_history_{txId}`
- `v1_candidate_history_{txId}`
- `v1_freeze_history_{txId}`
- `v1_recharge_history_{txId}`
- `v1_issue_token_history_{txId}`

### 3. 设计原理

**为什么只同步这些数据？**

- **账户数据**：核心状态，必须同步
- **冻结标记**：影响转账验证，必须同步
- **订单数据**：影响撤单验证，必须同步
- **Token 数据**：影响充值和发币验证，必须同步
- **充值记录**：防止重复充值，必须同步
- **Token 注册表**：维护 Token 列表，建议同步

**为什么历史和索引不同步？**

- **历史记录**：只用于查询，不影响共识
- **索引数据**：可以从主数据重建，不是必需状态

---

## 实施建议

### 阶段 1：核心数据（必须）⭐⭐⭐

1. 修改 `vm/freeze_handler.go` - 同步冻结标记
2. 修改 `vm/order_handler.go` - 同步订单数据
3. 修改 `stateDB/update.go` - 支持多前缀

**完成后效果**：轻节点可以处理转账和订单交易

### 阶段 2：Token 数据（推荐）⭐⭐

1. 修改 `vm/issue_token_handler.go` - 同步 Token 数据
2. 修改 `vm/recharge_handler.go` - 同步充值记录

**完成后效果**：轻节点可以处理所有交易类型

### 阶段 3：优化（可选）⭐

1. 添加 StateDB 数据压缩
2. 优化分片策略（256 片 vs 16 片）
3. 添加增量同步优化

---

## 总结

要支持轻节点同步，需要将 **冻结标记、订单、Token、充值记录** 这 4 类数据添加到 StateDB。这会使 StateDB 大小增加约 37%，但能保证轻节点只同步状态快照就能参与共识，无需下载完整历史。

**关键收益**：
- ✅ 轻节点快速启动（只需同步 ~685 MB 状态）
- ✅ 所有交易类型都能正常执行
- ✅ VM 产出与全节点完全一致
- ✅ 支持快速状态同步（Fast Sync）

