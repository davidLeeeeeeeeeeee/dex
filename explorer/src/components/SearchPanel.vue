<script setup lang="ts">
import { ref, watch } from 'vue'
import BlockDetail from './BlockDetail.vue'
import TxDetail from './TxDetail.vue'
import AddressDetail from './AddressDetail.vue'
import type { BlockInfo, TxInfo, TxSummary, AccountInfo } from '../types'
import { fetchBlock, fetchTx, fetchAddress } from '../api'

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

// 初始化选中节点
watch(() => props.defaultNode, (val) => {
  if (!selectedNode.value && val) {
    selectedNode.value = val
  }
}, { immediate: true })

watch(() => props.nodes, (nodes) => {
  if (!selectedNode.value && nodes.length > 0) {
    selectedNode.value = nodes[0]
  }
}, { immediate: true })

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

// 将 TxSummary 转换为 TxInfo（用于从区块内点击交易时）
function txSummaryToInfo(summary: TxSummary, blockHeight?: number): TxInfo {
  return {
    tx_id: summary.tx_id,
    tx_type: summary.tx_type,
    from_address: summary.from_address,
    to_address: summary.to_address,
    value: summary.value,
    status: summary.status,
    executed_height: blockHeight,
    fee: summary.fee,
    nonce: summary.nonce,
    details: summary.summary ? { summary: summary.summary } : undefined,
  }
}

function handleTxClick(txId: string) {
  // 先尝试从当前区块结果中找到交易
  if (blockResult.value?.transactions) {
    const txSummary = blockResult.value.transactions.find(tx => tx.tx_id === txId)
    if (txSummary) {
      viewingTx.value = txSummaryToInfo(txSummary, blockResult.value.height)
      return
    }
  }

  // 如果找不到，显示错误（不再尝试 API 调用，因为节点的 /getdata 可能没有索引）
  error.value = `Transaction ${txId} details not available`
}

function handleBack() {
  viewingTx.value = null
  viewingAddress.value = null
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

    <div v-else-if="!loading && !error" class="empty-state">
      Enter a block height, block hash, transaction hash, or account address to search.
    </div>
  </div>
</template>

