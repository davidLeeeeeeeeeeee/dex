# WAL (Write-Ahead Log) 集成说明

## 概述

WAL (Write-Ahead Log) 用于在进程崩溃时恢复内存中的 diff 数据，确保数据不丢失。

## 工作流程

### 1. 启动时恢复 (在 `New()` 中)

```
进程启动
  ↓
检查 UseWAL 配置
  ↓
调用 recoverFromWAL()
  ↓
扫描 WAL 目录，找到最新的 WAL 文件
  ↓
调用 ReplayWAL() 逐条回放
  ↓
每条记录通过 mem.apply() 重建内存窗口
  ↓
恢复完成，继续正常运行
```

**关键代码位置**: `stateDB/db.go` 的 `New()` 和 `recoverFromWAL()`

### 2. 写入时持久化 (在 `ApplyAccountUpdate()` 中)

```
收到账户更新请求
  ↓
检查是否跨 Epoch（如果是，先 FlushAndRotate）
  ↓
如果启用 WAL:
  ├─ 确保 WAL 文件已打开
  ├─ 将 KVUpdate 转换为 WalRecord
  │   ├─ Epoch: 当前 Epoch
  │   ├─ Op: 0=SET, 1=DEL
  │   ├─ Key: []byte(key)
  │   └─ Value: val
  ├─ 调用 wal.AppendBatch() 写入 WAL
  └─ fsync 确保持久化
  ↓
写入内存窗口 (mem.apply)
  ↓
更新 h<->seq 映射
  ↓
完成
```

**关键代码位置**: `stateDB/update.go` 的 `ApplyAccountUpdate()`

**重要**: WAL 写入在内存写入之前，确保崩溃时可以恢复。

### 3. Epoch 轮转时清理 (在 `FlushAndRotate()` 中)

```
Epoch 结束
  ↓
将内存 diff 固化为 overlay (写入 BadgerDB)
  ↓
清空内存窗口
  ↓
如果启用 WAL:
  ├─ 关闭当前 WAL 文件
  ├─ 删除已持久化的 WAL 文件
  └─ 将 s.wal 设为 nil
  ↓
下次写入时会创建新 Epoch 的 WAL
```

**关键代码位置**: `stateDB/update.go` 的 `FlushAndRotate()`

**原因**: 一旦 diff 已经持久化到 BadgerDB，WAL 就不再需要了。

## WAL 文件格式

### 文件命名
```
stateDB/data/wal/wal_E{epoch}.log
例如: wal_E00000000000000000000.log
```

### 记录格式
每条记录包含:
```
[Header: 21 bytes]
  - magic: 4 bytes (0x51A1F00D)
  - epoch: 8 bytes (BigEndian uint64)
  - op: 1 byte (0=SET, 1=DEL)
  - klen: 4 bytes (key 长度)
  - vlen: 4 bytes (value 长度)
[Key: klen bytes]
[Value: vlen bytes]
[CRC32: 4 bytes] (校验和)
```

## 配置

在 `Config` 中设置:
```go
cfg := statedb.Config{
    DataDir:    "stateDB/data",
    UseWAL:     true,  // 启用 WAL
    EpochSize:  40000,
    // ... 其他配置
}
```

## 崩溃恢复示例

### 场景
1. 进程在 Epoch 40000 运行
2. 写入了 1000 条账户更新到 WAL
3. 进程崩溃（内存数据丢失）
4. 重启进程

### 恢复过程
1. `New()` 被调用
2. `recoverFromWAL()` 扫描 WAL 目录
3. 找到 `wal_E00000000000000040000.log`
4. `ReplayWAL()` 读取 1000 条记录
5. 每条记录通过 `mem.apply()` 重建内存窗口
6. 恢复完成，curEpoch = 40000
7. 继续正常运行

## 性能考虑

### WAL 写入性能
- 每次 `AppendBatch()` 都会 `fsync`，确保持久化
- 如果需要更高性能，可以:
  - 使用组提交 (group commit)
  - 定时 fsync 而不是每次都 fsync
  - 使用 SSD 存储

### WAL 恢复性能
- 启动时需要回放整个 WAL
- 如果 Epoch 很大，恢复时间可能较长
- 建议定期 FlushAndRotate，避免 WAL 过大

## 注意事项

### 1. 高度信息丢失
当前实现中，WAL 记录不包含具体的 height，恢复时使用 Epoch 作为占位。
如果需要精确的 height 信息，需要:
- 在 `WalRecord` 中添加 `Height` 字段
- 在 `ApplyAccountUpdate()` 中记录 height
- 在 `recoverFromWAL()` 中使用正确的 height

### 2. 并发安全
- WAL 写入在 `s.mu` 锁保护下进行
- 确保 WAL 写入和内存写入的原子性

### 3. 错误处理
- WAL 写入失败会导致整个更新失败
- WAL 恢复失败会导致进程启动失败
- 生产环境中应该添加详细的日志记录

## 测试建议

### 基本功能测试
```go
// 1. 启用 WAL，写入数据
cfg := Config{UseWAL: true, ...}
db, _ := New(cfg)
db.ApplyAccountUpdate(100, KVUpdate{...})
db.Close()

// 2. 重新打开，验证数据恢复
db2, _ := New(cfg)
// 验证内存窗口中的数据
```

### 崩溃恢复测试
```go
// 1. 写入数据但不调用 FlushAndRotate
// 2. 强制关闭进程（模拟崩溃）
// 3. 重启，验证数据恢复
```

### 性能测试
```go
// 测试 WAL 写入对性能的影响
// 对比 UseWAL=true 和 UseWAL=false 的性能差异
```

## 相关文件

- `stateDB/wal.go` - WAL 核心实现
- `stateDB/db.go` - WAL 初始化和恢复
- `stateDB/update.go` - WAL 写入和清理
- `stateDB/types.go` - Config 配置

## 总结

WAL 集成遵循经典的 WAL 模式:
1. **Write-Ahead**: 先写 WAL，再写内存
2. **Recovery**: 启动时从 WAL 恢复
3. **Checkpoint**: 持久化后删除 WAL

这确保了即使进程崩溃，也不会丢失未持久化的数据。

