# StateRoot 不一致问题修复历程

> [!WARNING]
> **历史文档**：此文档记录的 `verkle/` 模块已在后续重构中完全移除。当前 DB 后端为 PebbleDB flat KV，不再使用 Verkle 状态树。本文仅作历史参考。

## 问题背景

在模拟环境中运行多节点共识时，发现 **StateRoot 不一致**问题：
- 之前第 1 个高度就不一致
- 经过余额分离存储重构后，进步到第 4-5 个高度才不一致
- 日志中大量出现 `[VM] Warning: StateDB sync failed via session: Txn is too big to fit into one request`

---

## 问题排查过程

### 阶段一：运行时同步问题

#### 现象分析
运行模拟器 2 分钟后，检查各节点 Verkle KV 日志发现 MD5 一致，说明**状态变更本身是确定性的**。

但日志中有大量警告：
```
[VM] Warning: StateDB sync failed via session: Txn is too big to fit into one request
```

#### 根因定位
警告来自 `verkle_statedb.go:ApplyUpdate` 中的 `SyncFromStateRootWithSession` 调用：

```go
// 之前的代码：每个区块执行前都尝试同步父区块状态
if height > 1 {
    if syncErr := s.db.tree.SyncFromStateRootWithSession(s.sess, parentRoot); syncErr != nil {
        fmt.Printf("[VM] Warning: StateDB sync failed via session: %v\n", syncErr)
    }
}
```

**问题**：当 Verkle 树变大后，`SyncFromStateRootWithSession` 需要递归读取大量节点，导致 BadgerDB 事务超限。

#### 设计分析
`SyncFromStateRootWithSession` 的设计初衷是在每个区块执行前同步父区块状态，确保多节点内存树一致。但这个设计有问题：

| 正确做法 | 错误做法（之前） |
|---------|---------------|
| 节点启动时一次性恢复状态树 | 每个区块执行前都重新同步 |
| 运行时内存树保持最新 | 每次都从头恢复 |

#### 修复方案
**移除 `ApplyUpdate` 中的运行时同步调用**，状态恢复只在节点启动时完成：

```go
// 修改后：verkle_statedb.go:ApplyUpdate
func (s *VerkleStateDBSession) ApplyUpdate(height uint64, kvs ...KVUpdate) error {
    // ========== 设计变更说明 ==========
    // 之前：每个区块执行前都尝试同步父区块状态（SyncFromStateRootWithSession）
    // 现在：状态恢复只在节点启动时一次性完成（recoverLatestVersion）
    // =================================
    
    // 直接执行更新，不再同步
    // ...
}
```

---

### 阶段二：flushNodes 事务过大

#### 现象
移除运行时同步后，仍有警告。高交易量（5000笔/区块）时问题更严重。

#### 根因定位
警告来自 `go_verkle_adapter.go:UpdateWithSession` 中的 `flushNodes`：

```go
// flushNodes 在单个事务中写入所有节点
func (t *VerkleTree) flushNodes(sess VersionedStoreSession, version Version) error {
    for _, sn := range serializedNodes {
        _ = sess.Set(nodeKey(sn.CommitmentBytes[:]), sn.SerializedBytes, version)
    }
    return nil
}
```

**问题**：单个事务写入所有 Verkle 节点，数据量超过 BadgerDB 10MB 限制。

#### 修复方案
**分批写入**，每批最多 200 个节点使用独立事务：

```go
// 修改后：分批写入
const batchSize = 200
for i := 0; i < len(validNodes); i += batchSize {
    batch := validNodes[i:end]
    
    batchSess, _ := t.store.NewSession()
    for _, nd := range batch {
        _ = batchSess.Set(nodeKey(nd.key), nd.data, version)
    }
    batchSess.Commit()
    batchSess.Close()
}
```

---

### 阶段三：大值写入事务过大

#### 现象
5000 笔交易时仍然出错。

#### 根因定位
`UpdateWithSession` 主事务中除了 `flushNodes`，还有**大值写入**：

```go
// 之前：大值写入在主事务中
for i := 0; i < len(keys); i++ {
    if len(val) > maxDirectValueSize {
        sess.Set(largeValueKey(hashPart), val, newVersion)  // 5000笔交易可能产生数千个大值
    }
}
```

#### 修复方案
**大值分批独立事务写入**：

```go
// 修改后：四阶段处理
// 第一阶段：收集并分批写入大值（每批100个，独立事务）
const largeValueBatchSize = 100
for i := 0; i < len(largeValues); i += largeValueBatchSize {
    batchSess, _ := t.store.NewSession()
    for _, lv := range batch {
        batchSess.Set(lv.key, lv.val, newVersion)
    }
    batchSess.Commit()
}

// 第二阶段：更新树结构（纯内存操作）
for _, entry := range processedEntries {
    t.root.Insert(entry.verkleKey[:], entry.treeVal, resolver)
}

// 第三阶段：保存根承诺和根节点（小数据，主事务）
sess.Set(rootKey(newVersion), rootCommitment[:], newVersion)

// 第四阶段：分批持久化树节点（已优化）
t.flushNodes(sess, newVersion)
```

---

## 修改文件汇总

| 文件 | 修改内容 |
|-----|---------|
| `verkle/verkle_statedb.go` | 移除 `ApplyUpdate` 中的运行时同步 |
| `verkle/go_verkle_adapter.go` | `flushNodes` 分批写入（每批200节点）；`UpdateWithSession` 大值分批写入（每批100个） |

---

## 容量设计

| 组件 | 批次大小 | 预估单批数据量 | BadgerDB 限制 |
|-----|---------|--------------|--------------|
| 大值写入 | 100 个 | ~20KB | 10MB |
| 树节点写入 | 200 个 | ~50KB | 10MB |

---

## 验证结果

修复后：
- ✅ 所有节点 KV 日志 MD5 完全一致
- ✅ 共识正常（所有节点同一高度，同一 LastAccepted）
- ✅ 无 "Txn is too big" 警告
