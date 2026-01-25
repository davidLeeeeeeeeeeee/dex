<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import type { TxInfo } from '../types'
import { fetchTx } from '../api'
import TxTypeRenderer from './TxTypeRenderer.vue'

const props = defineProps<{
  tx?: TxInfo | null
  node?: string
  txId?: string
  show?: boolean     // If provided, operates in Modal mode
}>()

const emit = defineEmits<{
  back: []
  close: []
  addressClick: [address: string]
}>()

const localTx = ref<TxInfo | null>(null)
const loading = ref(false)
const error = ref('')

// Mode detection: if 'show' prop is provided (even if false), we are in modal mode
const isModal = computed(() => typeof props.show !== 'undefined')

// Source of truth for transaction data: prefer local fetch, fallback to passed prop
const currentTx = computed(() => localTx.value || props.tx || null)

const loadTx = async () => {
  if (!props.node || !props.txId) {
    console.log('[TxDetail] Skipping load: node or txId missing', { node: props.node, txId: props.txId })
    return
  }
  
  loading.value = true
  error.value = ''
  localTx.value = null
  
  try {
    console.log('[TxDetail] Fetching tx:', props.txId, 'from node:', props.node)
    const resp = await fetchTx({ node: props.node, tx_id: props.txId })
    if (resp.error) {
      error.value = resp.error
    } else if (resp.transaction) {
      localTx.value = resp.transaction
    } else {
      error.value = 'Transaction not found'
    }
  } catch (e: any) {
    error.value = e.message || 'Failed to fetch transaction'
    console.error('[TxDetail] Fetch error:', e)
  } finally {
    loading.value = false
  }
}

// Watch for prop changes to trigger reload in modal mode or ID-based mode
watch(() => [props.show, props.txId, props.node], ([show, txId, node]) => {
  console.log('[TxDetail] Props Change:', { show, txId, node, isModal: isModal.value })
  if (isModal.value) {
    if (show && txId) {
      loadTx()
    }
  } else if (txId && node) {
    loadTx()
  }
}, { immediate: true })

const numberFormat = new Intl.NumberFormat('en-US')

function formatNumber(value?: any): string {
  if (value === undefined || value === null || value === '') return '-'
  const num = parseFloat(String(value))
  if (isNaN(num)) return String(value)
  return numberFormat.format(num)
}

function formatBalance(val?: any): string {
  if (val === undefined || val === null || val === '') return '0'
  try {
    const s = String(val)
    if (s.includes('.')) {
      return parseFloat(s).toLocaleString(undefined, { minimumFractionDigits: 0, maximumFractionDigits: 4 })
    }
    return BigInt(s).toLocaleString()
  } catch (e) {
    return String(val)
  }
}

function statusInfo(status?: string) {
  const s = (status || '').toUpperCase()
  if (s === 'SUCCEED' || s === 'SUCCESS') return { class: 'tc-good', label: 'Confirmed', icon: 'check' }
  if (s === 'FAILED' || s === 'FAIL') return { class: 'tc-bad', label: 'Reverted', icon: 'error' }
  return { class: 'tc-warn', label: 'Processing', icon: 'clock' }
}

function copyToClipboard(text: string) {
  if (!text) return
  navigator.clipboard.writeText(text)
}

function handleClose() {
  emit('close')
  emit('back')
}
</script>

<template>
  <!-- Modal Wrapper if in show mode -->
  <Teleport to="body" :disabled="!isModal">
    <Transition name="fade">
      <div v-if="!isModal || show" :class="{ 'modal-root': isModal }" @click.self="handleClose">
        <div v-if="isModal" class="modal-backdrop"></div>
        
        <div :class="['tx-view-container', { 'modal-container': isModal, 'animate-fade-in': !isModal }]" @click.stop>
          
          <!-- Loading State for Modal/Dynamic Fetch -->
          <div v-if="loading" class="state-msg">
            <span class="spinner"></span>
            <span>Retrieving transaction data...</span>
          </div>

          <!-- Error State -->
          <div v-else-if="error" class="error-msg-box">
             <div class="error-notification">
              <div class="error-header">
                <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" x2="12" y1="8" y2="12"/><line x1="12" x2="12.01" y1="16" y2="16"/></svg>
                <span>Query Failed</span>
              </div>
              <div class="error-body">{{ error }}</div>
              <button v-if="isModal" @click="handleClose" class="error-close-btn">Close</button>
            </div>
          </div>

          <!-- Content -->
          <div v-else-if="currentTx && currentTx.tx_id" class="tx-view custom-scrollbar" :class="{ 'inline-panel': !isModal }">
            <div class="tx-header-glass">
              <div class="header-left">
                <div class="tx-type-orb">
                  <svg v-if="currentTx.tx_type?.includes('Witness')" xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M2 12s3-7 10-7 10 7 10 7-3 7-10 7-10-7-10-7Z"/><circle cx="12" cy="12" r="3"/></svg>
                  <svg v-else xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M7 11V7a5 5 0 0 1 10 0v4"/><rect width="18" height="11" x="3" y="11" rx="2" ry="2"/></svg>
                </div>
                <div class="title-meta">
                  <div class="meta-top">
                    <span class="type-label">{{ currentTx.tx_type || 'Unknown Transaction' }}</span>
                    <div :class="['status-badge', statusInfo(currentTx.status).class]">
                      {{ statusInfo(currentTx.status).label }}
                    </div>
                  </div>
                  <div class="tx-id-row" @click="copyToClipboard(currentTx.tx_id || '')">
                    <h1 class="mono">{{ currentTx.tx_id }}</h1>
                    <svg class="copy-icon" xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/></svg>
                  </div>
                </div>
              </div>
              <button class="close-action" @click="handleClose">
                <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
              </button>
            </div>

            <!-- Core Grid -->
            <div class="info-matrix">
              <!-- Left: Summary Info -->
              <div class="info-card summary-card">
                <div class="card-title">General Information</div>
                <div class="kv-list">
                  <div class="kv-item">
                    <span class="k">Block Height</span>
                    <span class="v highlight"># {{ formatNumber(currentTx.executed_height) }}</span>
                  </div>
                  <div class="kv-item">
                    <span class="k">Index in Block</span>
                    <span class="v">{{ currentTx.index || 0 }}</span>
                  </div>
                  <div class="kv-item">
                    <span class="k">Nonce</span>
                    <span class="v mono">{{ currentTx.nonce }}</span>
                  </div>
                  <div class="kv-item">
                    <span class="k">Fee Consumption</span>
                    <span class="v text-orange-400">{{ currentTx.fee || '0.00' }} GAS</span>
                  </div>
                  <div class="kv-item">
                     <span class="k">Timestamp</span>
                     <span class="v text-gray-400">{{ currentTx.timestamp || 'N/A' }}</span>
                  </div>
                </div>
              </div>

              <!-- Center: Flow Trace -->
              <div class="info-card flow-card">
                <div class="card-title">Activity Flow</div>
                <div class="flow-layout">
                  <div class="flow-node" @click="emit('addressClick', currentTx.from_address!)">
                    <div class="node-icon from">F</div>
                    <div class="node-data">
                      <span class="label">Sender</span>
                      <code class="val mono">{{ currentTx.from_address || '-' }}</code>
                    </div>
                  </div>
                  
                  <div class="flow-connector">
                    <div class="connector-line"></div>
                    <div class="value-tag">
                      <span class="v-num">{{ formatBalance(currentTx.value) }}</span>
                      <span class="v-symbol">ASSET</span>
                    </div>
                    <div class="connector-arrow"></div>
                  </div>

                  <div class="flow-node" v-if="currentTx.to_address" @click="emit('addressClick', currentTx.to_address)">
                    <div class="node-icon to">T</div>
                    <div class="node-data">
                      <span class="label">Recipient</span>
                      <code class="val mono">{{ currentTx.to_address }}</code>
                    </div>
                  </div>
                  <div v-else class="flow-node system">
                    <div class="node-icon sys">S</div>
                    <div class="node-data">
                      <span class="label">Direction</span>
                      <code class="val">System / Contract Interaction</code>
                    </div>
                  </div>
                </div>
              </div>
            </div>

            <!-- Details / Payload -->
            <div v-if="currentTx.details && Object.keys(currentTx.details).length > 0" class="info-card details-card">
              <div class="card-title">Protocol Payload Data</div>
              <div class="payload-wrapper">
                <TxTypeRenderer :type="currentTx.tx_type || ''" :details="currentTx.details" />
              </div>
            </div>

            <!-- Raw Input Data -->
            <div class="info-card raw-data-card">
              <div class="card-title">Input Data (Hex / Raw)</div>
              <div class="raw-data-wrapper">
                <template v-if="currentTx.input_data || (currentTx as any).input || (currentTx as any).data">
                  <div class="data-meta">
                    <span class="byte-count">{{ ((currentTx.input_data || (currentTx as any).input || (currentTx as any).data || '').length / 2).toFixed(0) }} Bytes</span>
                  </div>
                  <div class="hex-scroll-box mono">
                    {{ currentTx.input_data || (currentTx as any).input || (currentTx as any).data }}
                  </div>
                </template>
                <div v-else class="empty-data-msg">
                   No specific input data associated with this transaction
                </div>
              </div>
            </div>

            <!-- Hash & Sig Section -->
            <div class="info-card signature-card">
              <div class="card-title">Security & Integrity</div>
              <div class="kv-list horizontal">
                <div class="kv-item full">
                  <span class="k">Transaction Hash</span>
                  <div class="v-row" @click="copyToClipboard(currentTx.tx_id || '')">
                    <code class="v mono break-all">{{ currentTx.tx_id }}</code>
                  </div>
                </div>
                <div class="kv-item full" v-if="currentTx.signature">
                  <span class="k">Validator Signature</span>
                  <code class="v mono break-all text-gray-500">{{ currentTx.signature }}</code>
                </div>
              </div>
            </div>

            <!-- Error Diagnostics -->
            <div v-if="currentTx.error" class="error-notification">
              <div class="error-header">
                <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z"/><line x1="12" x2="12" y1="9" y2="13"/><line x1="12" x2="12.01" y1="17" y2="17"/></svg>
                Execution Reverted
              </div>
              <div class="error-body">
                <p>{{ currentTx.error }}</p>
              </div>
            </div>
          </div>
          
          <div v-else class="state-msg">
            <svg xmlns="http://www.w3.org/2000/svg" width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1" stroke-linecap="round" stroke-linejoin="round" class="mb-4 opacity-20"><circle cx="12" cy="12" r="10"/><path d="M8 12h8"/></svg>
            <span>Transaction data unavailable</span>
            <p class="text-xs text-gray-500 mt-2">No transaction was selected or data is currenty being fetched.</p>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
@import url('https://fonts.googleapis.com/css2?family=Outfit:wght@400;500;600;700;800&family=JetBrains+Mono:wght@400;500&display=swap');

/* Modal Specific Styles */
.modal-root {
  position: fixed;
  inset: 0;
  z-index: 10000;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 40px;
}

.modal-backdrop {
  position: absolute;
  inset: 0;
  background: rgba(2, 6, 23, 0.85);
  backdrop-filter: blur(12px);
}

.modal-container {
  position: relative;
  width: 100%;
  max-width: 900px;
  max-height: 85vh;
  background: #0f172a;
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 24px;
  box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.7);
  display: flex;
  flex-direction: column;
  overflow: hidden;
  animation: modalSlide 0.4s cubic-bezier(0.4, 0, 0.2, 1);
}

@keyframes modalSlide {
  from { opacity: 0; transform: translateY(20px) scale(0.98); }
  to { opacity: 1; transform: translateY(0) scale(1); }
}

/* Base View Styles */
.tx-view { 
  display: flex; 
  flex-direction: column; 
  gap: 24px; 
  font-family: 'Outfit', sans-serif; 
}

.tx-view.inline-panel {
  padding: 32px;
  background: rgba(15, 23, 42, 0.4);
  border: 1px solid rgba(255, 255, 255, 0.05);
  border-radius: 24px;
  margin-top: 10px;
}

.tx-view.custom-scrollbar {
  overflow-y: auto;
}

/* Header Glass */
.tx-header-glass {
  display: flex; justify-content: space-between; align-items: center;
  padding: 32px; background: rgba(255, 255, 255, 0.03); border: 1px solid rgba(255, 255, 255, 0.06);
  border-radius: 24px; backdrop-filter: blur(10px);
}
.header-left { display: flex; align-items: center; gap: 24px; }
.tx-type-orb {
  width: 60px; height: 60px; background: linear-gradient(135deg, #6366f1, #a855f7);
  border-radius: 18px; display: flex; align-items: center; justify-content: center;
  color: #fff; box-shadow: 0 8px 24px rgba(99, 102, 241, 0.3);
}
.meta-top { display: flex; align-items: center; gap: 12px; margin-bottom: 6px; }
.type-label { font-size: 0.75rem; font-weight: 800; text-transform: uppercase; color: #6366f1; letter-spacing: 0.05em; }
.status-badge { padding: 4px 12px; border-radius: 99px; font-size: 0.7rem; font-weight: 800; text-transform: uppercase; }
.status-badge.tc-good { background: #10b98122; color: #10b981; border: 1px solid #10b98144; }
.status-badge.tc-bad { background: #ef444422; color: #ef4444; border: 1px solid #ef444444; }
.status-badge.tc-warn { background: #f59e0b22; color: #f59e0b; border: 1px solid #f59e0b44; }

.tx-id-row { display: flex; align-items: center; gap: 10px; cursor: pointer; transition: opacity 0.2s; }
.tx-id-row:hover { opacity: 0.7; }
.tx-id-row h1 { margin: 0; font-size: 1.4rem; font-weight: 800; letter-spacing: -0.02em; max-width: 500px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.copy-icon { color: #64748b; }

.close-action {
  background: rgba(255, 255, 255, 0.05); border: none; width: 44px; height: 44px;
  border-radius: 50%; color: #64748b; cursor: pointer; display: flex; align-items: center; justify-content: center;
  transition: all 0.2s;
}
.close-action:hover { background: rgba(239, 68, 68, 0.1); color: #ef4444; transform: rotate(90deg); }

/* Info Matrix */
.info-matrix { display: grid; grid-template-columns: 1fr 1.5fr; gap: 24px; }
.info-card {
  background: rgba(15, 23, 42, 0.4); border: 1px solid rgba(255, 255, 255, 0.04);
  border-radius: 20px; padding: 24px; position: relative;
}
.card-title {
  font-size: 0.65rem; font-weight: 800; text-transform: uppercase; color: #475569;
  letter-spacing: 0.1em; margin-bottom: 20px; border-bottom: 1px solid rgba(255,255,255,0.03);
  padding-bottom: 10px;
}

.kv-list { display: flex; flex-direction: column; gap: 12px; }
.kv-item { display: flex; justify-content: space-between; align-items: center; }
.k { font-size: 0.8rem; color: #64748b; font-weight: 500; }
.v { font-size: 0.9rem; font-weight: 700; color: #f8fafc; }
.v.highlight { color: #818cf8; }

/* Flow Layout (Vertical) */
.flow-layout { display: flex; flex-direction: column; gap: 8px; }
.flow-node {
  display: flex; align-items: center; gap: 16px; padding: 12px 16px;
  background: rgba(255, 255, 255, 0.02); border-radius: 12px; cursor: pointer; transition: all 0.2s;
}
.flow-node:hover { background: rgba(99, 102, 241, 0.05); }
.node-icon {
  width: 32px; height: 32px; border-radius: 50%; display: flex; align-items: center; justify-content: center;
  font-weight: 800; font-size: 0.7rem;
}
.node-icon.from { background: #3b82f622; color: #3b82f6; }
.node-icon.to { background: #10b98122; color: #10b981; }
.node-icon.sys { background: #64748b22; color: #64748b; }

.node-data { display: flex; flex-direction: column; }
.node-data .label { font-size: 0.6rem; color: #475569; text-transform: uppercase; font-weight: 700; }
.node-data .val { font-size: 0.85rem; color: #e2e8f0; break-all: break-all; }

.flow-connector { display: flex; align-items: center; gap: 16px; padding: 4px 14px; }
.connector-line { width: 3px; height: 32px; background: linear-gradient(180deg, #6366f1, #10b981); border-radius: 2px; }
.value-tag { display: flex; flex-direction: column; }
.v-num { font-size: 1.1rem; font-weight: 800; color: #f59e0b; font-family: 'JetBrains Mono', monospace; line-height: 1; }
.v-symbol { font-size: 0.6rem; color: #475569; font-weight: 700; }

/* Details & Signature */
.payload-wrapper { background: rgba(0, 0, 0, 0.2); border-radius: 12px; }

.raw-data-wrapper {
  background: rgba(0, 0, 0, 0.3); border-radius: 12px; padding: 16px;
  border: 1px solid rgba(255, 255, 255, 0.05);
}
.data-meta { margin-bottom: 12px; }
.byte-count { font-size: 0.65rem; font-weight: 800; color: #475569; background: rgba(255,255,255,0.05); padding: 2px 8px; border-radius: 4px; }
.hex-scroll-box {
  font-size: 0.75rem; color: #94a3b8; line-height: 1.5; word-break: break-all;
  max-height: 200px; overflow-y: auto;
}
.empty-data-msg {
  font-size: 0.8rem;
  color: #475569;
  font-style: italic;
  padding: 8px 0;
}
.kv-list.horizontal { flex-direction: column; gap: 20px; }
.kv-item.full { flex-direction: column; align-items: flex-start; gap: 8px; }

/* Error UI */
.error-notification {
  background: rgba(239, 68, 68, 0.05); border: 1px solid rgba(239, 68, 68, 0.1); border-radius: 20px; padding: 24px;
}
.error-header { display: flex; align-items: center; gap: 12px; color: #ef4444; font-weight: 800; font-size: 0.9rem; margin-bottom: 12px; }
.error-body { color: #94a3b8; font-size: 0.85rem; line-height: 1.6; }

.mono { font-family: 'JetBrains Mono', monospace; }

@keyframes fadeIn { from { opacity: 0; transform: translateY(10px); } to { opacity: 1; transform: translateY(0); } }
.animate-fade-in { animation: fadeIn 0.4s ease-out; }

@media (max-width: 1024px) { .info-matrix { grid-template-columns: 1fr; } }

/* States & Helper */
.state-msg {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 80px 0;
  gap: 20px;
  color: #475569;
  font-weight: 500;
}

.spinner {
  width: 32px;
  height: 32px;
  border: 3px solid rgba(99, 102, 241, 0.1);
  border-top-color: #6366f1;
  border-radius: 50%;
  animation: spin 1s linear infinite;
}

@keyframes spin { to { transform: rotate(360deg); } }

.fade-enter-active, .fade-leave-active { transition: opacity 0.3s ease; }
.fade-enter-from, .fade-leave-to { opacity: 0; }

.error-msg-box { padding: 40px; }
.error-close-btn {
  margin-top: 20px;
  background: #ef4444;
  color: #fff;
  border: none;
  padding: 8px 24px;
  border-radius: 8px;
  font-weight: 700;
  cursor: pointer;
}

.custom-scrollbar::-webkit-scrollbar { width: 6px; }
.custom-scrollbar::-webkit-scrollbar-track { background: transparent; }
.custom-scrollbar::-webkit-scrollbar-thumb { background: rgba(255, 255, 255, 0.1); border-radius: 10px; }
.custom-scrollbar::-webkit-scrollbar-thumb:hover { background: rgba(255, 255, 255, 0.2); }
</style>
