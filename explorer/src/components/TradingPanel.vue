<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { fetchOrderBook, fetchRecentTrades } from '../api'
import type { OrderBookData, TradeRecord } from '../types'

const props = defineProps<{
  nodes: string[]
  defaultNode: string
}>()

// 格式化时间为北京时区 (UTC+8)
function formatTimeBeijing(timeStr: string): string {
  if (!timeStr) return '-'
  try {
    const date = new Date(timeStr)
    if (isNaN(date.getTime())) return timeStr
    // 使用 Asia/Shanghai 时区格式化
    return date.toLocaleString('zh-CN', {
      timeZone: 'Asia/Shanghai',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
      hour12: false
    })
  } catch {
    return timeStr
  }
}

const selectedNode = ref(props.defaultNode || '')
const selectedPair = ref('FB_USDT')
const loading = ref(false)

// 分开管理两个 API 的错误
const orderBookError = ref('')
const tradesError = ref('')

// 订单簿数据
const orderBook = ref<OrderBookData>({
  bids: [],
  asks: [],
  pair: '',
  lastUpdate: ''
})

// 最近成交
const recentTrades = ref<TradeRecord[]>([])

// 可选交易对
const tradingPairs = ref(['FB_USDT', 'BTC_USDT', 'ETH_USDT'])

watch(() => props.defaultNode, (val) => {
  if (val && !selectedNode.value) selectedNode.value = val
})

const loadData = async () => {
  if (!selectedNode.value || !selectedPair.value) return

  loading.value = true
  orderBookError.value = ''
  tradesError.value = ''

  // 并行请求，分别处理错误
  const [obResult, tradesResult] = await Promise.allSettled([
    fetchOrderBook(selectedNode.value, selectedPair.value),
    fetchRecentTrades(selectedNode.value, selectedPair.value)
  ])

  // 处理订单簿结果
  if (obResult.status === 'fulfilled') {
    orderBook.value = obResult.value
  } else {
    orderBookError.value = obResult.reason?.message || 'Failed to load order book'
    orderBook.value = { bids: [], asks: [], pair: selectedPair.value, lastUpdate: '' }
  }

  // 处理成交记录结果
  if (tradesResult.status === 'fulfilled') {
    recentTrades.value = tradesResult.value
  } else {
    tradesError.value = tradesResult.reason?.message || 'Failed to load trades'
    recentTrades.value = []
  }

  loading.value = false
}

onMounted(() => {
  if (props.defaultNode) selectedNode.value = props.defaultNode
  loadData()
})

watch([selectedNode, selectedPair], () => {
  loadData()
})

// 自动刷新
let refreshTimer: number | null = null
onMounted(() => {
  refreshTimer = window.setInterval(loadData, 5000)
})

import { onUnmounted } from 'vue'
onUnmounted(() => {
  if (refreshTimer) clearInterval(refreshTimer)
})
</script>

<template>
  <div class="trading-panel">
    <!-- 头部选择器 -->
    <div class="trading-header glass-panel">
      <div class="selector-group">
        <label>Node:</label>
        <select v-model="selectedNode" class="select-input">
          <option v-for="node in nodes" :key="node" :value="node">{{ node }}</option>
        </select>
      </div>
      <div class="selector-group">
        <label>Trading Pair:</label>
        <select v-model="selectedPair" class="select-input">
          <option v-for="pair in tradingPairs" :key="pair" :value="pair">{{ pair.replace('_', '/') }}</option>
        </select>
      </div>
      <button @click="loadData" class="btn-refresh" :disabled="loading">
        {{ loading ? 'Loading...' : '🔄 Refresh' }}
      </button>
    </div>

    <div class="trading-content">
      <!-- 订单簿 -->
      <div class="order-book glass-panel">
        <h3>📊 Order Book</h3>

        <!-- 订单簿错误提示 -->
        <div v-if="orderBookError" class="error-message">
          <span class="error-icon">⚠️</span>
          {{ orderBookError }}
        </div>

        <div v-else class="order-book-content">
          <!-- 卖单 (Asks) -->
          <div class="asks-section">
            <div class="order-header">
              <span>Price</span>
              <span>Amount</span>
              <span>Total</span>
              <span>Status</span>
            </div>
            <div v-for="ask in (orderBook.asks || []).slice(0, 10)" :key="ask.price" class="order-row ask">
              <span class="price sell">{{ ask.price }}</span>
              <span>{{ ask.amount }}</span>
              <span>{{ ask.total }}</span>
              <span class="status-cell">
                <span v-if="ask.confirmedCount > 0" class="status-badge confirmed" :title="`${ask.confirmedCount} confirmed`">✓{{ ask.confirmedCount }}</span>
                <span v-if="ask.pendingCount > 0" class="status-badge pending" :title="`${ask.pendingCount} pending`">⏳{{ ask.pendingCount }}</span>
              </span>
            </div>
            <div v-if="!orderBook.asks || orderBook.asks.length === 0" class="empty-message">No sell orders</div>
          </div>

          <!-- 当前价格 -->
          <div class="spread-indicator">
            <span class="current-price">{{ orderBook.asks[0]?.price || '--' }}</span>
          </div>

          <!-- 买单 (Bids) -->
          <div class="bids-section">
            <div class="order-header">
              <span>Price</span>
              <span>Amount</span>
              <span>Total</span>
              <span>Status</span>
            </div>
            <div v-for="bid in (orderBook.bids || []).slice(0, 10)" :key="bid.price" class="order-row bid">
              <span class="price buy">{{ bid.price }}</span>
              <span>{{ bid.amount }}</span>
              <span>{{ bid.total }}</span>
              <span class="status-cell">
                <span v-if="bid.confirmedCount > 0" class="status-badge confirmed" :title="`${bid.confirmedCount} confirmed`">✓{{ bid.confirmedCount }}</span>
                <span v-if="bid.pendingCount > 0" class="status-badge pending" :title="`${bid.pendingCount} pending`">⏳{{ bid.pendingCount }}</span>
              </span>
            </div>
            <div v-if="!orderBook.bids || orderBook.bids.length === 0" class="empty-message">No buy orders</div>
          </div>
        </div>
      </div>

      <!-- 最近成交 -->
      <div class="recent-trades glass-panel">
        <h3>📈 Recent Trades</h3>

        <!-- 成交记录错误提示 -->
        <div v-if="tradesError" class="error-message">
          <span class="error-icon">⚠️</span>
          {{ tradesError }}
        </div>

        <template v-else>
          <div class="trades-header">
            <span>Time</span>
            <span>Price</span>
            <span>Amount</span>
            <span>Side</span>
          </div>
          <div class="trades-list">
            <div v-for="trade in (recentTrades || []).slice(0, 20)" :key="trade.id" class="trade-row">
              <span class="time" :title="trade.time">{{ formatTimeBeijing(trade.time) }}</span>
              <span :class="['price', trade.side]">{{ trade.price }}</span>
              <span>{{ trade.amount }}</span>
              <span :class="['side-badge', trade.side]">{{ trade.side?.toUpperCase() }}</span>
            </div>
            <div v-if="!recentTrades || recentTrades.length === 0" class="empty-message">No recent trades</div>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.trading-panel {
  padding: 20px;
  width: 100%;
}

.trading-header {
  display: flex;
  gap: 20px;
  align-items: center;
  padding: 16px 20px;
  margin-bottom: 20px;
  flex-wrap: wrap;
}

.selector-group {
  display: flex;
  align-items: center;
  gap: 8px;
}

.selector-group label {
  color: #9ca3af;
  font-size: 0.875rem;
}

.select-input {
  background: rgba(255,255,255,0.1);
  border: 1px solid rgba(255,255,255,0.2);
  border-radius: 8px;
  padding: 8px 12px;
  color: white;
  min-width: 150px;
}

.btn-refresh {
  background: linear-gradient(135deg, #6366f1 0%, #8b5cf6 100%);
  color: white;
  border: none;
  padding: 8px 16px;
  border-radius: 8px;
  cursor: pointer;
  font-weight: 500;
  transition: opacity 0.2s;
}

.btn-refresh:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.error-message {
  background: rgba(239, 68, 68, 0.15);
  border: 1px solid rgba(239, 68, 68, 0.4);
  color: #fca5a5;
  padding: 16px;
  border-radius: 8px;
  margin: 8px 0;
  display: flex;
  align-items: flex-start;
  gap: 8px;
  font-size: 0.875rem;
  line-height: 1.4;
}

.error-icon {
  flex-shrink: 0;
}

.trading-content {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 20px;
}

.glass-panel {
  background: rgba(255,255,255,0.05);
  border: 1px solid rgba(255,255,255,0.1);
  border-radius: 12px;
  padding: 16px;
}

.order-book h3, .recent-trades h3 {
  margin: 0 0 16px 0;
  color: white;
  font-size: 1rem;
}

.order-header, .trades-header {
  display: grid;
  grid-template-columns: 1fr 1fr 1fr 0.8fr;
  gap: 8px;
  padding: 8px 0;
  border-bottom: 1px solid rgba(255,255,255,0.1);
  color: #9ca3af;
  font-size: 0.75rem;
  text-transform: uppercase;
}

.trades-header {
  grid-template-columns: 1fr 1fr 1fr 0.5fr;
}

.order-row, .trade-row {
  display: grid;
  grid-template-columns: 1fr 1fr 1fr 0.8fr;
  gap: 8px;
  padding: 6px 0;
  font-size: 0.875rem;
  color: #e5e7eb;
}

.trade-row {
  grid-template-columns: 1fr 1fr 1fr 0.5fr;
}

.price.sell, .price.ask { color: #ef4444; }
.price.buy, .price.bid { color: #22c55e; }

.side-badge {
  font-size: 0.75rem;
  padding: 2px 6px;
  border-radius: 4px;
}

.side-badge.buy { background: rgba(34, 197, 94, 0.2); color: #22c55e; }
.side-badge.sell { background: rgba(239, 68, 68, 0.2); color: #ef4444; }

.status-cell {
  display: flex;
  gap: 4px;
  align-items: center;
}

.status-badge {
  font-size: 0.7rem;
  padding: 2px 5px;
  border-radius: 4px;
  display: inline-flex;
  align-items: center;
  gap: 2px;
}

.status-badge.confirmed {
  background: rgba(34, 197, 94, 0.2);
  color: #22c55e;
}

.status-badge.pending {
  background: rgba(251, 191, 36, 0.2);
  color: #fbbf24;
}

.spread-indicator {
  text-align: center;
  padding: 12px 0;
  border-top: 1px solid rgba(255,255,255,0.1);
  border-bottom: 1px solid rgba(255,255,255,0.1);
  margin: 8px 0;
}

.current-price {
  font-size: 1.25rem;
  font-weight: 600;
  color: white;
}

.empty-message {
  text-align: center;
  padding: 20px;
  color: #6b7280;
}

@media (max-width: 768px) {
  .trading-content {
    grid-template-columns: 1fr;
  }
}
</style>

