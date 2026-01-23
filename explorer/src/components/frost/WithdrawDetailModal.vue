<script setup lang="ts">
import { ref, watch, nextTick, onMounted, onUnmounted } from 'vue'
import mermaid from 'mermaid'
import type { FrostWithdrawQueueItem } from '../../types'

const props = defineProps<{
  show: boolean
  item: FrostWithdrawQueueItem | null
}>()

const emit = defineEmits(['close'])

const svgContent = ref('')
const isRendering = ref(false)
const renderError = ref('')
const initialized = ref(false)

const initMermaid = () => {
  if (initialized.value) return
  try {
    mermaid.initialize({
      startOnLoad: false,
      theme: 'dark',
      securityLevel: 'loose',
      fontFamily: 'Outfit, sans-serif',
      flowchart: { htmlLabels: true, curve: 'basis' },
    })
    initialized.value = true
  } catch (e) {
    console.error('[Mermaid] Init failed', e)
    renderError.value = 'Failed to initialize Mermaid'
  }
}

const renderChart = async () => {
  if (!props.item || !props.show) return

  initMermaid()

  const status = props.item.status.toUpperCase()
  let flow = 'graph LR\n'
  flow += '  A[QUEUED] -->|Coordinator Pick| B[SIGNING]\n'
  flow += '  B -->|FROST Success| C[SIGNED]\n'
  flow += '  C -->|Chain Broadcast| D((ON-CHAIN))\n'
  
  if (status === 'QUEUED') flow += '  style A fill:#3b82f6,stroke:#fff,stroke-width:2px,color:#fff\n'
  if (status === 'SIGNING') flow += '  style B fill:#8b5cf6,stroke:#fff,stroke-width:2px,color:#fff\n'
  if (status === 'SIGNED') flow += '  style C fill:#10b981,stroke:#fff,stroke-width:2px,color:#fff\n'
  if (status === 'BROADCAST' || status === 'COMPLETED') flow += '  style D fill:#10b981,stroke:#fff,stroke-width:2px,color:#fff\n'

  isRendering.value = true
  renderError.value = ''
  svgContent.value = ''

  await nextTick()

  try {
    const id = 'mermaid-svg-' + Math.random().toString(36).substring(2, 9)
    const result = await mermaid.render(id, flow)
    svgContent.value = result.svg
  } catch (e: any) {
    console.error('[Mermaid] Render failed:', e)
    renderError.value = String(e.message || e || 'Unknown render error')
  } finally {
    isRendering.value = false
  }
}

watch(() => props.show, (val) => {
  if (val) {
    nextTick(() => renderChart())
  } else {
    document.body.style.overflow = ''
    svgContent.value = ''
    renderError.value = ''
  }
})

watch(() => props.item, () => {
  if (props.show) {
    renderChart()
  }
})

onMounted(initMermaid)
onUnmounted(() => {
  document.body.style.overflow = ''
})

function formatTime(ts: number): string {
  if (!ts) return '-'
  const date = new Date(ts)
  return date.toLocaleTimeString() + '.' + String(ts % 1000).padStart(3, '0')
}

function getLogStatusClass(status: string) {
  const s = status.toUpperCase()
  if (s === 'OK') return 'log-ok'
  if (s === 'FAILED') return 'log-failed'
  if (s === 'WAITING') return 'log-waiting'
  return ''
}
</script>

<template>
  <Teleport to="body">
    <Transition name="fade">
      <div v-if="show && item" class="modal-root" @click.self="emit('close')">
        <div class="modal-backdrop"></div>

        <div class="modal-container" @click.stop>
          <!-- Header -->
          <div class="modal-header">
            <div class="header-left">
              <div class="header-icon">
                <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/><line x1="16" y1="13" x2="8" y2="13"/><line x1="16" y1="17" x2="8" y2="17"/><polyline points="10 9 9 9 8 9"/></svg>
              </div>
              <div>
                <h3 class="modal-title">Withdraw Detail</h3>
                <p class="modal-subtitle mono">{{ item.withdraw_id }}</p>
              </div>
            </div>
            <button @click="emit('close')" class="close-btn">&times;</button>
          </div>

          <!-- Content -->
          <div class="modal-body custom-scrollbar">
            <!-- Top Section: Visual Flow -->
            <section class="visual-section">
              <div class="section-label">Redistribution Flow</div>
              <div class="flow-container">
                <div v-if="isRendering" class="rendering-overlay">
                  <div class="spinner"></div>
                </div>
                <div v-if="svgContent" class="mermaid-output" v-html="svgContent"></div>
                <div v-else-if="renderError" class="error-msg">{{ renderError }}</div>
              </div>
            </section>

            <!-- Middle Section: Key Info -->
            <section class="info-grid">
              <div class="info-card">
                <span class="info-label">Current Status</span>
                <div :class="['status-val', item.status.toLowerCase()]">
                  <span class="dot"></span>
                  {{ item.status }}
                </div>
              </div>
              <div class="info-card">
                <span class="info-label">Target Chain</span>
                <span class="info-val font-bold text-white">{{ item.chain.toUpperCase() }}</span>
              </div>
              <div class="info-card">
                <span class="info-label">Recipient</span>
                <span class="info-val mono text-gray-400">{{ item.to }}</span>
              </div>
              <div class="info-card">
                <span class="info-label">Vault ID</span>
                <span class="info-val text-amber-500 font-bold">{{ item.vault_id ?? 'Auto' }}</span>
              </div>
            </section>

            <!-- Bottom Section: Detailed Logs -->
            <section class="logs-section">
              <div class="section-header">
                <div class="section-label">Execution Trace (Planning Logs)</div>
                <div class="log-count">{{ item.planning_logs?.length ?? 0 }} entries</div>
              </div>
              
              <div v-if="item.planning_logs && item.planning_logs.length > 0" class="log-timeline">
                <div v-for="(log, idx) in item.planning_logs" :key="idx" class="log-entry">
                  <div class="log-time mono">{{ formatTime(log.timestamp) }}</div>
                  <div class="log-marker">
                    <div :class="['marker-dot', getLogStatusClass(log.status)]"></div>
                    <div v-if="idx < item.planning_logs.length - 1" class="marker-line"></div>
                  </div>
                  <div class="log-content">
                    <div class="log-step">
                      <span class="step-name">{{ log.step }}</span>
                      <span :class="['step-status', getLogStatusClass(log.status)]">{{ log.status }}</span>
                    </div>
                    <div class="log-message">{{ log.message }}</div>
                  </div>
                </div>
              </div>
              <div v-else class="empty-logs">
                <svg xmlns="http://www.w3.org/2000/svg" width="40" height="40" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1" stroke-linecap="round" stroke-linejoin="round" class="text-gray-700 mb-4"><path d="M13 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z"/><polyline points="13 2 13 9 20 9"/></svg>
                <p>No execution logs yet. Waiting for Runtime nodes to report...</p>
              </div>
            </section>
          </div>

          <!-- Footer -->
          <div class="modal-footer">
            <button @click="emit('close')" class="close-primary-btn">Dismiss</button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.modal-root {
  position: fixed;
  inset: 0;
  z-index: 9999;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
}

.modal-backdrop {
  position: absolute;
  inset: 0;
  background: rgba(3, 7, 18, 0.9);
  backdrop-filter: blur(12px);
}

.modal-container {
  position: relative;
  width: 100%;
  max-width: 800px;
  max-height: 85vh;
  background: #0f172a;
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 28px;
  box-shadow: 0 30px 60px -12px rgba(0, 0, 0, 0.6);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.modal-header {
  padding: 24px 32px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.05);
  display: flex;
  justify-content: space-between;
  align-items: center;
  background: rgba(255, 255, 255, 0.01);
}

.header-left {
  display: flex;
  align-items: center;
  gap: 16px;
}

.header-icon {
  width: 40px;
  height: 40px;
  background: rgba(59, 130, 246, 0.1);
  border: 1px solid rgba(59, 130, 246, 0.2);
  border-radius: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #3b82f6;
}

.modal-title {
  font-size: 1.1rem;
  font-weight: 700;
  color: white;
  margin: 0;
}

.modal-subtitle {
  font-size: 0.75rem;
  color: #64748b;
  margin: 2px 0 0;
}

.close-btn {
  background: transparent;
  border: none;
  color: #475569;
  font-size: 1.5rem;
  cursor: pointer;
  transition: all 0.2s;
  width: 32px;
  height: 32px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 8px;
}

.close-btn:hover {
  background: rgba(255, 255, 255, 0.05);
  color: white;
}

.modal-body {
  flex: 1;
  padding: 32px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 32px;
}

.section-label {
  font-size: 0.7rem;
  font-weight: 800;
  text-transform: uppercase;
  color: #475569;
  letter-spacing: 0.1em;
  margin-bottom: 16px;
}

/* Visual Section */
.flow-container {
  background: rgba(0, 0, 0, 0.2);
  border-radius: 20px;
  padding: 24px;
  border: 1px solid rgba(255, 255, 255, 0.03);
  position: relative;
  min-height: 120px;
  display: flex;
  align-items: center;
  justify-content: center;
}

.mermaid-output {
  width: 100%;
}

:deep(.mermaid-output svg) {
  max-width: 100%;
  height: auto;
}

/* Info Grid */
.info-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 16px;
}

.info-card {
  background: rgba(255, 255, 255, 0.02);
  border: 1px solid rgba(255, 255, 255, 0.04);
  padding: 16px 20px;
  border-radius: 16px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.info-label {
  font-size: 0.65rem;
  font-weight: 600;
  color: #64748b;
  text-transform: uppercase;
}

.info-val {
  font-size: 0.9rem;
}

.status-val {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-weight: 800;
  font-size: 0.85rem;
  text-transform: uppercase;
}

.status-val.queued { color: #3b82f6; }
.status-val.signing { color: #8b5cf6; }
.status-val.signed { color: #10b981; }

.status-val .dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: currentColor;
  box-shadow: 0 0 10px currentColor;
}

/* Logs Section */
.logs-section {
  display: flex;
  flex-direction: column;
}

.section-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
}

.log-count {
  font-size: 0.75rem;
  color: #475569;
  background: rgba(255, 255, 255, 0.05);
  padding: 2px 10px;
  border-radius: 99px;
}

.log-timeline {
  display: flex;
  flex-direction: column;
}

.log-entry {
  display: flex;
  gap: 20px;
  min-height: 64px;
}

.log-time {
  font-size: 0.7rem;
  color: #475569;
  width: 100px;
  text-align: right;
  padding-top: 2px;
}

.log-marker {
  display: flex;
  flex-direction: column;
  align-items: center;
  padding-top: 6px;
}

.marker-dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
  background: #334155;
  z-index: 2;
}

.marker-dot.log-ok { background: #10b981; box-shadow: 0 0 10px rgba(16, 185, 129, 0.4); }
.marker-dot.log-failed { background: #ef4444; box-shadow: 0 0 10px rgba(239, 68, 68, 0.4); }
.marker-dot.log-waiting { background: #f59e0b; box-shadow: 0 0 10px rgba(245, 158, 11, 0.4); }

.marker-line {
  flex: 1;
  width: 2px;
  background: rgba(255, 255, 255, 0.05);
  margin: 4px 0;
}

.log-content {
  flex: 1;
  padding-bottom: 20px;
}

.log-step {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 4px;
}

.step-name {
  font-weight: 700;
  font-size: 0.9rem;
  color: #f1f5f9;
}

.step-status {
  font-size: 0.6rem;
  font-weight: 800;
  padding: 1px 6px;
  border-radius: 4px;
  text-transform: uppercase;
}

.step-status.log-ok { background: rgba(16, 185, 129, 0.1); color: #10b981; }
.step-status.log-failed { background: rgba(239, 68, 68, 0.1); color: #ef4444; }
.step-status.log-waiting { background: rgba(245, 158, 11, 0.1); color: #f59e0b; }

.log-message {
  font-size: 0.8rem;
  color: #94a3b8;
  line-height: 1.5;
}

.empty-logs {
  padding: 40px;
  text-align: center;
  background: rgba(255, 255, 255, 0.01);
  border-radius: 20px;
  color: #475569;
  font-size: 0.85rem;
}

/* Footer */
.modal-footer {
  padding: 24px 32px;
  border-top: 1px solid rgba(255, 255, 255, 0.05);
  display: flex;
  justify-content: flex-end;
}

.close-primary-btn {
  background: #1e293b;
  color: white;
  border: 1px solid rgba(255, 255, 255, 0.1);
  padding: 10px 24px;
  border-radius: 12px;
  font-weight: 600;
  font-size: 0.9rem;
  cursor: pointer;
  transition: all 0.2s;
}

.close-primary-btn:hover {
  background: #334155;
  border-color: rgba(255, 255, 255, 0.2);
}

.mono { font-family: 'JetBrains Mono', monospace; }

/* Scrollbar */
.custom-scrollbar::-webkit-scrollbar {
  width: 6px;
}
.custom-scrollbar::-webkit-scrollbar-track {
  background: transparent;
}
.custom-scrollbar::-webkit-scrollbar-thumb {
  background: rgba(255, 255, 255, 0.1);
  border-radius: 10px;
}

/* Transitions */
.fade-enter-active, .fade-leave-active { transition: opacity 0.3s; }
.fade-enter-from, .fade-leave-to { opacity: 0; }

.spinner {
  width: 24px;
  height: 24px;
  border: 2px solid rgba(255, 255, 255, 0.1);
  border-top-color: #3b82f6;
  border-radius: 50%;
  animation: spin 1s linear infinite;
}

@keyframes spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }
</style>
