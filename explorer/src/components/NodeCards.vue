<script setup lang="ts">
import type { NodeSummary } from '../types'

defineProps<{
  nodes: NodeSummary[]
}>()

const emit = defineEmits<{
  cardClick: [address: string]
}>()

const numberFormat = new Intl.NumberFormat('en-US')

function formatNumber(value?: number | string | null): string {
  if (value === undefined || value === null) return '-'
  if (typeof value === 'string') return value
  return numberFormat.format(value)
}

function truncate(value?: string, max = 16): string {
  if (!value) return '-'
  if (value.length <= max) return value
  return `${value.slice(0, max)}...`
}

function statusClass(node: NodeSummary): string {
  if (node.error) return 'status-bad'
  if (node.status?.toLowerCase() === 'ok') return 'status-good'
  return 'status-warn'
}
</script>

<template>
  <div class="cards">
    <div 
      v-if="nodes.length === 0" 
      class="empty-state"
    >
      Select nodes and press Refresh to see their status.
    </div>
    
    <div 
      v-for="(node, index) in nodes" 
      :key="node.address"
      class="card"
      :style="{ animationDelay: `${index * 30}ms` }"
      @click="emit('cardClick', node.address)"
    >
      <h3>{{ node.address || 'unknown' }}</h3>
      
      <div class="status-line">
        <span :class="['status-dot', statusClass(node)]"></span>
        <span>{{ node.error ? 'error' : (node.status || 'unknown') }}</span>
      </div>
      
      <div class="meta">
        <div class="kv">
          <span>Current height</span>
          <span>{{ formatNumber(node.current_height) }}</span>
        </div>
        <div class="kv">
          <span>Last accepted</span>
          <span>{{ formatNumber(node.last_accepted_height) }}</span>
        </div>
        <div class="kv">
          <span>Height delta</span>
          <span>{{ formatNumber(Math.max(0, (node.current_height ?? 0) - (node.last_accepted_height ?? 0))) }}</span>
        </div>
        <div class="kv">
          <span>Latency</span>
          <span>{{ node.latency_ms || 0 }} ms</span>
        </div>
        
        <div v-if="node.info" class="kv">
          <span>Info</span>
          <span>{{ node.info }}</span>
        </div>
        <div v-if="node.error" class="kv">
          <span>Error</span>
          <span>{{ node.error }}</span>
        </div>
        
        <!-- Block info -->
        <template v-if="node.block">
          <div class="kv">
            <span>Block hash</span>
            <span>{{ truncate(node.block.block_hash) }}</span>
          </div>
          <div class="kv">
            <span>Miner</span>
            <span>{{ node.block.miner || '-' }}</span>
          </div>
          <div class="kv">
            <span>Tx count</span>
            <span>{{ formatNumber(node.block.tx_count) }}</span>
          </div>
          <div v-if="node.block.accumulated_reward" class="kv">
            <span>Reward</span>
            <span>{{ node.block.accumulated_reward }}</span>
          </div>
        </template>
        
        <!-- Frost metrics -->
        <template v-if="node.frost_metrics">
          <div class="kv">
            <span>Frost jobs</span>
            <span>{{ formatNumber(node.frost_metrics.frost_jobs) }} | {{ formatNumber(node.frost_metrics.frost_withdraws) }}</span>
          </div>
          <div class="kv">
            <span>Goroutines</span>
            <span>{{ formatNumber(node.frost_metrics.num_goroutine) }}</span>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>

