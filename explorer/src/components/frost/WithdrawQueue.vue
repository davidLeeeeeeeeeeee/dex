<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { fetchFrostWithdrawQueue } from '../../api'
import type { FrostWithdrawQueueItem } from '../../types'
import ProtocolModal from './ProtocolModal.vue'

const props = defineProps<{
  node: string
}>()

const queue = ref<FrostWithdrawQueueItem[]>([])
const loading = ref(false)
const error = ref('')

const loadQueue = async () => {
  loading.value = true
  error.value = ''
  try {
    queue.value = await fetchFrostWithdrawQueue(props.node)
  } catch (e: any) {
    error.value = e.message || 'Failed to load'
    console.error('Failed to load withdraw queue', e)
  } finally {
    loading.value = false
  }
}

watch(() => props.node, loadQueue)
onMounted(loadQueue)

function formatAmount(amount: string): string {
  const num = parseFloat(amount) / 1e8
  return num.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 8 })
}

function getChainColor(chain: string): string {
  const c = chain.toLowerCase()
  if (c === 'btc') return '#f7931a'
  if (c === 'eth') return '#627eea'
  if (c === 'tron') return '#eb0029'
  return '#9ca3af'
}

// Modal state
const modalVisible = ref(false)
const modalTitle = ref('')
const mermaidDefinition = ref('')

const openFlow = (item: FrostWithdrawQueueItem) => {
  modalTitle.value = `Withdraw Flow: ${item.withdraw_id.substring(0, 12)}...`
  const status = item.status.toUpperCase()
  let flow = 'graph LR\n'
  flow += '  A[QUEUED] -->|Coordinator Pick| B[SIGNING]\n'
  flow += '  B -->|FROST Success| C[SIGNED]\n'
  flow += '  C -->|Chain Broadcast| D((ON-CHAIN))\n'
  if (status === 'QUEUED') flow += '  style A fill:#3b82f6,stroke:#fff,stroke-width:2px\n'
  if (status === 'SIGNING') flow += '  style B fill:#8b5cf6,stroke:#fff,stroke-width:2px\n'
  if (status === 'SIGNED') flow += '  style C fill:#10b981,stroke:#fff,stroke-width:2px\n'
  mermaidDefinition.value = flow
  modalVisible.value = true
}
</script>

<template>
  <div class="withdraw-dashboard">
    <!-- Header Section -->
    <header class="header-section">
      <div class="header-left">
        <div class="icon-orb blue">
          <svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 21h18"/><path d="M3 10h18"/><path d="m5 6 7-3 7 3"/><path d="M4 10v11"/><path d="M20 10v11"/><path d="M8 14v3"/><path d="M12 14v3"/><path d="M16 14v3"/></svg>
        </div>
        <div class="title-meta">
          <h3 class="premium-title">Withdraw Queue</h3>
          <p class="premium-subtitle">Chain-agnostic asset redistribution queue</p>
        </div>
      </div>
      <button @click="loadQueue" class="glass-btn primary" :disabled="loading">
        <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" :class="{ 'spin': loading }"><path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M3 21v-5h5"/></svg>
        <span>{{ loading ? 'Updating...' : 'Refresh Queue' }}</span>
      </button>
    </header>

    <!-- Error Alert -->
    <div v-if="error" class="error-alert">
      <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" x2="12" y1="8" y2="12"/><line x1="12" x2="12.01" y1="16" y2="16"/></svg>
      <span>{{ error }}</span>
    </div>

    <!-- Data Presentation -->
    <div v-if="queue.length > 0" class="data-viewport">
      <table class="premium-table">
        <thead>
          <tr>
            <th class="px-6 py-4 text-left">Request ID</th>
            <th class="px-6 py-4 text-left">Asset / Protocol</th>
            <th class="px-6 py-4 text-left">Destination Address</th>
            <th class="px-6 py-4 text-right">Volume</th>
            <th class="px-6 py-4 text-center">Status</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="item in queue" :key="item.withdraw_id" class="table-row group" @click="openFlow(item)">
            <td class="px-6 py-4">
              <div class="id-badge">
                <code class="mono">{{ item.withdraw_id.substring(0, 8) }}</code>
                <div class="hover-full-id">{{ item.withdraw_id }}</div>
              </div>
            </td>
            <td class="px-6 py-4">
              <div class="asset-combo">
                <div class="asset-logo" :style="{ '--col': getChainColor(item.chain) }">
                  {{ item.chain.charAt(0) }}
                </div>
                <div class="asset-meta">
                  <span class="asset-name text-white font-bold">{{ item.asset }}</span>
                  <span class="chain-name text-[10px] text-gray-500 uppercase tracking-widest">{{ item.chain }}</span>
                </div>
              </div>
            </td>
            <td class="px-6 py-4">
              <div class="addr-box">
                <span class="mono text-gray-400">{{ item.to.substring(0, 10) }}...{{ item.to.slice(-8) }}</span>
                <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="addr-icon"><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/><polyline points="15 3 21 3 21 9"/><line x1="10" x2="21" y1="14" y2="3"/></svg>
              </div>
            </td>
            <td class="px-6 py-4 text-right">
              <div class="amount-stack">
                <span class="amount-val text-amber-400 font-bold font-mono">{{ formatAmount(item.amount) }}</span>
                <span class="amount-unit text-[10px] text-gray-500">{{ item.asset }}</span>
              </div>
            </td>
            <td class="px-6 py-4 text-center">
              <div :class="['status-pill', item.status.toLowerCase()]">
                <div class="pill-dot"></div>
                <span>{{ item.status }}</span>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
      
      <!-- Footer Info -->
      <footer class="viewport-footer">
        <div class="stat-group">
          <div class="stat-item">
            <span class="stat-label">Total Volume</span>
            <span class="stat-val">{{ queue.length }} <small>Requests</small></span>
          </div>
          <div class="stat-item">
            <span class="stat-label">Ready to Dispatch</span>
            <span class="stat-val text-emerald-500">{{ queue.filter(q => q.status === 'SIGNED').length }}</span>
          </div>
        </div>
        <div class="auto-sync">
          <div class="pulse-dot"></div>
          <span>Real-time synchronization active</span>
        </div>
      </footer>
    </div>

    <!-- Empty State -->
    <div v-else-if="!loading" class="empty-state">
      <div class="empty-orb">
        <svg xmlns="http://www.w3.org/2000/svg" width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/><path d="m9 15 2 2 4-4"/></svg>
      </div>
      <p class="text-gray-500 font-medium">All clear! No pending withdrawals.</p>
    </div>

    <!-- Protocol Modal -->
    <ProtocolModal 
      :show="modalVisible" 
      :title="modalTitle" 
      :definition="mermaidDefinition"
      @close="modalVisible = false"
    />
  </div>
</template>

<style scoped>
@import url('https://fonts.googleapis.com/css2?family=Outfit:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap');

.withdraw-dashboard {
  font-family: 'Outfit', sans-serif;
  color: #fff;
  background: rgba(13, 17, 23, 0.4);
  border: 1px solid rgba(255, 255, 255, 0.05);
  border-radius: 24px;
  padding: 32px;
  backdrop-filter: blur(20px);
}

.header-section {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 40px;
}

.header-left {
  display: flex;
  align-items: center;
  gap: 20px;
}

.icon-orb {
  width: 56px;
  height: 56px;
  background: linear-gradient(135deg, rgba(59, 130, 246, 0.2), rgba(37, 99, 235, 0.2));
  border: 1px solid rgba(59, 130, 246, 0.3);
  border-radius: 18px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #3b82f6;
  box-shadow: 0 10px 30px rgba(59, 130, 246, 0.1);
}

.premium-title {
  font-size: 1.5rem;
  font-weight: 700;
  letter-spacing: -0.02em;
  margin: 0;
}

.premium-subtitle {
  font-size: 0.85rem;
  color: #64748b;
  margin: 4px 0 0;
}

.glass-btn {
  background: rgba(255, 255, 255, 0.05);
  border: 1px solid rgba(255, 255, 255, 0.1);
  color: #fff;
  padding: 10px 20px;
  border-radius: 12px;
  display: flex;
  align-items: center;
  gap: 10px;
  font-weight: 600;
  font-size: 0.9rem;
  transition: all 0.3s cubic-bezier(0.4, 0, 0.2, 1);
  cursor: pointer;
}

.glass-btn:hover:not(:disabled) {
  background: rgba(59, 130, 246, 0.1);
  border-color: #3b82f6;
  box-shadow: 0 0 20px rgba(59, 130, 246, 0.2);
  transform: translateY(-2px);
}

.data-viewport {
  background: rgba(0, 0, 0, 0.2);
  border-radius: 18px;
  overflow: hidden;
  border: 1px solid rgba(255, 255, 255, 0.03);
}

.premium-table {
  width: 100%;
  border-collapse: collapse;
}

.premium-table thead {
  background: rgba(255, 255, 255, 0.02);
  border-bottom: 1px solid rgba(255, 255, 255, 0.05);
}

.premium-table th {
  font-size: 0.75rem;
  font-weight: 700;
  color: #475569;
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

.table-row {
  border-bottom: 1px solid rgba(255, 255, 255, 0.02);
  transition: all 0.2s;
  cursor: pointer;
}

.table-row:hover {
  background: rgba(255, 255, 255, 0.03);
}

.id-badge {
  position: relative;
  display: inline-block;
}

.id-badge code {
  background: rgba(99, 102, 241, 0.1);
  color: #818cf8;
  padding: 4px 8px;
  border-radius: 6px;
  font-size: 0.75rem;
}

.hover-full-id {
  position: absolute;
  top: 100%;
  left: 0;
  background: #000;
  padding: 8px 12px;
  border-radius: 8px;
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.7rem;
  white-space: nowrap;
  opacity: 0;
  pointer-events: none;
  transform: translateY(4px);
  transition: all 0.2s;
  z-index: 50;
  box-shadow: 0 10px 30px rgba(0,0,0,0.5);
  border: 1px solid rgba(255, 255, 255, 0.1);
}

.id-badge:hover .hover-full-id {
  opacity: 1;
  transform: translateY(8px);
}

.asset-combo {
  display: flex;
  align-items: center;
  gap: 12px;
}

.asset-logo {
  width: 32px;
  height: 32px;
  background: var(--col, #444);
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 900;
  font-size: 0.8rem;
  color: #fff;
  box-shadow: inset 0 0 10px rgba(0,0,0,0.3);
}

.asset-meta {
  display: flex;
  flex-direction: column;
}

.addr-box {
  display: flex;
  align-items: center;
  gap: 8px;
}

.addr-icon {
  color: #334155;
  transition: color 0.2s;
}

.table-row:hover .addr-icon {
  color: #6366f1;
}

.mono {
  font-family: 'JetBrains Mono', monospace;
}

.amount-stack {
  display: flex;
  flex-direction: column;
}

.status-pill {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 4px 12px;
  border-radius: 99px;
  font-size: 0.7rem;
  font-weight: 800;
  text-transform: uppercase;
  letter-spacing: 0.02em;
}

.status-pill.queued { background: rgba(59, 130, 246, 0.1); color: #60a5fa; }
.status-pill.signing { background: rgba(139, 92, 246, 0.1); color: #a78bfa; }
.status-pill.signed { background: rgba(16, 185, 129, 0.1); color: #34d399; }

.pill-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
  box-shadow: 0 0 8px currentColor;
}

.status-pill.signing .pill-dot { animation: pulse 1.5s infinite; }

.viewport-footer {
  padding: 24px;
  background: rgba(255, 255, 255, 0.01);
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.stat-group {
  display: flex;
  gap: 32px;
}

.stat-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.stat-label {
  font-size: 0.65rem;
  font-weight: 700;
  color: #475569;
  text-transform: uppercase;
}

.stat-val {
  font-size: 1.1rem;
  font-weight: 700;
}

.stat-val small {
  font-size: 0.7rem;
  color: #64748b;
  margin-left: 4px;
}

.auto-sync {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 0.75rem;
  color: #475569;
}

.pulse-dot {
  width: 8px;
  height: 8px;
  background: #10b981;
  border-radius: 50%;
  box-shadow: 0 0 10px #10b981;
  animation: pulse 2s infinite;
}

@keyframes pulse {
  0% { transform: scale(0.95); opacity: 0.5; }
  50% { transform: scale(1.1); opacity: 1; }
  100% { transform: scale(0.95); opacity:0.5; }
}

@keyframes spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }

.error-alert {
  background: rgba(239, 68, 68, 0.1);
  color: #ef4444;
  padding: 16px;
  border-radius: 14px;
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 24px;
  border: 1px solid rgba(239, 68, 68, 0.2);
}

.empty-state {
  text-align: center;
  padding: 60px 0;
}

.empty-orb {
  width: 64px;
  height: 64px;
  background: rgba(255, 255, 255, 0.03);
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #1e293b;
  margin: 0 auto 20px;
}
</style>
