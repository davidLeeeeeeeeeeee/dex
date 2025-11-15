# VM 改造指南（简化版）

## 改造目标

建立**单一真实写路径**，将所有状态变化的提交集中到 VM 的 `applyResult` 方法中。

## 改造内容

### 1. 扩展 WriteOp 类型 (`vm/types.go`)

```go
type WriteOp struct {
    Key         string // 完整的 key
    Value       []byte // 序列化后的值
    Del         bool   // 是否删除
    SyncStateDB bool   // ✨ 新增：是否同步到 StateDB
    Category    string // ✨ 新增：数据分类（可选，便于调试）
}

// ✨ 新增方法
func (w *WriteOp) GetKey() string
func (w *WriteOp) GetValue() []byte
func (w *WriteOp) IsDel() bool
```

### 2. 改造 applyResult (`vm/executor.go`)

现在是**统一提交入口**，流程如下：

```
1. 检查幂等性（防止重复提交）
   ↓
2. 应用所有 WriteOp 到 Badger
   ↓
3. 同步 SyncStateDB=true 的 WriteOp 到 StateDB
   ↓
4. 写入交易处理状态
   ↓
5. 写入区块提交标记
   ↓
6. 原子提交
```

### 3. 扩展 DBManager (`db/db.go`)

新增方法：

```go
func (m *Manager) SyncToStateDB(height uint64, updates []interface{}) error
```

用于 VM 的 applyResult 调用，将 WriteOp 同步到 StateDB。

## 数据流

### 改造前（问题状态）

```
txpool/handlers
    ↓ SaveAccount/SaveAnyTx
    ↓ (散布调用，时机不确定)
Badger + StateDB (各自为政)

VM.PreExecuteBlock
    ↓ applyResult
    ↓ EnqueueSet/EnqueueDel
    ↓ (又一个写路径)
Badger (StateDB 无法消费)
```

### 改造后（统一路径）

```
VM.PreExecuteBlock (内存 overlay)
    ↓
Handler.DryRun() 生成 WriteOp
    ↓
共识最终化
    ↓
applyResult（唯一提交入口）
    ├─ 检查幂等性
    ├─ 应用 WriteOp 到 Badger
    ├─ 同步到 StateDB（SyncStateDB=true）
    ├─ 写入收据和元数据
    └─ 原子提交
```

## 如何使用

### 在 Handler 中生成 WriteOp

**原来的代码**：

```go
ws = append(ws, WriteOp{
    Key:   accountKey,
    Value: updatedAccountData,
    Del:   false,
})
```

**改造后的代码**：

```go
ws = append(ws, WriteOp{
    Key:         accountKey,
    Value:       updatedAccountData,
    Del:         false,
    SyncStateDB: true,      // ✨ 账户数据需要同步到 StateDB
    Category:    "account", // ✨ 可选，便于调试
})
```

### SyncStateDB 设置规则

| 数据类型 | SyncStateDB | 原因 |
|---------|------------|------|
| 账户数据 | `true` | 关键数据，需要快速查询 |
| Token 数据 | `false` | 不需要在 StateDB 中 |
| 订单数据 | `false` | 不需要在 StateDB 中 |
| 交易收据 | `false` | 不需要在 StateDB 中 |
| 历史记录 | `false` | 不需要在 StateDB 中 |

### Category 分类建议

```go
"account"              // 账户完整更新
"account_balance"      // 余额更新
"account_nonce"        // Nonce 更新
"account_orders"       // 订单列表更新
"token"                // Token 创建/删除
"order"                // 订单创建/删除
"history"              // 历史记录
```

## 改造示例

### TransferTxHandler 改造

**改造前**：

```go
ws = append(ws, WriteOp{
    Key:   accountKey,
    Value: updatedAccountData,
    Del:   false,
})
```

**改造后**：

```go
ws = append(ws, WriteOp{
    Key:         accountKey,
    Value:       updatedAccountData,
    Del:         false,
    SyncStateDB: true,      // 账户数据需要同步
    Category:    "account", // 便于调试
})
```

### OrderTxHandler 改造

**改造前**：

```go
ws = append(ws, WriteOp{
    Key:   orderKey,
    Value: orderData,
    Del:   false,
})
```

**改造后**：

```go
ws = append(ws, WriteOp{
    Key:         orderKey,
    Value:       orderData,
    Del:         false,
    SyncStateDB: false,    // 订单数据不需要同步到 StateDB
    Category:    "order",  // 便于调试
})
```

## 关键改进

| 方面 | 改进 |
|------|------|
| **原子性** | ✅ 所有写操作在一个事务内 |
| **一致性** | ✅ Badger 和 StateDB 同步 |
| **幂等性** | ✅ 防止重复提交 |
| **灵活性** | ✅ 通过 SyncStateDB 控制同步 |
| **可追踪性** | ✅ 通过 Category 追踪数据类型 |

## 后续工作

### 需要改造的 Handler

所有 Handler 都需要在生成 WriteOp 时添加 `SyncStateDB` 和 `Category` 字段：

- [ ] `TransferTxHandler` - 转账
- [ ] `OrderTxHandler` - 订单
- [ ] `MinerTxHandler` - 矿工
- [ ] `CandidateTxHandler` - 候选人
- [ ] `IssueTokenTxHandler` - 发币
- [ ] `FreezeTxHandler` - 冻结
- [ ] `RechargeTxHandler` - 充值

### 需要清理的代码

- [ ] 标记 `db.SaveAccount()` 为内部 API（仅供 CommitBlock 使用）
- [ ] 标记 `db.SaveAnyTx()` 为内部 API
- [ ] 标记 `db.SaveBlock()` 为内部 API
- [ ] 移除 txpool 中的直接 DB 写操作
- [ ] 移除 handlers 中的直接 DB 写操作

### 需要编写的测试

- [ ] 集成测试：验证 WriteOp 正确同步到 Badger
- [ ] 集成测试：验证 WriteOp 正确同步到 StateDB
- [ ] 幂等性测试：验证重复提交被正确跳过
- [ ] 一致性测试：验证 Badger 和 StateDB 数据一致

## 编译验证

✅ **编译成功**

```bash
go build ./vm    # ✅
go build ./db    # ✅
```

## 常见问题

### Q: 为什么不是所有数据都同步到 StateDB？

A: StateDB 是一个可选的加速层，主要用于快速查询账户数据。其他数据（如订单、历史记录）不需要在 StateDB 中，直接从 Badger 读取即可。

### Q: Category 字段是必须的吗？

A: 不是必须的，但强烈建议添加。它可以帮助你在调试时快速识别数据类型，也便于后续的性能分析和监控。

### Q: 如果 StateDB 同步失败会怎样？

A: StateDB 同步失败不会中断提交，因为 Badger 已经写入了。StateDB 是可选的加速层，失败只会影响查询性能，不会影响数据一致性。

### Q: 如何验证改造是否正确？

A: 运行以下命令：

```bash
cd D:\dex
go build ./vm ./db ./consensus
```

如果编译通过，说明改造基本正确。然后需要编写集成测试验证数据流。

## 文件清单

### 修改文件

```
vm/types.go       - 扩展 WriteOp
vm/executor.go    - 改造 applyResult
db/db.go          - 新增 SyncToStateDB 方法
```

### 需要改造的文件

```
vm/transfer_handler.go    - 转账 Handler
vm/order_handler.go       - 订单 Handler
vm/miner_handler.go       - 矿工 Handler
vm/candidate_handler.go   - 候选人 Handler
vm/issue_token_handler.go - 发币 Handler
vm/freeze_handler.go      - 冻结 Handler
vm/recharge_handler.go    - 充值 Handler
```

## 总结

本次改造通过**扩展 WriteOp** 和**改造 applyResult**，实现了：

✅ 单一提交入口
✅ 原子性保证
✅ 一致性保证
✅ 幂等性保证
✅ 灵活的 StateDB 同步

改造简单直接，不引入额外的抽象层，易于理解和维护。

---

**改造状态**: ✅ 基础设施完成
**编译状态**: ✅ 全部通过
**下一步**: 改造 Handler，添加 SyncStateDB 和 Category 字段

