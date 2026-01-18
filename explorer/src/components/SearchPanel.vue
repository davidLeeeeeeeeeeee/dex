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
    // 加载最近区块
    loadRecentBlocks()
  }
}, { immediate: true })

watch(() => props.nodes, (nodes) => {
  if (!selectedNode.value && nodes.length > 0) {
    selectedNode.value = nodes[0]
    // 加载最近区块
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
    if (result.error) {
      console.error('Failed to load recent blocks:', result.error)
    } else {
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
    } else {
      error.value = 'Block not found'
    }
  } catch (err: any) {
    error.value = err.message || 'Request failed'
  } finally {
    loading.value = false
  }
}

async function handleSearch() {
  if (!searchQuery.value.trim()) {
    error.value = 'Please enter a search query'
    return
  }
  if (!selectedNode.value) {
    error.value = 'Please select a node'
    return
  }

  loading.value = true
  error.value = ''
  blockResult.value = null
  txResult.value = null
  addressResult.value = null
  viewingTx.value = null
  viewingAddress.value = null

  try {
    if (searchType.value === 'block') {
      // 判断是高度还是哈希
      const query = searchQuery.value.trim()
      const isHeight = /^\d+$/.test(query)

      const result = await fetchBlock({
        node: selectedNode.value,
        height: isHeight ? parseInt(query, 10) : undefined,
        hash: isHeight ? undefined : query,
      })

      if (result.error) {
        error.value = result.error
      } else if (result.block) {
        blockResult.value = result.block
      } else {
        error.value = 'Block not found'
      }
    } else if (searchType.value === 'tx') {
      const result = await fetchTx({
        node: selectedNode.value,
        tx_id: searchQuery.value.trim(),
      })

      if (result.error) {
        error.value = result.error
      } else if (result.transaction) {
        txResult.value = result.transaction
      } else {
        error.value = 'Transaction not found'
      }
    } else if (searchType.value === 'address') {
      const result = await fetchAddress({
        node: selectedNode.value,
        address: searchQuery.value.trim(),
      })

      if (result.error) {
        error.value = result.error
      } else if (result.account) {
        addressResult.value = result.account
      } else {
        error.value = 'Address not found'
      }
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
    const result = await fetchTx({
      node: selectedNode.value,
      tx_id: txId,
    })

    if (result.error) {
      error.value = result.error
    } else if (result.transaction) {
      viewingTx.value = result.transaction
    } else {
      error.value = 'Transaction not found'
    }
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

// 点击地址时的处理
async function handleAddressClick(address: string) {
  if (!selectedNode.value || !address) return

  loading.value = true
  error.value = ''

  try {
    const result = await fetchAddress({
      node: selectedNode.value,
      address: address,
    })

    if (result.error) {
      error.value = result.error
    } else if (result.account) {
      viewingAddress.value = result.account
    } else {
      error.value = 'Address not found'
    }
  } catch (err: any) {
    error.value = err.message || 'Request failed'
  } finally {
    loading.value = false
  }
}

// 计算 placeholder 文本
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
  <div class="search-panel">
    <section class="panel search-controls">
      <div class="panel-header">
        <h2>Search</h2>
      </div>
      
      <div class="search-bar">
        <select v-model="selectedNode" class="node-select">
          <option value="" disabled>Select node</option>
          <option v-for="node in nodes" :key="node" :value="node">{{ node }}</option>
        </select>
        
        <select v-model="searchType">
          <option value="block">Block</option>
          <option value="tx">Transaction</option>
          <option value="address">Address</option>
        </select>

        <input
          v-model="searchQuery"
          type="text"
          :placeholder="getPlaceholder()"
          @keyup.enter="handleSearch"
        />
        
        <button class="primary" @click="handleSearch" :disabled="loading">
          {{ loading ? 'Searching...' : 'Search' }}
        </button>
      </div>
    </section>

    <div v-if="error" class="error-message">{{ error }}</div>

    <div v-if="loading" class="loading-state">Loading...</div>

    <AddressDetail
      v-else-if="viewingAddress"
      :account="viewingAddress"
      @back="handleBack"
      @tx-click="handleTxClick"
    />

    <TxDetail
      v-else-if="viewingTx"
      :tx="viewingTx"
      @back="handleBack"
      @address-click="handleAddressClick"
    />

    <BlockDetail
      v-else-if="blockResult"
      :block="blockResult"
      @tx-click="handleTxClick"
      @address-click="handleAddressClick"
      @back="handleBack"
    />

    <TxDetail
      v-else-if="txResult"
      :tx="txResult"
      @address-click="handleAddressClick"
    />

    <AddressDetail
      v-else-if="addressResult"
      :account="addressResult"
      @tx-click="handleTxClick"
    />

    <!-- 默认显示最近区块列表 -->
    <section v-else-if="!loading && !error" class="panel recent-blocks">
      <div class="panel-header">
        <h2>Recent Blocks</h2>
        <button class="btn-refresh" @click="loadRecentBlocks" :disabled="loadingRecentBlocks">
          {{ loadingRecentBlocks ? 'Loading...' : '↻ Refresh' }}
        </button>
      </div>

      <div v-if="loadingRecentBlocks" class="loading-state">Loading recent blocks...</div>

      <div v-else-if="recentBlocks.length === 0" class="empty-state">
        No blocks found. Select a node to view recent blocks.
      </div>

      <table v-else class="blocks-table">
        <thead>
          <tr>
            <th>Height</th>
            <th>Block Hash</th>
            <th>Miner</th>
            <th>Tx Count</th>
            <th>Reward</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="block in recentBlocks"
            :key="block.height"
            @click="handleBlockClick(block.height)"
            class="clickable-row"
          >
            <td class="height">{{ block.height }}</td>
            <td class="hash" :title="block.block_hash">
              {{ block.block_hash ? block.block_hash.substring(0, 16) + '...' : '-' }}
            </td>
            <td class="miner" :title="block.miner">
              {{ block.miner ? block.miner.substring(0, 12) + '...' : '-' }}
            </td>
            <td class="tx-count">{{ block.tx_count }}</td>
            <td class="reward">{{ block.accumulated_reward || '0' }}</td>
          </tr>
        </tbody>
      </table>
    </section>
  </div>
</template>

<style scoped>
.recent-blocks {
  margin-top: 1rem;
}

.panel-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.btn-refresh {
  padding: 0.25rem 0.5rem;
  font-size: 0.875rem;
  cursor: pointer;
  background: var(--bg-secondary, #2a2a3e);
  border: 1px solid var(--border-color, #444);
  color: var(--text-primary, #fff);
  border-radius: 4px;
}

.btn-refresh:hover:not(:disabled) {
  background: var(--bg-hover, #3a3a4e);
}

.btn-refresh:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.blocks-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.875rem;
}

.blocks-table th,
.blocks-table td {
  padding: 0.5rem 0.75rem;
  text-align: left;
  border-bottom: 1px solid var(--border-color, #333);
}

.blocks-table th {
  background: var(--bg-secondary, #2a2a3e);
  font-weight: 600;
  color: var(--text-secondary, #aaa);
}

.blocks-table .clickable-row {
  cursor: pointer;
  transition: background-color 0.15s;
}

.blocks-table .clickable-row:hover {
  background: var(--bg-hover, #3a3a4e);
}

.blocks-table .height {
  font-weight: 600;
  color: var(--accent-color, #4dabf7);
}

.blocks-table .hash {
  font-family: monospace;
  color: var(--text-secondary, #aaa);
}

.blocks-table .miner {
  font-family: monospace;
  color: var(--text-secondary, #aaa);
}

.blocks-table .tx-count {
  text-align: center;
}

.blocks-table .reward {
  text-align: right;
  font-family: monospace;
}
</style>

