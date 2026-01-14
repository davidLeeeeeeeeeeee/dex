<script setup lang="ts">
import { ref, onMounted, watch, computed } from 'vue'
import { fetchWitnessRequests } from '../../api'
import type { WitnessRequest } from '../../types'
import ProtocolModal from './ProtocolModal.vue'

const props = defineProps<{
  node: string
}>()

const requests = ref<WitnessRequest[]>([])
const loading = ref(false)
const error = ref('')

const loadRequests = async () => {
  loading.value = true
  error.value = ''
  try {
    requests.value = await fetchWitnessRequests(props.node)
  } catch (e: any) {
    error.value = e.message || 'Failed to load'
    console.error('Failed to load witness requests', e)
  } finally {
    loading.value = false
  }
}

watch(() => props.node, loadRequests)
onMounted(loadRequests)

function formatAmount(amount: string): string {
  const num = parseFloat(amount) / 1e8
  return num.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 8 })
}

function getStatusStr(status: any): string {
  if (!status) return ''
  return String(status).toUpperCase()
}

function getStatusStep(status: any): number {
  const s = getStatusStr(status)
  if (s.includes('PENDING')) return 1
  if (s.includes('VOTING')) return 2
  if (s.includes('CHALLENGE')) return 3
  if (s.includes('FINALIZED')) return 4
  return 1
}

const flowSteps = ['Pending', 'Voting', 'Challenge', 'Finalized']

const stats = computed(() => ({
  total: requests.value.length,
  pending: requests.value.filter(r => getStatusStr(r.status).includes('PENDING')).length,
  voting: requests.value.filter(r => getStatusStr(r.status).includes('VOTING')).length,
  finalized: requests.value.filter(r => getStatusStr(r.status).includes('FINALIZED')).length,
}))

// Modal state
const modalVisible = ref(false)
const modalTitle = ref('')
const mermaidDefinition = ref('')

const openFlow = (req: WitnessRequest) => {
  modalTitle.value = `Recharge Flow: ${(req.request_id || '').substring(0, 12)}...`

  const s = getStatusStr(req.status)
  const step = getStatusStep(req.status)
  const isFinalized = s.includes('FINALIZED')
  const isRejected = s.includes('REJECTED')
  
  let flow = 'graph LR\n'
  flow += '  A((Native Tx)) -->|Detect| B[Pending]\n'
  flow += '  B -->|Witness| C{Voting}\n'
  flow += '  C -->|Consensus| D[Challenge Period]\n'
  flow += '  D -->|Timeout| E[Finalized]\n'
  
  // Style current state
  if (step === 1) flow += '  style B fill:#f59e0b,stroke:#fff,stroke-width:2px\n'
  if (step === 2) flow += '  style C fill:#6366f1,stroke:#fff,stroke-width:2px\n'
  if (step === 3) flow += '  style D fill:#f97316,stroke:#fff,stroke-width:2px\n'
  if (isFinalized) flow += '  style E fill:#10b981,stroke:#fff,stroke-width:2px\n'
  if (isRejected) flow += '  style C fill:#ef4444,stroke:#fff,stroke-width:2px\n'
  
  mermaidDefinition.value = flow
  modalVisible.value = true
}
</script>

<template>
  <div class="glass-panel p-6">
    <!-- Header -->
    <div class="flex flex-col sm:flex-row justify-between items-start sm:items-center gap-4 mb-6">
      <div>
        <h3 class="text-xl font-bold text-white flex items-center gap-2">
          <span class="text-2xl">👁️</span>
          <span>Witness Recharge Flow</span>
        </h3>
        <p class="text-sm text-gray-500 mt-1">Cross-chain recharge requests and voting status</p>
      </div>
      <button @click="loadRequests" class="btn-primary flex items-center gap-2" :disabled="loading">
        <span v-if="loading" class="animate-spin">⟳</span>
        <span v-else>↻</span>
        {{ loading ? 'Loading...' : 'Refresh' }}
      </button>
    </div>

    <!-- Stats Bar -->
    <div v-if="requests.length > 0" class="grid grid-cols-4 gap-4 mb-6">
      <div class="stat-card">
        <div class="stat-value text-white">{{ stats.total }}</div>
        <div class="stat-label">Total</div>
      </div>
      <div class="stat-card">
        <div class="stat-value text-amber-400">{{ stats.pending }}</div>
        <div class="stat-label">Pending</div>
      </div>
      <div class="stat-card">
        <div class="stat-value text-indigo-400">{{ stats.voting }}</div>
        <div class="stat-label">Voting</div>
      </div>
      <div class="stat-card">
        <div class="stat-value text-emerald-400">{{ stats.finalized }}</div>
        <div class="stat-label">Finalized</div>
      </div>
    </div>

    <!-- Error State -->
    <div v-if="error" class="bg-red-500/10 border border-red-500/30 rounded-lg p-4 mb-4 text-red-400">
      {{ error }}
    </div>

    <!-- Empty State -->
    <div v-else-if="requests.length === 0 && !loading" class="text-center py-16">
      <div class="text-5xl mb-4 opacity-50">🔍</div>
      <p class="text-gray-400">No active witness requests found</p>
    </div>

    <!-- Request Cards -->
    <div v-else class="grid grid-cols-1 lg:grid-cols-2 gap-4">
      <div v-for="req in requests" :key="req.request_id || Math.random()" class="request-card cursor-pointer" @click="openFlow(req)">
        <!-- Card Header -->
        <div class="flex justify-between items-start mb-4">
          <div>
            <code class="text-xs text-indigo-400 bg-indigo-500/10 px-2 py-1 rounded">
              {{ (req.request_id || '').substring(0, 12) }}...
            </code>
            <div class="flex items-center gap-2 mt-2">
              <span :class="['chain-tag', (req.native_chain || '').toLowerCase()]">{{ req.native_chain || 'N/A' }}</span>
            </div>
          </div>
          <span :class="['status-badge', getStatusStr(req.status).toLowerCase().replace(/_/g, '-')]">
            {{ getStatusStr(req.status).replace(/_/g, ' ') || 'UNKNOWN' }}
          </span>
        </div>

        <!-- Flow Diagram -->
        <div class="flow-container mb-4">
          <div class="flow-track">
            <div class="flow-progress" :style="{ width: (getStatusStep(req.status) / 4 * 100) + '%' }"></div>
          </div>
          <div class="flow-steps">
            <div v-for="(step, i) in flowSteps" :key="step"
                 :class="['flow-step-item', { active: getStatusStep(req.status) > i }]">
              <div class="flow-dot"></div>
              <span class="flow-label">{{ step }}</span>
            </div>
          </div>
        </div>

        <!-- Details -->
        <div class="grid grid-cols-2 gap-3 text-sm">
          <div class="detail-item">
            <span class="detail-label">Amount</span>
            <span class="detail-value text-amber-400 font-bold">{{ formatAmount(req.amount || '0') }}</span>
          </div>
          <div class="detail-item">
            <span class="detail-label">Votes</span>
            <span class="detail-value">
              <span class="text-emerald-400">✓{{ req.pass_count || 0 }}</span>
              <span class="text-gray-500 mx-1">/</span>
              <span class="text-red-400">✗{{ req.fail_count || 0 }}</span>
            </span>
          </div>
          <div class="detail-item col-span-2">
            <span class="detail-label">Receiver</span>
            <code class="detail-value text-xs text-gray-400">
              {{ (req.receiver_address || '').length > 24
                 ? (req.receiver_address || '').substring(0, 12) + '...' + (req.receiver_address || '').slice(-10)
                 : (req.receiver_address || 'N/A') }}
            </code>
          </div>
        </div>
      </div>
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
.btn-primary {
  background: linear-gradient(135deg, #10b981 0%, #059669 100%);
  color: white;
  padding: 10px 20px;
  border-radius: 10px;
  font-size: 0.875rem;
  font-weight: 500;
  transition: all 0.2s;
  border: none;
  cursor: pointer;
}

.btn-primary:hover:not(:disabled) {
  transform: translateY(-1px);
  box-shadow: 0 8px 20px rgba(16, 185, 129, 0.3);
}

.btn-primary:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.stat-card {
  background: rgba(0, 0, 0, 0.3);
  border: 1px solid rgba(255, 255, 255, 0.05);
  border-radius: 12px;
  padding: 16px;
  text-align: center;
}

.stat-value {
  font-size: 1.5rem;
  font-weight: 700;
}

.stat-label {
  font-size: 0.75rem;
  color: #6b7280;
  text-transform: uppercase;
  margin-top: 4px;
}

.request-card {
  background: rgba(0, 0, 0, 0.3);
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 16px;
  padding: 20px;
  transition: all 0.2s;
}

.request-card:hover {
  border-color: rgba(255, 255, 255, 0.15);
  transform: translateY(-2px);
}

.chain-tag {
  padding: 4px 10px;
  border-radius: 6px;
  font-size: 0.7rem;
  font-weight: 700;
  text-transform: uppercase;
}

.chain-tag.btc { background: rgba(247, 147, 26, 0.2); color: #f7931a; }
.chain-tag.eth { background: rgba(98, 126, 234, 0.2); color: #627eea; }
.chain-tag.tron { background: rgba(235, 0, 41, 0.2); color: #eb0029; }
.chain-tag.sol { background: rgba(153, 69, 255, 0.2); color: #9945ff; }

.flow-container {
  position: relative;
  padding: 8px 0;
}

.flow-track {
  height: 4px;
  background: #1f2937;
  border-radius: 2px;
  overflow: hidden;
}

.flow-progress {
  height: 100%;
  background: linear-gradient(90deg, #10b981, #3b82f6);
  transition: width 0.5s ease;
}

.flow-steps {
  display: flex;
  justify-content: space-between;
  margin-top: 8px;
}

.flow-step-item {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
}

.flow-dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
  background: #374151;
  border: 2px solid #1f2937;
  transition: all 0.3s;
}

.flow-step-item.active .flow-dot {
  background: #10b981;
  border-color: #10b981;
  box-shadow: 0 0 10px rgba(16, 185, 129, 0.5);
}

.flow-label {
  font-size: 0.65rem;
  color: #6b7280;
  text-transform: uppercase;
}

.flow-step-item.active .flow-label {
  color: #10b981;
}

.detail-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.detail-label {
  font-size: 0.7rem;
  color: #6b7280;
  text-transform: uppercase;
}

.detail-value {
  color: white;
}

.animate-spin {
  animation: spin 1s linear infinite;
}

@keyframes spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}
</style>
