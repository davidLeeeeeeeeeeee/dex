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

const flowSteps = [
  { label: 'Pending', icon: '<path d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z"/>' },
  { label: 'Voting', icon: '<path d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z"/>' },
  { label: 'Review', icon: '<path d="M7 8h10M7 12h4m1 8l-4-4H5a2 2 0 01-2-2V6a2 2 0 012-2h14a2 2 0 012 2v8a2 2 0 01-2 2h-3l-4 4z"/>' },
  { label: 'Done', icon: '<path d="M5 13l4 4L19 7"/>' }
]

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
  modalTitle.value = `Witness Flow: ${(req.request_id || '').substring(0, 12)}...`
  const s = getStatusStr(req.status)
  const step = getStatusStep(req.status)
  const isFinalized = s.includes('FINALIZED')
  const isRejected = s.includes('REJECTED')
  let flow = 'graph LR\n'
  flow += '  A((Native Tx)) -->|Detect| B[Pending]\n'
  flow += '  B -->|Witness| C{Voting}\n'
  flow += '  C -->|Consensus| D[Challenge Period]\n'
  flow += '  D -->|Timeout| E[Finalized]\n'
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
  <div class="witness-dashboard">
    <!-- Header Section -->
    <header class="header-section">
      <div class="header-left">
        <div class="icon-orb teal">
          <svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M2 12s3-7 10-7 10 7 10 7-3 7-10 7-10-7-10-7Z"/><circle cx="12" cy="12" r="3"/></svg>
        </div>
        <div class="title-meta">
          <h3 class="premium-title">Witness Flow</h3>
          <p class="premium-subtitle">Gateway for cross-chain asset verification</p>
        </div>
      </div>
      <button @click="loadRequests" class="glass-btn primary" :disabled="loading">
        <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" :class="{ 'spin': loading }"><path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M3 21v-5h5"/></svg>
        <span>{{ loading ? 'Synchronizing...' : 'Refresh Status' }}</span>
      </button>
    </header>

    <!-- Stats Matrix -->
    <div v-if="requests.length > 0" class="stats-matrix">
      <div class="matrix-card">
        <span class="matrix-label">Active Requests</span>
        <span class="matrix-value">{{ stats.total }}</span>
      </div>
      <div class="matrix-card amber">
        <span class="matrix-label">Pending</span>
        <span class="matrix-value">{{ stats.pending }}</span>
      </div>
      <div class="matrix-card indigo">
        <span class="matrix-label">Voting</span>
        <span class="matrix-value">{{ stats.voting }}</span>
      </div>
      <div class="matrix-card emerald">
        <span class="matrix-label">Finalized</span>
        <span class="matrix-value">{{ stats.finalized }}</span>
      </div>
    </div>

    <!-- Error Alert -->
    <div v-if="error" class="error-alert">
      <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" x2="12" y1="8" y2="12"/><line x1="12" x2="12.01" y1="16" y2="16"/></svg>
      <span>{{ error }}</span>
    </div>

    <!-- Main Views -->
    <div v-if="requests.length > 0" class="requests-grid">
      <div v-for="req in requests" :key="req.request_id || Math.random()" class="request-card group" @click="openFlow(req)">
        <div class="card-glow"></div>
        
        <!-- ID Stripe -->
        <div class="request-header">
          <div class="req-id">
            <span class="hashtag">#</span>
            <code class="mono">{{ (req.request_id || '').substring(0, 12) }}</code>
          </div>
          <div :class="['status-pill-lite', getStatusStr(req.status).toLowerCase()]">
            {{ getStatusStr(req.status).replace(/_/g, ' ') }}
          </div>
        </div>

        <div class="card-divider"></div>

        <!-- Chain Info -->
        <div class="chain-info-block">
          <div :class="['chain-badge-logo', (req.native_chain || '').toLowerCase()]">
            {{ (req.native_chain || '!!').charAt(0) }}
          </div>
          <div class="chain-meta">
            <span class="chain-name">{{ req.native_chain || 'Protocol' }}</span>
            <span class="recipient mono">{{ (req.receiver_address || '').substring(0, 8) }}...{{ (req.receiver_address || '').slice(-6) }}</span>
          </div>
          <div class="value-block">
            <span class="val-num">{{ formatAmount(req.amount || '0') }}</span>
            <span class="val-asset">{{ req.native_chain }}</span>
          </div>
        </div>

        <!-- Flow Visualizer -->
        <div class="mini-flow">
          <div class="flow-rail">
            <div class="flow-rail-fill" :style="{ width: (getStatusStep(req.status) / 4 * 100) + '%' }"></div>
          </div>
          <div class="flow-steps">
            <div v-for="(step, i) in flowSteps" :key="i" 
                 :class="['flow-node-item', { active: getStatusStep(req.status) > i, current: getStatusStep(req.status) === i+1 }]">
              <div class="flow-node-circle">
                <svg width="6" height="6" viewBox="0 0 24 24" fill="currentColor" v-html="step.icon" />
              </div>
            </div>
          </div>
        </div>

        <!-- Interaction Area -->
        <div class="card-footer">
          <div class="vote-stats">
            <div class="v-pill pass">
              <svg xmlns="http://www.w3.org/2000/svg" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>
              <span>{{ req.pass_count || 0 }}</span>
            </div>
            <div class="v-pill fail">
              <svg xmlns="http://www.w3.org/2000/svg" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
              <span>{{ req.fail_count || 0 }}</span>
            </div>
          </div>
          <div class="inspect-btn">
            Detailed Trace
            <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12h14"/><path d="m12 5 7 7-7 7"/></svg>
          </div>
        </div>
      </div>
    </div>

    <!-- Empty State -->
    <div v-else-if="!loading" class="empty-state">
      <div class="empty-orb teal">
        <svg xmlns="http://www.w3.org/2000/svg" width="40" height="40" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2H2v10c0 5.5 4.5 10 10 10s10-4.5 10-10V2h-10z"/><path d="m9 12 2 2 4-4"/></svg>
      </div>
      <h3>Nothing to witness</h3>
      <p>Gateway is clear. No active recharge requests found in the mempool.</p>
    </div>

    <!-- Modals -->
    <ProtocolModal 
      :show="modalVisible" 
      :title="modalTitle" 
      :definition="mermaidDefinition"
      @close="modalVisible = false"
    />
  </div>
</template>

<style scoped>
@import url('https://fonts.googleapis.com/css2?family=Outfit:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap');

.witness-dashboard {
  font-family: 'Outfit', sans-serif;
  color: #fff;
  background: rgba(13, 17, 23, 0.4);
  border: 1px solid rgba(255, 255, 255, 0.05);
  border-radius: 24px;
  padding: 32px;
  backdrop-filter: blur(20px);
}

.header-section {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 40px;
}

.header-left { display: flex; align-items: center; gap: 20px; }

.icon-orb {
  width: 56px;
  height: 56px;
  background: linear-gradient(135deg, rgba(20, 184, 166, 0.2), rgba(16, 185, 129, 0.2));
  border: 1px solid rgba(20, 184, 166, 0.3);
  border-radius: 18px;
  display: flex; align-items: center; justify-content: center;
  color: #2dd4bf;
  box-shadow: 0 10px 30px rgba(20, 184, 166, 0.1);
}

.premium-title { font-size: 1.5rem; font-weight: 700; letter-spacing: -0.02em; margin: 0; }
.premium-subtitle { font-size: 0.85rem; color: #64748b; margin: 4px 0 0; }

.glass-btn {
  background: rgba(255, 255, 255, 0.05);
  border: 1px solid rgba(255, 255, 255, 0.1);
  color: #fff;
  padding: 10px 20px;
  border-radius: 12px;
  display: flex; align-items: center; gap: 10px;
  font-weight: 600;
  font-size: 0.9rem;
  transition: all 0.3s cubic-bezier(0.4, 0, 0.2, 1);
  cursor: pointer;
}

.glass-btn:hover:not(:disabled) {
  background: rgba(20, 184, 166, 0.1);
  border-color: #14b8a6;
  box-shadow: 0 0 20px rgba(20, 184, 166, 0.2);
  transform: translateY(-2px);
}

.stats-matrix {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 20px;
  margin-bottom: 40px;
}

.matrix-card {
  background: rgba(255, 255, 255, 0.02);
  border: 1px solid rgba(255, 255, 255, 0.04);
  padding: 20px;
  border-radius: 18px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.matrix-label { font-size: 0.7rem; font-weight: 700; color: #64748b; text-transform: uppercase; letter-spacing: 0.05em; }
.matrix-value { font-size: 1.8rem; font-weight: 800; }

.matrix-card.amber .matrix-value { color: #f59e0b; }
.matrix-card.indigo .matrix-value { color: #6366f1; }
.matrix-card.emerald .matrix-value { color: #10b981; }

.requests-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 24px; }

.request-card {
  background: rgba(255, 255, 255, 0.02);
  border: 1px solid rgba(255, 255, 255, 0.05);
  border-radius: 20px;
  padding: 24px;
  position: relative;
  overflow: hidden;
  cursor: pointer;
  transition: all 0.3s;
}

.request-card:hover {
  background: rgba(255, 255, 255, 0.04);
  border-color: rgba(255, 255, 255, 0.1);
  transform: translateY(-4px);
}

.card-glow {
  position: absolute;
  top: 0; right: 0;
  width: 100px; height: 100px;
  background: radial-gradient(circle, rgba(99, 102, 241, 0.05) 0%, transparent 70%);
  pointer-events: none;
}

.request-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px; }
.req-id { display: flex; align-items: center; gap: 6px; }
.hashtag { color: #6366f1; font-weight: 900; }
.mono { font-family: 'JetBrains Mono', monospace; font-size: 0.8rem; color: #94a3b8; }

.status-pill-lite {
  padding: 4px 12px;
  border-radius: 99px;
  background: rgba(255, 255, 255, 0.05);
  font-size: 0.65rem;
  font-weight: 800;
  text-transform: uppercase;
  color: #64748b;
}

.status-pill-lite.finalized { background: #10b98122; color: #10b981; }
.status-pill-lite.voting { background: #6366f122; color: #6366f1; }
.status-pill-lite.pending { background: #f59e0b22; color: #f59e0b; }

.card-divider { height: 1px; background: linear-gradient(90deg, rgba(255,255,255,0.05), transparent); margin-bottom: 20px; }

.chain-info-block { display: flex; align-items: center; gap: 16px; margin-bottom: 24px; }
.chain-badge-logo {
  width: 40px; height: 40px;
  background: #27272a;
  border-radius: 12px;
  display: flex; align-items: center; justify-content: center;
  font-weight: 900; font-size: 1.1rem;
}
.chain-badge-logo.btc { background: #f7931a22; color: #f7931a; }
.chain-badge-logo.eth { background: #627eea22; color: #627eea; }

.chain-meta { flex: 1; display: flex; flex-direction: column; }
.chain-name { font-weight: 700; color: #fff; }
.recipient { font-size: 0.7rem; color: #475569; }

.value-block { text-align: right; }
.val-num { display: block; font-weight: 800; color: #f59e0b; font-size: 1.1rem; font-family: 'JetBrains Mono', monospace; }
.val-asset { font-size: 0.65rem; font-weight: 700; color: #64748b; }

.mini-flow { margin-bottom: 24px; position: relative; }
.flow-rail { height: 4px; background: rgba(255,255,255,0.05); border-radius: 2px; }
.flow-rail-fill { height: 100%; background: linear-gradient(90deg, #6366f1, #10b981); border-radius: 2px; transition: width 1s; }
.flow-nodes { position: absolute; top: -6px; left: 0; width: 100%; display: flex; justify-content: space-between; }
.flow-node-circle {
  width: 16px; height: 16px; border-radius: 50%;
  background: #18181b; border: 2px solid #27272a;
  display: flex; align-items: center; justify-content: center;
  color: #3f3f46; transition: all 0.5s;
}
.flow-node-item.active .flow-node-circle { background: #10b981; border-color: #10b981; color: #fff; box-shadow: 0 0 10px #10b98144; }
.flow-node-item.current .flow-node-circle { background: #6366f1; border-color: #6366f1; color: #fff; box-shadow: 0 0 15px #6366f1; }

.card-footer { display: flex; justify-content: space-between; align-items: center; }
.vote-stats { display: flex; gap: 8px; }
.v-pill {
  padding: 4px 10px; border-radius: 8px; display: flex; align-items: center; gap: 6px;
  font-size: 0.7rem; font-weight: 800;
}
.v-pill.pass { background: rgba(16, 185, 129, 0.1); color: #10b981; }
.v-pill.fail { background: rgba(239, 68, 68, 0.1); color: #ef4444; }

.inspect-btn {
  display: flex; align-items: center; gap: 8px;
  font-size: 0.7rem; font-weight: 700; color: #6366f1;
  opacity: 0.5; transition: all 0.3s;
}
.request-card:hover .inspect-btn { opacity: 1; transform: translateX(-4px); }

.empty-state { text-align: center; padding: 100px 0; color: #64748b; }
.empty-orb {
  width: 80px; height: 80px; background: rgba(255, 255, 255, 0.02);
  border-radius: 50%; display: flex; align-items: center; justify-content: center;
  margin: 0 auto 24px; color: #1e293b;
}
.empty-orb.teal { color: #14b8a6; background: rgba(20, 184, 166, 0.05); }

.spin { animation: spin 1s linear infinite; }
@keyframes spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }

@media (max-width: 1024px) {
  .requests-grid { grid-template-columns: 1fr; }
  .stats-matrix { grid-template-columns: repeat(2, 1fr); }
}
</style>
