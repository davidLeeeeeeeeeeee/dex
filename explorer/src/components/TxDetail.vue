<script setup lang="ts">
import type { TxInfo } from '../types'

defineProps<{
  tx: TxInfo
}>()

const emit = defineEmits<{
  back: []
}>()

const numberFormat = new Intl.NumberFormat('en-US')

function formatNumber(value?: number | null): string {
  if (value === undefined || value === null) return '-'
  return numberFormat.format(value)
}

function statusClass(status?: string): string {
  if (!status) return ''
  const s = status.toUpperCase()
  if (s === 'SUCCEED' || s === 'SUCCESS') return 'status-good'
  if (s === 'FAILED' || s === 'FAIL') return 'status-bad'
  return 'status-warn'
}

function formatDetails(details?: Record<string, unknown>): string {
  if (!details || Object.keys(details).length === 0) return '-'
  try {
    return JSON.stringify(details, null, 2)
  } catch {
    return String(details)
  }
}
</script>

<template>
  <section class="panel tx-detail">
    <div class="panel-header">
      <h2>Transaction Details</h2>
      <button class="ghost" @click="emit('back')">← Back</button>
    </div>

    <div class="tx-meta">
      <div class="meta-row">
        <span class="label">Transaction ID</span>
        <span class="value mono">{{ tx.tx_id || '-' }}</span>
      </div>
      <div class="meta-row">
        <span class="label">Type</span>
        <span class="value">{{ tx.tx_type || '-' }}</span>
      </div>
      <div class="meta-row">
        <span class="label">Status</span>
        <span :class="['value', statusClass(tx.status)]">{{ tx.status || '-' }}</span>
      </div>
      <div class="meta-row">
        <span class="label">From Address</span>
        <span class="value mono">{{ tx.from_address || '-' }}</span>
      </div>
      <div v-if="tx.to_address" class="meta-row">
        <span class="label">To Address</span>
        <span class="value mono">{{ tx.to_address }}</span>
      </div>
      <div class="meta-row">
        <span class="label">Executed Height</span>
        <span class="value">{{ formatNumber(tx.executed_height) }}</span>
      </div>
      <div class="meta-row">
        <span class="label">Fee</span>
        <span class="value">{{ tx.fee || '-' }}</span>
      </div>
      <div class="meta-row">
        <span class="label">Nonce</span>
        <span class="value">{{ formatNumber(tx.nonce) }}</span>
      </div>
    </div>

    <div v-if="tx.details && Object.keys(tx.details).length > 0" class="details-section">
      <h3>Details</h3>
      <pre class="details-json">{{ formatDetails(tx.details) }}</pre>
    </div>
  </section>
</template>

