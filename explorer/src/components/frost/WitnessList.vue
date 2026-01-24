<script setup lang="ts">
import { ref, onMounted, watch, computed } from 'vue'
import { fetchWitnessList } from '../../api'
import type { WitnessInfo } from '../../types'

const props = defineProps<{
  node: string
}>()

const witnesses = ref<WitnessInfo[]>([])
const loading = ref(false)
const error = ref('')

const loadWitnesses = async () => {
  loading.value = true
  error.value = ''
  try {
    witnesses.value = await fetchWitnessList(props.node)
  } catch (e: any) {
    error.value = e.message || 'Failed to load'
    console.error('Failed to load witness list', e)
  } finally {
    loading.value = false
  }
}

watch(() => props.node, loadWitnesses)
onMounted(loadWitnesses)

function formatStake(amount: string): string {
  if (!amount || amount === '0') return '0'
  // 暂时移除 1e18 除法，因为测试环境使用较小数值
  const num = parseFloat(amount)
  return num.toLocaleString(undefined, { minimumFractionDigits: 0, maximumFractionDigits: 2 })
}

function getStatusClass(status: string): string {
  if (!status) return ''
  const s = status.toUpperCase()
  if (s.includes('ACTIVE')) return 'active'
  if (s.includes('CANDIDATE')) return 'candidate'
  if (s.includes('EXITING')) return 'exiting'
  if (s.includes('SLASHED')) return 'slashed'
  return ''
}

function getStatusLabel(status: string): string {
  if (!status) return 'Unknown'
  return status.replace('WITNESS_', '').replace(/_/g, ' ')
}

const stats = computed(() => ({
  total: witnesses.value.length,
  active: witnesses.value.filter(w => String(w.status || '').includes('ACTIVE')).length,
  candidates: witnesses.value.filter(w => String(w.status || '').includes('CANDIDATE')).length,
  totalStake: witnesses.value.reduce((sum, w) => sum + parseFloat(w.stake_amount || '0'), 0)
}))
</script>

<template>
  <div class="witness-list-dashboard">
    <header class="header-section">
      <div class="header-left">
        <div class="icon-orb purple">
          <svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/></svg>
        </div>
        <div class="title-meta">
          <h3 class="premium-title">Witness Registry</h3>
          <p class="premium-subtitle">Registered witnesses for cross-chain verification</p>
        </div>
      </div>
      <button @click="loadWitnesses" class="glass-btn primary" :disabled="loading">
        <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a9 9 0 0 0-9-9 9.75 9.75 0 0 0-6.74 2.74L3 8"/><path d="M3 3v5h5"/><path d="M3 12a9 9 0 0 0 9 9 9.75 9.75 0 0 0 6.74-2.74L21 16"/><path d="M16 16h5v5"/></svg>
        {{ loading ? 'Loading...' : 'Refresh' }}
      </button>
    </header>

    <!-- Stats -->
    <div class="stats-matrix">
      <div class="matrix-card">
        <span class="matrix-label">Total Witnesses</span>
        <span class="matrix-value">{{ stats.total }}</span>
      </div>
      <div class="matrix-card emerald">
        <span class="matrix-label">Active</span>
        <span class="matrix-value">{{ stats.active }}</span>
      </div>
      <div class="matrix-card amber">
        <span class="matrix-label">Candidates</span>
        <span class="matrix-value">{{ stats.candidates }}</span>
      </div>
      <div class="matrix-card purple">
        <span class="matrix-label">Total Staked</span>
        <span class="matrix-value">{{ formatStake(stats.totalStake.toString()) }}</span>
      </div>
    </div>

    <!-- Error -->
    <div v-if="error" class="error-banner">{{ error }}</div>

    <!-- Witnesses Table -->
    <div v-if="witnesses.length > 0" class="witnesses-table-wrap">
      <table class="witnesses-table">
        <thead>
          <tr>
            <th>Address</th>
            <th>Status</th>
            <th class="right">Stake</th>
            <th class="right">Pass / Fail</th>
            <th class="right">Pending Reward</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="w in witnesses" :key="w.address">
            <td class="mono">{{ w.address.slice(0, 12) }}...{{ w.address.slice(-6) }}</td>
            <td><span :class="['status-pill', getStatusClass(w.status)]">{{ getStatusLabel(w.status) }}</span></td>
            <td class="right mono">{{ formatStake(w.stake_amount) }}</td>
            <td class="right"><span class="pass">{{ w.pass_count || 0 }}</span> / <span class="fail">{{ w.fail_count || 0 }}</span></td>
            <td class="right mono">{{ formatStake(w.pending_reward) }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Empty State -->
    <div v-else-if="!loading" class="empty-state">
      <div class="empty-orb purple">
        <svg xmlns="http://www.w3.org/2000/svg" width="40" height="40" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/></svg>
      </div>
      <h3>No witnesses registered</h3>
      <p>No witnesses have staked yet. Stake tokens to become a witness.</p>
    </div>
  </div>
</template>

<style scoped>
@import url('https://fonts.googleapis.com/css2?family=Outfit:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap');

.witness-list-dashboard {
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
  width: 56px; height: 56px;
  border-radius: 18px;
  display: flex; align-items: center; justify-content: center;
  box-shadow: 0 10px 30px rgba(0, 0, 0, 0.1);
}
.icon-orb.purple {
  background: linear-gradient(135deg, rgba(139, 92, 246, 0.2), rgba(168, 85, 247, 0.2));
  border: 1px solid rgba(139, 92, 246, 0.3);
  color: #a78bfa;
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
  font-weight: 600; font-size: 0.9rem;
  transition: all 0.3s cubic-bezier(0.4, 0, 0.2, 1);
  cursor: pointer;
}
.glass-btn:hover:not(:disabled) {
  background: rgba(139, 92, 246, 0.1);
  border-color: #8b5cf6;
  box-shadow: 0 0 20px rgba(139, 92, 246, 0.2);
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
  display: flex; flex-direction: column; gap: 4px;
}
.matrix-label { font-size: 0.7rem; font-weight: 700; color: #64748b; text-transform: uppercase; letter-spacing: 0.05em; }
.matrix-value { font-size: 1.8rem; font-weight: 800; }
.matrix-card.emerald .matrix-value { color: #10b981; }
.matrix-card.amber .matrix-value { color: #f59e0b; }
.matrix-card.purple .matrix-value { color: #a78bfa; }

.error-banner {
  background: rgba(239, 68, 68, 0.1);
  border: 1px solid rgba(239, 68, 68, 0.3);
  padding: 12px 16px;
  border-radius: 12px;
  color: #f87171;
  margin-bottom: 20px;
}

.witnesses-table-wrap {
  overflow-x: auto;
}

.witnesses-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.9rem;
}
.witnesses-table th {
  text-align: left;
  padding: 12px 16px;
  font-weight: 600;
  font-size: 0.75rem;
  color: #64748b;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  border-bottom: 1px solid rgba(255, 255, 255, 0.05);
}
.witnesses-table th.right, .witnesses-table td.right { text-align: right; }
.witnesses-table td {
  padding: 16px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.03);
}
.witnesses-table tr:hover { background: rgba(255, 255, 255, 0.02); }
.mono { font-family: 'JetBrains Mono', monospace; font-size: 0.85rem; }

.status-pill {
  display: inline-block;
  padding: 4px 12px;
  border-radius: 9999px;
  font-size: 0.75rem;
  font-weight: 600;
  text-transform: uppercase;
}
.status-pill.active { background: rgba(16, 185, 129, 0.15); color: #10b981; }
.status-pill.candidate { background: rgba(245, 158, 11, 0.15); color: #f59e0b; }
.status-pill.exiting { background: rgba(99, 102, 241, 0.15); color: #6366f1; }
.status-pill.slashed { background: rgba(239, 68, 68, 0.15); color: #ef4444; }

.pass { color: #10b981; }
.fail { color: #ef4444; }

.empty-state {
  text-align: center;
  padding: 60px 20px;
  color: #64748b;
}
.empty-orb {
  width: 80px; height: 80px;
  margin: 0 auto 20px;
  border-radius: 50%;
  display: flex; align-items: center; justify-content: center;
}
.empty-orb.purple {
  background: rgba(139, 92, 246, 0.1);
  color: #a78bfa;
}
.empty-state h3 { font-size: 1.2rem; color: #94a3b8; margin-bottom: 8px; }
.empty-state p { font-size: 0.9rem; }
</style>

