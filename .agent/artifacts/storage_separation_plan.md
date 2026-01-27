# 存储分离方案：KV vs StateDB

## 核心原则

| 存储层 | 职责 | 数据特性 |
|--------|------|----------|
| **KV (BadgerDB)** | 不可变流水 + 索引 | 线性增长、只追加、无需版本 |
| **StateDB** | 可变证明状态 | 需要版本管理、参与 Merkle 树 |

---

## 一、数据分类明细

### 1. KV 存储（不可变流水/索引）

| Key 前缀 | 数据类型 | 说明 |
|----------|----------|------|
| `v1_blockdata_<hash>` | Block | 区块完整数据（不可变） |
| `v1_height_<h>_blocks` | []BlockID | 高度到区块ID映射 |
| `v1_blockid_<hash>` | Height | 区块ID到高度映射 |
| `v1_latest_block_height` | uint64 | 最新区块高度 |
| `v1_txraw_<txid>` | AnyTx | 交易原文（不可变） |
| `v1_anyTx_<txid>` | Key | 交易索引（指向具体类型） |
| `v1_pending_anytx_<txid>` | AnyTx | Pending 交易 |
| `v1_tx_<txid>` | Transaction | 普通交易 |
| `v1_order_<txid>` | OrderTx | 订单交易原文 |
| `v1_minerTx_<txid>` | MinerTx | 矿工交易 |
| `v1_pair:<p>\|is_filled:<b>\|price:<k>\|order_id:<id>` | Index | 订单价格索引 |
| `v1_trade_<pair>_<ts>_<id>` | Trade | 成交记录 |
| `v1_stakeIndex_<v>_address:<a>` | Index | 质押排序索引 |
| `v1_indexToAccount_<idx>` | Address | 矿工索引映射 |
| `v1_vm_applied_tx_<txid>` | Status | 交易执行状态 |
| `v1_vm_tx_error_<txid>` | Error | 交易错误信息 |
| `v1_vm_tx_height_<txid>` | Height | 交易所在高度 |
| `v1_vm_commit_h_<h>` | Mark | 区块提交标记 |
| `v1_transfer_history_<txid>` | History | 转账历史 |
| `v1_miner_history_<txid>` | History | 矿工历史 |
| `v1_recharge_history_<txid>` | History | 充值历史 |
| `v1_freeze_history_<txid>` | History | 冻结历史 |
| `v1_whist_<txid>` | History | 见证历史 |
| `v1_frost_*` | FROST | 所有 FROST 相关数据 |

### 2. StateDB（可变证明状态）

| Key 前缀 | 数据类型 | 说明 |
|----------|----------|------|
| `v1_account_<addr>` | Account | 账户状态（余额、Nonce、矿工标记） |
| `v1_orderstate_<txid>` | OrderState | 订单实时状态（成交量、是否完成） |
| `v1_token_<addr>` | Token | Token 信息 |
| `v1_token_registry` | Registry | Token 注册表 |
| `v1_freeze_<addr>_<token>` | FreezeMark | 冻结标记 |
| `v1_recharge_record_<addr>_<tweak>` | Record | 充值记录状态 |
| `v1_recharge_request_<id>` | Request | 入账请求状态 |
| `v1_winfo_<addr>` | WitnessInfo | 见证者信息 |
| `v1_wvote_<rid>_<addr>` | Vote | 见证投票 |
| `v1_wstake_<v>_<addr>` | Index | 见证者质押索引 |
| `v1_witness_config` | Config | 见证者配置 |
| `v1_witness_reward_pool` | Pool | 见证者奖励池 |
| `v1_challenge_<id>` | Challenge | 挑战记录 |
| `v1_shelved_request_<id>` | Request | 搁置请求 |

---

## 二、已完成的代码变更

### ✅ Phase 1: 统一 Key 分类

#### Step 1: 创建 `keys/category.go`
- 新增 `KeyCategory` 枚举类型
- 新增 `CategorizeKey(key string) KeyCategory` 函数
- 新增 `IsStatefulKey(key string) bool` 便捷函数
- 新增 `IsFlowKey(key string) bool` 便捷函数
- 新增多个分类判断函数（`IsAccountKey`, `IsBlockKey`, `IsTxKey` 等）

#### Step 2: 修改 `statedb/update.go`
- 移除冗余的 `isStatefulKey` 方法
- 改用 `keys.IsStatefulKey` 统一判断

#### Step 3: 修改 `db/db.go`
- 在 `SyncToStateDB` 方法中添加 `keys.IsStatefulKey` 过滤
- 确保只有状态数据才会同步到 StateDB

### ✅ Phase 2: 添加读取代理 + 移除双写

#### Step 4: 为 StateDB 添加 `Get` 方法 (`statedb/query.go`)
- 新增 `Get(key string) ([]byte, bool, error)` 方法
- 查找顺序：内存窗口 → Epoch overlay → 快照链
- 新增 `Exists(key string) (bool, error)` 便捷方法

#### Step 5: 为 memWindow 添加 `get` 方法 (`statedb/mem_window.go`)
- 支持从内存窗口中获取单个 key 的条目
- 返回副本避免并发问题

#### Step 6: 添加 `bytesToU64` 工具函数 (`statedb/utils.go`)
- 用于将字节切片转换为 uint64

#### Step 7: 修改 `db/manage_account.go`
- `SaveAccount`: 移除 StateDB 双写逻辑（现在由 VM 统一处理）
- `GetAccount`: 添加优先从 StateDB 读取的逻辑，回退到 KV

---

## 三、数据流向

```
┌─────────────────────────────────────────────────────────────┐
│                      VM Executor                            │
│                  (applyResult 方法)                         │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
              ┌───────────────┴───────────────┐
              │        WriteOp 分类           │
              │   (keys.IsStatefulKey)        │
              └───────────────┬───────────────┘
                              │
         ┌────────────────────┼────────────────────┐
         ▼                    ▼                    ▼
┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐
│     KV 流水     │  │      索引       │  │    StateDB      │
│  (BadgerDB)     │  │   (BadgerDB)    │  │  (可变状态)     │
├─────────────────┤  ├─────────────────┤  ├─────────────────┤
│ 区块数据        │  │ 价格索引        │  │ 账户余额        │
│ 交易原文        │  │ 质押索引        │  │ 订单状态        │
│ 历史记录        │  │ 高度映射        │  │ 见证者状态      │
│ FROST 数据      │  │                 │  │ Token 状态      │
└─────────────────┘  └─────────────────┘  └─────────────────┘
```

### 读取流程（新）

```
任意状态数据读取 (Account, OrderState, Token, Witness...)
      │
      ▼
┌─────────────────┐     ┌─────────────────┐
│keys.IsStatefulKey│────▶│ 是状态数据?     │
└─────────────────┘     └────────┬────────┘
                                 │ Yes
                                 ▼
                        ┌─────────────────┐     ┌─────────────────┐
                        │   StateDB.Get   │────▶│   Found?        │
                        │  (优先读取)     │     │   返回数据      │
                        └─────────────────┘     └────────┬────────┘
                                                         │ No
                                                         ▼
                                                ┌─────────────────┐
                                                │   KV.Read       │
                                                │  (回退读取)     │
                                                └─────────────────┘
```

---

## 四、已完成的变更（Phase 3）

### ✅ Step 8: 通用读取代理 (`db/db.go`)
- 修改 `Read(key)` 方法，对于状态数据优先从 StateDB 读取
- 修改 `Get(key)` 方法，对于状态数据优先从 StateDB 读取
- 使用 `keys.IsStatefulKey` 自动判断是否为状态数据
- **影响范围**：所有通过 `db.Manager` 的读取操作都会自动优先查询 StateDB

### ✅ Step 9: Token 读取代理 (`db/manage_account.go`)
- `GetToken`: 添加 StateDB 读取代理
- `GetTokenRegistry`: 添加 StateDB 读取代理

---

## 五、下一步计划（可选）

### Phase 4: 完全移除 KV 双写
- [ ] 移除 `SaveAccount` 中的 KV 写入（仅写 StateDB）
- [ ] 编写数据迁移脚本，清理 KV 中的冗余状态数据

### Phase 5: 性能优化（暂不需要）
- 关于缓存层的说明：
  1. StateDB 本身有内存窗口 (`memWindow`)，当前 Epoch 数据已在内存中
  2. BadgerDB 有自己的 LRU 缓存，频繁访问的数据会被自动缓存
  3. 建议等系统运行一段时间后，如果发现读取成为瓶颈，再考虑添加缓存层

