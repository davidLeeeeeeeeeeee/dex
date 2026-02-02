<script setup lang="ts">
import { ref } from 'vue'
import type { BlockInfo } from '../types'

const props = defineProps<{
  block: BlockInfo
}>()

const emit = defineEmits<{
  txClick: [txId: string]
  addressClick: [address: string]
  back: []
}>()

const numberFormat = new Intl.NumberFormat('en-US')

function formatNumber(value?: number | null): string {
  if (value === undefined || value === null) return '-'
  return numberFormat.format(value)
}

function truncateHash(hash?: string, chars = 12): string {
  if (!hash) return '-'
  if (hash.length <= chars * 2) return hash
  return `${hash.slice(0, chars)}...${hash.slice(-chars)}`
}

function statusInfo(status?: string) {
  const s = (status || '').toUpperCase()
  if (s === 'SUCCEED' || s === 'SUCCESS') return { class: 'st-good', label: 'Success' }
  if (s === 'FAILED' || s === 'FAIL') return { class: 'st-bad', label: 'Failed' }
  return { class: 'st-warn', label: 'Warning' }
}

function handleAddressClick(e: Event, address?: string) {
  e.stopPropagation()
  if (address) emit('addressClick', address)
}

// 轮次折叠状态
const expandedRounds = ref<Record<number, boolean>>({})

function toggleRound(index: number) {
  expandedRounds.value[index] = !expandedRounds.value[index]
}
</script>

<template>
  <div class="block-view animate-fade-in">
    <!-- Action Header -->
    <div class="action-header">
      <button class="back-btn" @click="emit('back')">
        <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="m15 18-6-6 6-6"/></svg>
        Back
      </button>
      <div class="header-main">
        <div class="block-orb">
          <svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/><polyline points="3.29 7 12 12 20.71 7"/><line x1="12" x2="12" y1="22" y2="12"/></svg>
        </div>
        <div class="text-group">
          <h1>Block Details</h1>
          <p class="mono text-indigo-400"># {{ formatNumber(block.height) }}</p>
        </div>
      </div>
    </div>

    <div class="block-details-grid">
      <!-- Primary Specs -->
      <section class="panel specs-panel">
        <div class="section-title">
          <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/></svg>
          Overview
        </div>
        <div class="specs-list">
          <div class="spec-row">
            <span class="s-label">Block Hash</span>
            <div class="s-val-group">
               <code class="mono text-gray-300">{{ block.block_hash || '-' }}</code>
            </div>
          </div>
          <div class="spec-row">
            <span class="s-label">Parent Hash</span>
            <code class="mono text-gray-500">{{ truncateHash(block.prev_block_hash, 16) }}</code>
          </div>
          <div class="spec-row">
            <span class="s-label">Miner</span>
            <code class="mono text-indigo-400">{{ block.miner || '-' }}</code>
          </div>
          <div class="spec-row">
            <span class="s-label">State Root</span>
            <div class="s-val-group state-root">
               <code class="mono text-emerald-400">{{ block.state_root || 'N/A (Genesis or Pending)' }}</code>
            </div>
          </div>
        </div>
      </section>

      <!-- Metrics Panel -->
      <section class="panel metrics-panel">
        <div class="section-title">
          <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20v-6h4v6h-4ZM6 20v-10h4v10H6ZM18 20V4h4v16h-4Z"/></svg>
          Metrics
        </div>
        <div class="metrics-grid">
           <div class="m-box">
              <span class="m-label">Transactions</span>
              <span class="m-val">{{ formatNumber(block.tx_count) }}</span>
           </div>
           <div class="m-box">
              <span class="m-label">Height</span>
              <span class="m-val mono">#{{ block.height }}</span>
           </div>
           <div class="m-box highlight">
              <span class="m-label">Block Reward</span>
              <span class="m-val text-amber-500">{{ block.accumulated_reward || '0' }}</span>
           </div>
           <div class="m-box">
              <span class="m-label">Window</span>
              <span class="m-val">{{ block.window ?? 'N/A' }}</span>
           </div>
        </div>
      </section>
    </div>

    <!-- Transactions -->
    <section class="panel transactions-panel">
      <div class="panel-header-sub">
        <div class="title-meta">
          <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M7 11V7a5 5 0 0 1 10 0v4"/><rect width="18" height="11" x="3" y="11" rx="2" ry="2"/></svg>
          <h3>Transactions</h3>
          <span class="count-tag">{{ block.transactions?.length || 0 }} Txs</span>
        </div>
      </div>

      <div class="table-wrap">
        <table class="premium-table">
          <thead>
            <tr>
              <th class="pl-6 w-32">Tx Hash</th>
              <th>Type</th>
              <th>From</th>
              <th>To</th>
              <th class="text-right">Value</th>
              <th class="text-right pr-6">Status</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="tx in block.transactions" :key="tx.tx_id" @click="emit('txClick', tx.tx_id)" class="table-row">
              <td class="pl-6">
                <code class="mono text-indigo-400">{{ truncateHash(tx.tx_id, 6) }}</code>
              </td>
              <td><span class="type-pill">{{ tx.tx_type }}</span></td>
              <td>
                <span v-if="tx.from_address" class="mono text-gray-500 link" @click="handleAddressClick($event, tx.from_address)">
                  {{ truncateHash(tx.from_address, 6) }}
                </span>
                <span v-else>-</span>
              </td>
              <td>
                <span v-if="tx.to_address" class="mono text-gray-400 link" @click="handleAddressClick($event, tx.to_address)">
                  {{ truncateHash(tx.to_address, 6) }}
                </span>
                <span v-else>-</span>
              </td>
              <td class="text-right">
                <span class="mono font-bold">{{ tx.value || '0' }}</span>
              </td>
              <td class="text-right pr-6">
                <div :class="['status-chip', statusInfo(tx.status).class]">
                  {{ statusInfo(tx.status).label }}
                </div>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-if="!block.transactions || block.transactions.length === 0" class="empty-state-mini py-20">
           No transactions in this block.
        </div>
      </div>
    </section>

    <!-- Finalization Votes (Debug Info) -->
    <section v-if="block.finalization_chits" class="panel chits-panel">
      <div class="panel-header-sub">
        <div class="title-meta">
          <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="m7 11 2 2 4-4"/><path d="M3.43 13.61a2 2 0 0 1 0-3.22l9-6A2 2 0 0 1 15 6v12a2 2 0 0 1-2.57 1.92l-9-6Z"/></svg>
          <h3>Finalization Votes</h3>
          <span class="count-tag highlight-green">{{ block.finalization_chits.total_votes }} Votes</span>
          <span v-if="block.finalization_chits.total_rounds" class="count-tag highlight-blue">{{ block.finalization_chits.total_rounds }} Rounds</span>
        </div>
        <span v-if="block.finalization_chits.finalized_at" class="finalized-time text-gray-500 text-xs">
          Finalized at {{ new Date(block.finalization_chits.finalized_at).toLocaleString() }}
        </span>
      </div>

      <!-- 多轮数据展示 -->
      <div v-if="block.finalization_chits.rounds && block.finalization_chits.rounds.length > 0" class="rounds-container">
        <div v-for="(round, ridx) in block.finalization_chits.rounds" :key="ridx" class="round-section">
          <div class="round-header" @click="toggleRound(ridx)">
            <span class="round-toggle">{{ expandedRounds[ridx] ? '▼' : '▶' }}</span>
            <span class="round-label">Round {{ round.round }}</span>
            <span class="round-votes">{{ round.votes?.length || 0 }} votes</span>
            <span class="round-time text-gray-500 text-xs">{{ new Date(round.timestamp).toLocaleTimeString() }}</span>
          </div>
          <div v-if="expandedRounds[ridx]" class="round-votes-list">
            <table class="premium-table">
              <thead>
                <tr>
                  <th class="pl-6">Voter Node</th>
                  <th>Preferred Block</th>
                  <th class="text-right pr-6">Vote Time</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(vote, vidx) in round.votes" :key="vidx" class="table-row no-hover">
                  <td class="pl-6">
                    <code class="mono text-teal-400">{{ vote.node_id }}</code>
                  </td>
                  <td>
                    <code class="mono" :class="vote.preferred_id === block.block_hash ? 'text-emerald-400' : 'text-gray-500'">
                      {{ truncateHash(vote.preferred_id, 12) }}
                    </code>
                    <span v-if="vote.preferred_id === block.block_hash" class="vote-match-tag">✓ Matched</span>
                  </td>
                  <td class="text-right pr-6 text-gray-500 text-xs">
                    {{ new Date(vote.timestamp).toLocaleTimeString() }}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>

      <!-- 兼容旧版单列表数据 -->
      <div v-else-if="block.finalization_chits.chits && block.finalization_chits.chits.length > 0" class="table-wrap">
        <table class="premium-table">
          <thead>
            <tr>
              <th class="pl-6">Voter Node</th>
              <th>Preferred Block</th>
              <th class="text-right pr-6">Vote Time</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(chit, idx) in block.finalization_chits.chits" :key="idx" class="table-row no-hover">
              <td class="pl-6">
                <code class="mono text-teal-400">{{ chit.node_id }}</code>
              </td>
              <td>
                <code class="mono" :class="chit.preferred_id === block.block_hash ? 'text-emerald-400' : 'text-gray-500'">
                  {{ truncateHash(chit.preferred_id, 12) }}
                </code>
                <span v-if="chit.preferred_id === block.block_hash" class="vote-match-tag">✓ Matched</span>
              </td>
              <td class="text-right pr-6 text-gray-500 text-xs">
                {{ new Date(chit.timestamp).toLocaleTimeString() }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-else class="empty-state-mini py-12">
         No individual vote details available.
      </div>
    </section>
  </div>
</template>

<style scoped>
@import url('https://fonts.googleapis.com/css2?family=Outfit:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap');

.block-view { display: flex; flex-direction: column; gap: 32px; font-family: 'Outfit', sans-serif; }

.action-header { display: flex; flex-direction: column; gap: 20px; }

.back-btn {
  width: fit-content; display: flex; align-items: center; gap: 8px;
  background: rgba(255, 255, 255, 0.03); border: 1px solid rgba(255, 255, 255, 0.05);
  padding: 8px 16px; border-radius: 10px; color: #64748b; font-size: 0.75rem;
  font-weight: 700; cursor: pointer; transition: all 0.3s;
}
.back-btn:hover { background: rgba(255, 255, 255, 0.05); color: #fff; transform: translateX(-4px); }

.header-main { display: flex; align-items: center; gap: 24px; }
.block-orb {
  width: 64px; height: 64px; background: linear-gradient(135deg, #10b981, #34d399);
  border-radius: 20px; display: flex; align-items: center; justify-content: center;
  box-shadow: 0 10px 30px rgba(16, 185, 129, 0.3); color: #fff;
}
.text-group h1 { margin: 0; font-size: 1.8rem; font-weight: 800; letter-spacing: -0.03em; }
.text-group p { margin: 4px 0 0; font-size: 1.2rem; font-weight: 700; }

.block-details-grid { display: grid; grid-template-columns: 2fr 1fr; gap: 24px; }

.section-title {
  display: flex; align-items: center; gap: 10px; font-size: 0.65rem;
  font-weight: 800; text-transform: uppercase; color: #475569; letter-spacing: 0.05em;
  margin-bottom: 24px; border-bottom: 1px solid rgba(255,255,255,0.03); padding-bottom: 12px;
}

.specs-list { display: flex; flex-direction: column; gap: 20px; }
.spec-row { display: flex; flex-direction: column; gap: 8px; }
.s-label { font-size: 0.65rem; font-weight: 700; color: #475569; text-transform: uppercase; }
.s-val-group { background: rgba(0, 0, 0, 0.2); padding: 12px; border-radius: 12px; border: 1px solid rgba(255,255,255,0.02); }
.s-val-group.state-root { background: rgba(16, 185, 129, 0.05); border-color: rgba(16, 185, 129, 0.1); word-break: break-all; }
.text-emerald-400 { color: #34d399; }

.metrics-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 16px; }
.m-box {
  background: rgba(0, 0, 0, 0.2); border: 1px solid rgba(255, 255, 255, 0.05);
  border-radius: 16px; padding: 20px; display: flex; flex-direction: column; gap: 4px;
}
.m-box.highlight { background: rgba(245, 158, 11, 0.03); border-color: rgba(245, 158, 11, 0.1); }
.m-label { font-size: 0.65rem; font-weight: 700; color: #475569; text-transform: uppercase; }
.m-val { font-size: 1.2rem; font-weight: 800; color: #fff; }

.panel-header-sub { display: flex; justify-content: space-between; align-items: center; margin-bottom: 24px; }
.title-meta { display: flex; align-items: center; gap: 12px; }
.title-meta h3 { margin: 0; font-size: 1.1rem; color: #fff; }
.count-tag { font-size: 0.65rem; font-weight: 800; background: rgba(99, 102, 241, 0.1); color: #818cf8; padding: 2px 8px; border-radius: 6px; }

.table-wrap { overflow: hidden; border-radius: 16px; }
.premium-table { width: 100%; border-collapse: collapse; }
.premium-table th {
  padding: 16px; text-align: left; font-size: 0.6rem; font-weight: 800;
  text-transform: uppercase; color: #475569; background: rgba(0, 0, 0, 0.1);
}
.table-row { border-bottom: 1px solid rgba(255,255,255,0.02); cursor: pointer; transition: all 0.2s; }
.table-row:hover { background: rgba(99, 102, 241, 0.03); }

.type-pill { font-size: 0.6rem; font-weight: 800; background: rgba(255,255,255,0.03); padding: 2px 8px; border-radius: 4px; color: #94a3b8; }

.status-chip { font-size: 0.65rem; font-weight: 800; display: inline-block; padding: 2px 10px; border-radius: 99px; }
.status-chip.st-good { background: rgba(16, 185, 129, 0.1); color: #10b981; }
.status-chip.st-bad { background: rgba(239, 68, 68, 0.1); color: #ef4444; }
.status-chip.st-warn { background: rgba(245, 158, 11, 0.1); color: #f59e0b; }

.link { cursor: pointer; text-decoration: underline; text-decoration-style: dotted; }
.link:hover { color: #6366f1; text-decoration-style: solid; }

.empty-state-mini { text-align: center; padding: 40px 0; color: #334155; }
.mono { font-family: 'JetBrains Mono', monospace; }
@keyframes fadeIn { from { opacity: 0; transform: translateY(10px); } to { opacity: 1; transform: translateY(0); } }
.animate-fade-in { animation: fadeIn 0.4s ease-out; }

@media (max-width: 1024px) { .block-details-grid { grid-template-columns: 1fr; } }

/* Finalization Chits Panel Styles */
.chits-panel { margin-top: 24px; border-top: 1px solid rgba(16, 185, 129, 0.1); }
.count-tag.highlight-green { background: rgba(16, 185, 129, 0.15); color: #10b981; }
.text-teal-400 { color: #2dd4bf; }
.text-xs { font-size: 0.75rem; }
.text-gray-500 { color: #64748b; }
.finalized-time { font-family: 'JetBrains Mono', monospace; }
.table-row.no-hover { cursor: default; }
.table-row.no-hover:hover { background: transparent; }
.vote-match-tag {
  font-size: 0.55rem; font-weight: 700; background: rgba(16, 185, 129, 0.15);
  color: #10b981; padding: 1px 6px; border-radius: 4px; margin-left: 8px;
}

/* Multi-round voting styles */
.count-tag.highlight-blue { background: rgba(59, 130, 246, 0.15); color: #3b82f6; }
.rounds-container { display: flex; flex-direction: column; gap: 12px; }
.round-section { border: 1px solid rgba(255,255,255,0.05); border-radius: 12px; overflow: hidden; }
.round-header {
  display: flex; align-items: center; gap: 12px; padding: 12px 16px;
  background: rgba(0,0,0,0.2); cursor: pointer; transition: background 0.2s;
}
.round-header:hover { background: rgba(99, 102, 241, 0.05); }
.round-toggle { color: #64748b; font-size: 0.75rem; }
.round-label { font-weight: 700; color: #fff; font-size: 0.9rem; }
.round-votes { font-size: 0.7rem; color: #10b981; background: rgba(16, 185, 129, 0.1); padding: 2px 8px; border-radius: 4px; }
.round-time { margin-left: auto; }
.round-votes-list { border-top: 1px solid rgba(255,255,255,0.03); }
</style>
