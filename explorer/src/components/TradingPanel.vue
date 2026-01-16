<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { fetchOrderBook, fetchRecentTrades } from '../api'
import type { OrderBookData, TradeRecord } from '../types'

const props = defineProps<{
  nodes: string[]
  defaultNode: string
}>()

const selectedNode = ref(props.defaultNode || '')
const selectedPair = ref('FB_USDT')
const loading = ref(false)
const error = ref('')

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
  error.value = ''
  
  try {
    const [obData, tradesData] = await Promise.all([
      fetchOrderBook(selectedNode.value, selectedPair.value),
      fetchRecentTrades(selectedNode.value, selectedPair.value)
    ])
    orderBook.value = obData
    recentTrades.value = tradesData
  } catch (e: any) {
    error.value = e.message || 'Failed to load trading data'
    console.error('Failed to load trading data', e)
  } finally {
    loading.value = false
  }
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

    <div v-if="error" class="error-message">{{ error }}</div>

    <div class="trading-content">
      <!-- 订单簿 -->
      <div class="order-book glass-panel">
        <h3>📊 Order Book</h3>
        <div class="order-book-content">
          <!-- 卖单 (Asks) -->
          <div class="asks-section">
            <div class="order-header">
              <span>Price</span>
              <span>Amount</span>
              <span>Total</span>
            </div>
            <div v-for="ask in orderBook.asks.slice(0, 10)" :key="ask.price" class="order-row ask">
              <span class="price sell">{{ ask.price }}</span>
              <span>{{ ask.amount }}</span>
              <span>{{ ask.total }}</span>
            </div>
            <div v-if="orderBook.asks.length === 0" class="empty-message">No sell orders</div>
          </div>
          
          <!-- 当前价格 -->
          <div class="spread-indicator">
            <span class="current-price">{{ orderBook.asks[0]?.price || '--' }}</span>
          </div>
          
          <!-- 买单 (Bids) -->
          <div class="bids-section">
            <div v-for="bid in orderBook.bids.slice(0, 10)" :key="bid.price" class="order-row bid">
              <span class="price buy">{{ bid.price }}</span>
              <span>{{ bid.amount }}</span>
              <span>{{ bid.total }}</span>
            </div>
            <div v-if="orderBook.bids.length === 0" class="empty-message">No buy orders</div>
          </div>
        </div>
      </div>

      <!-- 最近成交 -->
      <div class="recent-trades glass-panel">
        <h3>📈 Recent Trades</h3>
        <div class="trades-header">
          <span>Time</span>
          <span>Price</span>
          <span>Amount</span>
          <span>Side</span>
        </div>
        <div class="trades-list">
          <div v-for="trade in recentTrades.slice(0, 20)" :key="trade.id" class="trade-row">
            <span class="time">{{ trade.time }}</span>
            <span :class="['price', trade.side]">{{ trade.price }}</span>
            <span>{{ trade.amount }}</span>
            <span :class="['side-badge', trade.side]">{{ trade.side.toUpperCase() }}</span>
          </div>
          <div v-if="recentTrades.length === 0" class="empty-message">No recent trades</div>
        </div>
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
  background: rgba(239, 68, 68, 0.2);
  border: 1px solid rgba(239, 68, 68, 0.5);
  color: #fca5a5;
  padding: 12px;
  border-radius: 8px;
  margin-bottom: 20px;
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
  grid-template-columns: repeat(3, 1fr);
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
  grid-template-columns: repeat(3, 1fr);
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

