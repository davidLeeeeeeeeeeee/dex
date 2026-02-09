<script setup lang="ts">
import { ref, watch, onMounted } from 'vue'
import type { AccountInfo, TxRecord } from '../types'
import { fetchTxHistory } from '../api'

const props = defineProps<{
  account: AccountInfo
}>()

const emit = defineEmits<{
  back: []
  txClick: [txId: string]
}>()

const txHistory = ref<TxRecord[]>([])
const txTotalCount = ref(0)
const txLoading = ref(false)
const txError = ref('')

async function loadTxHistory() {
  if (!props.account.address) return
  txLoading.value = true
  txError.value = ''
  try {
    const resp = await fetchTxHistory(props.account.address)
    txHistory.value = resp.txs || []
    txTotalCount.value = resp.total_count || 0
  } catch (e: any) {
    txError.value = e.message || 'Failed to load transaction history'
  } finally {
    txLoading.value = false
  }
}

onMounted(() => { loadTxHistory() })
watch(() => props.account.address, () => { loadTxHistory() })

function formatBalance(val?: string): string {
  if (!val) return '0'
  try {
    if (val.includes('.')) {
      return parseFloat(val).toLocaleString(undefined, { minimumFractionDigits: 0, maximumFractionDigits: 4 })
    }
    return BigInt(val).toLocaleString()
  } catch (e) {
    console.warn('formatBalance failed for:', val, e)
    return val
  }
}

function truncateHash(hash?: string, len: number = 8): string {
  if (!hash) return '-'
  if (hash.length <= len * 2 + 3) return hash
  return hash.slice(0, len) + '...' + hash.slice(-len)
}

function statusInfo(status?: string) {
  const s = (status || '').toUpperCase()
  if (s === 'SUCCEED' || s === 'SUCCESS') return { class: 'sc-good', label: 'Success' }
  if (s === 'FAILED' || s === 'FAIL') return { class: 'sc-bad', label: 'Failed' }
  return { class: 'sc-warn', label: 'Processing' }
}

// 解析 FB 余额 JSON 字符串
interface FBBalanceSnap {
  balance: string
  miner_locked?: string
  order_frozen?: string
  witness_locked?: string
  liquid_locked?: string
}

function parseFBBalanceObj(balStr?: string): FBBalanceSnap | null {
  if (!balStr) return null
  try {
    return JSON.parse(balStr) as FBBalanceSnap
  } catch {
    // 兼容旧格式（纯数字或“6 (miner:xxx)”格式）
    const numPart = balStr.split(' ')[0]
    return { balance: numPart }
  }
}

function parseFBBalance(balStr?: string): bigint | null {
  const obj = parseFBBalanceObj(balStr)
  if (!obj || !obj.balance) return null
  try {
    // 处理小数点的情况，只取整数部分
    const intPart = obj.balance.split('.')[0]
    return BigInt(intPart)
  } catch {
    return null
  }
}

// 计算余额变化的方向（与下一笔交易对比，因为列表是倒序的）
function balanceChangeClass(index: number): string {
  if (txHistory.value.length <= 1) return ''
  const current = parseFBBalance(txHistory.value[index]?.fb_balance_after)
  // 与下一条（更早的交易）对比
  const prev = index < txHistory.value.length - 1
    ? parseFBBalance(txHistory.value[index + 1]?.fb_balance_after)
    : null
  if (current === null || prev === null) return ''
  if (current > prev) return 'bal-up'
  if (current < prev) return 'bal-down'
  return ''
}

function getFBBalanceDisplay(balStr?: string): string {
  const obj = parseFBBalanceObj(balStr)
  if (!obj) return '-'
  return formatBalance(obj.balance)
}

function getFBBalanceTooltip(balStr?: string): string {
  const obj = parseFBBalanceObj(balStr)
  if (!obj) return ''
  const parts: string[] = []
  if (obj.miner_locked && obj.miner_locked !== '0') parts.push(`Miner: ${formatBalance(obj.miner_locked)}`)
  if (obj.order_frozen && obj.order_frozen !== '0') parts.push(`Order: ${formatBalance(obj.order_frozen)}`)
  if (obj.witness_locked && obj.witness_locked !== '0') parts.push(`Witness: ${formatBalance(obj.witness_locked)}`)
  if (obj.liquid_locked && obj.liquid_locked !== '0') parts.push(`Liquid: ${formatBalance(obj.liquid_locked)}`)
  return parts.length > 0 ? `Locked: ${parts.join(', ')}` : ''
}
</script>

<template>
  <div class="account-view animate-fade-in">
    <!-- Action Header -->
    <div class="action-header">
      <button class="back-btn" @click="emit('back')">
        <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="m15 18-6-6 6-6"/></svg>
        Back
      </button>
      <div class="header-main">
        <div class="account-orb">
          <svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M19 21v-2a4 4 0 0 0-4-4H9a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/></svg>
        </div>
        <div class="text-group">
          <h1>Address Details</h1>
          <p class="mono text-indigo-400">{{ account.address }}</p>
        </div>
      </div>
    </div>

    <div class="account-grid">
      <!-- Summary Card -->
      <section class="panel summary-panel">
        <div class="section-title">
          <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" x2="12" y1="8" y2="12"/><line x1="12" x2="12.01" y1="16" y2="16"/></svg>
          Overview
        </div>
        <div class="profile-rows">
          <div class="p-row">
             <span class="p-label">Nonce</span>
             <span class="p-val">{{ account.nonce ?? 0 }}</span>
          </div>
          <div class="p-row">
             <span class="p-label">Is Miner</span>
             <span :class="['p-val', account.is_miner ? 'text-emerald-400' : 'text-gray-500']">
               {{ account.is_miner ? 'Yes' : 'No' }}
             </span>
          </div>
          <div v-if="account.index" class="p-row">
             <span class="p-label">Miner Index</span>
             <span class="p-val mono"># {{ account.index }}</span>
          </div>
          <div v-if="account.unclaimed_reward" class="p-row">
             <span class="p-label">Unclaimed Reward</span>
             <span class="p-val text-amber-500 font-bold">{{ formatBalance(account.unclaimed_reward) }}</span>
          </div>
        </div>
      </section>

      <!-- Token Balances -->
      <section class="panel balance-panel">
        <div class="section-title">
          <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2v20"/><path d="M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6"/></svg>
          Token Balances
        </div>
        <div class="balance-grid">
          <div v-for="(bal, token) in account.balances" :key="token" class="balance-card">
            <div class="b-header">
               <span class="token-name">{{ token }}</span>
               <div class="b-dot" :style="{ background: token === 'FB' ? '#6366f1' : '#f59e0b' }"></div>
            </div>
            <div class="b-amount">{{ formatBalance(bal.balance) }}</div>
            <div class="b-locked-grid">
               <div class="l-item"><span>Miner Locked</span><b>{{ formatBalance(bal.miner_locked_balance) }}</b></div>
               <div class="l-item"><span>Witness Locked</span><b>{{ formatBalance(bal.witness_locked_balance) }}</b></div>
               <div class="l-item"><span>Liquid Locked</span><b>{{ formatBalance(bal.liquid_locked_balance) }}</b></div>
            </div>
          </div>
          <div v-if="!account.balances || Object.keys(account.balances).length === 0" class="empty-state-mini">
             No token balances found.
          </div>
        </div>
      </section>
    </div>

    <!-- Transaction History -->
    <section class="panel history-panel">
      <div class="panel-header-sub">
        <div class="title-meta">
          <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M3 21v-5h5"/></svg>
          <h3>Transaction History</h3>
          <span class="count-tag">{{ txTotalCount }} Txs</span>
        </div>
        <button class="sync-btn" @click="loadTxHistory" :disabled="txLoading">
          <svg :class="{ 'spin': txLoading }" xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M3 21v-5h5"/></svg>
        </button>
      </div>

      <div class="table-wrap">
        <table class="premium-table">
          <thead>
            <tr>
              <th class="pl-6 w-32">Tx Hash</th>
              <th>Type</th>
              <th>Block</th>
              <th>From / To</th>
              <th class="text-right">Amount</th>
              <th class="text-right">FB Balance</th>
              <th class="text-right pr-6">Status</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(tx, idx) in txHistory" :key="tx.tx_id" @click="emit('txClick', tx.tx_id)" class="table-row">
              <td class="pl-6">
                <code class="mono text-indigo-400">{{ truncateHash(tx.tx_id, 6) }}</code>
              </td>
              <td><span class="type-pill">{{ tx.tx_type }}</span></td>
              <td><span class="mono text-gray-500" style="font-size: 0.7rem">#{{ tx.height }}</span></td>
              <td>
                <div class="counterparty">
                  <div class="dir-icon" :class="tx.from_address === account.address ? 'out' : 'in'">
                    <svg v-if="tx.from_address === account.address" xmlns="http://www.w3.org/2000/svg" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12h14"/><path d="m12 5 7 7-7 7"/></svg>
                    <svg v-else xmlns="http://www.w3.org/2000/svg" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><path d="M19 12H5"/><path d="m12 19-7-7 7-7"/></svg>
                  </div>
                  <code class="mono text-gray-500">
                    {{ tx.from_address === account.address ? truncateHash(tx.to_address, 6) : truncateHash(tx.from_address, 6) }}
                  </code>
                </div>
              </td>
              <td class="text-right">
                <span class="mono font-bold">{{ tx.value || '0' }}</span>
              </td>
              <td class="text-right">
                <div v-if="tx.fb_balance_after" :class="['fb-bal', balanceChangeClass(idx)]" :title="getFBBalanceTooltip(tx.fb_balance_after)">
                  {{ getFBBalanceDisplay(tx.fb_balance_after) }}
                  <span v-if="balanceChangeClass(idx) === 'bal-up'" class="change-arrow">↑</span>
                  <span v-else-if="balanceChangeClass(idx) === 'bal-down'" class="change-arrow">↓</span>
                </div>
                <span v-else class="text-gray-600">-</span>
              </td>
              <td class="text-right pr-6">
                <div :class="['status-chip', statusInfo(tx.status).class]">
                  {{ statusInfo(tx.status).label }}
                </div>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-if="txHistory.length === 0 && !txLoading" class="empty-state-mini py-20">
           No transactions found for this address.
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
@import url('https://fonts.googleapis.com/css2?family=Outfit:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap');

.account-view { display: flex; flex-direction: column; gap: 32px; font-family: 'Outfit', sans-serif; }

.action-header { display: flex; flex-direction: column; gap: 20px; }

.back-btn {
  width: fit-content; display: flex; align-items: center; gap: 8px;
  background: rgba(255, 255, 255, 0.03); border: 1px solid rgba(255, 255, 255, 0.05);
  padding: 8px 16px; border-radius: 10px; color: #64748b; font-size: 0.75rem;
  font-weight: 700; cursor: pointer; transition: all 0.3s;
}
.back-btn:hover { background: rgba(255, 255, 255, 0.05); color: #fff; transform: translateX(-4px); }

.header-main { display: flex; align-items: center; gap: 24px; }
.account-orb {
  width: 64px; height: 64px; background: linear-gradient(135deg, #6366f1, #3b82f6);
  border-radius: 20px; display: flex; align-items: center; justify-content: center;
  box-shadow: 0 10px 30px rgba(99, 102, 241, 0.3); color: #fff;
}
.text-group h1 { margin: 0; font-size: 1.8rem; font-weight: 800; letter-spacing: -0.03em; }
.text-group p { margin: 4px 0 0; font-size: 0.9rem; }

.account-grid { display: grid; grid-template-columns: 1fr 2fr; gap: 24px; }

.section-title {
  display: flex; align-items: center; gap: 10px; font-size: 0.65rem;
  font-weight: 800; text-transform: uppercase; color: #475569; letter-spacing: 0.05em;
  margin-bottom: 24px; border-bottom: 1px solid rgba(255,255,255,0.03); padding-bottom: 12px;
}

.profile-rows { display: flex; flex-direction: column; gap: 16px; }
.p-row { display: flex; justify-content: space-between; align-items: center; }
.p-label { font-size: 0.8rem; color: #64748b; font-weight: 500; }
.p-val { font-size: 0.9rem; font-weight: 700; color: #e2e8f0; }

.balance-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(240px, 1fr)); gap: 16px; }
.balance-card {
  background: rgba(0, 0, 0, 0.2); border: 1px solid rgba(255, 255, 255, 0.05);
  border-radius: 16px; padding: 20px;
}
.b-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
.token-name { font-weight: 800; font-size: 0.8rem; color: #64748b; }
.b-dot { width: 8px; height: 8px; border-radius: 50%; }
.b-amount { font-size: 1.5rem; font-weight: 800; color: #fff; margin-bottom: 16px; font-family: 'JetBrains Mono', monospace; }

.b-locked-grid { display: flex; flex-direction: column; gap: 6px; }
.l-item { display: flex; justify-content: space-between; font-size: 0.65rem; }
.l-item span { color: #475569; }
.l-item b { color: #94a3b8; font-family: 'JetBrains Mono', monospace; }

.panel-header-sub { display: flex; justify-content: space-between; align-items: center; margin-bottom: 24px; }
.title-meta { display: flex; align-items: center; gap: 12px; }
.title-meta h3 { margin: 0; font-size: 1.1rem; color: #fff; }
.count-tag { font-size: 0.65rem; font-weight: 800; background: rgba(99, 102, 241, 0.1); color: #818cf8; padding: 2px 8px; border-radius: 6px; }

.sync-btn {
  background: none; border: none; color: #475569; cursor: pointer; transition: color 0.3s;
}
.sync-btn:hover { color: #6366f1; }

.table-wrap { overflow: hidden; border-radius: 16px; }
.premium-table { width: 100%; border-collapse: collapse; }
.premium-table th {
  padding: 16px; text-align: left; font-size: 0.6rem; font-weight: 800;
  text-transform: uppercase; color: #475569; background: rgba(0, 0, 0, 0.1);
}
.table-row { border-bottom: 1px solid rgba(255,255,255,0.02); cursor: pointer; transition: all 0.2s; }
.table-row:hover { background: rgba(99, 102, 241, 0.03); }

.type-pill { font-size: 0.6rem; font-weight: 800; background: rgba(255,255,255,0.03); padding: 2px 8px; border-radius: 4px; color: #94a3b8; }

.counterparty { display: flex; align-items: center; gap: 12px; }
.dir-icon {
  width: 20px; height: 20px; border-radius: 50%; display: flex; align-items: center; justify-content: center;
}
.dir-icon.out { background: rgba(239, 68, 68, 0.1); color: #ef4444; }
.dir-icon.in { background: rgba(16, 185, 129, 0.1); color: #10b981; }

.status-chip { font-size: 0.65rem; font-weight: 800; display: inline-block; padding: 2px 10px; border-radius: 99px; }
.status-chip.sc-good { background: rgba(16, 185, 129, 0.1); color: #10b981; }
.status-chip.sc-bad { background: rgba(239, 68, 68, 0.1); color: #ef4444; }
.status-chip.sc-warn { background: rgba(245, 158, 11, 0.1); color: #f59e0b; }

.empty-state-mini { text-align: center; padding: 40px 0; color: #334155; }
.mono { font-family: 'JetBrains Mono', monospace; }

.fb-bal { font-family: 'JetBrains Mono', monospace; font-size: 0.75rem; font-weight: 600; color: #94a3b8; white-space: nowrap; }
.fb-bal.bal-up { color: #10b981; }
.fb-bal.bal-down { color: #ef4444; }
.change-arrow { font-size: 0.65rem; margin-left: 2px; }

.spin { animation: spin 1s linear infinite; }
@keyframes spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }
@keyframes fadeIn { from { opacity: 0; transform: translateY(10px); } to { opacity: 1; transform: translateY(0); } }

@media (max-width: 1024px) { .account-grid { grid-template-columns: 1fr; } }
</style>
