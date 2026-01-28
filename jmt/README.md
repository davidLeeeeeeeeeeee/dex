# JMT - Jellyfish Merkle Tree

16 叉版本化 Jellyfish Merkle Tree 实现，用于 DEX 公链的状态存储。

## 特性

- **16 叉树结构**: 树深度从 256 层降至 64 层，大幅减少磁盘 I/O
- **版本化 (MVCC)**: 支持历史状态查询，每个版本对应一个区块高度
- **高效 Proof**: 紧凑的 Merkle 证明，万级数据只需 3-4 层 siblings
- **BadgerDB 集成**: 提供 `VersionedBadgerStore` 适配器

## 基准测试结果

测试环境: AMD Ryzen 9 9950X3D, Windows

| 操作 | 耗时 | 内存分配 | 分配次数 |
|------|------|----------|----------|
| **Insert (单次)** | 9.0 μs | 11.9 KB | 114 |
| **Batch Insert (100)** | 822 μs | 1.17 MB | 11,703 |
| **Get** | 1.7 μs | 3.1 KB | 63 |
| **Prove** | 2.7 μs | 5.8 KB | 88 |
| **VerifyProof** | 5.2 μs | 14.1 KB | 57 |
| **Delete** | 5.0 μs | 4.8 KB | 61 |

### JMT + BadgerDB 实际磁盘性能对比

在真实的 BadgerDB 环境（开启 I/O）下，对比优化前（无 Session）与优化后（使用 `DBSession` 合并事务）的性能差异：

| 场景 | 耗时 (100 次更新/Batch) | 内存分配 | 优势 |
| :--- | :--- | :--- | :--- |
| **未优化 (Legacy)** | **25.3 ms** | 27.4 MB | 每次操作独立写盘 |
| **已优化 (DBSession)** | **10.8 ms** | 7.9 MB | **性能提升 ~2.3倍**，内存下降 ~70% |

#### 详细基准测试指标 (Real-World)

以下是使用真实 BadgerDB 后端运行的典型指标：

| 测试项目 | 耗时 (ns/op) | 吞吐量 (ops/sec) | 内存分配 | 分配次数 |
| :--- | :--- | :--- | :--- | :--- |
| **JMT (With Session)** | 10,881,369 | ~9,189 | 7.98 MB | 100,594 |
| **JMT (No Session)** | 25,381,244 | ~3,940 | 27.46 MB | 247,771 |
| **StateDB.ApplyUpdate** | 10,430,936 | ~9,586 | 7.71 MB | 101,524 |

> *注：上述测试基于每 Batch 包含 100 次状态变更（如账户余额修改）的场景。*

> **结论**: 通过 `DBSession` 共享同一个 Badger 事务，并在此期间缓存 JMT 节点中间状态，极大地降低了磁盘 I/O 开销与内存对象开销。

### Proof 大小分析

| Key 数量 | 平均 Proof 大小 | 平均 Siblings 深度 |
|----------|-----------------|-------------------|
| 100 | ~897 bytes | 2 |
| 1,000 | ~1.2 KB | 3 |
| 10,000 | ~1.5 KB | 3-4 |

## 使用示例

```go
package main

import (
    "crypto/sha256"
    smt "dex/jmt"
    "github.com/dgraph-io/badger/v4"
)

func main() {
    // 使用 BadgerDB 作为后端
    db, _ := badger.Open(badger.DefaultOptions("/path/to/db"))
    defer db.Close()

    store := smt.NewVersionedBadgerStore(db, []byte("state:"))
    tree := smt.NewJMT(store, sha256.New())

    // 批量更新 (版本 = 区块高度)
    root, _ := tree.Update(
        [][]byte{[]byte("balance:alice"), []byte("balance:bob")},
        [][]byte{[]byte("1000"), []byte("500")},
        1, // blockHeight
    )

    // 查询 (version=0 表示最新)
    value, _ := tree.Get([]byte("balance:alice"), 0)

    // 历史版本查询
    oldValue, _ := tree.Get([]byte("balance:alice"), 1)

    // 生成 Merkle Proof
    proof, _ := tree.Prove([]byte("balance:alice"))

    // 验证 Proof
    ok := smt.VerifyJMTProof(proof, root, sha256.New())
}
```

### 内存存储 (用于测试)

```go
store := smt.NewSimpleVersionedMap()
tree := smt.NewJMT(store, sha256.New())
```

## 核心 API

### JellyfishMerkleTree

```go
// 创建
NewJMT(store VersionedStore, hasher hash.Hash) *JellyfishMerkleTree
ImportJMT(store VersionedStore, hasher hash.Hash, version Version, root []byte) *JellyfishMerkleTree

// 操作
Update(keys, values [][]byte, newVersion Version) (root []byte, err error)
Get(key []byte, version Version) (value []byte, err error)
Delete(key []byte, newVersion Version) (root []byte, err error)

// Proof
Prove(key []byte) (*JMTProof, error)
VerifyJMTProof(proof *JMTProof, root []byte, hasher hash.Hash) bool

// 访问器
Root() []byte
Version() Version
```

### VersionedStore 接口

```go
type VersionedStore interface {
    Get(key []byte, version Version) ([]byte, error)
    Set(key, value []byte, version Version) error
    Delete(key []byte, version Version) error
    GetLatestVersion(key []byte) (Version, error)
    Prune(version Version) error
    Close() error
}
```

## 文件结构

```
jmt/
├── jmt.go                      # 核心树结构和操作
├── jmt_hasher.go               # 16 叉哈希计算
├── jmt_proof.go                # Proof 生成和验证
├── node.go                     # 节点类型定义
├── utils.go                    # Nibble 操作工具
├── versioned_store.go          # 版本化存储接口 + 内存实现
├── versioned_badger_store.go   # BadgerDB 适配器
├── *_test.go                   # 测试文件
├── README.md                   # 本文档
└── QA.md                       # 设计 Q&A
```

## 运行测试

```bash
# 运行所有测试
go test -v ./jmt/...

# 运行基准测试
go test -bench=BenchmarkJMT -benchmem ./jmt/...
```
