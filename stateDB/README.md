# StateDB - 状态数据库

## 概述

StateDB 是一个基于 Badger 的高性能状态数据库，专为区块链账户状态同步设计。它采用快照（Snapshot）+ 增量（Diff）的混合模型，支持高效的状态同步和崩溃恢复。

### 核心特性

- **快照层（SNAP）**：在 Epoch 边界（每 40,000 个区块）生成虚拟快照
- **内存 Diff**：当前 Epoch 的变更保存在内存窗口中
- **分片存储**：支持 16 或 256 个分片，便于并行同步
- **WAL 支持**：可选的预写日志，防止崩溃时数据丢失
- **高度映射**：精确的区块高度到序列号的双向映射

---

## 架构设计

### 1. 数据模型

#### 快照层（SNAP）

在高度 E（整除 40,000）产出"虚拟快照"，不做整库全量拷贝：

- **首个快照**：可做全量（在线扫描，不暂停）
- **后续快照**：采用 Base + Overlay 模式
  - 以上一个快照为基底
  - 仅为"本 Epoch 改过的键"写 overlay
  - 极大减轻 IO 与磁盘压力

#### 内存 Diff（DIFF_MEM）

- **范围**：[E+1, E+40000] 的变更都进内存窗口
- **特性**：支持分片化 & 分页导出
- **可选 WAL**：若担心宕机丢失，可开启 WAL，按块滚动写入（TTL=一个 Epoch）

### 2. 分片与分页

#### 一级前缀
```
v1_account_<address>
```
与现有系统一致，方便筛选 account 键

#### 二级分片

对 `addr` 做 `sha256([]byte(addr))`，取前 N 个十六进制字符：
- **N=1**：16 个分片（0-f）
- **N=2**：256 个分片（00-ff）

键格式：
```
s1|snap|<E>|<shard>|<addr>  # 快照键
s1|ovl|<E>|<shard>|<addr>   # Overlay 键
```

#### 分页机制

- 每个 shard 内按字典序分页
- 返回参数：`total_pages`、`page`、`page_size`
- 提供 `next_page_token`（基于最后一个 key 的 seek token）

### 3. 版本与高度映射

- **Badger Ts**：仅用于留存 10 个版本的元信息
- **Sequence**：使用 Badger Sequence 生成单调递增的提交序号

映射关系：
```
meta:h2seq:<height> => seq
meta:seq2h:<seq>    => height
```

查询或审计时可按高度精确定位到对应的提交点。

---

## 外部初始化流程

### 方式一：独立使用 StateDB

如果需要单独使用 StateDB（不集成到现有系统）：

```go
package main

import (
    "dex/stateDB"
    "log"
)

func main() {
    // 1. 创建配置
    cfg := statedb.Config{
        DataDir:         "stateDB/data",      // 数据目录
        EpochSize:       40000,                // Epoch 大小
        ShardHexWidth:   1,                    // 分片宽度（1=16片，2=256片）
        PageSize:        1000,                 // 默认分页大小
        UseWAL:          true,                 // 启用 WAL
        VersionsToKeep:  10,                   // 保留版本数
        AccountNSPrefix: "v1_account_",        // 账户键前缀
    }

    // 2. 创建 StateDB 实例
    db, err := statedb.New(cfg)
    if err != nil {
        log.Fatalf("Failed to create StateDB: %v", err)
    }
    defer db.Close()

    // 3. 应用账户更新
    err = db.ApplyAccountUpdate(100, statedb.KVUpdate{
        Key:     "v1_account_0x1234",
        Value:   []byte("balance:1000"),
        Deleted: false,
    })
    if err != nil {
        log.Fatalf("Failed to apply update: %v", err)
    }

    // 4. Epoch 结束时刷新
    err = db.FlushAndRotate(39999)
    if err != nil {
        log.Fatalf("Failed to flush: %v", err)
    }
}
```

### 方式二：集成到现有系统

#### 步骤 1：在 db.Manager 中添加 StateDB

修改 `db/db.go`：

```go
type Manager struct {
    Db       *badger.DB
    StateDB  *statedb.DB  // 添加 StateDB 字段
    // ... 其他字段
}

func NewManager(path string) (*Manager, error) {
    // ... 现有代码 ...

    // 初始化 StateDB
    stateCfg := statedb.Config{
        DataDir:         path + "/state",
        EpochSize:       40000,
        ShardHexWidth:   1,
        PageSize:        1000,
        UseWAL:          true,
        VersionsToKeep:  10,
        AccountNSPrefix: "v1_account_",
    }

    stateDB, err := statedb.New(stateCfg)
    if err != nil {
        db.Close()
        return nil, fmt.Errorf("failed to create StateDB: %w", err)
    }

    manager := &Manager{
        Db:      db,
        StateDB: stateDB,
        // ... 其他字段
    }

    return manager, nil
}

func (m *Manager) Close() error {
    if m.StateDB != nil {
        m.StateDB.Close()
    }
    return m.Db.Close()
}
```

#### 步骤 2：在账户更新时同步到 StateDB

修改 `db/manage_account.go`：

```go
func (mgr *Manager) SaveAccount(account *pb.Account) error {
    key := KeyAccount(account.Address)
    data, err := ProtoMarshal(account)
    if err != nil {
        return err
    }

    // 保存到主数据库
    mgr.EnqueueSet(key, string(data))

    // 同步到 StateDB（如果已初始化）
    if mgr.StateDB != nil {
        // 获取当前区块高度（需要从 consensus 或其他地方获取）
        height := mgr.GetCurrentHeight() // 需要实现此方法

        err = mgr.StateDB.ApplyAccountUpdate(height, statedb.KVUpdate{
            Key:     key,
            Value:   data,
            Deleted: false,
        })
        if err != nil {
            log.Printf("Failed to sync to StateDB: %v", err)
            // 根据需求决定是否返回错误
        }
    }

    return nil
}
```

**注意**：`ApplyAccountUpdate` 会自动检测 Epoch 边界，当检测到跨 Epoch 时会自动调用 `FlushAndRotate`，因此**不需要**在共识模块或其他地方手动处理 Epoch 切换。

### 方式三：在节点初始化时集成

修改 `cmd/main.go` 中的 `initializeNode` 函数：

```go
func initializeNode(node *NodeInstance) error {
    // ... 现有代码 ...

    // 3. 初始化数据库（已包含 StateDB）
    dbManager, err := db.NewManager(node.DataPath)
    if err != nil {
        return fmt.Errorf("failed to init db: %v", err)
    }
    node.DBManager = dbManager

    // StateDB 已在 NewManager 中初始化，无需额外操作

    // ... 其余代码 ...
}
```

---

## 与主库交互

### 账户更新流程

在 `db/manage_account.go` 中修改账户时，同时调用：

```go
stateDB.ApplyAccountUpdate(height, KVUpdate{
    Key:     "v1_account_<addr>",
    Value:   accountData,
    Deleted: false,
})
```

### 自动 Epoch 切换

**重要**：`ApplyAccountUpdate` 内部已实现自动 Epoch 切换逻辑：

```go
// 当检测到跨 Epoch 时（通过 height 参数）
if E != s.curEpoch {
    // 自动调用 FlushAndRotate 刷新上一个 Epoch
    s.FlushAndRotate(E - 1)
    // 切换到新 Epoch
    s.curEpoch = E
}
```

此操作会自动：
1. 把内存 diff 固化为 overlay
2. 记录快照元数据（按 shard 的计数、覆盖范围等）
3. 清空当前 Epoch 的内存窗口
4. 删除已过期的 WAL 文件

**因此，使用方只需要在每次账户更新时传入正确的 height，无需手动管理 Epoch 切换。**

---

## 新节点同步流程


### 同步步骤

1. **计算目标 Epoch**
   ```
   目标高度 H
   E = floor(H/40000) * 40000
   ```

2. **并行拉取快照**

   从多个矿工按 shard + 分页拉取：
   - **SNAP(E) 的 Base**
     - 如果 E 是首个快照：拉取全量
     - 否则：拉取 Base+Overlay 链（链长一般为 1）
     - 为防链过长，可每 N 个 Epoch 做一次全量快照

3. **拉取增量 Diff**

   拉取 DIFF(E+1 .. H) 的内存 diff（可跨多矿工并行）

4. **本地合并**
   ```
   Base → 依次应用 overlay → 应用 diff
   ```
   得到 H 时刻的同步状态

### 示例代码

```go
// 同步到目标高度
func SyncToHeight(db *statedb.DB, targetHeight uint64) error {
    E := (targetHeight / 40000) * 40000

    // 1. 拉取快照
    for _, shard := range db.GetShards() {
        page := 0
        token := ""
        for {
            p, err := db.PageSnapshotShard(E, shard, page, 1000, token)
            if err != nil {
                return err
            }

            // 应用到本地
            for _, kv := range p.Items {
                // 写入本地数据库
            }

            if p.NextPageToken == "" {
                break
            }
            token = p.NextPageToken
            page++
        }
    }

    // 2. 拉取并应用 diff
    for h := E + 1; h <= targetHeight; h++ {
        // 拉取该高度的 diff 并应用
    }

    return nil
}
```

---

## WAL（预写日志）

### 启用 WAL

在配置中设置：
```go
cfg := statedb.Config{
    UseWAL: true,  // 启用 WAL
    // ... 其他配置
}
```

### WAL 工作流程

1. **启动时恢复**：自动扫描并回放最新的 WAL 文件
2. **写入时持久化**：每次更新前先写 WAL，再写内存
3. **Epoch 结束清理**：FlushAndRotate 后删除已过期的 WAL

详细说明请参考 [WAL_INTEGRATION.md](./WAL_INTEGRATION.md)

---

## API 参考

### 核心方法

#### New(cfg Config) (*DB, error)
创建 StateDB 实例，如果启用 WAL 会自动恢复未刷新的数据。

#### ApplyAccountUpdate(height uint64, kvs ...KVUpdate) error
应用账户更新到当前 Epoch。**会自动检测并处理 Epoch 切换**。

#### FlushAndRotate(endHeight uint64) error
刷新当前 Epoch 并切换到下一个。**通常由 ApplyAccountUpdate 自动调用，无需手动调用**。

#### PageSnapshotShard(E uint64, shard string, page, pageSize int, pageToken string) (Page, error)
分页查询快照数据（包含 snap + overlay 合并结果）。

#### PageCurrentDiff(shard string, pageSize int, pageToken string) (Page, error)
分页查询当前 Epoch 的内存 diff。

#### ListSnapshotShards(E uint64) ([]ShardInfo, error)
列出指定 Epoch 的所有分片信息及数量。

#### BuildFullSnapshot(ctx context.Context, E uint64, iterAccount func(...) error) error
构建全量快照（用于首个快照或周期性瘦身）。

---

## 测试

运行测试：
```bash
cd stateDB
go test -v
```

查看示例：
```bash
# 查看 WAL 使用示例
cat example_wal_usage.go
```

---

## 文件说明

| 文件 | 说明 |
|------|------|
| `db.go` | 核心数据库结构和初始化 |
| `types.go` | 类型定义（Config、KVUpdate、Page 等） |
| `update.go` | 账户更新逻辑（含自动 Epoch 切换） |
| `snapshot.go` | 快照构建和查询 |
| `query.go` | Diff 查询接口 |
| `mem_window.go` | 内存窗口实现 |
| `wal.go` | WAL 实现 |
| `shard.go` | 分片计算 |
| `keys.go` | 键格式定义 |
| `utils.go` | 工具函数 |
| `db_test.go` | 单元测试 |
| `example_wal_usage.go` | WAL 使用示例 |
| `WAL_INTEGRATION.md` | WAL 集成文档 |

---

## 性能优化建议

1. **分片数量**：根据账户数量选择合适的 ShardHexWidth
   - 账户数 < 10万：使用 1（16 个分片）
   - 账户数 > 10万：使用 2（256 个分片）

2. **WAL 配置**：
   - 高可用场景：启用 WAL
   - 性能优先场景：禁用 WAL

3. **全量快照周期**：
   - 建议每 10-20 个 Epoch 做一次全量快照
   - 避免 Overlay 链过长影响同步性能

4. **分页大小**：
   - 网络同步：建议 500-1000
   - 本地查询：建议 1000-5000

---

## 注意事项

1. **高度一致性**：确保传入的 height 参数单调递增
2. **自动 Epoch 切换**：`ApplyAccountUpdate` 会自动处理 Epoch 边界，无需手动调用 `FlushAndRotate`
3. **并发安全**：所有公开方法都是并发安全的
4. **资源清理**：程序退出前务必调用 `Close()` 方法
5. **WAL 恢复**：启用 WAL 后，重启会自动恢复未刷新的数据

---

## 常见问题

### Q: 需要手动调用 FlushAndRotate 吗？
**A**: 不需要。`ApplyAccountUpdate` 会根据传入的 height 自动检测 Epoch 边界并调用 `FlushAndRotate`。

### Q: 如何获取当前区块高度？
**A**: 需要在 `db.Manager` 中添加一个方法来获取共识模块的当前高度，或者在调用 `SaveAccount` 时传入高度参数。

### Q: WAL 文件什么时候删除？
**A**: 当 Epoch 结束并成功执行 `FlushAndRotate` 后，对应的 WAL 文件会自动删除。

### Q: 如何做全量快照？
**A**: 调用 `BuildFullSnapshot` 方法，传入一个迭代器函数来遍历所有账户数据。

---

## 许可证

本项目遵循与主项目相同的许可证。

