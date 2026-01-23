<script setup lang="ts">
import { ref, onMounted, watch, onUnmounted } from 'vue'
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
    return date.toLocaleString('zh-CN', {
      timeZone: 'Asia/Shanghai',
      hour: '2-digit', minute: '2-digit', second: '2-digit',
      hour12: false
    })
  } catch { return timeStr }
}

const selectedNode = ref(props.defaultNode || '')
const selectedPair = ref('FB_USDT')
const loading = ref(false)

const orderBookError = ref('')
const tradesError = ref('')

const orderBook = ref<OrderBookData>({
  bids: [], asks: [], pair: '', lastUpdate: ''
})
const recentTrades = ref<TradeRecord[]>([])
const tradingPairs = ref(['FB_USDT', 'BTC_USDT', 'ETH_USDT'])

watch(() => props.defaultNode, (val) => {
  if (val && !selectedNode.value) selectedNode.value = val
})

const loadData = async () => {
  if (!selectedNode.value || !selectedPair.value) return
  loading.value = true
  orderBookError.value = ''
  tradesError.value = ''

  const [obResult, tradesResult] = await Promise.allSettled([
    fetchOrderBook(selectedNode.value, selectedPair.value),
    fetchRecentTrades(selectedNode.value, selectedPair.value)
  ])

  if (obResult.status === 'fulfilled') {
    orderBook.value = obResult.value
  } else {
    orderBookError.value = obResult.reason?.message || 'Order book synchronization failed'
  }

  if (tradesResult.status === 'fulfilled') {
    recentTrades.value = tradesResult.value
  } else {
    tradesError.value = tradesResult.reason?.message || 'Trade history retrieval failed'
  }
  loading.value = false
}

onMounted(() => {
  if (props.defaultNode) selectedNode.value = props.defaultNode
  loadData()
})

watch([selectedNode, selectedPair], () => { loadData() })

let refreshTimer: number | null = null
onMounted(() => { refreshTimer = window.setInterval(loadData, 5000) })
onUnmounted(() => { if (refreshTimer) clearInterval(refreshTimer) })
</script>

<template>
  <div class="trading-viewport">
    <!-- Action Bar -->
    <header class="action-bar panel">
      <div class="settings-group">
        <div class="setting-item">
          <label>Liquidity Node</label>
          <div class="select-box">
             <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="icon"><path d="M21 12a9 9 0 0 1-9 9m9-9a9 9 0 0 0-9-9m9 9H3m9-9a9 9 0 0 1-9 9m9-9c1.657 0 3 4.03 3 9s-1.343 9-3 9m0-18c-1.657 0-3 4.03-3 9s1.343 9 3 9"/></svg>
             <select v-model="selectedNode">
               <option v-for="node in nodes" :key="node" :value="node">{{ node }}</option>
             </select>
          </div>
        </div>
        <div class="setting-item">
          <label>Trading Pair</label>
          <div class="select-box">
             <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="icon"><path d="m3 11 18-5v12L3 14v-3z"/><path d="M11.6 16.8 a3 3 0 1 1 -3.2 -4.8"/></svg>
             <select v-model="selectedPair">
               <option v-for="pair in tradingPairs" :key="pair" :value="pair">{{ pair.replace('_', '/') }}</option>
             </select>
          </div>
        </div>
      </div>

      <button @click="loadData" class="refresh-orb" :disabled="loading">
        <svg :class="{ 'spin': loading }" xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M3 21v-5h5"/></svg>
      </button>
    </header>

    <div class="trading-core-grid">
      <!-- Order Book Section -->
      <section class="panel order-book-premium">
        <div class="section-header">
           <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="12" x2="12" y1="20" y2="10"/><line x1="18" x2="18" y1="20" y2="4"/><line x1="6" x2="6" y1="20" y2="16"/></svg>
           <h3>Order Liquidity</h3>
           <div class="live-tag">LIVE</div>
        </div>

        <div v-if="orderBookError" class="error-notice">{{ orderBookError }}</div>

        <div class="ob-table-header">
          <span class="price-col">Price</span>
          <span class="amount-col">Amount</span>
          <span class="total-col">Total</span>
        </div>

        <!-- Asks (Sells) -->
        <div class="asks-viewport">
          <div v-for="ask in (orderBook.asks || []).slice(0, 12).reverse()" :key="ask.price" class="ob-row ask">
             <div class="row-bg" :style="{ width: (parseFloat(ask.total) / (parseFloat(orderBook.asks[orderBook.asks.length-1]?.total || '1')) * 100) + '%' }"></div>
             <span class="price-val">{{ ask.price }}</span>
             <span class="amount-val">{{ ask.amount }}</span>
             <span class="total-val">{{ ask.total }}</span>
             <div class="conf-stats">
               <span v-if="ask.confirmedCount" class="c-badge">✓{{ ask.confirmedCount }}</span>
             </div>
          </div>
        </div>

        <div class="spread-banner">
          <div class="price-big">{{ orderBook.asks[0]?.price || '--' }}</div>
          <div class="usd-approx">≈$ {{ orderBook.asks[0]?.price }}</div>
        </div>

        <!-- Bids (Buys) -->
        <div class="bids-viewport">
          <div v-for="bid in (orderBook.bids || []).slice(0, 12)" :key="bid.price" class="ob-row bid">
             <div class="row-bg" :style="{ width: (parseFloat(bid.total) / (parseFloat(orderBook.bids[orderBook.bids.length-1]?.total || '1')) * 100) + '%' }"></div>
             <span class="price-val">{{ bid.price }}</span>
             <span class="amount-val">{{ bid.amount }}</span>
             <span class="total-val">{{ bid.total }}</span>
             <div class="conf-stats">
               <span v-if="bid.confirmedCount" class="c-badge">✓{{ bid.confirmedCount }}</span>
             </div>
          </div>
        </div>
      </section>

      <!-- Recent Trades Section -->
      <section class="panel trade-history-premium">
        <div class="section-header">
           <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/></svg>
           <h3>Execution History</h3>
        </div>

        <div v-if="tradesError" class="error-notice">{{ tradesError }}</div>

        <div class="th-table-header">
           <span>Time</span>
           <span>Price</span>
           <span class="text-right">Amount</span>
        </div>

        <div class="trades-viewport">
          <div v-for="(trade, index) in recentTrades" :key="index" class="trade-item">
             <span class="t-time text-gray-500">{{ formatTimeBeijing(trade.time) }}</span>
             <span :class="['t-price font-bold', trade.side]">{{ trade.price }}</span>
             <div class="t-vol-group">
                <span class="t-amount">{{ trade.amount }}</span>
                <div :class="['side-tick', trade.side]"></div>
             </div>
          </div>
          <div v-if="recentTrades.length === 0" class="empty-trades">Waiting for market activity...</div>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
@import url('https://fonts.googleapis.com/css2?family=Outfit:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap');

.trading-viewport {
  display: flex; flex-direction: column; gap: 24px;
  font-family: 'Outfit', sans-serif;
}

.action-bar {
  display: flex; justify-content: space-between; align-items: center;
  padding: 16px 24px; background: rgba(13, 17, 23, 0.4);
}

.settings-group { display: flex; gap: 32px; }
.setting-item { display: flex; flex-direction: column; gap: 6px; }
.setting-item label { font-size: 0.65rem; font-weight: 800; color: #475569; text-transform: uppercase; letter-spacing: 0.05em; }

.select-box {
  display: flex; align-items: center; gap: 10px;
  background: rgba(0, 0, 0, 0.2); border: 1px solid rgba(255, 255, 255, 0.05);
  border-radius: 10px; padding: 2px 12px;
}
.select-box .icon { color: #6366f1; }
.select-box select {
  background: transparent; border: none; color: #fff;
  padding: 8px 0; font-size: 0.85rem; font-weight: 600; outline: none;
}

.refresh-orb {
  width: 44px; height: 44px; border-radius: 50%;
  background: rgba(99, 102, 241, 0.1); border: 1px solid rgba(99, 102, 241, 0.2);
  display: flex; align-items: center; justify-content: center;
  color: #818cf8; cursor: pointer; transition: all 0.3s;
}
.refresh-orb:hover { background: #6366f1; color: #fff; transform: rotate(180deg); }

.trading-core-grid { display: grid; grid-template-columns: 1.2fr 1fr; gap: 24px; }

.section-header {
  display: flex; align-items: center; gap: 12px; margin-bottom: 20px;
}
.section-header h3 { margin: 0; font-size: 1rem; font-weight: 700; color: #fff; }

.live-tag {
  background: rgba(239, 68, 68, 0.1); color: #ef4444; border: 1px solid rgba(239, 68, 68, 0.2);
  padding: 2px 8px; border-radius: 4px; font-size: 0.6rem; font-weight: 900;
  animation: pulse 2s infinite;
}

/* Order Book Styles */
.ob-table-header {
  display: grid; grid-template-columns: 1fr 1fr 1fr;
  font-size: 0.65rem; font-weight: 800; color: #475569;
  text-transform: uppercase; padding: 10px 0;
}

.ob-row {
  display: grid; grid-template-columns: 1fr 1fr 1fr;
  padding: 6px 0; font-size: 0.8rem; font-family: 'JetBrains Mono', monospace;
  position: relative; align-items: center;
}
.row-bg { position: absolute; height: 100%; top: 0; right: 0; pointer-events: none; opacity: 0.1; transition: width 0.5s; }

.ask .price-val { color: #ef4444; }
.ask .row-bg { background: #ef4444; }
.bid .price-val { color: #10b981; }
.bid .row-bg { background: #10b981; }

.conf-stats { position: absolute; right: -5px; }
.c-badge { font-size: 0.6rem; background: #10b98122; color: #10b981; padding: 2px 4px; border-radius: 4px; }

.spread-banner {
  text-align: center; padding: 24px 0; margin: 12px 0;
  background: rgba(255, 255, 255, 0.02); border-radius: 12px;
}
.price-big { font-size: 1.8rem; font-weight: 800; color: #fff; }
.usd-approx { font-size: 0.75rem; color: #64748b; margin-top: 4px; }

/* Trade History */
.th-table-header {
  display: grid; grid-template-columns: 1fr 1fr 1fr;
  font-size: 0.65rem; font-weight: 800; color: #475569;
  text-transform: uppercase; padding: 10px 0;
}

.trade-item {
  display: grid; grid-template-columns: 1fr 1fr 1fr;
  padding: 10px 0; border-bottom: 1px solid rgba(255,255,255,0.02);
  font-size: 0.8rem; font-family: 'JetBrains Mono', monospace;
}
.t-price.buy { color: #10b981; }
.t-price.sell { color: #ef4444; }

.t-vol-group { display: flex; align-items: center; justify-content: flex-end; gap: 10px; }
.side-tick { width: 4px; height: 12px; border-radius: 2px; }
.side-tick.buy { background: #10b981; }
.side-tick.sell { background: #ef4444; }

.spin { animation: spin 1s linear infinite; }
@keyframes pulse { 0% { opacity: 0.5; } 50% { opacity: 1; } 100% { opacity: 0.5; } }
@keyframes spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }

.error-notice { color: #ef4444; font-size: 0.75rem; background: #ef444411; padding: 10px; border-radius: 8px; margin-bottom: 20px; }
.empty-trades { text-align: center; padding: 40px; color: #334155; }

@media (max-width: 1024px) {
  .trading-core-grid { grid-template-columns: 1fr; }
}
</style>
