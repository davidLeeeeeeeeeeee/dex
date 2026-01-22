---
name: Explorer Frontend
description: Vue 3 + TypeScript frontend for real-time DEX monitoring, including node status, trading panel, and FROST visualization.
triggers:
  - 前端
  - UI
  - 界面
  - Vue
  - 组件
  - 样式
  - explorer
---

# Explorer (前端) 模块指南

Vue 3 + TypeScript 单页应用，实时监控 DEX 节点状态。

## 目录结构

```
explorer/
├── index.html           # 入口 HTML
├── style.css            # 全局样式
├── vite.config.ts       # Vite 配置
├── package.json
│
└── src/
    ├── App.vue          # ⭐ 主应用，Tab 导航
    ├── api.ts           # API 调用封装
    ├── types.ts         # TypeScript 类型
    │
    └── components/
        ├── TradingPanel.vue    # 订单簿 + 成交记录
        ├── SearchPanel.vue     # 搜索面板
        │
        └── frost/
            ├── WithdrawQueue.vue   # 提现队列
            ├── WitnessFlow.vue     # 见证流程
            ├── DkgTimeline.vue     # DKG 时间线
            └── ProtocolModal.vue   # 协议详情弹窗

cmd/explorer/
├── main.go              # Explorer 后端 API 代理
└── indexdb/             # 本地索引数据库
```

## 技术栈

- **Framework**: Vue 3 (Composition API)
- **Language**: TypeScript
- **Build**: Vite
- **Style**: Vanilla CSS (全局 `style.css`)

## 核心组件

| 组件 | 功能 |
|:---|:---|
| `App.vue` | 主框架，Tab 切换，节点选择 |
| `TradingPanel.vue` | 订单簿、成交记录显示 |
| `SearchPanel.vue` | 区块/交易/地址搜索 |
| `WithdrawQueue.vue` | FROST 提现队列 |

## API 调用 (api.ts)

```typescript
// 获取订单簿
fetchOrderBook(node: string, pair: string): Promise<OrderBookData>

// 获取成交记录
fetchRecentTrades(node: string, pair: string, limit?: number): Promise<TradeRecord[]>

// 获取节点状态
fetchSummary(request: SummaryRequest): Promise<SummaryResponse>
```

## 开发规范

1. **Composition API**: 使用 `<script setup>` 语法
2. **响应式**: 使用 `ref()`, `reactive()`, `computed()`
3. **样式**: 优先使用全局 `style.css`，组件内用 `<style scoped>`
4. **类型安全**: 所有 props 和返回值需定义类型

## 运行命令

```bash
cd explorer
npm install        # 安装依赖
npm run dev        # 开发模式
npm run build      # 生产构建
```

## 添加新组件

```vue
<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { fetchXxx } from '../api'

const data = ref<SomeType[]>([])
const loading = ref(false)

onMounted(async () => {
  loading.value = true
  data.value = await fetchXxx()
  loading.value = false
})
</script>

<template>
  <div class="glass-panel">
    <h3>Title</h3>
    <div v-if="loading">Loading...</div>
    <div v-for="item in data" :key="item.id">
      {{ item.name }}
    </div>
  </div>
</template>

<style scoped>
/* 组件专属样式 */
</style>
```
