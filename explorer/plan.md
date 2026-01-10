# Explorer 功能扩展实现计划

## 需求概述

基于 `explorer/requirements.md` 的需求：
1. 顶部 Tab 导航：第一个 Tab 为节点运行情况总览（当前现状），第二个 Tab 为 Search
2. Search 功能：
   - 2.1 区块查询（按高度或 hash）
   - 2.1.1 区块内的 tx 列表展示
   - 2.1.2 tx 列表可点击查看详情
   - 2.2 按 txhash 查询交易
   - 2.3 交易详情页面

---

## 现有技术栈

- **前端**: Vue 3 + TypeScript + Vite
- **后端**: Go HTTP 服务 (`cmd/explorer/main.go`)
- **通信**: HTTP/3 (QUIC) + Protobuf
- **样式**: 纯 CSS（暗色主题）

---

## 前端项目结构

```
explorer/
├── src/
│   ├── main.ts              # Vue 入口
│   ├── App.vue              # 主组件（Tab 导航）
│   ├── api.ts               # API 调用封装
│   ├── types.ts             # TypeScript 类型定义
│   └── components/
│       ├── ControlPanel.vue # 控制面板
│       ├── NodeList.vue     # 节点列表
│       ├── NodeCards.vue    # 节点卡片
│       ├── NodeModal.vue    # 节点详情弹窗
│       ├── SearchPanel.vue  # (待新增) Search 面板
│       ├── BlockDetail.vue  # (待新增) 区块详情
│       └── TxDetail.vue     # (待新增) 交易详情
├── index.html
├── style.css
├── vite.config.ts
└── tsconfig.json
```

---

## 实现计划

### 阶段一：前端 Tab 导航结构 (约 0.5 小时)

#### 1.1 修改 `App.vue`
- 添加 Tab 导航状态 `activeTab: 'overview' | 'search'`
- 使用 `v-if` 切换 Tab 内容
- 将现有 Overview 内容保留在 overview Tab

```vue
<template>
  <header class="hero">...</header>

  <nav class="tab-nav">
    <button
      :class="['tab-btn', { active: activeTab === 'overview' }]"
      @click="activeTab = 'overview'"
    >节点总览</button>
    <button
      :class="['tab-btn', { active: activeTab === 'search' }]"
      @click="activeTab = 'search'"
    >Search</button>
  </nav>

  <main v-if="activeTab === 'overview'" class="layout">
    <!-- 现有节点总览内容 -->
  </main>

  <main v-else-if="activeTab === 'search'" class="layout">
    <SearchPanel />
  </main>
</template>
```

#### 1.2 修改 `style.css`
- 添加 Tab 导航样式（`.tab-nav`, `.tab-btn`, `.tab-btn.active`）

---

### 阶段二：后端 API 新增 (约 1.5 小时)

#### 2.1 新增 `/api/block` 接口 (区块查询)

**请求参数**:
- `height` (uint64, 可选): 区块高度
- `hash` (string, 可选): 区块哈希
- `node` (string): 目标节点地址

**响应结构**:
```json
{
  "block": {
    "height": 123,
    "block_hash": "0x...",
    "prev_block_hash": "0x...",
    "txs_hash": "0x...",
    "miner": "bc1q...",
    "tx_count": 10,
    "accumulated_reward": "100",
    "window": 1,
    "transactions": [
      {
        "tx_id": "0x...",
        "tx_type": "Transaction",
        "from_address": "bc1q...",
        "status": "SUCCEED",
        "summary": "Transfer 100 FB to bc1q..."
      }
    ]
  },
  "error": ""
}
```

#### 2.2 新增 `/api/tx` 接口 (交易查询)

**请求参数**:
- `tx_id` (string): 交易哈希
- `node` (string): 目标节点地址

**响应结构**:
```json
{
  "transaction": {
    "tx_id": "0x...",
    "tx_type": "Transaction",
    "from_address": "bc1q...",
    "to_address": "bc1q...",
    "status": "SUCCEED",
    "executed_height": 123,
    "fee": "0.001",
    "nonce": 5,
    "details": {
      "token_address": "FB",
      "amount": "100"
    }
  },
  "error": ""
}
```

#### 2.3 后端实现位置: `cmd/explorer/main.go`
- 新增 `handleBlock()` 函数
- 新增 `handleTx()` 函数
- 复用现有的 `fetchProto()` 方法

---

### 阶段三：前端 Search UI 实现 (约 1.5 小时)

#### 3.1 新增 `SearchPanel.vue` 组件

```vue
<script setup lang="ts">
import { ref, computed } from 'vue'
import BlockDetail from './BlockDetail.vue'
import TxDetail from './TxDetail.vue'
import type { BlockInfo, TxInfo } from '../types'
import { fetchBlock, fetchTx } from '../api'

const searchQuery = ref('')
const searchType = ref<'block' | 'tx'>('block')
const selectedNode = ref('')  // 从 props 或 provide 获取

// 搜索结果
const blockResult = ref<BlockInfo | null>(null)
const txResult = ref<TxInfo | null>(null)
const error = ref('')
const loading = ref(false)

async function handleSearch() {
  // 调用对应 API
}
</script>

<template>
  <div class="search-panel">
    <div class="search-bar">
      <input v-model="searchQuery" placeholder="Height / Block Hash / Tx Hash" />
      <select v-model="searchType">
        <option value="block">Block</option>
        <option value="tx">Transaction</option>
      </select>
      <button @click="handleSearch">Search</button>
    </div>

    <BlockDetail v-if="blockResult" :block="blockResult" @tx-click="..." />
    <TxDetail v-if="txResult" :tx="txResult" />
  </div>
</template>
```

#### 3.2 新增 `BlockDetail.vue` 组件
- Props: `block: BlockInfo`
- 显示区块元信息（高度、哈希、矿工等）
- 显示交易列表表格
- 每行交易可点击，emit `tx-click` 事件

#### 3.3 新增 `TxDetail.vue` 组件
- Props: `tx: TxInfo`
- 显示交易类型、状态
- 根据交易类型展示不同字段
- 返回按钮

#### 3.4 更新 `types.ts` 类型定义

```typescript
export interface BlockInfo {
  height: number
  block_hash: string
  prev_block_hash: string
  txs_hash: string
  miner: string
  tx_count: number
  accumulated_reward: string
  window: number
  transactions: TxSummary[]
}

export interface TxSummary {
  tx_id: string
  tx_type: string
  from_address: string
  status: string
  summary: string
}

export interface TxInfo {
  tx_id: string
  tx_type: string
  from_address: string
  to_address?: string
  status: string
  executed_height: number
  fee: string
  nonce: number
  details: Record<string, unknown>
}
```

#### 3.5 更新 `api.ts`

```typescript
export async function fetchBlock(params: {
  node: string
  height?: number
  hash?: string
}): Promise<{ block?: BlockInfo; error?: string }> {
  const resp = await fetch('/api/block', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(params),
  })
  return resp.json()
}

export async function fetchTx(params: {
  node: string
  tx_id: string
}): Promise<{ transaction?: TxInfo; error?: string }> {
  const resp = await fetch('/api/tx', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(params),
  })
  return resp.json()
}
```

---

### 阶段四：节点 Handler 扩展 (约 1 小时)

需要在节点侧添加新的 API 端点，或复用现有端点：

#### 4.1 检查现有端点
- `/getblock` - 已存在，按高度获取区块
- 可能需要新增按 hash 查询的功能

#### 4.2 新增 `/gettx` 端点 (如果不存在)
- 按 tx_id 查询交易详情
- 需要在 `handlers/` 目录中添加

---

## 文件修改清单

| 文件 | 修改类型 | 说明 |
|------|----------|------|
| `explorer/src/App.vue` | 修改 | 添加 Tab 导航和切换逻辑 |
| `explorer/src/types.ts` | 修改 | 添加 BlockInfo, TxSummary, TxInfo 类型 |
| `explorer/src/api.ts` | 修改 | 添加 fetchBlock, fetchTx 函数 |
| `explorer/src/components/SearchPanel.vue` | 新增 | Search 面板组件 |
| `explorer/src/components/BlockDetail.vue` | 新增 | 区块详情组件 |
| `explorer/src/components/TxDetail.vue` | 新增 | 交易详情组件 |
| `explorer/style.css` | 修改 | 添加 Tab 和 Search 相关样式 |
| `cmd/explorer/main.go` | 修改 | 新增 `/api/block` 和 `/api/tx` 接口 |
| `handlers/handleBlock.go` | 可能修改 | 按 hash 查询区块 |
| `handlers/handleTx.go` | 新增 | 交易查询处理器 |

---

## 实现顺序建议

1. **先后端**：确保 API 可用
   - 检查现有节点 API 能力
   - 在 explorer 后端添加代理接口

2. **再前端**：
   - Tab 导航（修改 App.vue）
   - SearchPanel 组件
   - BlockDetail 和 TxDetail 组件
   - 样式调整

3. **测试**：
   - 本地多节点测试
   - 边界情况（无数据、错误输入）

---

## 预估工时

| 阶段 | 预估时间 |
|------|----------|
| 阶段一：Tab 导航 | 0.5 小时 |
| 阶段二：后端 API | 1.5 小时 |
| 阶段三：前端 Search UI | 1.5 小时 |
| 阶段四：节点 Handler | 1 小时 |
| 测试和调试 | 0.5 小时 |
| **总计** | **5 小时** |


