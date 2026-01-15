<script setup lang="ts">
import { ref, watch, onMounted } from 'vue'
import type { AccountInfo, TxRecord } from '../types'
import { fetchTxHistory } from '../api'

const props = defineProps<{
  account: AccountInfo
}>()

const emit = defineEmits<{
  back: []
  txClick: [txId: string]
}>()

// 交易历史
const txHistory = ref<TxRecord[]>([])
const txTotalCount = ref(0)
const txLoading = ref(false)
const txError = ref('')

async function loadTxHistory() {
  if (!props.account.address) return
  txLoading.value = true
  txError.value = ''
  try {
    const resp = await fetchTxHistory(props.account.address, 50)
    txHistory.value = resp.txs || []
    txTotalCount.value = resp.total_count || 0
  } catch (e: any) {
    txError.value = e.message || 'Failed to load transaction history'
  } finally {
    txLoading.value = false
  }
}

onMounted(() => {
  loadTxHistory()
})

watch(() => props.account.address, () => {
  loadTxHistory()
})

function formatBalance(val?: string): string {
  if (!val) return '0'
  // 简单格式化大数字
  const num = BigInt(val)
  return num.toLocaleString()
}

function getBalanceEntries(balances?: Record<string, any>): [string, any][] {
  if (!balances) return []
  return Object.entries(balances)
}

function truncateHash(hash?: string, len: number = 8): string {
  if (!hash) return '-'
  if (hash.length <= len * 2 + 3) return hash
  return hash.slice(0, len) + '...' + hash.slice(-len)
}

function statusClass(status?: string): string {
  if (!status) return ''
  const s = status.toUpperCase()
  if (s === 'SUCCEED' || s === 'SUCCESS') return 'status-good'
  if (s === 'FAILED' || s === 'FAIL') return 'status-bad'
  return 'status-warn'
}

function handleTxClick(txId: string) {
  emit('txClick', txId)
}
</script>

<template>
  <section class="panel address-detail">
    <div class="panel-header">
      <h2>Address Details</h2>
      <button class="ghost" @click="emit('back')">← Back</button>
    </div>

    <div class="address-meta">
      <div class="meta-row">
        <span class="label">Address</span>
        <span class="value mono">{{ account.address || '-' }}</span>
      </div>
      <div class="meta-row">
        <span class="label">Nonce</span>
        <span class="value">{{ account.nonce ?? 0 }}</span>
      </div>
      <div class="meta-row">
        <span class="label">Is Miner</span>
        <span class="value">{{ account.is_miner ? 'Yes' : 'No' }}</span>
      </div>
      <div v-if="account.index" class="meta-row">
        <span class="label">Miner Index</span>
        <span class="value">{{ account.index }}</span>
      </div>
      <div v-if="account.unclaimed_reward" class="meta-row">
        <span class="label">Unclaimed Reward</span>
        <span class="value">{{ formatBalance(account.unclaimed_reward) }}</span>
      </div>
    </div>

    <div v-if="getBalanceEntries(account.balances).length > 0" class="balances-section">
      <h3>Token Balances</h3>
      <table class="balance-table">
        <thead>
          <tr>
            <th>Token</th>
            <th>Available</th>
            <th>Miner Locked</th>
            <th>Witness Locked</th>
            <th>Liquid Locked</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="[token, bal] in getBalanceEntries(account.balances)" :key="token">
            <td class="mono token-cell">{{ token }}</td>
            <td class="amount">{{ formatBalance(bal.balance) }}</td>
            <td class="amount muted">{{ formatBalance(bal.miner_locked_balance) }}</td>
            <td class="amount muted">{{ formatBalance(bal.witness_locked_balance) }}</td>
            <td class="amount muted">{{ formatBalance(bal.liquid_locked_balance) }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-else class="empty-balances">
      <p>No token balances found for this address.</p>
    </div>

    <!-- Transaction History Section -->
    <div class="tx-history-section">
      <div class="section-header">
        <h3>Transaction History</h3>
        <span v-if="txTotalCount > 0" class="tx-count">{{ txTotalCount }} transactions</span>
        <button class="ghost refresh-btn" @click="loadTxHistory" :disabled="txLoading">
          {{ txLoading ? '⟳ Loading...' : '↻ Refresh' }}
        </button>
      </div>

      <div v-if="txError" class="error-message">{{ txError }}</div>

      <div v-if="txLoading && txHistory.length === 0" class="loading-state">
        Loading transaction history...
      </div>

      <div v-else-if="txHistory.length > 0" class="tx-list">
        <table class="tx-table">
          <thead>
            <tr>
              <th>Tx ID</th>
              <th>Type</th>
              <th>From</th>
              <th>To</th>
              <th>Value</th>
              <th>Height</th>
              <th>Status</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="tx in txHistory" :key="tx.tx_id" class="tx-row" @click="handleTxClick(tx.tx_id)">
              <td class="mono tx-id-cell">{{ truncateHash(tx.tx_id, 8) }}</td>
              <td>{{ tx.tx_type || '-' }}</td>
              <td class="mono">
                <span v-if="tx.from_address === account.address" class="self-tag">Self</span>
                <span v-else>{{ truncateHash(tx.from_address, 6) }}</span>
              </td>
              <td class="mono">
                <span v-if="tx.to_address === account.address" class="self-tag">Self</span>
                <span v-else-if="tx.to_address">{{ truncateHash(tx.to_address, 6) }}</span>
                <span v-else>-</span>
              </td>
              <td class="mono amount">{{ tx.value || '-' }}</td>
              <td class="mono">{{ tx.height }}</td>
              <td :class="statusClass(tx.status)">{{ tx.status || '-' }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-else class="empty-tx-history">
        <p>No transaction history found for this address.</p>
        <p class="muted hint">Note: Transaction history requires the explorer to sync blocks from the network.</p>
      </div>
    </div>
  </section>
</template>

<style scoped>
.address-detail {
  max-width: 900px;
}

.address-meta {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  margin-bottom: 1.5rem;
}

.meta-row {
  display: flex;
  gap: 1rem;
  align-items: baseline;
}

.meta-row .label {
  min-width: 140px;
  color: var(--muted);
  font-size: 0.875rem;
}

.meta-row .value {
  flex: 1;
  word-break: break-all;
}

.balances-section h3 {
  margin-bottom: 1rem;
  font-size: 1rem;
  color: var(--fg);
}

.balance-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.875rem;
}

.balance-table th,
.balance-table td {
  padding: 0.5rem 0.75rem;
  text-align: left;
  border-bottom: 1px solid var(--border);
}

.balance-table th {
  color: var(--muted);
  font-weight: 500;
}

.token-cell {
  max-width: 200px;
  overflow: hidden;
  text-overflow: ellipsis;
}

.amount {
  text-align: right;
  font-family: var(--font-mono);
}

.amount.muted {
  color: var(--muted);
}

.empty-balances {
  padding: 2rem;
  text-align: center;
  color: var(--muted);
}

/* Transaction History Styles */
.tx-history-section {
  margin-top: 2rem;
  padding-top: 1.5rem;
  border-top: 1px solid var(--border);
}

.section-header {
  display: flex;
  align-items: center;
  gap: 1rem;
  margin-bottom: 1rem;
}

.section-header h3 {
  margin: 0;
  font-size: 1rem;
  color: var(--fg);
}

.tx-count {
  color: var(--muted);
  font-size: 0.875rem;
}

.refresh-btn {
  margin-left: auto;
  font-size: 0.875rem;
}

.tx-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.875rem;
}

.tx-table th,
.tx-table td {
  padding: 0.5rem 0.5rem;
  text-align: left;
  border-bottom: 1px solid var(--border);
}

.tx-table th {
  color: var(--muted);
  font-weight: 500;
}

.tx-row {
  cursor: pointer;
  transition: background-color 0.15s ease;
}

.tx-row:hover {
  background-color: var(--row-hover);
}

.tx-id-cell {
  color: var(--accent, #60a5fa);
  cursor: pointer;
}

.tx-id-cell:hover {
  text-decoration: underline;
}

.self-tag {
  color: var(--accent);
  font-weight: 500;
}

.empty-tx-history {
  padding: 2rem;
  text-align: center;
  color: var(--muted);
}

.empty-tx-history .hint {
  font-size: 0.875rem;
  margin-top: 0.5rem;
}

.loading-state {
  padding: 2rem;
  text-align: center;
  color: var(--muted);
}

.error-message {
  padding: 0.75rem;
  margin-bottom: 1rem;
  background-color: var(--error-bg, rgba(239, 68, 68, 0.1));
  border: 1px solid var(--error-border, #ef4444);
  border-radius: 4px;
  color: var(--error, #f87171);
  font-size: 0.875rem;
}

.status-good {
  color: var(--status-good, #22c55e);
}

.status-bad {
  color: var(--status-bad, #ef4444);
}

.status-warn {
  color: var(--status-warn, #f59e0b);
}
</style>

