<script setup lang="ts">
import { ref, watch, onMounted, onUnmounted, nextTick } from 'vue'
import mermaid from 'mermaid'
import type { WitnessRequest } from '../../types'


const props = defineProps<{
  show: boolean
  title: string
  definition: string
  request?: WitnessRequest | null
}>()

const emit = defineEmits(['close', 'select-tx'])

const svgContent = ref('')
const isRendering = ref(false)
const renderError = ref('')
const rawDefinition = ref('')
const initialized = ref(false)

const initMermaid = () => {
  if (initialized.value) return
  try {
    mermaid.initialize({
      startOnLoad: false,
      theme: 'dark',
      securityLevel: 'loose',
      fontFamily: 'ui-monospace, monospace',
      flowchart: { htmlLabels: true, curve: 'basis' },
    })
    initialized.value = true
    console.log('[Mermaid] Initialized successfully')
  } catch (e) {
    console.error('[Mermaid] Init failed', e)
    renderError.value = 'Failed to initialize Mermaid'
  }
}

const renderChart = async () => {
  console.log('[Mermaid] renderChart called, show:', props.show, 'definition:', props.definition?.substring(0, 50))

  if (!props.definition) {
    renderError.value = 'No diagram definition provided'
    return
  }
  if (!props.show) return

  initMermaid()

  if (!initialized.value) {
    renderError.value = 'Mermaid not initialized'
    return
  }

  isRendering.value = true
  renderError.value = ''
  svgContent.value = ''
  rawDefinition.value = props.definition

  await nextTick()

  try {
    const id = 'mermaid-svg-' + Math.random().toString(36).substring(2, 9)
    console.log('[Mermaid] Rendering with id:', id)
    const result = await mermaid.render(id, props.definition)
    console.log('[Mermaid] Render result:', result ? 'success' : 'empty')
    svgContent.value = result.svg
  } catch (e: any) {
    console.error('[Mermaid] Render failed:', e)
    renderError.value = String(e.message || e || 'Unknown render error')
  } finally {
    isRendering.value = false
  }
}

onMounted(() => {
  initMermaid()
})

// 确保组件销毁时恢复滚动
onUnmounted(() => {
  document.body.style.overflow = ''
})

watch(() => props.show, (val) => {
  if (val) {
    nextTick(() => renderChart())
  } else {
    document.body.style.overflow = ''
    svgContent.value = ''
    renderError.value = ''
  }
})

watch(() => props.definition, () => {
  if (props.show) {
    renderChart()
  }
})
const getVoteLabel = (vote: any) => {
  const vt = vote.vote_type ?? vote.voteType
  if (vt === undefined) return 'VOTE'
  if (typeof vt === 'number') {
    const labels: Record<number, string> = { 0: 'UNK', 1: 'PASS', 2: 'FAIL', 3: 'ABS' }
    return labels[vt] || 'VOTE'
  }
  if (typeof vt === 'string') {
    if (vt.startsWith('VOTE_')) return vt.substring(5)
    return vt
  }
  return 'VOTE'
}

const getVoteClass = (vote: any) => {
  const vt = vote.vote_type ?? vote.voteType
  if (vt === undefined) return ''
  if (typeof vt === 'number') {
    const classes: Record<number, string> = { 1: 'vote_pass', 2: 'vote_fail', 3: 'vote_abstain' }
    return classes[vt] || ''
  }
  if (typeof vt === 'string') {
    const s = vt.toLowerCase()
    return s.startsWith('vote_') ? s : `vote_${s}`
  }
  return ''
}
</script>


<template>
  <Teleport to="body">
    <Transition name="fade">
      <div v-if="show" class="modal-root" @click.self="emit('close')">
        <div class="modal-backdrop"></div>

        <div class="modal-container" @click.stop>
          <!-- Header -->
          <div class="modal-header">
            <h3 class="modal-title">{{ title }}</h3>
            <button @click="emit('close')" class="close-btn">&times;</button>
          </div>

          <!-- Content -->
          <div class="modal-body">
            <div class="content-left">
              <div v-if="isRendering" class="status-msg">
                <span class="spinner"></span>
                <span>Rendering diagram...</span>
              </div>
              <div v-else-if="renderError" class="error-container">
                <div class="error-msg">
                  <strong>Render Error:</strong> {{ renderError }}
                </div>
                <div v-if="rawDefinition" class="raw-definition">
                  <p class="raw-label">Raw Mermaid Definition:</p>
                  <pre>{{ rawDefinition }}</pre>
                </div>
              </div>
              <div v-else-if="svgContent" class="mermaid-output" v-html="svgContent"></div>
              <div v-else class="status-msg">
                <span>⏳ Waiting for diagram...</span>
              </div>
            </div>

            <div class="content-right" v-if="request">
              <div class="trace-section">
                <div class="section-tag">Audit Trail</div>
                <h4 class="trace-title">Involved Transactions</h4>
                
                <div class="tx-link-item" @click="emit('select-tx', request.request_id)">
                  <div class="tx-badge req">REQ</div>
                  <div class="tx-info">
                    <span class="tx-label">Original Deposit Request</span>
                    <code class="tx-hash">{{ request.request_id }}</code>
                  </div>
                  <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m9 18 6-6-6-6"/></svg>
                </div>

                <div v-if="request.votes && request.votes.length > 0" class="votes-list">
                  <div v-for="vote in request.votes" :key="vote.tx_id || Math.random()" class="tx-link-item" @click="emit('select-tx', vote.tx_id)">
                    <div :class="['tx-badge', getVoteClass(vote)]">{{ getVoteLabel(vote) }}</div>
                    <div class="tx-info">
                      <span class="tx-label">Witness: {{ (vote.witness_address ?? vote.witnessAddress ?? '').substring(0, 8) }}...{{ (vote.witness_address ?? vote.witnessAddress ?? '').slice(-6) }}</span>
                      <code class="tx-hash">{{ vote.tx_id || vote.txId || 'Unknown ID' }}</code>
                    </div>
                    <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m9 18 6-6-6-6"/></svg>
                  </div>
                </div>

                <div v-else class="empty-votes">
                  <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" x2="12" y1="8" y2="12"/><line x1="12" x2="12.01" y1="16" y2="16"/></svg>
                  <span>No voting records found yet.</span>
                </div>
              </div>
            </div>
          </div>

          <!-- Footer -->
          <div class="modal-footer">
            <button @click="emit('close')" class="close-button">Close</button>
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
  padding: 20px;
}

.modal-backdrop {
  position: absolute;
  inset: 0;
  background: rgba(0, 0, 0, 0.85);
  backdrop-filter: blur(8px);
}

.modal-container {
  position: relative;
  width: 100%;
  max-width: 1000px;
  max-height: 90vh;
  background: #111827;
  border: 1px solid rgba(255, 255, 255, 0.1);
  border-radius: 16px;
  box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.5);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.modal-header {
  padding: 20px 24px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.05);
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.modal-title {
  font-size: 1.25rem;
  font-weight: 700;
  color: white;
  margin: 0;
}

.close-btn {
  background: transparent;
  border: none;
  color: #9ca3af;
  font-size: 2rem;
  line-height: 1;
  cursor: pointer;
  transition: color 0.2s;
}

.close-btn:hover {
  color: white;
}

.modal-body {
  flex: 1;
  padding: 0;
  overflow: hidden;
  display: flex;
  background: rgba(0, 0, 0, 0.2);
}

.content-left {
  flex: 1.5;
  padding: 32px;
  overflow: auto;
  display: flex;
  align-items: center;
  justify-content: center;
  border-right: 1px solid rgba(255, 255, 255, 0.05);
}

.content-right {
  flex: 1;
  padding: 32px;
  overflow: auto;
  background: rgba(255, 255, 255, 0.01);
}

.trace-section {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.section-tag {
  font-size: 0.65rem;
  font-weight: 800;
  color: #6366f1;
  text-transform: uppercase;
  letter-spacing: 0.1em;
}

.trace-title {
  margin: 0;
  font-size: 1rem;
  font-weight: 700;
  color: #fff;
}

.tx-link-item {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 16px;
  background: rgba(255, 255, 255, 0.03);
  border: 1px solid rgba(255, 255, 255, 0.05);
  border-radius: 12px;
  cursor: pointer;
  transition: all 0.2s cubic-bezier(0.4, 0, 0.2, 1);
}

.tx-link-item:hover {
  background: rgba(99, 102, 241, 0.05);
  border-color: rgba(99, 102, 241, 0.3);
  transform: translateX(4px);
}

.tx-badge {
  padding: 4px 8px;
  border-radius: 6px;
  font-size: 0.6rem;
  font-weight: 800;
  text-transform: uppercase;
}

.tx-badge.req { background: #6366f122; color: #6366f1; }
.tx-badge.vote_pass { background: #10b98122; color: #10b981; }
.tx-badge.vote_fail { background: #ef444422; color: #ef4444; }
.tx-badge.vote_abstain { background: #f59e0b22; color: #f59e0b; }

.tx-info {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 4px;
  overflow: hidden;
}

.tx-label {
  font-size: 0.75rem;
  font-weight: 600;
  color: #94a3b8;
}

.tx-hash {
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.7rem;
  color: #64748b;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.empty-votes {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 24px;
  color: #475569;
  font-size: 0.85rem;
  background: rgba(0, 0, 0, 0.1);
  border-radius: 12px;
  border: 1px dashed rgba(255, 255, 255, 0.05);
}

.mermaid-output {
  width: 100%;
  display: flex;
  justify-content: center;
}


:deep(.mermaid-output svg) {
  max-width: 100%;
  height: auto;
}

.status-msg {
  color: #9ca3af;
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 1rem;
}

.error-container {
  width: 100%;
  max-width: 600px;
}

.error-msg {
  color: #f87171;
  background: rgba(239, 68, 68, 0.1);
  padding: 16px;
  border-radius: 8px;
  border: 1px solid rgba(239, 68, 68, 0.2);
  margin-bottom: 16px;
}

.raw-definition {
  background: rgba(0, 0, 0, 0.3);
  border-radius: 8px;
  padding: 16px;
  border: 1px solid rgba(255, 255, 255, 0.1);
}

.raw-label {
  color: #9ca3af;
  font-size: 0.75rem;
  margin-bottom: 8px;
  text-transform: uppercase;
}

.raw-definition pre {
  color: #d1d5db;
  font-size: 0.75rem;
  white-space: pre-wrap;
  word-break: break-all;
  margin: 0;
  font-family: ui-monospace, monospace;
}

.modal-footer {
  padding: 16px;
  text-align: center;
  background: rgba(0, 0, 0, 0.3);
  border-top: 1px solid rgba(255, 255, 255, 0.05);
}

.close-button {
  background: linear-gradient(135deg, #4b5563 0%, #374151 100%);
  color: white;
  border: none;
  padding: 10px 24px;
  border-radius: 8px;
  font-size: 0.875rem;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.2s;
}

.close-button:hover {
  background: linear-gradient(135deg, #6b7280 0%, #4b5563 100%);
}

.spinner {
  width: 20px;
  height: 20px;
  border: 2px solid rgba(255, 255, 255, 0.1);
  border-top-color: #6366f1;
  border-radius: 50%;
  animation: spin 1s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

.fade-enter-active, .fade-leave-active {
  transition: opacity 0.3s ease;
}

.fade-enter-from, .fade-leave-to {
  opacity: 0;
}
</style>
