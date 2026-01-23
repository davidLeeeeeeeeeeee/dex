<script setup lang="ts">
import { ref, watch } from 'vue'
import BlockDetail from './BlockDetail.vue'
import TxDetail from './TxDetail.vue'
import AddressDetail from './AddressDetail.vue'
import type { BlockInfo, TxInfo, AccountInfo } from '../types'
import { fetchBlock, fetchTx, fetchAddress, fetchRecentBlocks, type BlockHeaderInfo } from '../api'

const props = defineProps<{
  nodes: string[]
  defaultNode: string
}>()

// 状态
const searchQuery = ref('')
const searchType = ref<'block' | 'tx' | 'address'>('block')
const selectedNode = ref('')
const loading = ref(false)
const error = ref('')

// 结果
const blockResult = ref<BlockInfo | null>(null)
const txResult = ref<TxInfo | null>(null)
const addressResult = ref<AccountInfo | null>(null)

// 查看交易详情时的状态
const viewingTx = ref<TxInfo | null>(null)
// 查看地址详情时的状态
const viewingAddress = ref<AccountInfo | null>(null)

// 最近区块列表（默认显示）
const recentBlocks = ref<BlockHeaderInfo[]>([])
const loadingRecentBlocks = ref(false)

// 初始化选中节点
watch(() => props.defaultNode, (val) => {
  if (!selectedNode.value && val) {
    selectedNode.value = val
    loadRecentBlocks()
  }
}, { immediate: true })

watch(() => props.nodes, (nodes) => {
  if (!selectedNode.value && nodes.length > 0) {
    selectedNode.value = nodes[0]
    loadRecentBlocks()
  }
}, { immediate: true })

// 加载最近区块
async function loadRecentBlocks() {
  if (!selectedNode.value) return
  loadingRecentBlocks.value = true
  try {
    const result = await fetchRecentBlocks({
      node: selectedNode.value,
      count: 100,
    })
    if (!result.error) {
      recentBlocks.value = result.blocks || []
    }
  } catch (err: any) {
    console.error('Failed to load recent blocks:', err)
  } finally {
    loadingRecentBlocks.value = false
  }
}

// 点击区块行查看详情
async function handleBlockClick(height: number) {
  if (!selectedNode.value) return
  loading.value = true
  error.value = ''
  try {
    const result = await fetchBlock({
      node: selectedNode.value,
      height: height,
    })
    if (result.error) {
      error.value = result.error
    } else if (result.block) {
      blockResult.value = result.block
    }
  } catch (err: any) {
    error.value = err.message || 'Request failed'
  } finally {
    loading.value = false
  }
}

async function handleSearch() {
  if (!searchQuery.value.trim() || !selectedNode.value) return
  loading.value = true
  error.value = ''
  blockResult.value = null
  txResult.value = null
  addressResult.value = null
  viewingTx.value = null
  viewingAddress.value = null

  try {
    if (searchType.value === 'block') {
      const query = searchQuery.value.trim()
      const isHeight = /^\d+$/.test(query)
      const result = await fetchBlock({
        node: selectedNode.value,
        height: isHeight ? parseInt(query, 10) : undefined,
        hash: isHeight ? undefined : query,
      })
      if (result.error) error.value = result.error
      else if (result.block) blockResult.value = result.block
    } else if (searchType.value === 'tx') {
      const result = await fetchTx({ node: selectedNode.value, tx_id: searchQuery.value.trim() })
      if (result.error) error.value = result.error
      else if (result.transaction) txResult.value = result.transaction
    } else if (searchType.value === 'address') {
      const result = await fetchAddress({ node: selectedNode.value, address: searchQuery.value.trim() })
      if (result.error) error.value = result.error
      else if (result.account) addressResult.value = result.account
    }
  } catch (err: any) {
    error.value = err.message || 'Request failed'
  } finally {
    loading.value = false
  }
}

async function handleTxClick(txId: string) {
  if (!selectedNode.value || !txId) return
  loading.value = true
  error.value = ''
  try {
    const result = await fetchTx({ node: selectedNode.value, tx_id: txId })
    if (result.error) error.value = result.error
    else if (result.transaction) viewingTx.value = result.transaction
  } catch (err: any) {
    error.value = err.message || 'Request failed'
  } finally {
    loading.value = false
  }
}

function handleBack() {
  viewingTx.value = null
  viewingAddress.value = null
  blockResult.value = null
  txResult.value = null
  addressResult.value = null
  error.value = ''
}

async function handleAddressClick(address: string) {
  if (!selectedNode.value || !address) return
  loading.value = true
  error.value = ''
  try {
    const result = await fetchAddress({ node: selectedNode.value, address: address })
    if (result.error) error.value = result.error
    else if (result.account) viewingAddress.value = result.account
  } catch (err: any) {
    error.value = err.message || 'Request failed'
  } finally {
    loading.value = false
  }
}

function getPlaceholder(): string {
  switch (searchType.value) {
    case 'block': return 'Height or Block Hash'
    case 'tx': return 'Transaction Hash'
    case 'address': return 'Account Address'
    default: return 'Search...'
  }
}
</script>

<template>
  <div class="explorer-view">
    <!-- Search Bar Section -->
    <section class="panel search-premium">
      <div class="search-wrap">
        <div class="search-config">
          <div class="select-group">
            <svg class="icon-prefix" xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a9 9 0 0 1-9 9m9-9a9 9 0 0 0-9-9m9 9H3m9-9a9 9 0 0 1-9 9m9-9c1.657 0 3 4.03 3 9s-1.343 9-3 9m0-18c-1.657 0-3 4.03-3 9s1.343 9 3 9"/></svg>
            <select v-model="selectedNode" class="node-select">
              <option value="" disabled>Select Endpoint</option>
              <option v-for="node in nodes" :key="node" :value="node">{{ node }}</option>
            </select>
          </div>
          <div class="select-group">
            <svg class="icon-prefix" xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="18" height="18" x="3" y="3" rx="2" ry="2"/><line x1="3" x2="21" y1="9" y2="9"/><line x1="3" x2="21" y1="15" y2="15"/><line x1="9" x2="9" y1="3" y2="21"/><line x1="15" x2="15" y1="3" y2="21"/></svg>
            <select v-model="searchType" class="type-select">
              <option value="block">Block</option>
              <option value="tx">Transaction</option>
              <option value="address">Address</option>
            </select>
          </div>
        </div>
        
        <div class="search-input-field">
          <div class="input-orb">
            <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/></svg>
          </div>
          <input
            v-model="searchQuery"
            type="text"
            :placeholder="getPlaceholder()"
            @keyup.enter="handleSearch"
          />
          <button class="search-btn" @click="handleSearch" :disabled="loading">
            <span v-if="loading">Querying...</span>
            <span v-else>Search</span>
          </button>
        </div>
      </div>
    </section>

    <!-- Results Area -->
    <div v-if="error" class="error-slate animate-shake">
      <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" x2="12" y1="8" y2="12"/><line x1="12" x2="12.01" y1="16" y2="16"/></svg>
      <span>{{ error }}</span>
      <button @click="error = ''" class="close-error">✕</button>
    </div>

    <!-- Details View (Address/Tx/Block) -->
    <AddressDetail v-if="viewingAddress" :account="viewingAddress" @back="handleBack" @tx-click="handleTxClick" />
    <TxDetail v-else-if="viewingTx" :tx="viewingTx" @back="handleBack" @address-click="handleAddressClick" />
    <BlockDetail v-else-if="blockResult" :block="blockResult" @tx-click="handleTxClick" @address-click="handleAddressClick" @back="handleBack" />
    <TxDetail v-else-if="txResult" :tx="txResult" @address-click="handleAddressClick" />
    <AddressDetail v-else-if="addressResult" :account="addressResult" @tx-click="handleTxClick" />

    <!-- Default Dashboard -->
    <section v-else class="panel blocks-dashboard">
      <div class="panel-header">
        <div class="title-with-icon">
          <div class="icon-circle">
            <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/><polyline points="3.29 7 12 12 20.71 7"/><line x1="12" x2="12" y1="22" y2="12"/></svg>
          </div>
          <h2>Recent Blocks</h2>
        </div>
        <button class="glass-refresh" @click="loadRecentBlocks" :disabled="loadingRecentBlocks">
          <svg :class="{ 'spin': loadingRecentBlocks }" xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M3 21v-5h5"/></svg>
          {{ loadingRecentBlocks ? 'Loading...' : 'Refresh' }}
        </button>
      </div>

      <div class="table-container">
        <table class="premium-table">
          <thead>
            <tr>
              <th class="pl-6 w-32">Height</th>
              <th>Block Hash</th>
              <th>Miner</th>
              <th class="text-right">Tx Count</th>
              <th class="text-right pr-6">Reward</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="block in recentBlocks" :key="block.height" @click="handleBlockClick(block.height)" class="table-row">
              <td class="pl-6">
                <span class="height-badge">#{{ block.height }}</span>
              </td>
              <td class="hash-cell">
                <code class="mono text-gray-400">{{ block.block_hash ? block.block_hash.substring(0, 16) + '...' : '-' }}</code>
              </td>
              <td class="miner-cell">
                <div class="proposer">
                  <div class="proposer-avatar"></div>
                  <span class="mono">{{ block.miner ? block.miner.substring(0, 12) + '...' : '-' }}</span>
                </div>
              </td>
              <td class="text-right">
                <span class="tx-badge">{{ block.tx_count }} TXs</span>
              </td>
              <td class="text-right pr-6">
                <span class="reward-val">{{ block.accumulated_reward || '0' }}</span>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-if="recentBlocks.length === 0 && !loadingRecentBlocks" class="empty-slate-mini">
           No blockchain history synchronized.
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
@import url('https://fonts.googleapis.com/css2?family=Outfit:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap');

.explorer-view {
  display: flex;
  flex-direction: column;
  gap: 24px;
  font-family: 'Outfit', sans-serif;
}

/* Search Premium Bar */
.search-premium {
  padding: 12px;
  background: rgba(13, 17, 23, 0.4);
}

.search-wrap {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

@media (min-width: 1024px) {
  .search-wrap { flex-direction: row; align-items: center; }
}

.search-config {
  display: flex;
  gap: 10px;
}

.select-group {
  position: relative;
  display: flex;
  align-items: center;
  background: rgba(0, 0, 0, 0.2);
  border: 1px solid rgba(255, 255, 255, 0.05);
  border-radius: 12px;
  padding: 0 12px;
}

.icon-prefix { color: #475569; }

.select-group select {
  background: transparent;
  border: none;
  color: #fff;
  padding: 10px 8px;
  font-size: 0.85rem;
  font-weight: 600;
  cursor: pointer;
  outline: none;
}

.search-input-field {
  flex: 1;
  display: flex;
  align-items: center;
  background: rgba(255, 255, 255, 0.02);
  border: 1px solid rgba(255, 255, 255, 0.05);
  border-radius: 14px;
  padding: 4px 4px 4px 16px;
  transition: all 0.3s;
}

.search-input-field:focus-within {
  border-color: #6366f1;
  background: rgba(99, 102, 241, 0.05);
  box-shadow: 0 0 20px rgba(99, 102, 241, 0.1);
}

.input-orb { color: #64748b; }

.search-input-field input {
  background: transparent;
  border: none;
  flex: 1;
  padding: 12px;
  color: #fff;
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.9rem;
}

.search-input-field input::placeholder { color: #334155; font-family: 'Outfit'; }

.search-btn {
  background: #6366f1;
  color: #fff;
  border: none;
  padding: 10px 24px;
  border-radius: 10px;
  font-weight: 700;
  cursor: pointer;
  transition: all 0.2s;
}

.search-btn:hover:not(:disabled) {
  background: #4f46e5;
  transform: translateY(-1px);
}

/* Dashboard */
.blocks-dashboard { padding: 0; overflow: hidden; }

.panel-header {
  padding: 24px 32px;
  display: flex;
  justify-content: space-between;
  align-items: center;
  border-bottom: 1px solid rgba(255,255,255,0.05);
}

.title-with-icon { display: flex; align-items: center; gap: 14px; }
.icon-circle {
  width: 32px; height: 32px; background: rgba(99, 102, 241, 0.1);
  border-radius: 50%; display: flex; align-items: center; justify-content: center;
  color: #6366f1;
}

.glass-refresh {
  background: rgba(255, 255, 255, 0.03);
  border: 1px solid rgba(255, 255, 255, 0.08);
  color: #94a3b8;
  padding: 8px 16px;
  border-radius: 8px;
  font-size: 0.75rem;
  font-weight: 700;
  display: flex; align-items: center; gap: 8px;
  cursor: pointer;
}

.glass-refresh:hover { background: rgba(255, 255, 255, 0.05); color: #fff; }

.table-container { min-height: 400px; position: relative; }

.premium-table { width: 100%; border-collapse: collapse; }
.premium-table th {
  padding: 16px; text-align: left; font-size: 0.65rem; font-weight: 800;
  text-transform: uppercase; color: #475569; letter-spacing: 0.05em;
  background: rgba(0, 0, 0, 0.1);
}

.table-row {
  border-bottom: 1px solid rgba(255, 255, 255, 0.02);
  cursor: pointer;
  transition: all 0.2s;
}

.table-row:hover { background: rgba(99, 102, 241, 0.03); }

.height-badge {
  background: rgba(99, 102, 241, 0.1);
  color: #818cf8;
  font-family: 'JetBrains Mono', monospace;
  font-weight: 700;
  padding: 4px 10px;
  border-radius: 6px;
  font-size: 0.85rem;
}

.proposer { display: flex; align-items: center; gap: 10px; }
.proposer-avatar { width: 24px; height: 24px; background: #1e293b; border-radius: 6px; }

.tx-badge {
  background: rgba(255,255,255,0.03);
  padding: 4px 8px; border-radius: 6px;
  font-size: 0.7rem; font-weight: 700; color: #94a3b8;
}

.reward-val { font-family: 'JetBrains Mono', monospace; font-weight: 700; color: #f59e0b; }

.empty-slate-mini { text-align: center; padding: 100px 0; color: #334155; }

.error-slate {
  background: rgba(239, 68, 68, 0.1);
  border: 1px solid rgba(239, 68, 68, 0.2);
  color: #ef4444;
  padding: 16px 24px;
  border-radius: 12px;
  display: flex; align-items: center; gap: 12px;
}

.close-error { margin-left: auto; background: none; border: none; color: #991b1b; cursor: pointer; }

.mono { font-family: 'JetBrains Mono', monospace; }
.spin { animation: spin 1s linear infinite; }
@keyframes spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }
@keyframes shake {
  0%, 100% { transform: translateX(0); }
  25% { transform: translateX(-4px); }
  75% { transform: translateX(4px); }
}
.animate-shake { animation: shake 0.4s ease-in-out; }
</style>
