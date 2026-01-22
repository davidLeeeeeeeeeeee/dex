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
  { key: 'COMMITTING', label: 'Commit', color: '#6366f1' },
  { key: 'SHARING', label: 'Share', color: '#a855f7' },
  { key: 'RESOLVING', label: 'Resolve', color: '#f59e0b' },
  { key: 'KEY_READY', label: 'Ready', color: '#10b981' },
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
    .then(() => alert('Copied to clipboard!'))
    .catch(err => console.error('Failed to copy', err))
}

const expandedSessions = ref(new Set<string>())
const toggleExpand = (id: string) => {
  if (expandedSessions.value.has(id)) {
    expandedSessions.value.delete(id)
  } else {
    expandedSessions.value.add(id)
  }
}
</script>

<template>
  <div class="dkg-dashboard">
    <!-- Header Section -->
    <header class="header-section">
      <div class="header-left">
        <div class="icon-orb">
          <svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="header-icon"><rect width="18" height="11" x="3" y="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>
        </div>
        <div class="title-meta">
          <h3 class="premium-title">DKG Sessions</h3>
          <p class="premium-subtitle">Threshold Key Management & Distribution</p>
        </div>
      </div>
      <button @click="loadSessions" class="glass-btn" :disabled="loading">
        <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" :class="{ 'spin': loading }"><path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M3 21v-5h5"/></svg>
        <span>{{ loading ? 'Syncing...' : 'Sync Now' }}</span>
      </button>
    </header>

    <!-- Error Alert -->
    <div v-if="error" class="error-alert">
      <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="alert-icon"><circle cx="12" cy="12" r="10"/><line x1="12" x2="12" y1="8" y2="12"/><line x1="12" x2="12.01" y1="16" y2="16"/></svg>
      <span>{{ error }}</span>
    </div>

    <!-- Main List -->
    <div v-if="sessions.length > 0" class="session-list">
      <div v-for="s in sessions" :key="s.dkg_session_id" 
           :class="['premium-card', { expanded: expandedSessions.has(s.dkg_session_id) }]">
        
        <!-- Interactive Header -->
        <div class="card-top">
          <!-- Vault Info (Modal Trigger) -->
          <div class="vault-info-group" @click="openFlow(s)">
            <div class="vault-hero-icon" :style="{ '--col': s.chain === 'BTC' ? '#f7931a' : s.chain === 'ETH' ? '#627eea' : '#eb0029' }">
              <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 21h18"/><path d="M3 10h18"/><path d="m5 6 7-3 7 3"/><path d="M4 10v11"/><path d="M20 10v11"/><path d="M8 14v3"/><path d="M12 14v3"/><path d="M16 14v3"/></svg>
            </div>
            <div class="v-labels">
              <div class="top-row">
                <span :class="['chain-pill', s.chain.toLowerCase()]">{{ s.chain }}</span>
                <span class="vault-num">Vault #{{ s.vault_id }}</span>
                <div class="flow-link">
                  <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M2 3h6a4 4 0 0 1 4 4v14a3 3 0 0 0-3-3H2z"/><path d="M22 3h-6a4 4 0 0 0-4 4v14a3 3 0 0 1 3-3h7z"/></svg>
                  <span>Flow</span>
                </div>
              </div>
              <div class="bottom-row">
                <span class="metric">Epoch <b>{{ s.epoch_id }}</b></span>
                <span class="metric"><b>{{ s.dkg_threshold_t }}</b> / {{ s.dkg_n }} Members</span>
              </div>
            </div>
          </div>

          <!-- Status & Expansion -->
          <div class="status-action-group" @click="toggleExpand(s.dkg_session_id)">
            <div class="status-stack">
              <span :class="['status-glow-label', s.dkg_status.toLowerCase()]">{{ s.dkg_status.replace('_', ' ') }}</span>
              <div class="mini-progress-track">
                <div class="mini-progress-fill" :style="{ width: getProgress(s.dkg_status) + '%', backgroundColor: dkgPhases[getPhaseIndex(s.dkg_status)]?.color || '#ccc' }"></div>
              </div>
            </div>
            <div class="chevron-orb" :class="{ 'active': expandedSessions.has(s.dkg_session_id) }">
              <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m6 9 6 6 6-6"/></svg>
            </div>
          </div>
        </div>

        <!-- Detail Content -->
        <div v-if="expandedSessions.has(s.dkg_session_id)" class="expanded-pane animate-slide-down">
          <!-- Timeline -->
          <div class="modern-timeline">
            <div class="t-line">
              <div class="t-fill" :style="{ width: getProgress(s.dkg_status) + '%' }"></div>
            </div>
            <div class="t-steps">
              <div v-for="(phase, i) in dkgPhases" :key="phase.key" 
                   :class="['t-step', { 'done': getPhaseIndex(s.dkg_status) >= i, 'current': s.dkg_status === phase.key }]">
                <div class="t-circle">
                  <svg v-if="getPhaseIndex(s.dkg_status) > i" xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6L9 17l-5-5"/></svg>
                  <div v-else class="t-dot"></div>
                </div>
                <span class="t-label">{{ phase.label }}</span>
              </div>
            </div>
          </div>

          <!-- Info Grid -->
          <div class="info-grid">
            <div class="info-box">
              <div class="i-head"><svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"/></svg> Session ID</div>
              <div class="i-val mono">{{ s.dkg_session_id.substring(0, 16) }}...</div>
            </div>
            <div class="info-box">
              <div class="i-head"><svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="18" height="18" x="3" y="4" rx="2" ry="2"/><line x1="16" x2="16" y1="2" y2="6"/><line x1="8" x2="8" y1="2" y2="6"/><line x1="3" x2="21" y1="10" y2="10"/></svg> Commit Deadline</div>
              <div class="i-val warn">#{{ s.dkg_commit_deadline }}</div>
            </div>
            <div class="info-box">
              <div class="i-head"><svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 22c5.523 0 10-4.477 10-10S17.523 2 12 2 2 6.477 2 12s4.477 10 10 10z"/><path d="m9 12 2 2 4-4"/></svg> Lifecycle</div>
              <div :class="['i-val', s.lifecycle === 'ACTIVE' ? 'good' : '']">{{ s.lifecycle || 'PENDING' }}</div>
            </div>
            <div class="info-box">
              <div class="i-head"><svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2H2v10c0 5.523 4.477 10 10 10s10-4.477 10-10V2h-10z"/><path d="m9 12 2 2 4-4"/></svg> Validation</div>
              <div :class="['i-val', s.validation_status === 'PASSED' ? 'good' : '']">{{ s.validation_status || 'PENDING' }}</div>
            </div>
          </div>

          <!-- PubKey Strip -->
          <div v-if="s.new_group_pubkey" class="pubkey-strip">
            <div class="p-meta">
              <span class="p-tag">AGGREGATED PUBLIC KEY</span>
              <span class="p-algo">{{ s.sign_algo }}</span>
            </div>
            <div class="p-body">
              <code class="p-code">{{ s.new_group_pubkey }}</code>
              <button @click.stop="copyToClipboard(s.new_group_pubkey)" class="icon-btn" title="Copy Key">
                <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/></svg>
              </button>
            </div>
          </div>

          <!-- Committee Section -->
          <div class="committee-section">
            <h4 class="section-title">Voter Committee</h4>
            <div class="member-cloud">
              <div v-for="(member, i) in s.new_committee_members" :key="i" class="member-item group">
                <span class="m-idx">{{ Number(i) + 1 }}</span>
                <span class="m-addr">{{ member.substring(0, 10) }}...{{ member.substring(member.length-4) }}</span>
                <button @click.stop="copyToClipboard(member)" class="m-copy">
                  <svg xmlns="http://www.w3.org/2000/svg" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/></svg>
                </button>
                <div class="m-tooltip">{{ member }}</div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Empty State -->
    <div v-else-if="!loading" class="empty-state">
      <div class="empty-orb">
        <svg xmlns="http://www.w3.org/2000/svg" width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 6v12a3 3 0 1 0 3-3H6a3 3 0 1 0 3 3V6a3 3 0 1 0-3 3h12a3 3 0 1 0-3-3"/></svg>
      </div>
      <p>No active sessions found for this node.</p>
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
@import url('https://fonts.googleapis.com/css2?family=Outfit:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap');

.dkg-dashboard {
  font-family: 'Outfit', sans-serif;
  color: #fff;
  background: rgba(13, 17, 23, 0.4);
  border: 1px solid rgba(255, 255, 255, 0.05);
  border-radius: 24px;
  padding: 32px;
  backdrop-filter: blur(20px);
}

/* Header */
.header-section {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 40px;
}

.header-left {
  display: flex;
  align-items: center;
  gap: 20px;
}

.icon-orb {
  width: 56px;
  height: 56px;
  background: linear-gradient(135deg, rgba(99, 102, 241, 0.2), rgba(168, 85, 247, 0.2));
  border: 1px solid rgba(139, 92, 246, 0.3);
  border-radius: 18px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #a78bfa;
}

.premium-title {
  font-size: 1.5rem;
  font-weight: 700;
  letter-spacing: -0.02em;
  margin: 0;
}

.premium-subtitle {
  font-size: 0.85rem;
  color: #64748b;
  margin: 4px 0 0;
}

.glass-btn {
  background: rgba(255, 255, 255, 0.05);
  border: 1px solid rgba(255, 255, 255, 0.1);
  color: #fff;
  padding: 10px 20px;
  border-radius: 12px;
  display: flex;
  align-items: center;
  gap: 10px;
  font-weight: 600;
  font-size: 0.9rem;
  transition: all 0.3s cubic-bezier(0.4, 0, 0.2, 1);
  cursor: pointer;
}

.glass-btn:hover:not(:disabled) {
  background: rgba(255, 255, 255, 0.1);
  border-color: #6366f1;
  box-shadow: 0 0 20px rgba(99, 102, 241, 0.3);
  transform: translateY(-2px);
}

.spin { animation: spin 1.5s linear infinite; }

/* Cards */
.session-list {
  display: grid;
  gap: 20px;
}

.premium-card {
  background: rgba(255, 255, 255, 0.02);
  border: 1px solid rgba(255, 255, 255, 0.06);
  border-radius: 20px;
  transition: all 0.4s cubic-bezier(0.4, 0, 0.2, 1);
}

.premium-card:hover {
  background: rgba(255, 255, 255, 0.04);
  border-color: rgba(99, 102, 241, 0.3);
}

.premium-card.expanded {
  background: rgba(255, 255, 255, 0.05);
  border-color: rgba(99, 102, 241, 0.5);
  box-shadow: 0 20px 50px -12px rgba(0, 0, 0, 0.5);
}

.card-top {
  padding: 24px;
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.vault-info-group {
  display: flex;
  align-items: center;
  gap: 18px;
  cursor: pointer;
  padding-right: 20px;
}

.vault-hero-icon {
  width: 50px;
  height: 50px;
  background: rgba(0, 0, 0, 0.3);
  border: 1px solid var(--col, #444);
  background: radial-gradient(circle at center, rgba(0,0,0,0.1) 0%, var(--col, #444) 300%);
  border-radius: 14px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--col, #fff);
  box-shadow: 0 0 15px rgba(0,0,0,0.2);
  transition: all 0.3s;
}

.vault-info-group:hover .vault-hero-icon {
  transform: scale(1.08) rotate(5deg);
  box-shadow: 0 0 20px var(--col);
}

.top-row {
  display: flex;
  align-items: center;
  gap: 10px;
}

.chain-pill {
  padding: 2px 10px;
  font-size: 0.65rem;
  font-weight: 800;
  text-transform: uppercase;
  border-radius: 6px;
  letter-spacing: 0.05em;
}
.chain-pill.btc { background: #f7931a22; color: #f7931a; border: 1px solid #f7931a33; }
.chain-pill.eth { background: #627eea22; color: #627eea; border: 1px solid #627eea33; }
.chain-pill.tron { background: #eb002922; color: #eb0029; border: 1px solid #eb002933; }

.vault-num {
  font-size: 1.1rem;
  font-weight: 700;
}

.flow-link {
  display: flex;
  align-items: center;
  gap: 5px;
  font-size: 0.7rem;
  font-weight: 700;
  color: #6366f1;
  opacity: 0;
  transform: translateX(-10px);
  transition: all 0.3s;
}

.vault-info-group:hover .flow-link {
  opacity: 1;
  transform: translateX(0);
}

.bottom-row {
  display: flex;
  gap: 12px;
  margin-top: 6px;
}

.metric {
  font-size: 0.75rem;
  color: #64748b;
}
.metric b { color: #cbd5e1; }

.status-action-group {
  display: flex;
  align-items: center;
  gap: 20px;
  cursor: pointer;
}

.status-stack {
  text-align: right;
  min-width: 120px;
}

.status-glow-label {
  font-size: 0.7rem;
  font-weight: 900;
  text-transform: uppercase;
  letter-spacing: 0.1em;
}
.status-glow-label.committing { color: #818cf8; text-shadow: 0 0 8px #818cf844; }
.status-glow-label.sharing { color: #a78bfa; text-shadow: 0 0 8px #a78bfa44; }
.status-glow-label.resolving { color: #f59e0b; text-shadow: 0 0 8px #f59e0b44; }
.status-glow-label.key_ready { color: #10b981; text-shadow: 0 0 8px #10b98144; }

.mini-progress-track {
  width: 80px;
  height: 3px;
  background: rgba(255, 255, 255, 0.05);
  border-radius: 2px;
  margin: 6px 0 0 auto;
  overflow: hidden;
}

.mini-progress-fill {
  height: 100%;
  transition: width 1s cubic-bezier(0.4, 0, 0.2, 1);
}

.chevron-orb {
  width: 32px;
  height: 32px;
  background: rgba(255, 255, 255, 0.05);
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #64748b;
  transition: all 0.3s;
}

.premium-card:hover .chevron-orb {
  background: rgba(99, 102, 241, 0.2);
  color: #818cf8;
}

.chevron-orb.active {
  transform: rotate(180deg);
  background: #6366f1;
  color: #fff;
}

/* Expanded Pane */
.expanded-pane {
  padding: 0 24px 32px;
}

.modern-timeline {
  padding: 20px 0 40px;
}

.t-line {
  height: 2px;
  background: rgba(255, 255, 255, 0.05);
  border-radius: 1px;
  position: relative;
}

.t-fill {
  position: absolute;
  height: 100%;
  background: linear-gradient(90deg, #6366f1, #a855f7, #10b981);
  box-shadow: 0 0 15px #6366f188;
  transition: width 0.8s;
}

.t-steps {
  display: flex;
  justify-content: space-between;
  margin-top: -8px;
}

.t-step {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
}

.t-circle {
  width: 16px;
  height: 16px;
  border-radius: 50%;
  background: #0f172a;
  border: 2px solid #1e293b;
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 10;
  transition: all 0.4s;
}

.t-step.done .t-circle {
  background: #10b981;
  border-color: #10b981;
  color: #fff;
}

.t-step.current .t-circle {
  background: #6366f1;
  border-color: #6366f1;
  box-shadow: 0 0 20px #6366f1;
}

.t-dot {
  width: 4px;
  height: 4px;
  background: #1e293b;
  border-radius: 50%;
}

.t-label {
  font-size: 0.65rem;
  font-weight: 700;
  text-transform: uppercase;
  color: #475569;
}
.t-step.done .t-label, .t-step.current .t-label { color: #cbd5e1; }

.info-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 16px;
  margin-bottom: 24px;
}

.info-box {
  background: rgba(0, 0, 0, 0.2);
  border: 1px solid rgba(255, 255, 255, 0.03);
  padding: 16px;
  border-radius: 14px;
}

.i-head {
  font-size: 0.6rem;
  font-weight: 700;
  color: #64748b;
  text-transform: uppercase;
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 8px;
}

.i-val {
  font-size: 0.9rem;
  font-weight: 600;
  color: #e2e8f0;
}
.i-val.mono { font-family: 'JetBrains Mono', monospace; font-size: 0.75rem; }
.i-val.good { color: #10b981; }
.i-val.warn { color: #f59e0b; }

.pubkey-strip {
  background: rgba(16, 185, 129, 0.03);
  border: 1px solid rgba(16, 185, 129, 0.1);
  border-radius: 16px;
  padding: 20px;
  margin-bottom: 32px;
}

.p-meta {
  display: flex;
  justify-content: space-between;
  margin-bottom: 12px;
}

.p-tag {
  font-size: 0.6rem;
  font-weight: 800;
  color: #10b981;
  background: rgba(16, 185, 129, 0.1);
  padding: 2px 8px;
  border-radius: 4px;
}

.p-algo {
  font-size: 0.6rem;
  font-family: 'JetBrains Mono', monospace;
  color: #475569;
}

.p-body {
  display: flex;
  gap: 12px;
  align-items: center;
}

.p-code {
  flex: 1;
  background: rgba(0, 0, 0, 0.4);
  padding: 12px 16px;
  border-radius: 10px;
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.8rem;
  color: #10b981;
  word-break: break-all;
  border: 1px solid rgba(16, 185, 129, 0.05);
}

.icon-btn {
  width: 40px;
  height: 40px;
  background: rgba(16, 185, 129, 0.1);
  border: none;
  border-radius: 10px;
  color: #10b981;
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
  transition: all 0.2s;
}

.icon-btn:hover { background: #10b981; color: #fff; transform: translateY(-2px); }

.committee-section {
  padding-top: 24px;
  border-top: 1px solid rgba(255, 255, 255, 0.05);
}

.section-title {
  font-size: 0.75rem;
  font-weight: 800;
  color: #475569;
  text-transform: uppercase;
  letter-spacing: 0.1em;
  margin-bottom: 20px;
}

.member-cloud {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
}

.member-item {
  display: flex;
  align-items: center;
  gap: 10px;
  background: rgba(255, 255, 255, 0.03);
  padding: 6px 14px;
  border-radius: 10px;
  border: 1px solid rgba(255, 255, 255, 0.02);
  position: relative;
  transition: all 0.2s;
}

.member-item:hover {
  background: rgba(99, 102, 241, 0.1);
  border-color: rgba(99, 102, 241, 0.3);
}

.m-idx {
  font-size: 0.6rem;
  font-weight: 800;
  color: #6366f1;
}

.m-addr {
  font-size: 0.75rem;
  font-family: 'JetBrains Mono', monospace;
  color: #94a3b8;
}

.m-copy {
  background: none;
  border: none;
  color: #4b5563;
  padding: 0;
  cursor: pointer;
  opacity: 0;
  transition: opacity 0.2s;
}

.member-item:hover .m-copy { opacity: 1; color: #6366f1; }

.m-tooltip {
  position: absolute;
  bottom: 120%;
  left: 50%;
  transform: translateX(-50%);
  background: #000;
  padding: 6px 12px;
  border-radius: 6px;
  font-size: 0.65rem;
  font-family: 'JetBrains Mono', monospace;
  white-space: nowrap;
  pointer-events: none;
  opacity: 0;
  transition: all 0.2s;
  box-shadow: 0 10px 20px rgba(0,0,0,0.5);
  z-index: 100;
}

.member-item:hover .m-tooltip { opacity: 1; bottom: 135%; }

/* Animations */
@keyframes spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }
@keyframes slideDown { from { opacity: 0; transform: translateY(-10px); } to { opacity: 1; transform: translateY(0); } }
.animate-slide-down { animation: slideDown 0.4s cubic-bezier(0.4, 0, 0.2, 1); }

.error-alert {
  background: rgba(239, 68, 68, 0.1);
  border: 1px solid rgba(239, 68, 68, 0.3);
  color: #ef4444;
  padding: 16px;
  border-radius: 14px;
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 32px;
}

.empty-state {
  text-align: center;
  padding: 80px 0;
  color: #64748b;
}

.empty-orb {
  width: 80px; height: 80px;
  background: rgba(255, 255, 255, 0.03);
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  margin: 0 auto 24px;
  color: #1e293b;
}

@media (max-width: 1024px) {
  .info-grid { grid-template-columns: repeat(2, 1fr); }
}
</style>
