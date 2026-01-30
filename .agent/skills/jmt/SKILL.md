---
name: JMT (Jellyfish Merkle Tree)
description: 16叉版本化 Merkle 树实现，用于状态根计算和历史版本查询，支持并行批量更新。
triggers:
  - JMT
  - Merkle
  - 状态根
  - StateRoot
  - 版本化存储
  - 并行更新
---

# JMT (Jellyfish Merkle Tree) 模块指南

16 叉版本化稀疏 Merkle 树，用于高效计算和验证全局状态根。

## 目录结构

```
jmt/
├── jmt.go                    # ⭐ 核心实现：Get/Update/Delete
├── node.go                   # 节点类型：LeafNode / InternalNode
├── jmt_hasher.go             # 哈希计算逻辑
├── jmt_parallel.go           # ⭐ 并行批量更新（高性能）
├── jmt_statedb.go            # StateDB 适配器
│
├── versioned_store.go        # 版本化存储接口
├── versioned_badger_store.go # BadgerDB 持久化实现
│
├── jmt_proof.go              # Merkle 证明生成
├── utils.go                  # 工具函数
│
├── README.md                 # 设计说明
└── QA.md                     # 常见问题
```

## 核心概念

| 概念 | 说明 |
|:---|:---|
| **16 叉树** | 每个内部节点最多 16 个子节点，路径按 nibble（4 bit）分割 |
| **Version** | 每次更新递增版本号，支持历史版本查询 |
| **Session** | 批量读写会话，减少 BadgerDB 事务开销 |
| **并行更新** | `UpdateParallel` 分片并发计算子树哈希 |

## 关键接口

```go
// 创建/导入
jmt := smt.NewJMT(store, sha256.New())
jmt := smt.ImportJMT(store, hasher, version, root)

// 读取（指定版本，0=当前）
value, _ := jmt.Get(key, version)

// 更新（返回新根哈希）
newRoot, _ := jmt.Update(keys, values, newVersion)

// 并行批量更新（高性能）
newRoot, _ := jmt.UpdateParallel(keys, values, newVersion, workers)

// 删除
newRoot, _ := jmt.Delete(key, newVersion)
```

## 与 StateDB 的关系

```
VM 执行区块 → WriteOps
      ↓
StateDB.ApplyUpdates(height, ops)
      ↓
JMT.UpdateParallel(keys, values, height)
      ↓
返回 StateRoot → 写入 Block.Header.StateRoot
```

适配器在 `jmt_statedb.go`：
```go
type JMTStateDB struct {
    jmt   *JellyfishMerkleTree
    store *VersionedBadgerStore
}
```

## 性能优化

1. **并行更新**: `UpdateParallel` 使用 worker 池并发处理子树
2. **LRU 缓存**: 内部节点缓存减少 BadgerDB 读取
3. **Session 复用**: 使用 `UpdateWithSession` 避免重复创建事务

## 开发规范

1. **版本递增**: 每个区块高度对应一个 JMT 版本
2. **原子提交**: 同一区块的所有更新必须在同一 Session 内完成
3. **日志前缀**: `[JMT]`, `[JMTStore]`

## 测试

```bash
go test ./jmt/... -v
go test ./jmt/ -run TestParallel -v
go test ./jmt/ -bench BenchmarkUpdate
```
