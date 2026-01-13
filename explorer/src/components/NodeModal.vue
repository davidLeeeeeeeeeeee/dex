<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import type { NodeDetails } from '../types'
import { fetchNodeDetails } from '../api'

const props = defineProps<{
  visible: boolean
  address: string
}>()

const emit = defineEmits<{
  close: []
}>()

const loading = ref(false)
const error = ref('')
const details = ref<NodeDetails | null>(null)

const numberFormat = new Intl.NumberFormat('en-US')

function formatNumber(value?: number | string | null): string {
  if (value === undefined || value === null) return '-'
  if (typeof value === 'string') return value
  return numberFormat.format(value)
}

// API 统计计算属性
const sortedApiStats = computed(() => {
  const stats = details.value?.frost_metrics?.api_call_stats
  if (!stats) return []
  return Object.entries(stats).sort((a, b) => b[1] - a[1])
})

const totalApiCalls = computed(() => {
  const stats = details.value?.frost_metrics?.api_call_stats
  if (!stats) return 0
  return Object.values(stats).reduce((sum, count) => sum + count, 0)
})

async function loadDetails() {
  if (!props.address) return
  
  loading.value = true
  error.value = ''
  details.value = null
  
  try {
    details.value = await fetchNodeDetails(props.address)
  } catch (err: any) {
    error.value = err.message
  } finally {
    loading.value = false
  }
}

watch(() => props.visible, (visible) => {
  if (visible && props.address) {
    loadDetails()
  }
})

watch(() => props.address, () => {
  if (props.visible && props.address) {
    loadDetails()
  }
})
</script>

<template>
  <div 
    v-if="visible" 
    class="modal-overlay"
    @click.self="emit('close')"
  >
    <div class="modal">
      <div class="modal-header">
        <h2>Node: {{ address }}</h2>
        <button class="ghost" @click="emit('close')">Close</button>
      </div>
      
      <div class="modal-content">
        <div v-if="loading" class="empty-state">
          Loading details for {{ address }}...
        </div>
        
        <div v-else-if="error" class="empty-state">
          Error: {{ error }}
        </div>
        
        <template v-else-if="details">
          <!-- Basic info -->
          <div class="meta">
            <div class="kv"><span>Address</span><span>{{ details.address }}</span></div>
            <div class="kv"><span>Status</span><span>{{ details.status || '-' }}</span></div>
            <div class="kv"><span>Current height</span><span>{{ formatNumber(details.current_height) }}</span></div>
            <div class="kv"><span>Last accepted</span><span>{{ formatNumber(details.last_accepted_height) }}</span></div>
            <div v-if="details.info" class="kv"><span>Info</span><span>{{ details.info }}</span></div>
            <div v-if="details.error" class="kv"><span>Error</span><span>{{ details.error }}</span></div>
            
            <template v-if="details.block">
              <div class="kv"><span>Block hash</span><span>{{ details.block.block_hash || '-' }}</span></div>
              <div class="kv"><span>Proposer</span><span>{{ details.block.miner || '-' }}</span></div>
              <div class="kv"><span>Tx count</span><span>{{ formatNumber(details.block.tx_count) }}</span></div>
            </template>
          </div>
          
          <!-- API Call Stats -->
          <div v-if="details.frost_metrics?.api_call_stats && Object.keys(details.frost_metrics.api_call_stats).length > 0" class="log-container">
            <h4>API Call Statistics</h4>
            <table class="block-table">
              <thead>
                <tr>
                  <th>API</th>
                  <th>Calls</th>
                  <th>%</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="[api, count] in sortedApiStats" :key="api">
                  <td>{{ api }}</td>
                  <td>{{ formatNumber(count) }}</td>
                  <td>{{ ((count / totalApiCalls) * 100).toFixed(1) }}%</td>
                </tr>
                <tr class="total-row">
                  <td><strong>TOTAL</strong></td>
                  <td><strong>{{ formatNumber(totalApiCalls) }}</strong></td>
                  <td><strong>100%</strong></td>
                </tr>
              </tbody>
            </table>
          </div>

          <!-- Logs -->
          <div class="log-container">
            <div class="log-list">
              <template v-if="details.logs && details.logs.length > 0">
                <div v-for="(log, i) in details.logs" :key="i" class="log-line">
                  <span class="log-time">{{ log.timestamp }}</span>
                  <span :class="['log-level', log.level]">{{ log.level }}</span>
                  <span class="log-msg">{{ log.message }}</span>
                </div>
              </template>
              <div v-else class="muted">No logs available.</div>
            </div>
          </div>
          
          <!-- Recent blocks -->
          <div class="log-container" style="margin-top: 24px">
            <h4>Recent Blocks (Top 50)</h4>
            <table v-if="details.recent_blocks && details.recent_blocks.length > 0" class="block-table">
              <thead>
                <tr>
                  <th>Height</th>
                  <th>Hash</th>
                  <th>Tx</th>
                  <th>Miner</th>
                  <th>Reward</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="block in details.recent_blocks" :key="block.block_hash">
                  <td>{{ formatNumber(block.height) }}</td>
                  <td class="hash-cell">{{ block.block_hash || '-' }}</td>
                  <td>{{ formatNumber(block.tx_count) }}</td>
                  <td class="hash-cell">{{ block.miner || '-' }}</td>
                  <td>{{ block.accumulated_reward || '-' }}</td>
                </tr>
              </tbody>
            </table>
            <div v-else class="muted">No recent blocks available.</div>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>

