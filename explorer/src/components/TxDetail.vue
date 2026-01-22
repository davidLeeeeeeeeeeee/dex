<script setup lang="ts">
import type { TxInfo } from '../types'
import TxTypeRenderer from './TxTypeRenderer.vue'

defineProps<{
  tx: TxInfo
}>()

const emit = defineEmits<{
  back: []
  addressClick: [address: string]
}>()

const numberFormat = new Intl.NumberFormat('en-US')

function formatNumber(value?: number | null): string {
  if (value === undefined || value === null) return '-'
  return numberFormat.format(value)
}

function statusInfo(status?: string) {
  const s = (status || '').toUpperCase()
  if (s === 'SUCCEED' || s === 'SUCCESS') return { class: 'tc-good', label: 'Confirmed', icon: 'check' }
  if (s === 'FAILED' || s === 'FAIL') return { class: 'tc-bad', label: 'Reverted', icon: 'error' }
  return { class: 'tc-warn', label: 'Processing', icon: 'clock' }
}
</script>

<template>
  <div class="tx-view animate-fade-in">
    <!-- Action Header -->
    <div class="action-header">
      <button class="back-btn" @click="emit('back')">
        <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="m15 18-6-6 6-6"/></svg>
        Return to Source
      </button>
      <div class="header-main">
        <div class="tx-orb">
          <svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M7 11V7a5 5 0 0 1 10 0v4"/><rect width="18" height="11" x="3" y="11" rx="2" ry="2"/></svg>
        </div>
        <div class="text-group">
          <h1>Transaction Blueprint</h1>
          <p class="mono text-indigo-400">{{ tx.tx_id }}</p>
        </div>
        <div :class="['status-master-badge', statusInfo(tx.status).class]">
           <svg v-if="statusInfo(tx.status).icon === 'check'" xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>
           <svg v-else-if="statusInfo(tx.status).icon === 'error'" xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
           <svg v-else xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/></svg>
           {{ statusInfo(tx.status).label }}
        </div>
      </div>
    </div>

    <div class="tx-details-grid">
      <!-- Core Ledger Info -->
      <section class="panel ledger-panel">
        <div class="section-title">
          <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><rect width="18" height="18" x="3" y="3" rx="2" ry="2"/><line x1="3" x2="21" y1="9" y2="9"/><line x1="15" x2="15" y1="9" y2="21"/></svg>
          Ledger Entry
        </div>
        <div class="ledger-rows">
          <div class="l-row">
             <span class="l-label">Sequence Nonce</span>
             <span class="l-val mono"># {{ tx.nonce }}</span>
          </div>
          <div class="l-row">
             <span class="l-label">Protocol Type</span>
             <span class="type-badge">{{ tx.tx_type || 'Unknown' }}</span>
          </div>
          <div class="l-row">
             <span class="l-label">Block Origin</span>
             <span class="l-val text-indigo-400 font-bold"># {{ formatNumber(tx.executed_height) }}</span>
          </div>
          <div class="l-row">
             <span class="l-label">Service Fee</span>
             <span class="l-val text-gray-400 font-bold">{{ tx.fee || '0.00' }} Gas</span>
          </div>
        </div>
      </section>

      <!-- Flow Trace -->
      <section class="panel flow-panel">
        <div class="section-title">
          <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 12h20"/><path d="m15 5 7 7-7 7"/></svg>
          Resource Transfer
        </div>
        <div class="flow-visualization">
          <div class="node-point from" @click="emit('addressClick', tx.from_address!)">
             <span class="n-tag">SENDER / PROPOSER</span>
             <code class="n-addr mono">{{ tx.from_address || '-' }}</code>
          </div>
          <div class="flow-arrow">
             <div class="arrow-line"></div>
             <div class="value-bubble">{{ tx.value || 'NO VAL' }}</div>
          </div>
          <div class="node-point to" v-if="tx.to_address" @click="emit('addressClick', tx.to_address)">
             <span class="n-tag">RECIPIENT / CONTRACT</span>
             <code class="n-addr mono">{{ tx.to_address }}</code>
          </div>
          <div v-else class="node-point contract">
             <span class="n-tag">SYSTEM TARGET</span>
             <code class="n-addr">Contract Deployment / System Op</code>
          </div>
        </div>
      </section>
    </div>

    <!-- Payload Details -->
    <section v-if="tx.details && Object.keys(tx.details).length > 0" class="panel payload-panel">
      <div class="section-title">
        <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/></svg>
        Structured Decoded Payload
      </div>
      <div class="decoded-content">
        <TxTypeRenderer :type="tx.tx_type || ''" :details="tx.details" />
      </div>
    </section>

    <!-- Error Trace -->
    <div v-if="tx.error" class="panel error-panel">
      <div class="section-title text-red-500">
        <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z"/><line x1="12" x2="12" y1="9" y2="13"/><line x1="12" x2="12.01" y1="17" y2="17"/></svg>
        Execution Fault Detected
      </div>
      <div class="error-slate-box">
         <div class="error-code">REVERT_DURING_VM_INVOCATION</div>
         <p class="error-desc">{{ tx.error }}</p>
      </div>
    </div>
  </div>
</template>

<style scoped>
@import url('https://fonts.googleapis.com/css2?family=Outfit:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap');

.tx-view { display: flex; flex-direction: column; gap: 32px; font-family: 'Outfit', sans-serif; }

.action-header { display: flex; flex-direction: column; gap: 20px; }

.back-btn {
  width: fit-content; display: flex; align-items: center; gap: 8px;
  background: rgba(255, 255, 255, 0.03); border: 1px solid rgba(255, 255, 255, 0.05);
  padding: 8px 16px; border-radius: 10px; color: #64748b; font-size: 0.75rem;
  font-weight: 700; cursor: pointer; transition: all 0.3s;
}
.back-btn:hover { background: rgba(255, 255, 255, 0.05); color: #fff; transform: translateX(-4px); }

.header-main { display: flex; align-items: center; gap: 24px; position: relative; }
.tx-orb {
  width: 64px; height: 64px; background: linear-gradient(135deg, #6366f1, #a855f7);
  border-radius: 20px; display: flex; align-items: center; justify-content: center;
  box-shadow: 0 10px 30px rgba(99, 102, 241, 0.3); color: #fff;
}
.text-group h1 { margin: 0; font-size: 1.8rem; font-weight: 800; letter-spacing: -0.03em; }
.text-group p { margin: 4px 0 0; font-size: 0.9rem; max-width: 600px; word-break: break-all; }

.status-master-badge {
  display: flex; align-items: center; gap: 10px; padding: 6px 16px; border-radius: 99px; font-size: 0.8rem; font-weight: 800;
  text-transform: uppercase; letter-spacing: 0.05em;
}
.status-master-badge.tc-good { background: #10b98122; color: #10b981; border: 1px solid #10b98133; }
.status-master-badge.tc-bad { background: #ef444422; color: #ef4444; border: 1px solid #ef444433; }
.status-master-badge.tc-warn { background: #f59e0b22; color: #f59e0b; border: 1px solid #f59e0b33; }

.tx-details-grid { display: grid; grid-template-columns: 1fr 2fr; gap: 24px; }

.section-title {
  display: flex; align-items: center; gap: 10px; font-size: 0.65rem;
  font-weight: 800; text-transform: uppercase; color: #475569; letter-spacing: 0.05em;
  margin-bottom: 24px; border-bottom: 1px solid rgba(255,255,255,0.03); padding-bottom: 12px;
}

.ledger-rows { display: flex; flex-direction: column; gap: 16px; }
.l-row { display: flex; justify-content: space-between; align-items: center; }
.l-label { font-size: 0.8rem; color: #64748b; font-weight: 500; }
.l-val { font-size: 0.9rem; font-weight: 700; color: #e2e8f0; }
.type-badge { background: rgba(99, 102, 241, 0.1); color: #818cf8; padding: 4px 12px; border-radius: 8px; font-size: 0.75rem; font-weight: 800; }

.flow-visualization { display: flex; flex-direction: column; gap: 32px; padding: 20px; background: rgba(0, 0, 0, 0.1); border-radius: 20px; }
.node-point {
  display: flex; flex-direction: column; gap: 8px; cursor: pointer; padding: 16px;
  background: rgba(255, 255, 255, 0.02); border: 1px solid rgba(255, 255, 255, 0.05); border-radius: 12px;
  transition: all 0.3s;
}
.node-point:hover { background: rgba(99, 102, 241, 0.05); border-color: rgba(99, 102, 241, 0.2); transform: translateY(-2px); }
.n-tag { font-size: 0.6rem; font-weight: 800; color: #475569; }
.n-addr { font-size: 0.9rem; color: #e2e8f0; word-break: break-all; }

.flow-arrow { display: flex; align-items: center; gap: 16px; padding: 0 40px; position: relative; }
.arrow-line { height: 2px; background: linear-gradient(90deg, #6366f1, #a855f7); flex: 1; border-radius: 1px; position: relative; }
.arrow-line::after { content: ''; position: absolute; right: -2px; top: -4px; border-left: 6px solid #a855f7; border-top: 5px solid transparent; border-bottom: 5px solid transparent; }
.value-bubble {
  background: #1e1b4b; border: 1px solid #6366f1; color: #fff; padding: 6px 16px; border-radius: 99px;
  font-size: 1rem; font-weight: 800; font-family: 'JetBrains Mono', monospace;
  box-shadow: 0 0 20px rgba(99, 102, 241, 0.2);
}

.decoded-content { background: rgba(0, 0, 0, 0.2); border-radius: 16px; padding: 24px; }

.error-slate-box { background: rgba(239, 68, 68, 0.05); border: 1px solid rgba(239, 68, 68, 0.1); border-radius: 16px; padding: 24px; }
.error-code { font-family: 'JetBrains Mono', monospace; font-size: 0.75rem; font-weight: 700; color: #ef4444; margin-bottom: 12px; }
.error-desc { font-size: 0.9rem; color: #94a3b8; line-height: 1.6; }

.mono { font-family: 'JetBrains Mono', monospace; }
@keyframes fadeIn { from { opacity: 0; transform: translateY(10px); } to { opacity: 1; transform: translateY(0); } }
.animate-fade-in { animation: fadeIn 0.4s ease-out; }

@media (max-width: 1024px) { .tx-details-grid { grid-template-columns: 1fr; } }
</style>
