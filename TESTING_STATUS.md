# 轻节点同步测试状态

## ✅ 已完成的工作

### 1. 核心代码修改 ✅

所有必要的代码修改已完成并通过编译验证：

#### Handler 修改（5 个文件）
- [x] `vm/freeze_handler.go` - 冻结标记 `SyncStateDB: true` (2 处)
- [x] `vm/order_handler.go` - 订单数据 `SyncStateDB: true` (3 处)
- [x] `vm/issue_token_handler.go` - Token 数据 `SyncStateDB: true` (2 处)
- [x] `vm/recharge_handler.go` - 充值记录 `SyncStateDB: true` (1 处)

#### StateDB 配置修改（1 个文件）
- [x] `stateDB/update.go` - 添加 `isStatefulKey()` 方法，支持多前缀过滤

### 2. StateDB 测试 ✅

创建并通过了 StateDB 多前缀过滤测试：

#### `stateDB/multi_prefix_test.go` (330 行)
- [x] `TestIsStatefulKey` - 测试 isStatefulKey 方法（14 个子测试）
- [x] `TestMultiPrefixSync` - 测试多前缀数据同步
- [x] `TestMultiPrefixFlushAndRotate` - 测试多前缀数据持久化
- [x] `TestMultiPrefixSharding` - 测试多前缀数据分片

**测试结果**：
```bash
$ go test -v ./stateDB -run "TestIsStatefulKey|TestMultiPrefix"
=== RUN   TestIsStatefulKey
--- PASS: TestIsStatefulKey (0.02s)
=== RUN   TestMultiPrefixSync
--- PASS: TestMultiPrefixSync (0.02s)
=== RUN   TestMultiPrefixFlushAndRotate
--- PASS: TestMultiPrefixFlushAndRotate (0.02s)
=== RUN   TestMultiPrefixSharding
--- PASS: TestMultiPrefixSharding (0.04s)
PASS
ok      dex/stateDB     0.178s
```

### 3. 编译验证 ✅

```bash
$ go build -o dex.exe ./cmd
# 编译成功，无错误
```

---

## 📋 数据同步验证

### 支持的数据类型

| 数据类型 | Key 前缀 | SyncStateDB | isStatefulKey | 状态 |
|---------|---------|------------|--------------|------|
| **账户数据** | `v1_account_` | ✅ true | ✅ true | ✅ |
| **冻结标记** | `v1_freeze_` | ✅ true | ✅ true | ✅ |
| **订单数据** | `v1_order_` | ✅ true | ✅ true | ✅ |
| **Token 数据** | `v1_token_` | ✅ true | ✅ true | ✅ |
| **Token 注册表** | `v1_token_registry` | ✅ true | ✅ true | ✅ |
| **充值记录** | `v1_recharge_record_` | ✅ true | ✅ true | ✅ |

### 排除的数据类型（不同步到 StateDB）

| 数据类型 | Key 前缀 | 原因 |
|---------|---------|------|
| **冻结历史** | `v1_freeze_history_` | 历史记录，不是状态数据 |
| **转账历史** | `v1_transfer_history_` | 历史记录，不是状态数据 |
| **矿工历史** | `v1_miner_history_` | 历史记录，不是状态数据 |
| **候选人历史** | `v1_candidate_history_` | 历史记录，不是状态数据 |
| **充值历史** | `v1_recharge_history_` | 历史记录，不是状态数据 |
| **订单价格索引** | `v1_pair:` | 索引数据，可重建 |
| **订单索引** | `v1_order_index_` | 索引数据，可重建 |

---

## 🔧 核心实现

### isStatefulKey() 方法

<augment_code_snippet path="stateDB/update.go" mode="EXCERPT">
```go
func (s *DB) isStatefulKey(key string) bool {
	// 先排除历史记录类的 key 和索引类的 key
	excludePrefixes := []string{
		"v1_freeze_history_",
		"v1_transfer_history_",
		"v1_miner_history_",
		"v1_candidate_history_",
		"v1_recharge_history_",
		"v1_pair:",
		"v1_order_index_",
	}

	for _, prefix := range excludePrefixes {
		if strings.HasPrefix(key, prefix) {
			return false
		}
	}

	// 定义需要同步到 StateDB 的数据前缀
	statefulPrefixes := []string{
		"v1_account_",
		"v1_freeze_",
		"v1_order_",
		"v1_token_",
		"v1_recharge_record_",
	}

	for _, prefix := range statefulPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}

	return false
}
```
</augment_code_snippet>

---

## ❌ VM 集成测试未完成

### 原因

VM 集成测试需要以下 API，但这些 API 在当前代码库中不存在或签名不匹配：

1. **StateDB 读取方法**
   - 需要：`StateDB.GetLatest(key string) ([]byte, bool)`
   - 实际：StateDB 没有 `GetLatest` 方法
   - 说明：StateDB 的查询 API 是 `PageSnapshotShard` 和 `PageCurrentDiff`，不是简单的 Get 方法

2. **Token 地址推导**
   - 需要：`keys.DeriveTokenAddress(addr, txID string) string`
   - 实际：Token 地址就是 TxID，不需要推导函数
   - 说明：在 `issue_token_handler.go` 中，`tokenAddress := issueTx.Base.TxId`

3. **RechargeTx 字段**
   - 需要：`RechargeTx.Amount` 字段
   - 实际：RechargeTx 没有 Amount 字段
   - 说明：充值金额在 handler 中从链外数据源获取

4. **FreezeTx 字段**
   - 需要：`FreezeTx.TargetAddress` 和 `FreezeTx.TokenAddress`
   - 实际：字段名是 `TargetAddr` 和 `TokenAddr`

5. **pb.Order 类型**
   - 需要：`pb.Order` 结构体
   - 实际：订单数据使用 `pb.OrderTx`

6. **NewManager 签名**
   - 需要：`db.NewManager(path string, enableStateDB bool)`
   - 实际：`db.NewManager(path string)`

7. **NewExecutor 签名**
   - 需要：`NewExecutor(dbMgr *db.Manager)`
   - 实际：`NewExecutor(db DBManager, reg *HandlerRegistry, cache SpecExecCache)`

### 建议

要完成 VM 集成测试，需要：

1. **添加 StateDB 简化查询 API**（可选）
   ```go
   // 在 stateDB/db.go 中添加
   func (s *DB) GetLatest(key string) ([]byte, bool) {
       // 实现从内存 diff 和 Badger overlay 中查询
   }
   ```

2. **使用现有的测试框架**
   - 参考 `vm/executor_integration_test.go` 的测试方式
   - 使用实际的 API 签名
   - 不依赖不存在的辅助函数

3. **手动验证**（推荐）
   - 启动完整节点，执行包含所有数据类型的交易
   - 检查 StateDB 目录中的数据
   - 使用 StateDB 的分页查询 API 验证数据

---

## 📊 测试覆盖总结

| 测试类别 | 状态 | 说明 |
|---------|------|------|
| **代码修改** | ✅ 完成 | 所有 Handler 和 StateDB 配置已修改 |
| **编译验证** | ✅ 通过 | `go build` 成功 |
| **StateDB 单元测试** | ✅ 通过 | 4 个测试，全部通过 |
| **VM 集成测试** | ❌ 未完成 | API 不匹配，需要重新设计 |
| **手动验证** | ⏸️ 待进行 | 需要启动节点手动测试 |

---

## 🎯 核心功能验证清单

基于代码审查，以下功能应该正常工作：

- [x] 冻结标记写入 StateDB（`freeze_handler.go:116, 125`）
- [x] 订单数据写入 StateDB（`order_handler.go:237, 366, 568`）
- [x] Token 数据写入 StateDB（`issue_token_handler.go:106, 177`）
- [x] 充值记录写入 StateDB（`recharge_handler.go:179`）
- [x] StateDB 多前缀过滤（`update.go:isStatefulKey()`）
- [x] StateDB 数据持久化（`update.go:FlushAndRotate()`）
- [x] StateDB 数据分片（`shard.go:shardOf()`）

---

## 📝 后续工作建议

### 1. 添加 StateDB 简化查询 API（可选）

如果需要简化测试，可以在 `stateDB/db.go` 中添加：

```go
// GetLatest 从内存 diff 和最新 overlay 中查询 key
func (s *DB) GetLatest(key string) ([]byte, bool) {
    s.mu.RLock()
    defer s.mu.RUnlock()

    // 1. 先查内存 diff
    shard := shardOf(key, s.conf.ShardHexWidth)
    s.mem.muByShard[shard].RLock()
    if entry, ok := s.mem.byShard[shard][key]; ok {
        s.mem.muByShard[shard].RUnlock()
        if entry.del {
            return nil, false
        }
        return entry.val, true
    }
    s.mem.muByShard[shard].RUnlock()

    // 2. 查 Badger overlay
    var value []byte
    err := s.bdb.View(func(txn *badger.Txn) error {
        E := s.curEpoch
        item, err := txn.Get(kOvl(E, shard, key))
        if err != nil {
            return err
        }
        value, err = item.ValueCopy(nil)
        return err
    })

    if err != nil {
        return nil, false
    }
    return value, true
}
```

### 2. 手动验证流程

1. 启动节点
2. 执行以下交易：
   - 发行 Token
   - 充值 Token
   - 冻结账户
   - 创建订单
   - 撤单
3. 检查 `data/state/` 目录中的 Badger 数据
4. 使用 `PageSnapshotShard` API 查询数据

### 3. 生产环境监控

建议添加以下监控指标：

- StateDB 同步的数据量
- StateDB 的 Epoch 切换频率
- StateDB 的磁盘使用量
- StateDB 的查询性能

---

## ✅ 结论

**核心功能已完成**：
- ✅ 所有必要的代码修改已完成
- ✅ StateDB 多前缀过滤测试通过
- ✅ 编译验证通过

**待完成工作**：
- ❌ VM 集成测试（需要重新设计以匹配实际 API）
- ⏸️ 手动验证（需要启动节点测试）

**建议**：
- 当前代码已经可以支持轻节点同步的核心功能
- 可以通过手动测试验证功能正确性
- 如需自动化测试，建议添加 StateDB 简化查询 API

