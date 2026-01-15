<script setup lang="ts">
import type { BlockInfo } from '../types'

const props = defineProps<{
  block: BlockInfo
}>()

const emit = defineEmits<{
  txClick: [txId: string]
  addressClick: [address: string]
}>()

const numberFormat = new Intl.NumberFormat('en-US')

function formatNumber(value?: number | null): string {
  if (value === undefined || value === null) return '-'
  return numberFormat.format(value)
}

function truncateHash(hash?: string, chars = 12): string {
  if (!hash) return '-'
  if (hash.length <= chars * 2) return hash
  return `${hash.slice(0, chars)}...${hash.slice(-chars)}`
}

function statusClass(status?: string): string {
  if (!status) return ''
  const s = status.toUpperCase()
  if (s === 'SUCCEED' || s === 'SUCCESS') return 'status-good'
  if (s === 'FAILED' || s === 'FAIL') return 'status-bad'
  return 'status-warn'
}

function handleAddressClick(e: Event, address?: string) {
  e.stopPropagation()  // 防止触发行点击事件
  if (address) {
    emit('addressClick', address)
  }
}
</script>

<template>
  <section class="panel block-detail">
    <div class="panel-header">
      <h2>Block #{{ formatNumber(block.height) }}</h2>
    </div>

    <div class="block-meta">
      <div class="meta-row">
        <span class="label">Block Hash</span>
        <span class="value mono">{{ block.block_hash || '-' }}</span>
      </div>
      <div class="meta-row">
        <span class="label">Previous Hash</span>
        <span class="value mono">{{ truncateHash(block.prev_block_hash) }}</span>
      </div>
      <div class="meta-row">
        <span class="label">Txs Hash</span>
        <span class="value mono">{{ truncateHash(block.txs_hash) }}</span>
      </div>
      <div class="meta-row">
        <span class="label">Miner</span>
        <span class="value mono">{{ block.miner || '-' }}</span>
      </div>
      <div class="meta-row">
        <span class="label">Transaction Count</span>
        <span class="value">{{ formatNumber(block.tx_count) }}</span>
      </div>
      <div class="meta-row">
        <span class="label">Accumulated Reward</span>
        <span class="value">{{ block.accumulated_reward || '-' }}</span>
      </div>
      <div class="meta-row">
        <span class="label">Window</span>
        <span class="value">{{ block.window ?? '-' }}</span>
      </div>
    </div>

    <div v-if="block.transactions && block.transactions.length > 0" class="tx-section">
      <h3>Transactions ({{ block.transactions.length }})</h3>
      <table class="tx-table">
        <thead>
          <tr>
            <th>Tx ID</th>
            <th>Type</th>
            <th>From</th>
            <th>To</th>
            <th>Value</th>
            <th>Status</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="tx in block.transactions"
            :key="tx.tx_id"
            class="clickable"
            @click="emit('txClick', tx.tx_id)"
          >
            <td class="mono">{{ truncateHash(tx.tx_id, 8) }}</td>
            <td>{{ tx.tx_type || '-' }}</td>
            <td class="mono">
              <span
                v-if="tx.from_address"
                class="address-link"
                @click="handleAddressClick($event, tx.from_address)"
              >{{ truncateHash(tx.from_address, 8) }}</span>
              <span v-else>-</span>
            </td>
            <td class="mono">
              <span
                v-if="tx.to_address"
                class="address-link"
                @click="handleAddressClick($event, tx.to_address)"
              >{{ truncateHash(tx.to_address, 8) }}</span>
              <span v-else>-</span>
            </td>
            <td>{{ tx.value || '-' }}</td>
            <td :class="statusClass(tx.status)">{{ tx.status || '-' }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-else class="empty-state">
      No transactions in this block.
    </div>
  </section>
</template>

<style scoped>
.address-link {
  cursor: pointer;
  color: var(--accent, #60a5fa);
  text-decoration: underline;
  text-decoration-style: dotted;
}

.address-link:hover {
  color: var(--accent-hover, #93c5fd);
  text-decoration-style: solid;
}
</style>
