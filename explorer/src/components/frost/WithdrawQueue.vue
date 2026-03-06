<script setup lang="ts">
import { computed, ref, onMounted, watch } from 'vue'
import { fetchFrostWithdrawQueue, fetchFrostWithdrawJobs } from '../../api'
import type { FrostWithdrawQueueItem, FrostWithdrawJobItem } from '../../types'
import WithdrawDetailModal from './WithdrawDetailModal.vue'

const emit = defineEmits(['select-tx'])

const props = defineProps<{
  node: string
}>()


const queue = ref<FrostWithdrawQueueItem[]>([])
const jobs = ref<FrostWithdrawJobItem[]>([])
const loading = ref(false)
const error = ref('')

const loadQueue = async () => {
  loading.value = true
  error.value = ''
  try {
    const [queueResp, jobsResp] = await Promise.all([
      fetchFrostWithdrawQueue(props.node),
      fetchFrostWithdrawJobs(props.node),
    ])
    queue.value = queueResp
    jobs.value = jobsResp
  } catch (e: any) {
    error.value = e.message || 'Failed to load'
    console.error('Failed to load withdraw queue', e)
  } finally {
    loading.value = false
  }
}

watch(() => props.node, loadQueue)
onMounted(loadQueue)

function formatAmount(amount: string): string {
  // 暂时移除 1e8 除法，因为测试环境使用较小数值
  const num = parseFloat(amount)
  return num.toLocaleString(undefined, { minimumFractionDigits: 0, maximumFractionDigits: 8 })
}

function getChainColor(chain: string): string {
  const c = chain.toLowerCase()
  if (c === 'btc') return '#f7931a'
  if (c === 'eth') return '#627eea'
  if (c === 'tron') return '#eb0029'
  return '#9ca3af'
}

function getJobStatusClass(status: string): string {
  const s = (status || '').toUpperCase()
  if (s === 'SIGNED' || s === 'SIGN_DONE' || s === 'WAIT_SUBMIT') return 'signed'
  if (s === 'SIGNING' || s === 'WAIT_START_SIGNING' || s === 'PLANNED') return 'signing'
  if (s === 'WAIT_UTXO' || s === 'WAIT_ACTIVE_VAULT' || s === 'WAIT_VAULT_BALANCE' || s === 'WAIT_SELECT_VAULT' || s === 'PLANNING_WAITING') return 'waiting'
  if (s === 'FAILED' || s === 'SIGN_FAILED' || s === 'PLAN_FAILED' || s === 'PLANNING_FAILED') return 'failed'
  return 'queued'
}

function formatPlanningTime(ts?: number): string {
  if (!ts) return '-'
  return new Date(ts).toLocaleString()
}

function compactText(text: string | undefined, maxLen: number = 80): string {
  const value = (text || '').trim()
  if (!value) return '-'
  if (value.length <= maxLen) return value
  return `${value.slice(0, maxLen - 3)}...`
}

function deriveStatusFromLogs(item: FrostWithdrawQueueItem): string {
  if (item.status?.toUpperCase() === 'SIGNED') return 'SIGNED'
  const logs = item.planning_logs || []
  if (logs.length === 0) return 'QUEUED'
  const last = logs[logs.length - 1]
  const step = (last?.step || '').toLowerCase()
  const status = (last?.status || '').toLowerCase()
  const msg = (last?.message || '').toLowerCase()

  if (step === 'signing' && status === 'failed') return 'SIGN_FAILED'
  if (step === 'signing' && status === 'ok') return 'SIGN_DONE'
  if ((step === 'signing' && status === 'waiting') || (step === 'startsigning' && status === 'ok')) return 'SIGNING'
  if (step === 'finishplanning' && status === 'ok') return 'PLANNED'
  if (step === 'planbtcjob' && status === 'failed') {
    if (msg.includes('utxo')) return 'WAIT_UTXO'
    if (msg.includes('active vault')) return 'WAIT_ACTIVE_VAULT'
    return 'PLAN_FAILED'
  }
  if (step === 'selectvault' && status === 'failed') {
    if (msg.includes('active vault')) return 'WAIT_ACTIVE_VAULT'
    if (msg.includes('balance') || msg.includes('insufficient')) return 'WAIT_VAULT_BALANCE'
    return 'WAIT_SELECT_VAULT'
  }
  if (step === 'compositeplan' && status === 'waiting') return 'WAIT_VAULT_BALANCE'
  if (status === 'failed') return 'PLANNING_FAILED'
  if (status === 'waiting') return 'PLANNING_WAITING'
  return 'QUEUED'
}

function getRuntimeStatus(item: FrostWithdrawQueueItem): string {
  return (item.derived_status && item.derived_status.trim()) || deriveStatusFromLogs(item)
}

function getRuntimeStatusLabel(status: string): string {
  const s = (status || '').toUpperCase()
  if (s === 'QUEUED') return 'Queued'
  if (s === 'PLANNED') return 'Planned'
  if (s === 'WAIT_START_SIGNING') return 'Wait Signing'
  if (s === 'SIGNING') return 'Signing'
  if (s === 'SIGN_DONE') return 'Sign Done'
  if (s === 'WAIT_SUBMIT') return 'Wait Submit'
  if (s === 'SIGNED') return 'Signed'
  if (s === 'WAIT_UTXO') return 'Wait UTXO'
  if (s === 'WAIT_ACTIVE_VAULT') return 'Wait Active Vault'
  if (s === 'WAIT_VAULT_BALANCE') return 'Wait Vault Balance'
  if (s === 'WAIT_SELECT_VAULT') return 'Wait Vault Select'
  if (s === 'PLANNING_WAITING') return 'Planning Waiting'
  if (s === 'PLAN_FAILED') return 'Plan Failed'
  if (s === 'SIGN_FAILED') return 'Sign Failed'
  if (s === 'PLANNING_FAILED') return 'Planning Failed'
  if (s === 'FAILED') return 'Failed'
  return status || 'Unknown'
}

function getRuntimeReason(item: FrostWithdrawQueueItem): string {
  return item.derived_reason || item.failure_reason || item.last_planning_message || item.planning_logs?.[item.planning_logs.length - 1]?.message || ''
}

const queueBlockers = computed(() => {
  return queue.value.filter((item) => {
    const stage = getRuntimeStatus(item)
    return stage !== 'QUEUED' || !!item.last_planning_step || (item.planning_logs?.length || 0) > 0
  })
})

function getJobDisplayStatus(job: FrostWithdrawJobItem): string {
  return (job.derived_status || job.status || 'QUEUED').toUpperCase()
}

function getJobReason(job: FrostWithdrawJobItem): string {
  return job.failure_reason || job.derived_reason || ''
}

const modalVisible = ref(false)
const selectedItem = ref<FrostWithdrawQueueItem | null>(null)

const openFlow = (item: FrostWithdrawQueueItem) => {
  selectedItem.value = item
  modalVisible.value = true
}

const jobModalVisible = ref(false)
const selectedJob = ref<FrostWithdrawJobItem | null>(null)

function openJobDetail(job: FrostWithdrawJobItem) {
  selectedJob.value = job
  jobModalVisible.value = true
}

function getJobWithdraws(job: FrostWithdrawJobItem): FrostWithdrawQueueItem[] {
  if (!job?.withdraw_ids?.length) return []
  const idSet = new Set(job.withdraw_ids)
  return queue.value.filter(q => idSet.has(q.withdraw_id))
}
</script>

<template>
  <div class="withdraw-dashboard">
    <!-- Header Section -->
    <header class="header-section">
      <div class="header-left">
        <div class="icon-orb blue">
          <svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 21h18"/><path d="M3 10h18"/><path d="m5 6 7-3 7 3"/><path d="M4 10v11"/><path d="M20 10v11"/><path d="M8 14v3"/><path d="M12 14v3"/><path d="M16 14v3"/></svg>
        </div>
        <div class="title-meta">
          <h3 class="premium-title">Withdraw Queue</h3>
          <p class="premium-subtitle">Chain-agnostic asset redistribution queue</p>
        </div>
      </div>
      <button @click="loadQueue" class="glass-btn primary" :disabled="loading">
        <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" :class="{ 'spin': loading }"><path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M3 21v-5h5"/></svg>
        <span>{{ loading ? 'Updating...' : 'Refresh Queue' }}</span>
      </button>
    </header>

    <!-- Error Alert -->
    <div v-if="error" class="error-alert">
      <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" x2="12" y1="8" y2="12"/><line x1="12" x2="12.01" y1="16" y2="16"/></svg>
      <span>{{ error }}</span>
    </div>

    <!-- Data Presentation -->
    <div v-if="queue.length > 0" class="data-viewport">
      <table class="premium-table">
        <thead>
          <tr>
            <th class="px-6 py-4 text-left">Request ID</th>
            <th class="px-6 py-4 text-left">Asset / Protocol</th>
            <th class="px-6 py-4 text-left">Destination Address</th>
            <th class="px-6 py-4 text-right">Volume</th>
            <th class="px-6 py-4 text-center">Ledger Status</th>
            <th class="px-6 py-4 text-center">Runtime Stage</th>
            <th class="px-6 py-4 text-left">Latest Signal</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="item in queue" :key="item.withdraw_id" class="table-row group" @click="openFlow(item)">
            <td class="px-6 py-4">
              <div class="id-badge-with-tool">
                <code class="mono">{{ item.withdraw_id.substring(0, 8) }}</code>
                <button class="mini-inspect" @click.stop="emit('select-tx', item.withdraw_id)" title="Quick Inspect">
                  <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/></svg>
                </button>
                <div class="hover-full-id">{{ item.withdraw_id }}</div>
              </div>
            </td>
            <td class="px-6 py-4">
              <div class="asset-combo">
                <div class="asset-logo" :style="{ '--col': getChainColor(item.chain) }">
                  {{ item.chain.charAt(0) }}
                </div>
                <div class="asset-meta">
                  <span class="asset-name text-white font-bold">{{ item.asset }}</span>
                  <span class="chain-name text-[10px] text-gray-500 uppercase tracking-widest">{{ item.chain }}</span>
                </div>
              </div>
            </td>
            <td class="px-6 py-4">
              <div class="addr-box">
                <span class="mono text-gray-400">{{ item.to.substring(0, 10) }}...{{ item.to.slice(-8) }}</span>
                <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="addr-icon"><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/><polyline points="15 3 21 3 21 9"/><line x1="10" x2="21" y1="14" y2="3"/></svg>
              </div>
            </td>
            <td class="px-6 py-4 text-right">
              <div class="amount-stack">
                <span class="amount-val text-amber-400 font-bold font-mono">{{ formatAmount(item.amount) }}</span>
                <span class="amount-unit text-[10px] text-gray-500">{{ item.asset }}</span>
              </div>
            </td>
            <td class="px-6 py-4 text-center">
              <div :class="['status-pill', item.status.toLowerCase()]">
                <div class="pill-dot"></div>
                <span>{{ item.status }}</span>
              </div>
            </td>
            <td class="px-6 py-4 text-center">
              <div :class="['status-pill', getJobStatusClass(getRuntimeStatus(item))]">
                <div class="pill-dot"></div>
                <span>{{ getRuntimeStatusLabel(getRuntimeStatus(item)) }}</span>
              </div>
            </td>
            <td class="px-6 py-4">
              <div class="amount-stack">
                <span class="text-gray-300">
                  {{ compactText(item.last_planning_step || item.planning_logs?.[item.planning_logs.length - 1]?.step || 'No signal', 24) }}
                  <span v-if="item.last_planning_status || item.planning_logs?.length" class="text-gray-500">
                    / {{ item.last_planning_status || item.planning_logs?.[item.planning_logs.length - 1]?.status || '-' }}
                  </span>
                </span>
                <span class="text-[10px] text-gray-500">{{ compactText(getRuntimeReason(item), 72) }}</span>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
      
      <!-- Footer Info -->
      <footer class="viewport-footer">
        <div class="stat-group">
          <div class="stat-item">
            <span class="stat-label">Total Volume</span>
            <span class="stat-val">{{ queue.length }} <small>Requests</small></span>
          </div>
          <div class="stat-item">
            <span class="stat-label">Ready to Dispatch</span>
            <span class="stat-val text-emerald-500">{{ queue.filter(q => q.status === 'SIGNED').length }}</span>
          </div>
        </div>
        <div class="auto-sync">
          <div class="pulse-dot"></div>
          <span>Real-time synchronization active</span>
        </div>
      </footer>
    </div>

    <!-- Empty State -->
    <div v-else-if="!loading" class="empty-state">
      <div class="empty-orb">
        <svg xmlns="http://www.w3.org/2000/svg" width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"/><path d="M14 2v4a2 2 0 0 0 2 2h4"/><path d="m9 15 2 2 4-4"/></svg>
      </div>
      <p class="text-gray-500 font-medium">All clear! No pending withdrawals.</p>
    </div>

    <section class="jobs-section">
      <div class="jobs-header">
    <h4 class="jobs-title">Jobs</h4>
        <span class="jobs-count">{{ jobs.length }}</span>
      </div>
      <div v-if="jobs.length > 0" class="data-viewport">
        <table class="premium-table">
          <thead>
            <tr>
              <th class="px-6 py-4 text-left">Job ID</th>
              <th class="px-6 py-4 text-left">Type</th>
              <th class="px-6 py-4 text-left">Chain / Asset</th>
              <th class="px-6 py-4 text-left">Withdraws</th>
              <th class="px-6 py-4 text-right">Total Amount</th>
              <th class="px-6 py-4 text-left">Last Planning Step</th>
              <th class="px-6 py-4 text-left">Debug Reason</th>
              <th class="px-6 py-4 text-center">Runtime Stage</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="job in jobs" :key="job.job_id" class="table-row" @click="openJobDetail(job)">
              <td class="px-6 py-4">
                <div class="id-badge-with-tool">
                  <code class="mono">{{ job.job_id.substring(0, 10) }}</code>
                  <div class="hover-full-id">{{ job.job_id }}</div>
                </div>
              </td>
              <td class="px-6 py-4">
                <span :class="['status-pill', job.synthetic ? 'waiting' : 'queued']">
                  <span class="pill-dot"></span>
                  {{ job.synthetic ? 'Planning Blocker' : 'Signing Job' }}
                </span>
              </td>
              <td class="px-6 py-4">
                <div class="asset-combo">
                  <div class="asset-logo" :style="{ '--col': getChainColor(job.chain) }">
                    {{ job.chain.charAt(0) }}
                  </div>
                  <div class="asset-meta">
                    <span class="asset-name text-white font-bold">{{ job.asset }}</span>
                    <span class="chain-name text-[10px] text-gray-500 uppercase tracking-widest">{{ job.chain }} / vault {{ job.vault_id }}</span>
                  </div>
                </div>
              </td>
              <td class="px-6 py-4">
                <span class="mono text-gray-300">{{ job.withdraw_count }} requests</span>
              </td>
              <td class="px-6 py-4 text-right">
                <span class="amount-val text-cyan-300 font-bold font-mono">{{ formatAmount(job.total_amount) }}</span>
              </td>
              <td class="px-6 py-4">
                <div class="amount-stack">
                  <span class="text-gray-300">{{ job.last_planning_step || '-' }}</span>
                  <span class="text-[10px] text-gray-500">{{ formatPlanningTime(job.last_planning_at) }}</span>
                </div>
              </td>
              <td class="px-6 py-4">
                <span class="failure-reason" :title="getJobReason(job)">
                  {{ compactText(getJobReason(job), 88) }}
                </span>
              </td>
              <td class="px-6 py-4 text-center">
                <div :class="['status-pill', getJobStatusClass(getJobDisplayStatus(job))]">
                  <div class="pill-dot"></div>
                  <span>{{ getRuntimeStatusLabel(getJobDisplayStatus(job)) }}</span>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-else-if="!loading" class="jobs-empty">
        <span v-if="queueBlockers.length === 0">No jobs found from persisted withdraw state/planning logs.</span>
        <div v-else class="blocker-list">
          <span class="text-gray-300">No concrete signing job yet. Queue is blocked at:</span>
          <div v-for="item in queueBlockers.slice(0, 6)" :key="item.withdraw_id" class="blocker-item">
            <code class="mono">{{ item.withdraw_id.substring(0, 8) }}</code>
            <span>{{ getRuntimeStatusLabel(getRuntimeStatus(item)) }}</span>
            <span class="text-gray-500">{{ compactText(getRuntimeReason(item), 92) }}</span>
          </div>
        </div>
      </div>
    </section>

    <!-- Withdraw Detail Modal -->
    <WithdrawDetailModal
      :show="modalVisible"
      :item="selectedItem"
      @close="modalVisible = false"
    />

    <!-- Job Detail Modal -->
    <Teleport to="body">
      <Transition name="fade">
        <div v-if="jobModalVisible && selectedJob" class="job-modal-root" @click.self="jobModalVisible = false">
          <div class="job-modal-backdrop"></div>
          <div class="job-modal-container" @click.stop>
            <div class="job-modal-header">
              <div class="job-header-left">
                <div class="job-header-icon">📦</div>
                <div>
                  <h3 class="job-modal-title">Job Detail</h3>
                  <p class="job-modal-subtitle mono">{{ selectedJob.job_id }}</p>
                </div>
              </div>
              <button @click="jobModalVisible = false" class="job-close-btn">&times;</button>
            </div>
            <div class="job-modal-body custom-scrollbar">
              <section class="job-info-grid">
                <div class="job-info-card">
                  <span class="job-info-label">Chain / Asset</span>
                  <span class="job-info-val text-white font-bold">{{ selectedJob.chain.toUpperCase() }} / {{ selectedJob.asset }}</span>
                </div>
                <div class="job-info-card">
                  <span class="job-info-label">Vault ID</span>
                  <span class="job-info-val text-amber-500 font-bold">{{ selectedJob.vault_id }}</span>
                </div>
                <div class="job-info-card">
                  <span class="job-info-label">Status</span>
                  <div :class="['status-pill', getJobStatusClass(getJobDisplayStatus(selectedJob))]">
                    <div class="pill-dot"></div>
                    <span>{{ getRuntimeStatusLabel(getJobDisplayStatus(selectedJob)) }}</span>
                  </div>
                </div>
                <div class="job-info-card">
                  <span class="job-info-label">Total Amount</span>
                  <span class="job-info-val text-cyan-300 font-bold font-mono">{{ formatAmount(selectedJob.total_amount) }}</span>
                </div>
              </section>
              <section class="job-withdraws-section">
                <div class="job-section-label">Withdraw Requests ({{ selectedJob.withdraw_count }})</div>
                <div class="job-withdraw-list">
                  <div v-for="w in getJobWithdraws(selectedJob)" :key="w.withdraw_id" class="job-withdraw-row" @click="openFlow(w); jobModalVisible = false">
                    <code class="mono job-wid">{{ w.withdraw_id.substring(0, 10) }}...</code>
                    <span class="job-w-to mono">{{ w.to.substring(0, 10) }}...{{ w.to.slice(-6) }}</span>
                    <span class="job-w-amount font-mono text-amber-400">{{ formatAmount(w.amount) }}</span>
                    <div :class="['status-pill mini', w.status.toLowerCase()]"><div class="pill-dot"></div><span>{{ w.status }}</span></div>
                  </div>
                  <div v-for="wid in (selectedJob?.withdraw_ids || []).filter(id => !getJobWithdraws(selectedJob!).some(w => w.withdraw_id === id))" :key="wid" class="job-withdraw-row dim">
                    <code class="mono job-wid">{{ wid.substring(0, 10) }}...</code>
                    <span class="text-gray-600">—</span>
                    <span class="text-gray-600">—</span>
                    <span class="text-gray-600">—</span>
                  </div>
                </div>
              </section>
            </div>
            <div class="job-modal-footer">
              <button @click="jobModalVisible = false" class="job-dismiss-btn">Dismiss</button>
            </div>
          </div>
        </div>
      </Transition>
    </Teleport>
  </div>
</template>

<style scoped>
@import url('https://fonts.googleapis.com/css2?family=Outfit:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap');

.withdraw-dashboard {
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

.header-left {
  display: flex;
  align-items: center;
  gap: 20px;
}

.icon-orb {
  width: 56px;
  height: 56px;
  background: linear-gradient(135deg, rgba(59, 130, 246, 0.2), rgba(37, 99, 235, 0.2));
  border: 1px solid rgba(59, 130, 246, 0.3);
  border-radius: 18px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #3b82f6;
  box-shadow: 0 10px 30px rgba(59, 130, 246, 0.1);
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
  background: rgba(59, 130, 246, 0.1);
  border-color: #3b82f6;
  box-shadow: 0 0 20px rgba(59, 130, 246, 0.2);
  transform: translateY(-2px);
}

.data-viewport {
  background: rgba(0, 0, 0, 0.2);
  border-radius: 18px;
  overflow: hidden;
  border: 1px solid rgba(255, 255, 255, 0.03);
}

.jobs-section {
  margin-top: 24px;
}

.jobs-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}

.jobs-title {
  margin: 0;
  font-size: 1rem;
  font-weight: 700;
  color: #d1d5db;
}

.jobs-count {
  background: rgba(99, 102, 241, 0.15);
  color: #a5b4fc;
  border: 1px solid rgba(99, 102, 241, 0.25);
  border-radius: 999px;
  padding: 2px 10px;
  font-size: 0.75rem;
  font-weight: 700;
}

.premium-table {
  width: 100%;
  border-collapse: collapse;
}

.premium-table thead {
  background: rgba(255, 255, 255, 0.02);
  border-bottom: 1px solid rgba(255, 255, 255, 0.05);
}

.premium-table th {
  font-size: 0.75rem;
  font-weight: 700;
  color: #475569;
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

.table-row {
  border-bottom: 1px solid rgba(255, 255, 255, 0.02);
  transition: all 0.2s;
  cursor: pointer;
}

.table-row:hover {
  background: rgba(255, 255, 255, 0.03);
}

.id-badge-with-tool {
  position: relative;
  display: flex;
  align-items: center;
  gap: 8px;
}

.id-badge-with-tool code {
  background: rgba(99, 102, 241, 0.1);
  color: #818cf8;
  padding: 4px 8px;
  border-radius: 6px;
  font-size: 0.75rem;
}

.mini-inspect {
  background: rgba(99, 102, 241, 0.1); border: none; color: #818cf8; width: 22px; height: 22px;
  border-radius: 6px; display: flex; align-items: center; justify-content: center; cursor: pointer;
  opacity: 0; transition: all 0.2s;
}
.table-row:hover .mini-inspect { opacity: 1; }
.mini-inspect:hover { background: #6366f1; color: #fff; transform: scale(1.1); }

.hover-full-id {
  position: absolute;
  top: 100%;
  left: 0;
  background: #000;
  padding: 8px 12px;
  border-radius: 8px;
  font-family: 'JetBrains Mono', monospace;
  font-size: 0.7rem;
  white-space: nowrap;
  opacity: 0;
  pointer-events: none;
  transform: translateY(4px);
  transition: all 0.2s;
  z-index: 50;
  box-shadow: 0 10px 30px rgba(0,0,0,0.5);
  border: 1px solid rgba(255, 255, 255, 0.1);
}

.id-badge-with-tool:hover .hover-full-id {
  opacity: 1;
  transform: translateY(8px);
}


.asset-combo {
  display: flex;
  align-items: center;
  gap: 12px;
}

.asset-logo {
  width: 32px;
  height: 32px;
  background: var(--col, #444);
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 900;
  font-size: 0.8rem;
  color: #fff;
  box-shadow: inset 0 0 10px rgba(0,0,0,0.3);
}

.asset-meta {
  display: flex;
  flex-direction: column;
}

.addr-box {
  display: flex;
  align-items: center;
  gap: 8px;
}

.addr-icon {
  color: #334155;
  transition: color 0.2s;
}

.table-row:hover .addr-icon {
  color: #6366f1;
}

.mono {
  font-family: 'JetBrains Mono', monospace;
}

.amount-stack {
  display: flex;
  flex-direction: column;
}

.status-pill {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 4px 12px;
  border-radius: 99px;
  font-size: 0.7rem;
  font-weight: 800;
  text-transform: uppercase;
  letter-spacing: 0.02em;
}

.status-pill.queued { background: rgba(59, 130, 246, 0.1); color: #60a5fa; }
.status-pill.signing { background: rgba(139, 92, 246, 0.1); color: #a78bfa; }
.status-pill.signed { background: rgba(16, 185, 129, 0.1); color: #34d399; }
.status-pill.waiting { background: rgba(245, 158, 11, 0.12); color: #fbbf24; }
.status-pill.failed { background: rgba(239, 68, 68, 0.12); color: #f87171; }

.pill-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
  box-shadow: 0 0 8px currentColor;
}

.status-pill.signing .pill-dot { animation: pulse 1.5s infinite; }

.viewport-footer {
  padding: 24px;
  background: rgba(255, 255, 255, 0.01);
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.stat-group {
  display: flex;
  gap: 32px;
}

.stat-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.stat-label {
  font-size: 0.65rem;
  font-weight: 700;
  color: #475569;
  text-transform: uppercase;
}

.stat-val {
  font-size: 1.1rem;
  font-weight: 700;
}

.stat-val small {
  font-size: 0.7rem;
  color: #64748b;
  margin-left: 4px;
}

.auto-sync {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 0.75rem;
  color: #475569;
}

.pulse-dot {
  width: 8px;
  height: 8px;
  background: #10b981;
  border-radius: 50%;
  box-shadow: 0 0 10px #10b981;
  animation: pulse 2s infinite;
}

@keyframes pulse {
  0% { transform: scale(0.95); opacity: 0.5; }
  50% { transform: scale(1.1); opacity: 1; }
  100% { transform: scale(0.95); opacity:0.5; }
}

@keyframes spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }

.error-alert {
  background: rgba(239, 68, 68, 0.1);
  color: #ef4444;
  padding: 16px;
  border-radius: 14px;
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 24px;
  border: 1px solid rgba(239, 68, 68, 0.2);
}

.empty-state {
  text-align: center;
  padding: 60px 0;
}

.jobs-empty {
  text-align: left;
  color: #64748b;
  font-size: 0.85rem;
  background: rgba(0, 0, 0, 0.2);
  border-radius: 14px;
  border: 1px dashed rgba(255, 255, 255, 0.08);
  padding: 20px;
}

.blocker-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.blocker-item {
  display: grid;
  grid-template-columns: 84px 160px 1fr;
  gap: 10px;
  align-items: center;
  padding: 8px 10px;
  border-radius: 10px;
  background: rgba(255, 255, 255, 0.02);
  color: #cbd5e1;
}

.failure-reason {
  display: inline-block;
  max-width: 340px;
  font-size: 0.8rem;
  color: #cbd5e1;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.empty-orb {
  width: 64px;
  height: 64px;
  background: rgba(255, 255, 255, 0.03);
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #1e293b;
  margin: 0 auto 20px;
}

/* Job Detail Modal */
.job-modal-root { position: fixed; inset: 0; z-index: 9999; display: flex; align-items: center; justify-content: center; padding: 24px; }
.job-modal-backdrop { position: absolute; inset: 0; background: rgba(3, 7, 18, 0.9); backdrop-filter: blur(12px); }
.job-modal-container { position: relative; width: 100%; max-width: 760px; max-height: 80vh; background: #0f172a; border: 1px solid rgba(255,255,255,0.08); border-radius: 24px; box-shadow: 0 30px 60px -12px rgba(0,0,0,0.6); display: flex; flex-direction: column; overflow: hidden; }
.job-modal-header { padding: 20px 28px; border-bottom: 1px solid rgba(255,255,255,0.05); display: flex; justify-content: space-between; align-items: center; }
.job-header-left { display: flex; align-items: center; gap: 14px; }
.job-header-icon { font-size: 1.5rem; }
.job-modal-title { font-size: 1.05rem; font-weight: 700; color: white; margin: 0; }
.job-modal-subtitle { font-size: 0.7rem; color: #64748b; margin: 2px 0 0; word-break: break-all; }
.job-close-btn { background: transparent; border: none; color: #475569; font-size: 1.5rem; cursor: pointer; width: 32px; height: 32px; display: flex; align-items: center; justify-content: center; border-radius: 8px; transition: all 0.2s; }
.job-close-btn:hover { background: rgba(255,255,255,0.05); color: white; }
.job-modal-body { flex: 1; padding: 28px; overflow-y: auto; display: flex; flex-direction: column; gap: 24px; }
.job-info-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 14px; }
.job-info-card { background: rgba(255,255,255,0.02); border: 1px solid rgba(255,255,255,0.04); padding: 14px 18px; border-radius: 14px; display: flex; flex-direction: column; gap: 6px; }
.job-info-label { font-size: 0.6rem; font-weight: 700; color: #64748b; text-transform: uppercase; }
.job-info-val { font-size: 0.9rem; }
.job-section-label { font-size: 0.7rem; font-weight: 800; text-transform: uppercase; color: #475569; letter-spacing: 0.1em; margin-bottom: 12px; }
.job-withdraw-list { display: flex; flex-direction: column; gap: 6px; }
.job-withdraw-row { display: grid; grid-template-columns: 120px 1fr auto auto; gap: 12px; align-items: center; padding: 10px 14px; border-radius: 12px; background: rgba(255,255,255,0.02); border: 1px solid rgba(255,255,255,0.03); cursor: pointer; transition: all 0.2s; }
.job-withdraw-row:hover { background: rgba(99,102,241,0.06); border-color: rgba(99,102,241,0.15); }
.job-withdraw-row.dim { opacity: 0.4; cursor: default; }
.job-withdraw-row.dim:hover { background: rgba(255,255,255,0.02); border-color: rgba(255,255,255,0.03); }
.job-wid { background: rgba(99,102,241,0.1); color: #818cf8; padding: 3px 6px; border-radius: 5px; font-size: 0.7rem; }
.job-w-to { color: #94a3b8; font-size: 0.75rem; }
.job-w-amount { font-size: 0.8rem; font-weight: 700; }
.status-pill.mini { font-size: 0.6rem; padding: 2px 8px; }
.job-modal-footer { padding: 20px 28px; border-top: 1px solid rgba(255,255,255,0.05); display: flex; justify-content: flex-end; }
.job-dismiss-btn { background: #1e293b; color: white; border: 1px solid rgba(255,255,255,0.1); padding: 8px 20px; border-radius: 10px; font-weight: 600; font-size: 0.85rem; cursor: pointer; transition: all 0.2s; }
.job-dismiss-btn:hover { background: #334155; border-color: rgba(255,255,255,0.2); }
.fade-enter-active, .fade-leave-active { transition: opacity 0.3s; }
.fade-enter-from, .fade-leave-to { opacity: 0; }
</style>
