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

function getChainClass(chain: string): string {
  const c = chain.toLowerCase()
  if (c === 'btc') return 'chain-btc'
  if (c === 'eth') return 'chain-eth'
  if (c === 'tron') return 'chain-tron'
  return 'chain-default'
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
  
  // Style current state
  if (status === 'QUEUED') flow += '  style A fill:#3b82f6,stroke:#fff,stroke-width:2px\n'
  if (status === 'SIGNING') flow += '  style B fill:#8b5cf6,stroke:#fff,stroke-width:2px\n'
  if (status === 'SIGNED') flow += '  style C fill:#10b981,stroke:#fff,stroke-width:2px\n'
  
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
          <span class="text-2xl">🏦</span>
          <span>Withdraw Queue</span>
        </h3>
        <p class="text-sm text-gray-500 mt-1">Pending and signed withdrawal requests</p>
      </div>
      <button @click="loadQueue" class="btn-primary flex items-center gap-2" :disabled="loading">
        <span v-if="loading" class="animate-spin">⟳</span>
        <span v-else>↻</span>
        {{ loading ? 'Loading...' : 'Refresh' }}
      </button>
    </div>

    <!-- Error State -->
    <div v-if="error" class="bg-red-500/10 border border-red-500/30 rounded-lg p-4 mb-4 text-red-400">
      {{ error }}
    </div>

    <!-- Empty State -->
    <div v-else-if="queue.length === 0 && !loading" class="text-center py-16">
      <div class="text-5xl mb-4 opacity-50">📭</div>
      <p class="text-gray-400">No pending withdraw requests</p>
    </div>

    <!-- Data Table -->
    <div v-else class="overflow-x-auto rounded-lg border border-white/5">
      <table class="data-table">
        <thead class="bg-black/30">
          <tr>
            <th class="w-32">ID</th>
            <th>Chain / Asset</th>
            <th>Destination</th>
            <th class="text-right">Amount</th>
            <th class="text-center">Vault</th>
            <th class="text-center">Status</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="item in queue" :key="item.withdraw_id" class="cursor-pointer" @click="openFlow(item)">
            <td>
              <code class="text-xs text-indigo-400 bg-indigo-500/10 px-2 py-1 rounded">
                {{ item.withdraw_id.substring(0, 10) }}...
              </code>
            </td>
            <td>
              <div class="flex items-center gap-2">
                <span :class="['chain-tag', getChainClass(item.chain)]">{{ item.chain }}</span>
                <span class="text-white font-medium">{{ item.asset }}</span>
              </div>
            </td>
            <td>
              <code class="text-xs text-gray-400 font-mono">
                {{ item.to.length > 20 ? item.to.substring(0, 10) + '...' + item.to.slice(-8) : item.to }}
              </code>
            </td>
            <td class="text-right">
              <span class="text-amber-400 font-bold">{{ formatAmount(item.amount) }}</span>
            </td>
            <td class="text-center">
              <span class="text-xs text-gray-500">#{{ item.vault_id }}</span>
            </td>
            <td class="text-center">
              <span :class="['status-badge', item.status.toLowerCase()]">{{ item.status }}</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Summary -->
    <div v-if="queue.length > 0" class="mt-4 pt-4 border-t border-white/5 flex justify-between text-sm text-gray-500">
      <span>Total: {{ queue.length }} requests</span>
      <span>Signed: {{ queue.filter(q => q.status === 'SIGNED').length }}</span>
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
  background: linear-gradient(135deg, #6366f1 0%, #8b5cf6 100%);
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
  box-shadow: 0 8px 20px rgba(99, 102, 241, 0.3);
}

.btn-primary:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.chain-tag {
  padding: 4px 10px;
  border-radius: 6px;
  font-size: 0.7rem;
  font-weight: 700;
  text-transform: uppercase;
}

.chain-btc { background: rgba(247, 147, 26, 0.2); color: #f7931a; }
.chain-eth { background: rgba(98, 126, 234, 0.2); color: #627eea; }
.chain-tron { background: rgba(235, 0, 41, 0.2); color: #eb0029; }
.chain-default { background: rgba(156, 163, 175, 0.2); color: #9ca3af; }

.animate-spin {
  animation: spin 1s linear infinite;
}

@keyframes spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}
</style>
