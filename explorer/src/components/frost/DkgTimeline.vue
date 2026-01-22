<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { fetchFrostDKGSessions } from '../../api'
import ProtocolModal from './ProtocolModal.vue'

const props = defineProps<{
  node: string
}>()

const sessions = ref<any[]>([])
const loading = ref(false)
const error = ref('')

const loadSessions = async () => {
  loading.value = true
  error.value = ''
  try {
    sessions.value = await fetchFrostDKGSessions(props.node)
  } catch (e: any) {
    error.value = e.message || 'Failed to load'
    console.error('Failed to load DKG sessions', e)
  } finally {
    loading.value = false
  }
}

watch(() => props.node, loadSessions)
onMounted(loadSessions)

const dkgPhases = [
  { key: 'COMMITTING', label: 'Commit', icon: '📝' },
  { key: 'SHARING', label: 'Share', icon: '🔗' },
  { key: 'RESOLVING', label: 'Resolve', icon: '⚖️' },
  { key: 'KEY_READY', label: 'Ready', icon: '🔐' },
]

function getPhaseIndex(status: string): number {
  const idx = dkgPhases.findIndex(p => p.key === status)
  return idx >= 0 ? idx : -1
}

function getProgress(status: string): number {
  const map: Record<string, number> = {
    'NOT_STARTED': 0,
    'COMMITTING': 25,
    'SHARING': 50,
    'RESOLVING': 75,
    'KEY_READY': 100
  }
  return map[status] || 0
}

// Modal state
const modalVisible = ref(false)
const modalTitle = ref('')
const mermaidDefinition = ref('')

const openFlow = (session: any) => {
  modalTitle.value = `DKG Flow: ${session.chain} Vault #${session.vault_id}`
  
  const status = session.dkg_status
  
  let flow = 'graph TD\n'
  flow += '  A[COMMITTING] -->|All Committed| B[SHARING]\n'
  flow += '  B -->|Shares Distributed| C[RESOLVING]\n'
  flow += '  C -->|No Disputes| D[KEY_READY]\n'
  flow += '  D -->|Verification| E((VAULT ACTIVE))\n'
  
  // Style current state
  if (status === 'COMMITTING') flow += '  style A fill:#6366f1,stroke:#fff,stroke-width:2px\n'
  if (status === 'SHARING') flow += '  style B fill:#8b5cf6,stroke:#fff,stroke-width:2px\n'
  if (status === 'RESOLVING') flow += '  style C fill:#f59e0b,stroke:#fff,stroke-width:2px\n'
  if (status === 'KEY_READY') {
    flow += '  style D fill:#10b981,stroke:#fff,stroke-width:2px\n'
    if (session.validation_status === 'PASSED' || session.lifecycle === 'ACTIVE') {
      flow += '  style E fill:#059669,stroke:#fff,stroke-width:2px\n'
    }
  }
  
  mermaidDefinition.value = flow
  modalVisible.value = true
}
const copyToClipboard = (text: string) => {
  navigator.clipboard.writeText(text)
    .then(() => alert('Public Key copied to clipboard!'))
    .catch(err => console.error('Failed to copy', err))
}
</script>

<template>
  <div class="glass-panel p-6">
    <!-- Header -->
    <div class="flex flex-col sm:flex-row justify-between items-start sm:items-center gap-4 mb-6">
      <div>
        <h3 class="text-xl font-bold text-white flex items-center gap-2">
          <span class="text-2xl">🔐</span>
          <span>DKG Sessions</span>
        </h3>
        <p class="text-sm text-gray-500 mt-1">Distributed Key Generation timeline</p>
      </div>
      <button @click="loadSessions" class="btn-primary flex items-center gap-2" :disabled="loading">
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
    <div v-else-if="sessions.length === 0 && !loading" class="text-center py-16">
      <div class="text-5xl mb-4 opacity-50">🔑</div>
      <p class="text-gray-400">No active DKG sessions found</p>
    </div>

    <!-- Session Cards -->
    <div v-else class="space-y-4">
      <div v-for="s in sessions" :key="s.dkg_session_id" class="session-card cursor-pointer" @click="openFlow(s)">
        <!-- Card Header -->
        <div class="flex flex-wrap justify-between items-center gap-4 mb-6">
          <div class="flex items-center gap-3">
            <div class="vault-icon">
              <span class="text-2xl">🏛️</span>
            </div>
            <div>
              <div class="flex items-center gap-2">
                <span :class="['chain-tag', s.chain.toLowerCase()]">{{ s.chain }}</span>
                <span class="text-white font-bold">Vault #{{ s.vault_id }}</span>
              </div>
              <div class="text-xs text-gray-500 mt-1">Epoch {{ s.epoch_id }}</div>
            </div>
          </div>
          <span :class="['status-badge', s.dkg_status.toLowerCase()]">{{ s.dkg_status.replace('_', ' ') }}</span>
        </div>

        <!-- Progress Timeline -->
        <div class="timeline-container mb-6">
          <div class="timeline-track">
            <div class="timeline-progress" :style="{ width: getProgress(s.dkg_status) + '%' }"></div>
          </div>
          <div class="timeline-phases">
            <div v-for="(phase, i) in dkgPhases" :key="phase.key"
                 :class="['phase-item', { active: getPhaseIndex(s.dkg_status) >= i, current: s.dkg_status === phase.key }]">
              <div class="phase-dot">
                <span v-if="getPhaseIndex(s.dkg_status) >= i" class="phase-icon">{{ phase.icon }}</span>
              </div>
              <span class="phase-label">{{ phase.label }}</span>
            </div>
          </div>
        </div>

        <!-- Details Grid -->
        <div class="grid grid-cols-2 lg:grid-cols-4 gap-4">
          <div class="detail-box">
            <span class="detail-label">Threshold</span>
            <span class="detail-value">
              <span class="text-purple-400 font-bold">{{ s.dkg_threshold_t }}</span>
              <span class="text-gray-500"> of </span>
              <span class="text-white">{{ s.dkg_n }}</span>
            </span>
          </div>
          <div class="detail-box">
            <span class="detail-label">Commit Deadline</span>
            <span class="detail-value text-amber-400">#{{ s.dkg_commit_deadline }}</span>
          </div>
          <div class="detail-box">
            <span class="detail-label">Lifecycle</span>
            <span :class="['detail-value', s.lifecycle === 'ACTIVE' ? 'text-emerald-400' : 'text-gray-400']">
              {{ s.lifecycle || 'ACTIVE' }}
            </span>
          </div>
          <div class="detail-box">
            <span class="detail-label">Validation</span>
            <span :class="['detail-value', s.validation_status === 'PASSED' ? 'text-emerald-400' : 'text-gray-400']">
              {{ s.validation_status || 'PENDING' }}
            </span>
          </div>
        </div>
        
        <!-- Group PubKey (Display when ready) -->
        <div v-if="s.new_group_pubkey" class="mt-4 p-3 bg-emerald-500/10 border border-emerald-500/20 rounded-lg">
          <div class="flex justify-between items-center mb-1">
            <span class="text-[10px] text-emerald-500 font-bold uppercase tracking-wider">Generated Group Public Key</span>
            <span class="text-[10px] text-gray-500 uppercase font-mono">{{ s.sign_algo }}</span>
          </div>
          <div class="flex items-center gap-2">
            <code class="text-xs text-emerald-400 break-all font-mono leading-relaxed bg-black/40 p-2 rounded block w-full border border-emerald-500/10">
              {{ s.new_group_pubkey }}
            </code>
            <button @click.stop="copyToClipboard(s.new_group_pubkey)" class="p-2 hover:bg-emerald-500/20 rounded text-emerald-500 transition-colors shadow-sm" title="Copy PubKey">
              📋
            </button>
          </div>
        </div>

        <!-- Committee Info -->
        <div class="mt-4 pt-4 border-t border-white/5">
          <div class="text-xs text-gray-500 uppercase mb-2">Committee Members</div>
          <div class="flex flex-wrap gap-1">
            <span v-for="(member, i) in (s.new_committee_members || []).slice(0, 5)" :key="i"
                  class="member-tag">
              {{ member.substring(0, 8) }}...
            </span>
            <span v-if="(s.new_committee_members || []).length > 5" class="member-tag more">
              +{{ s.new_committee_members.length - 5 }} more
            </span>
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
  background: linear-gradient(135deg, #7c3aed 0%, #6366f1 100%);
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
  box-shadow: 0 8px 20px rgba(124, 58, 237, 0.3);
}

.btn-primary:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.session-card {
  background: rgba(0, 0, 0, 0.3);
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 16px;
  padding: 24px;
  transition: all 0.2s;
}

.session-card:hover {
  border-color: rgba(124, 58, 237, 0.3);
}

.vault-icon {
  width: 48px;
  height: 48px;
  background: linear-gradient(135deg, rgba(124, 58, 237, 0.2), rgba(99, 102, 241, 0.2));
  border-radius: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
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

.status-badge {
  padding: 6px 12px;
  border-radius: 8px;
  font-size: 0.75rem;
  font-weight: 700;
  text-transform: uppercase;
}

.status-badge.committing { background: rgba(99, 102, 241, 0.2); color: #818cf8; }
.status-badge.sharing { background: rgba(139, 92, 246, 0.2); color: #a78bfa; }
.status-badge.resolving { background: rgba(245, 158, 11, 0.2); color: #fbbf24; }
.status-badge.key_ready { background: rgba(16, 185, 129, 0.2); color: #34d399; }

.timeline-container {
  position: relative;
  padding: 16px 0;
}

.timeline-track {
  height: 6px;
  background: #1f2937;
  border-radius: 3px;
  overflow: hidden;
}

.timeline-progress {
  height: 100%;
  background: linear-gradient(90deg, #7c3aed, #6366f1, #8b5cf6);
  transition: width 0.5s ease;
  box-shadow: 0 0 15px rgba(124, 58, 237, 0.5);
}

.timeline-phases {
  display: flex;
  justify-content: space-between;
  margin-top: 12px;
}

.phase-item {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
}

.phase-dot {
  width: 32px;
  height: 32px;
  border-radius: 50%;
  background: #1f2937;
  border: 2px solid #374151;
  display: flex;
  align-items: center;
  justify-content: center;
  transition: all 0.3s;
}

.phase-item.active .phase-dot {
  background: rgba(124, 58, 237, 0.2);
  border-color: #7c3aed;
}

.phase-item.current .phase-dot {
  box-shadow: 0 0 20px rgba(124, 58, 237, 0.6);
  animation: pulse 2s infinite;
}

.phase-icon {
  font-size: 14px;
}

.phase-label {
  font-size: 0.7rem;
  color: #6b7280;
  text-transform: uppercase;
  font-weight: 500;
}

.phase-item.active .phase-label {
  color: #a78bfa;
}

.detail-box {
  background: rgba(0, 0, 0, 0.2);
  border-radius: 10px;
  padding: 12px;
}

.detail-label {
  display: block;
  font-size: 0.65rem;
  color: #6b7280;
  text-transform: uppercase;
  margin-bottom: 4px;
}

.detail-value {
  font-size: 0.875rem;
}

.member-tag {
  background: rgba(99, 102, 241, 0.1);
  color: #818cf8;
  padding: 4px 8px;
  border-radius: 4px;
  font-size: 0.7rem;
  font-family: monospace;
}

.member-tag.more {
  background: rgba(107, 114, 128, 0.2);
  color: #9ca3af;
}

.animate-spin {
  animation: spin 1s linear infinite;
}

@keyframes spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}

@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.7; }
}
</style>
