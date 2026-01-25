<script setup lang="ts">
import { ref, onMounted, watch, computed } from 'vue'
import mermaid from 'mermaid'
import { fetchWitnessRequests } from '../../api'
import type { WitnessRequest } from '../../types'
import ProtocolModal from './ProtocolModal.vue'

const props = defineProps<{
  node: string
}>()

const emit = defineEmits(['select-tx'])

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
  const num = parseFloat(amount)
  return num.toLocaleString(undefined, { minimumFractionDigits: 0, maximumFractionDigits: 4 })
}

function getStatusStr(status: any): string {
  if (!status) return 'UNKNOWN'
  return String(status).toUpperCase()
}

function formatTime(ts: number | undefined) {
  if (!ts) return 'N/A'
  return new Date(ts * 1000).toLocaleString()
}

function truncate(str: string | undefined, start = 8, end = 8) {
  if (!str) return 'N/A'
  if (str.length <= start + end) return str
  return `${str.substring(0, start)}...${str.slice(-end)}`
}

function getProgress(status: any) {
  const s = getStatusStr(status)
  if (s.includes('FINALIZED')) return { percent: 100, color: 'linear-gradient(90deg, #10b981, #34d399)' }
  if (s.includes('FAILED') || s.includes('REJECTED')) return { percent: 100, color: 'linear-gradient(90deg, #ef4444, #f87171)' }
  if (s.includes('VOTING')) return { percent: 60, color: 'linear-gradient(90deg, #6366f1, #818cf8)' }
  if (s.includes('PENDING')) return { percent: 20, color: 'linear-gradient(90deg, #f59e0b, #fbbf24)' }
  return { percent: 0, color: '#334155' }
}

const stats = computed(() => ({
  total: requests.value.length,
  pending: requests.value.filter(r => getStatusStr(r.status).includes('PENDING')).length,
  voting: requests.value.filter(r => getStatusStr(r.status).includes('VOTING')).length,
  finalized: requests.value.filter(r => getStatusStr(r.status).includes('FINALIZED')).length,
}))

const expandedRequests = ref<Set<string>>(new Set())

const toggleTrace = (requestId: string) => {
  if (expandedRequests.value.has(requestId)) {
    expandedRequests.value.delete(requestId)
  } else {
    expandedRequests.value.add(requestId)
  }
}

const modalVisible = ref(false)
const modalTitle = ref('')
const mermaidDefinition = ref('')
const selectedRequest = ref<WitnessRequest | null>(null)

const getMermaidDefinition = (req: WitnessRequest) => {
  const status = getStatusStr(req.status)
  const isFinalized = status.includes('FINALIZED')
  const isFailed = status.includes('FAILED') || status.includes('REJECTED')
  
  const pass = req.pass_count || 0
  const fail = req.fail_count || 0
  
  let flow = 'graph LR\n'
  
  flow += '  A[Deposit Detected] --> B[Witness Voting]\n'
  flow += `  B --> C{${pass} Pass / ${fail} Fail}\n`
  
  // Progress Logic Colors
  const doneColor = '#10b981'
  const todoColor = '#000000'
  const isFinished = isFinalized || isFailed

  if (isFinalized) {
    flow += '  C -->|Consensus| D[Finalized]\n'
    flow += `  style D fill:${doneColor},color:#fff\n`
  } else if (isFailed) {
    flow += '  C -->|Rejected| E[Rejected]\n'
    flow += `  style E fill:${doneColor},color:#fff\n`
  } else {
    flow += '  C -.->|In Progress| F[Verifying]\n'
    flow += `  style F fill:${todoColor},color:#fff\n`
  }

  // Node A is always completed if we see the request
  flow += `  style A fill:${doneColor},color:#fff\n`
  
  // B and C are completed only if the process is finalized or failed
  flow += `  style B fill:${isFinished ? doneColor : todoColor},color:#fff\n`
  flow += `  style C fill:${isFinished ? doneColor : todoColor},color:#fff\n`

  // Base styles for font
  flow += '  classDef default font-family:Outfit,font-size:12px\n'
  
  return flow
}

const openFlow = (req: WitnessRequest) => {
  modalTitle.value = `Witness Flow: ${req.request_id.substring(0, 12)}`
  mermaidDefinition.value = getMermaidDefinition(req)
  selectedRequest.value = req
  modalVisible.value = true
}

// For embedded mermaid
const renderedSvgs = ref<Record<string, string>>({})

const renderEmbed = async (req: WitnessRequest) => {
  const rid = req.request_id || ''
  const id = `mermaid-embed-${rid.replace(/[^a-zA-Z0-9]/g, '')}`
  const def = getMermaidDefinition(req)
  try {
    const { svg } = await mermaid.render(id, def)
    renderedSvgs.value[rid] = svg
  } catch (e) {
    console.error('Mermaid render failed', e)
    renderedSvgs.value[rid] = '<p class="render-error">Diagram render failed</p>'
  }
}

// Re-render when expanded list changes or when request data updates
watch([() => Array.from(expandedRequests.value), () => requests.value], async ([newExpanded, newReqs]) => {
  // Update embedded diagrams
  for (const rid of newExpanded) {
    const req = newReqs.find(r => r.request_id === rid)
    if (req) {
      await renderEmbed(req)
    }
  }

  // Update modal diagram if open
  if (selectedRequest.value && modalVisible.value) {
    const currentId = selectedRequest.value.request_id
    const updated = newReqs.find(r => r.request_id === currentId)
    if (updated) {
      selectedRequest.value = updated
      mermaidDefinition.value = getMermaidDefinition(updated)
    }
  }
}, { deep: true })

onMounted(() => {
  mermaid.initialize({
    startOnLoad: false,
    theme: 'dark',
    securityLevel: 'loose',
    fontFamily: 'Outfit, sans-serif'
  })
})

const handleSelectTx = (txId: string) => {
  emit('select-tx', txId)
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
        <span>{{ loading ? 'Syncing...' : 'Refresh' }}</span>
      </button>
    </header>

    <!-- Stats Matrix -->
    <div v-if="requests.length > 0" class="stats-matrix">
      <div class="matrix-card">
        <span class="matrix-label">Total</span>
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

    <!-- Main List -->
    <div v-if="requests.length > 0" class="requests-list">
      <div v-for="req in requests" :key="req.request_id || Math.random()" 
           :class="['request-card', { expanded: expandedRequests.has(req.request_id || '') }]">
        
        <div class="card-main-content" @click="toggleTrace(req.request_id || '')">
          <!-- Row 1: ID & Status & Meta -->
          <div class="card-top-row">
            <div class="id-badge-group">
              <span class="req-id-short">#{{ (req.request_id || '').substring(0, 10) }}</span>
              <span v-if="req.vault_id !== undefined" class="meta-label">Vault {{ req.vault_id }}</span>
              <span v-if="req.create_height" class="meta-label height">H:{{ req.create_height }}</span>
              <div class="flow-entry-link" @click.stop="openFlow(req)">
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M2 3h6a4 4 0 0 1 4 4v14a3 3 0 0 0-3-3H2z"/><path d="M22 3h-6a4 4 0 0 0-4 4v14a3 3 0 0 1 3-3h7z"/></svg>
                <span>Flow</span>
              </div>
            </div>
            
            <div class="progress-cell">
              <div class="progress-info">
                <span class="progress-status">{{ getStatusStr(req.status).replace(/_/g, ' ') }}</span>
                <span class="progress-percent">{{ getProgress(req.status).percent }}%</span>
              </div>
              <div class="progress-track">
                <div class="progress-fill" 
                     :style="{ 
                        width: getProgress(req.status).percent + '%',
                        background: getProgress(req.status).color 
                     }">
                  <div class="progress-glow" :style="{ background: getProgress(req.status).color }"></div>
                </div>
              </div>
            </div>
          </div>

          <!-- Row 2: Asset Movement -->
          <div class="asset-flow-row">
            <div class="chain-display">
              <div :class="['chain-icon', (req.native_chain || '').toLowerCase()]">
                {{ (req.native_chain || '?').charAt(0).toUpperCase() }}
              </div>
              <div class="chain-detail">
                <div class="chain-name">{{ req.native_chain || 'Protocol' }}</div>
                <div class="asset-symbol">{{ req.token_address || 'Token' }}</div>
              </div>
            </div>

            <div class="flow-arrow">
              <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="5" y1="12" x2="19" y2="12"/><polyline points="12 5 19 12 12 19"/></svg>
            </div>

            <div class="amount-display">
              <div class="amount-val">{{ formatAmount(req.amount || '0') }}</div>
              <div class="recipient-addr">{{ truncate(req.receiver_address) }}</div>
            </div>
          </div>

          <!-- Interaction Footer -->
          <div class="card-action-bar">
            <div class="vote-summary">
              <div class="v-dot pass"></div>
              <span>{{ req.pass_count || 0 }} Pass</span>
              <div class="v-sep"></div>
              <div class="v-dot fail"></div>
              <span>{{ req.fail_count || 0 }} Fail</span>
              <div class="v-sep" v-if="req.abstain_count"></div>
              <div class="v-dot abstain" v-if="req.abstain_count"></div>
              <span v-if="req.abstain_count">{{ req.abstain_count }} Abstain</span>
            </div>
            <div class="details-toggle">
              Trace Breakdown
              <svg :class="{ 'rotated': expandedRequests.has(req.request_id || '') }" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="m9 18 6-6-6-6"/></svg>
            </div>
          </div>
        </div>

        <!-- Expanded Section -->
        <Transition name="expand">
          <div v-if="expandedRequests.has(req.request_id || '')" class="details-pane">
            <!-- Embedded Flow Visual -->
            <div class="embedded-flow-section">
              <div class="timeline-header">Visual Process Diagram</div>
              <div class="flow-container-inner" v-if="renderedSvgs[req.request_id]" v-html="renderedSvgs[req.request_id]"></div>
              <div class="flow-loading-box" v-else>
                <div class="spinner-mini"></div>
                <span>Generating diagram...</span>
              </div>
            </div>

            <!-- Full Metadata Grid -->
            <div class="meta-explorer">
              <div class="meta-item full">
                <span class="m-label">Request ID</span>
                <span class="m-value mono highlight">{{ req.request_id }}</span>
              </div>
              <div class="meta-item">
                <span class="m-label">Native TX Hash</span>
                <span class="m-value mono clickable" @click.stop>{{ req.native_tx_hash || 'None' }}</span>
              </div>
              <div class="meta-item">
                <span class="m-label">Requester</span>
                <span class="m-value mono">{{ req.requester_address || 'N/A' }}</span>
              </div>
              <div class="meta-item">
                <span class="m-label">Token Contract</span>
                <span class="m-value mono">{{ req.token_address || 'Native' }}</span>
              </div>
              <div class="meta-item">
                <span class="m-label">Target Receiver</span>
                <span class="m-value mono">{{ req.receiver_address }}</span>
              </div>
            </div>

            <!-- Detailed Vote Timeline -->
            <div class="vote-timeline">
              <div class="timeline-header">Verification Audit Trail</div>
              <div class="vote-entries">
                <!-- Original Intent -->
                <div class="vote-entry-item intent" @click="handleSelectTx(req.request_id)">
                  <div class="v-marker intent"></div>
                  <div class="v-body">
                    <div class="v-header">
                      <span class="v-type">DEPOSIT_INTENT</span>
                      <span class="v-time">Block H:{{ req.create_height }}</span>
                    </div>
                    <div class="v-footer">Triggered by deposit on {{ req.native_chain }}</div>
                  </div>
                </div>

                <!-- Individual Witness Votes -->
                <div v-for="vote in req.votes" :key="vote.tx_id" 
                     class="vote-entry-item" 
                     @click="handleSelectTx(vote.tx_id || '')">
                  <div :class="['v-marker', Number(vote.vote_type) === 1 ? 'pass' : 'fail']"></div>
                  <div class="v-body">
                    <div class="v-header">
                      <span class="v-type">{{ Number(vote.vote_type) === 1 ? 'APPROVE' : 'REJECT' }}</span>
                      <span class="v-time">{{ formatTime(vote.timestamp) }}</span>
                    </div>
                    <div class="v-footer">
                      Witness: <span class="mono">{{ truncate(vote.witness_address, 6, 6) }}</span>
                    </div>
                    <div class="v-tx-line">
                      TX: <span class="mono">{{ truncate(vote.tx_id, 10, 10) }}</span>
                    </div>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </Transition>
      </div>
    </div>

    <!-- Empty State -->
    <div v-else-if="!loading" class="empty-state">
      <div class="empty-icon">
        <svg xmlns="http://www.w3.org/2000/svg" width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2H2v10c0 5.5 4.5 10 10 10s10-4.5 10-10V2h-10z"/><path d="m9 12 2 2 4-4"/></svg>
      </div>
      <h3>No Active Flows</h3>
      <p>The witness gateway is currently idle. New recharge requests will appear here.</p>
    </div>

    <!-- Protocol Detail Modal -->
    <ProtocolModal 
      :show="modalVisible" 
      :title="modalTitle" 
      :definition="mermaidDefinition"
      :request="selectedRequest"
      @close="modalVisible = false"
      @select-tx="handleSelectTx"
    />
  </div>
</template>

<style scoped>
@import url('https://fonts.googleapis.com/css2?family=Outfit:wght@300;400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap');

.witness-dashboard {
  font-family: 'Outfit', sans-serif;
  color: #e2e8f0;
}

.header-section {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 32px;
}

.header-left { display: flex; align-items: center; gap: 16px; }

.icon-orb {
  width: 48px; height: 48px;
  background: rgba(45, 212, 191, 0.1);
  border: 1px solid rgba(45, 212, 191, 0.2);
  border-radius: 12px;
  display: flex; align-items: center; justify-content: center;
  color: #2dd4bf;
}

.premium-title { font-size: 1.25rem; font-weight: 700; margin: 0; color: #fff; }
.premium-subtitle { font-size: 0.8rem; color: #94a3b8; margin: 2px 0 0; }

.glass-btn {
  background: rgba(255, 255, 255, 0.05);
  border: 1px solid rgba(255, 255, 255, 0.1);
  color: #fff;
  padding: 8px 16px;
  border-radius: 10px;
  display: flex; align-items: center; gap: 8px;
  font-weight: 600; font-size: 0.85rem;
  cursor: pointer; transition: all 0.2s;
}

.glass-btn:hover { background: rgba(255, 255, 255, 0.1); transform: translateY(-1px); }

/* Stats Matrix */
.stats-matrix {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 16px;
  margin-bottom: 32px;
}

.matrix-card {
  background: rgba(15, 23, 42, 0.4);
  border: 1px solid rgba(255, 255, 255, 0.05);
  padding: 16px;
  border-radius: 14px;
}

.matrix-label { font-size: 0.65rem; font-weight: 700; color: #64748b; text-transform: uppercase; letter-spacing: 0.05em; }
.matrix-value { font-size: 1.5rem; font-weight: 700; display: block; margin-top: 4px; color: #fff; }

.matrix-card.amber .matrix-value { color: #fbbf24; }
.matrix-card.indigo .matrix-value { color: #818cf8; }
.matrix-card.emerald .matrix-value { color: #34d399; }

/* Request List & Cards */
.requests-list { display: flex; flex-direction: column; gap: 16px; }

.request-card {
  background: rgba(30, 41, 59, 0.3);
  border: 1px solid rgba(255, 255, 255, 0.04);
  border-radius: 16px;
  overflow: hidden;
  transition: all 0.25s cubic-bezier(0.4, 0, 0.2, 1);
}

.request-card:hover { 
  background: rgba(30, 41, 59, 0.5); 
  border-color: rgba(99, 102, 241, 0.3);
}

.card-main-content { padding: 20px; cursor: pointer; }

/* Top Row Styles */
.card-top-row { display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px; }

.id-badge-group { display: flex; align-items: center; gap: 8px; }
.req-id-short { font-family: 'JetBrains Mono', monospace; font-size: 0.85rem; font-weight: 600; color: #818cf8; }
.meta-label { 
  font-size: 0.65rem; font-weight: 700; 
  background: rgba(255, 255, 255, 0.05); 
  padding: 2px 8px; border-radius: 4px; color: #94a3b8; 
}
.meta-label.height { background: rgba(52, 211, 153, 0.1); color: #34d399; }

/* Progress Bar Styles */
.progress-cell {
  width: 180px;
}

.progress-info {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 6px;
}

.progress-status {
  font-size: 0.65rem;
  font-weight: 800;
  color: #94a3b8;
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

.progress-percent {
  font-size: 0.7rem;
  font-weight: 700;
  color: #fff;
}

.progress-track {
  height: 6px;
  background: rgba(0, 0, 0, 0.4);
  border-radius: 99px;
  overflow: hidden;
  position: relative;
}

.progress-fill {
  height: 100%;
  border-radius: 99px;
  transition: width 1s cubic-bezier(0.4, 0, 0.2, 1);
  position: relative;
  overflow: hidden;
}

.progress-fill::after {
  content: '';
  position: absolute;
  top: 0; left: -100%; width: 100%; height: 100%;
  background: linear-gradient(90deg, transparent, rgba(255,255,255,0.2), transparent);
  animation: shimmer 2s infinite;
}

@keyframes shimmer {
  0% { left: -100%; }
  100% { left: 100%; }
}

.progress-glow {
  position: absolute;
  top: 0; right: 0; bottom: 0;
  width: 20px;
  filter: blur(8px);
  opacity: 0.6;
}
@keyframes pulse { 0% { opacity: 0.4; } 50% { opacity: 1; } 100% { opacity: 0.4; } }

/* Asset Flow Row */
.asset-flow-row {
  display: flex; align-items: center; justify-content: space-between;
  background: rgba(0, 0, 0, 0.2);
  padding: 16px; border-radius: 12px;
  margin-bottom: 16px;
}

.chain-display { display: flex; align-items: center; gap: 12px; }
.chain-icon {
  width: 32px; height: 32px; border-radius: 8px;
  display: flex; align-items: center; justify-content: center;
  font-weight: 800; font-size: 1rem;
}
.chain-icon.btc { background: #f7931a22; color: #f7931a; }
.chain-icon.eth { background: #627eea22; color: #627eea; }

.chain-name { font-size: 0.9rem; font-weight: 700; color: #fff; }
.asset-symbol { font-size: 0.7rem; color: #64748b; font-weight: 600; }

.flow-arrow { color: #334155; }

.amount-display { text-align: right; }
.amount-val { font-family: 'JetBrains Mono', monospace; font-size: 1.1rem; font-weight: 700; color: #fbbf24; }
.recipient-addr { font-size: 0.7rem; color: #64748b; font-family: 'JetBrains Mono', monospace; margin-top: 2px; }

/* Action Bar */
.card-action-bar { display: flex; justify-content: space-between; align-items: center; }
.vote-summary { display: flex; align-items: center; gap: 8px; font-size: 0.75rem; color: #94a3b8; font-weight: 600; }
.v-dot { width: 6px; height: 6px; border-radius: 50%; }
.v-dot.pass { background: #10b981; box-shadow: 0 0 8px #10b98166; }
.v-dot.fail { background: #ef4444; box-shadow: 0 0 8px #ef444466; }
.v-dot.abstain { background: #64748b; }
.v-sep { width: 1px; height: 10px; background: rgba(255,255,255,0.05); }

.details-toggle {
  font-size: 0.75rem; font-weight: 700; color: #818cf8;
  display: flex; align-items: center; gap: 6px;
}
.details-toggle svg { transition: transform 0.25s; }
.details-toggle svg.rotated { transform: rotate(90deg); }

/* Expanded Details */
.details-pane {
  background: rgba(0, 0, 0, 0.25);
  border-top: 1px solid rgba(255, 255, 255, 0.03);
  padding: 24px;
}

.meta-explorer {
  display: grid; grid-template-columns: 1fr 1fr; gap: 16px;
  margin-bottom: 24px;
}

.meta-item { display: flex; flex-direction: column; gap: 4px; }
.meta-item.full { grid-column: span 2; }
.m-label { font-size: 0.6rem; font-weight: 700; color: #475569; text-transform: uppercase; letter-spacing: 0.05em; }
.m-value { font-size: 0.75rem; font-weight: 500; color: #94a3b8; }
.m-value.mono { font-family: 'JetBrains Mono', monospace; }
.m-value.highlight { color: #818cf8; }
.m-value.clickable { cursor: pointer; color: #38bdf8; text-decoration: underline; text-underline-offset: 4px; }
.m-value.clickable:hover { color: #7dd3fc; }

/* Vote Timeline */
.vote-timeline { margin-top: 24px; }
.timeline-header { font-size: 0.7rem; font-weight: 800; color: #475569; text-transform: uppercase; margin-bottom: 16px; }

.vote-entries { display: flex; flex-direction: column; gap: 12px; }

.vote-entry-item {
  display: flex; gap: 16px; padding: 12px;
  background: rgba(255, 255, 255, 0.02);
  border: 1px solid rgba(255, 255, 255, 0.03);
  border-radius: 10px;
  cursor: pointer; transition: all 0.2s;
}
.vote-entry-item:hover { 
  background: rgba(255, 255, 255, 0.04);
  transform: translateX(4px);
  border-color: rgba(255,255,255,0.08); 
}

.v-marker { width: 4px; border-radius: 2px; flex-shrink: 0; }
.v-marker.intent { background: #818cf8; }
.v-marker.pass { background: #10b981; }
.v-marker.fail { background: #ef4444; }

.v-body { flex: 1; display: flex; flex-direction: column; gap: 4px; }
.v-header { display: flex; justify-content: space-between; align-items: center; }
.v-type { font-size: 0.7rem; font-weight: 800; color: #fff; }
.v-time { font-size: 0.65rem; color: #475569; font-weight: 600; }
.v-footer { font-size: 0.7rem; color: #94a3b8; }
.v-tx-line { font-size: 0.65rem; color: #475569; font-style: italic; }

.embedded-flow-section {
  margin-top: 32px;
  background: rgba(0, 0, 0, 0.2);
  border-radius: 12px;
  padding: 20px;
  border: 1px solid rgba(255, 255, 255, 0.02);
}

.flow-container-inner {
  width: 100%;
  display: flex;
  justify-content: center;
  padding-top: 10px;
}

:deep(.flow-container-inner svg) {
  max-width: 100%;
  height: auto;
}

.flow-loading-box {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
  padding: 40px 0;
  color: #475569;
  font-size: 0.8rem;
}

.spinner-mini {
  width: 16px;
  height: 16px;
  border: 2px solid rgba(255, 255, 255, 0.1);
  border-top-color: #6366f1;
  border-radius: 50%;
  animation: spin 1s linear infinite;
}

.flow-entry-link {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 0.65rem;
  font-weight: 800;
  color: #6366f1;
  text-transform: uppercase;
  background: rgba(99, 102, 241, 0.1);
  padding: 2px 8px;
  border-radius: 4px;
  cursor: pointer;
  transition: all 0.2s;
  border: 1px solid transparent;
}

.flow-entry-link:hover {
  background: #6366f1;
  color: #fff;
}

.mono { font-family: 'JetBrains Mono', monospace; }

/* Transitions */
.expand-enter-active, .expand-leave-active { transition: all 0.35s cubic-bezier(0.4, 0, 0.2, 1); max-height: 1000px; overflow: hidden; }
.expand-enter-from, .expand-leave-to { max-height: 0; opacity: 0; }

.empty-state { text-align: center; padding: 80px 20px; color: #64748b; }
.empty-icon { margin-bottom: 16px; opacity: 0.5; }

.spin { animation: spin 1s linear infinite; }
@keyframes spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }

@media (max-width: 768px) {
  .stats-matrix { grid-template-columns: 1fr 1fr; }
  .meta-explorer { grid-template-columns: 1fr; }
  .meta-item.full { grid-column: auto; }
}
</style>
