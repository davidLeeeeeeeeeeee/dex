<script setup lang="ts">
import { ref, watch, onMounted, computed } from 'vue'
import { wallet } from '../walletStore'
import { fetchWitnessRequests } from '../api'
import type { WitnessRequest } from '../types'

const props = defineProps<{ node: string }>()

const requests = ref<WitnessRequest[]>([])
const loading = ref(false)
const error = ref('')
const submitting = ref<Record<string, boolean>>({})
const txResult = ref<Record<string, string>>({})

// 过滤：仅显示分配给当前钱包地址且处于待投票状态的见证单
const myRequests = computed(() =>
  requests.value.filter((r: WitnessRequest) => {
    const s = String(r.status || '').toUpperCase()
    const isPending = s.includes('PENDING') || s.includes('VOTING')
    if (!isPending) return false
    if (!wallet.connected) return false
    return (r.selected_witnesses || []).includes(wallet.address)
  })
)

async function loadRequests() {
  loading.value = true
  error.value = ''
  try { requests.value = await fetchWitnessRequests(props.node) }
  catch (e: any) { error.value = e.message || 'Failed to load' }
  finally { loading.value = false }
}

watch(() => props.node, loadRequests)
watch(() => wallet.address, loadRequests)
onMounted(loadRequests)

function truncate(s = '', a = 8, b = 6) {
  return s.length <= a + b ? s : `${s.slice(0, a)}…${s.slice(-b)}`
}

function formatAmount(v: string) {
  const n = parseFloat(v)
  return isNaN(n) ? v : n.toLocaleString(undefined, { maximumFractionDigits: 4 })
}

async function vote(req: WitnessRequest, voteType: 'PASS' | 'FAIL') {
  if (!window.frostbit) return alert('请先安装 FrostBit Wallet 扩展')
  if (!wallet.connected) return alert('请先连接钱包')
  const rid = req.request_id || ''
  submitting.value[rid + voteType] = true
  txResult.value[rid] = ''
  try {
    const res = await window.frostbit.sendTransaction({
      type: 'witness_vote',
      requestId: rid,
      vote: voteType,
    })
    txResult.value[rid] = `投票已广播${res?.txId ? '，TxID: ' + res.txId : ''}`
    setTimeout(loadRequests, 2000)
  } catch (e: any) {
    txResult.value[rid] = '错误: ' + (e.message || e)
  } finally {
    submitting.value[rid + voteType] = false
  }
}
</script>

<template>
  <div class="miner-panel">
    <header class="mp-header">
      <div class="mp-title-group">
        <div class="icon-orb amber">
          <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 2L2 7l10 5 10-5-10-5z"/><path d="M2 17l10 5 10-5"/><path d="M2 12l10 5 10-5"/></svg>
        </div>
        <div>
          <h3 class="mp-title">Witness Miner</h3>
          <p class="mp-sub">分配给你的待处理见证单</p>
        </div>
      </div>
      <button class="glass-btn" @click="loadRequests" :disabled="loading">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" :class="{ spin: loading }"><path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M3 21v-5h5"/></svg>
        刷新
      </button>
    </header>

    <!-- 未连接 -->
    <div v-if="!wallet.connected" class="notice amber">
      <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>
      请先连接钱包，以查看分配给你的见证单
    </div>

    <div v-else-if="error" class="notice red">{{ error }}</div>

    <!-- 无待处理 -->
    <div v-else-if="myRequests.length === 0 && !loading" class="empty-state">
      <svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1"><path d="M12 2L2 7l10 5 10-5-10-5z"/><path d="M2 17l10 5 10-5"/></svg>
      <p>暂无分配给 <span class="mono">{{ wallet.address.slice(0,12) }}…</span> 的待处理见证单</p>
    </div>

    <div v-else class="req-list">
      <div v-for="req in myRequests" :key="req.request_id" class="req-card">
        <div class="req-top">
          <span class="req-id mono">{{ truncate(req.request_id, 12, 8) }}</span>
          <span class="status-pill voting">{{ String(req.status).replace('RECHARGE_','').replace(/_/g,' ') }}</span>
        </div>
        <div class="req-info">
          <div class="info-item"><span class="lbl">链</span><span class="mono">{{ req.native_chain || '-' }}</span></div>
          <div class="info-item"><span class="lbl">金额</span><span class="amber-val">{{ formatAmount(req.amount || '0') }}</span></div>
          <div class="info-item"><span class="lbl">接收地址</span><span class="mono">{{ truncate(req.receiver_address) }}</span></div>
          <div class="info-item"><span class="lbl">票数</span><span>✅ {{ req.pass_count || 0 }} / ❌ {{ req.fail_count || 0 }}</span></div>
        </div>

        <div class="vote-actions">
          <button class="vote-btn pass" @click="vote(req, 'PASS')"
            :disabled="submitting[req.request_id + 'PASS'] || submitting[req.request_id + 'FAIL']">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><polyline points="20 6 9 17 4 12"/></svg>
            批准
          </button>
          <button class="vote-btn fail" @click="vote(req, 'FAIL')"
            :disabled="submitting[req.request_id + 'PASS'] || submitting[req.request_id + 'FAIL']">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
            拒签
          </button>
        </div>
        <div v-if="txResult[req.request_id]" class="tx-result" :class="{ error: txResult[req.request_id].startsWith('错误') }">
          {{ txResult[req.request_id] }}
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
@import url('https://fonts.googleapis.com/css2?family=Outfit:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap');
.miner-panel { font-family: 'Outfit', sans-serif; color: #e2e8f0; }
.mp-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 28px; }
.mp-title-group { display: flex; align-items: center; gap: 16px; }
.icon-orb { width: 48px; height: 48px; border-radius: 14px; display: flex; align-items: center; justify-content: center; }
.icon-orb.amber { background: rgba(245,158,11,0.12); border: 1px solid rgba(245,158,11,0.3); color: #fbbf24; }
.mp-title { font-size: 1.25rem; font-weight: 700; margin: 0; color: #fff; }
.mp-sub { font-size: 0.8rem; color: #64748b; margin: 2px 0 0; }
.glass-btn { background: rgba(255,255,255,0.05); border: 1px solid rgba(255,255,255,0.1); color: #fff; padding: 8px 16px; border-radius: 10px; display: flex; align-items: center; gap: 8px; font-weight: 600; font-size: 0.85rem; cursor: pointer; transition: all 0.2s; }
.glass-btn:hover:not(:disabled) { background: rgba(255,255,255,0.1); }
.glass-btn:disabled { opacity: 0.5; cursor: not-allowed; }
.notice { display: flex; align-items: center; gap: 12px; padding: 16px 20px; border-radius: 14px; font-size: 0.9rem; }
.notice.amber { background: rgba(245,158,11,0.08); border: 1px solid rgba(245,158,11,0.2); color: #fbbf24; }
.notice.red { background: rgba(239,68,68,0.08); border: 1px solid rgba(239,68,68,0.2); color: #f87171; }
.empty-state { text-align: center; padding: 60px 20px; color: #475569; }
.empty-state svg { margin: 0 auto 16px; display: block; }
.empty-state p { font-size: 0.9rem; }
.req-list { display: flex; flex-direction: column; gap: 16px; }
.req-card { background: rgba(30,41,59,0.4); border: 1px solid rgba(255,255,255,0.06); border-radius: 16px; padding: 20px; transition: border-color 0.2s; }
.req-card:hover { border-color: rgba(245,158,11,0.3); }
.req-top { display: flex; align-items: center; gap: 12px; margin-bottom: 16px; }
.req-id { font-family: 'JetBrains Mono', monospace; font-size: 0.85rem; color: #818cf8; }
.status-pill { padding: 3px 10px; border-radius: 99px; font-size: 0.65rem; font-weight: 800; text-transform: uppercase; }
.status-pill.voting { background: rgba(99,102,241,0.1); color: #818cf8; border: 1px solid rgba(99,102,241,0.2); }
.req-info { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; margin-bottom: 18px; background: rgba(0,0,0,0.2); padding: 14px; border-radius: 10px; }
.info-item { display: flex; flex-direction: column; gap: 4px; }
.lbl { font-size: 0.6rem; font-weight: 700; color: #475569; text-transform: uppercase; }
.mono { font-family: 'JetBrains Mono', monospace; font-size: 0.8rem; color: #94a3b8; }
.amber-val { font-family: 'JetBrains Mono', monospace; font-size: 0.9rem; color: #fbbf24; font-weight: 700; }
.vote-actions { display: flex; gap: 12px; }
.vote-btn { flex: 1; padding: 10px; border: none; border-radius: 10px; font-size: 0.85rem; font-weight: 700; display: flex; align-items: center; justify-content: center; gap: 8px; cursor: pointer; transition: all 0.2s; }
.vote-btn:disabled { opacity: 0.4; cursor: not-allowed; }
.vote-btn.pass { background: rgba(16,185,129,0.12); color: #34d399; border: 1px solid rgba(16,185,129,0.25); }
.vote-btn.pass:hover:not(:disabled) { background: #10b981; color: #fff; }
.vote-btn.fail { background: rgba(239,68,68,0.1); color: #f87171; border: 1px solid rgba(239,68,68,0.2); }
.vote-btn.fail:hover:not(:disabled) { background: #ef4444; color: #fff; }
.tx-result { margin-top: 10px; font-size: 0.78rem; color: #34d399; padding: 8px 12px; background: rgba(16,185,129,0.08); border-radius: 8px; }
.tx-result.error { color: #f87171; background: rgba(239,68,68,0.08); }
@keyframes spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }
.spin { animation: spin 1s linear infinite; }
</style>
