<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import ControlPanel from './components/ControlPanel.vue'
import NodeList from './components/NodeList.vue'
import NodeCards from './components/NodeCards.vue'
import NodeModal from './components/NodeModal.vue'
import SearchPanel from './components/SearchPanel.vue'
import type { NodeSummary, NodesResponse } from './types'
import { fetchNodes, fetchSummary } from './api'
import WithdrawQueue from './components/frost/WithdrawQueue.vue'
import WitnessFlow from './components/frost/WitnessFlow.vue'
import DkgTimeline from './components/frost/DkgTimeline.vue'
import WitnessList from './components/frost/WitnessList.vue'
import TradingPanel from './components/TradingPanel.vue'
import TxDetail from './components/TxDetail.vue'

type TabType = 'overview' | 'search' | 'trading' | 'withdrawals' | 'recharges' | 'dkg'

// Tab 状态
const activeTab = ref<TabType>('overview')

const navigationNodes = [
  {id: 'overview' as TabType, icon: '<path d="M3 9l9-7 9 7v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><polyline points="9 22 9 12 15 12 15 22"/>', label: 'Monitor'},
  {id: 'search' as TabType, icon: '<circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/>', label: 'Explorer'},
  {id: 'trading' as TabType, icon: '<path d="m3 11 18-5v12L3 14v-3z"/><path d="M11.6 16.8 a3 3 0 1 1 -3.2 -4.8"/>', label: 'DEX Pool'},
  {id: 'withdrawals' as TabType, icon: '<path d="M3 21h18"/><path d="M3 10h18"/><path d="m5 6 7-3 7 3"/><path d="M4 10v11"/><path d="M20 10v11"/>', label: 'Withdraws'},
  {id: 'recharges' as TabType, icon: '<path d="M2 12s3-7 10-7 10 7 10 7-3 7-10 7-10-7-10-7Z"/><circle cx="12" cy="12" r="3"/>', label: 'Witness'},
  {id: 'dkg' as TabType, icon: '<rect width="18" height="11" x="3" y="11" rx="2" ry="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>', label: 'Frost DKG'}
]

// 状态
const nodes = ref<string[]>([])
const customNodes = ref<string[]>([])
const selected = ref<Set<string>>(new Set())
const summaryNodes = ref<NodeSummary[]>([])
const lastUpdate = ref<string>('Sync pending')
const elapsedMs = ref<number>(0)
const statusText = ref<string>('System Idle')
const defaultInfo = ref<string>('Detecting seed nodes...')

// 控制选项
const auto = ref(false)
const includeBlock = ref(false)
const includeFrost = ref(false)
const intervalMs = ref(5000)
let timer: number | null = null

// Modal 状态
const modalVisible = ref(false)
const modalAddress = ref('')

// 为 Search 提供选中的第一个节点
const firstSelectedNode = computed(() => {
  const arr = Array.from(selected.value)
  return arr.length > 0 ? arr[0] : ''
})

const selectedProtocolNode = ref('')
const pendingSearchTx = ref('')

const txModalVisible = ref(false)
const txModalId = ref('')
const txModalNode = ref('')

function handleSelectTx(txId: string) {
  // Instead of switching tabs, we show the modal for quick inspection
  txModalId.value = txId
  txModalNode.value = selectedProtocolNode.value || firstSelectedNode.value 
  txModalVisible.value = true
}

function handleAddressClick(_address: string) {
  // For now, switch to search tab for address lookup 
  // (In future we could also do an AddressModal)
  activeTab.value = 'search'
  // We'll need a mechanism in SearchPanel to auto-query address
}

watch(nodes, (newNodes) => {

  if (newNodes.length > 0 && !selectedProtocolNode.value) {
    selectedProtocolNode.value = newNodes[0]
  }
}, { immediate: true })

const storageKeys = {
  custom: 'dex_explorer_custom_nodes',
  selected: 'dex_explorer_selected_nodes',
}

function loadStoredState() {
  try {
    const storedCustom = localStorage.getItem(storageKeys.custom)
    if (storedCustom) customNodes.value = JSON.parse(storedCustom)
    const storedSelected = localStorage.getItem(storageKeys.selected)
    if (storedSelected) selected.value = new Set(JSON.parse(storedSelected))
  } catch {}
}

function normalizeNode(value: string): string {
  return String(value || '').trim().replace(/^https?:\/\//, '').replace(/\/$/, '')
}

async function loadDefaults() {
  statusText.value = 'Contacting seed cluster...'
  try {
    const data: NodesResponse = await fetchNodes()
    const defaults = Array.isArray(data.nodes) ? data.nodes : []
    const set = new Set<string>()
    defaults.forEach(n => set.add(normalizeNode(n)))
    customNodes.value.forEach(n => set.add(normalizeNode(n)))
    nodes.value = Array.from(set).filter(Boolean)
    
    if (selected.value.size === 0 && nodes.value.length > 0) {
      nodes.value.slice(0, 3).forEach(n => selected.value.add(n))
    }
    defaultInfo.value = `Cluster ${data.base_port} + ${data.count}`
    statusText.value = 'Pool synchronized'
  } catch (err: any) {
    statusText.value = `Sync failed: ${err.message}`
  }
}

async function refreshSummary() {
  if (selected.value.size === 0) {
    summaryNodes.value = []
    return
  }
  statusText.value = 'Sampling...'
  try {
    const data = await fetchSummary({
      nodes: Array.from(selected.value),
      include_block: includeBlock.value,
      include_frost: includeFrost.value,
    })
    summaryNodes.value = data.nodes || []
    lastUpdate.value = data.generated_at ? `Live ${data.generated_at}` : 'Snapshot Live'
    elapsedMs.value = data.elapsed_ms || 0
    statusText.value = 'Health OK'
  } catch (err: any) {
    statusText.value = `Scan failed: ${err.message}`
  }
}

function scheduleAuto() {
  if (timer) { clearInterval(timer); timer = null; }
  if (auto.value) timer = window.setInterval(refreshSummary, intervalMs.value)
}

function handleAutoChange(val: boolean) {
  auto.value = val
  scheduleAuto()
}

function handleIntervalChange(val: number) {
  intervalMs.value = val
  scheduleAuto()
}

function handleAddNode(node: string) {
  const normalized = normalizeNode(node)
  if (!normalized) return
  if (!nodes.value.includes(normalized)) {
    nodes.value.push(normalized)
    customNodes.value.push(normalized)
    localStorage.setItem(storageKeys.custom, JSON.stringify(customNodes.value))
  }
  selected.value.add(normalized)
  persistSelection()
}

function handleSelectAll() {
  nodes.value.forEach(n => selected.value.add(n))
  persistSelection()
}

function handleClear() {
  selected.value.clear()
  persistSelection()
}

function handleToggleNode(node: string, checked: boolean) {
  if (checked) selected.value.add(node)
  else selected.value.delete(node)
  persistSelection()
}

function persistSelection() {
  localStorage.setItem(storageKeys.selected, JSON.stringify(Array.from(selected.value)))
}

function handleCardClick(address: string) {
  modalAddress.value = address
  modalVisible.value = true
}

onMounted(() => {
  loadStoredState()
  loadDefaults()
})
</script>

<template>
  <div class="pro-container">
    <header class="main-hero">
      <div class="hero-bg-blur"></div>
      <div class="hero-content">
        <div class="brand-block">
          <div class="p-brand-orb">
            <svg xmlns="http://www.w3.org/2000/svg" width="36" height="36" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2L2 7l10 5 10-5-10-5z"/><path d="M2 17l10 5 10-5"/><path d="M2 12l10 5 10-5"/></svg>
          </div>
          <div class="brand-meta">
            <h1 class="p-title">DEX DISCOVERY <span class="v-pill">PRO</span></h1>
            <p class="p-tagline">Real-time Cross-chain Intelligence Engine</p>
          </div>
        </div>
        <div class="hero-actions">
           <div class="pulse-status">
              <div class="p-dot"></div>
              <span>NETWORK OPERATIONAL</span>
           </div>
        </div>
      </div>
    </header>

    <nav class="p-nav-bar">
      <div class="nav-inner">
        <button 
          v-for="tab in navigationNodes" 
          :key="tab.id" 
          :class="['nav-btn', { active: activeTab === tab.id }]" 
          @click="activeTab = tab.id"
        >
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" v-html="tab.icon"></svg>
          <span>{{ tab.label }}</span>
        </button>
      </div>
    </nav>

    <div class="view-transition">
      <main v-if="activeTab === 'overview'" class="p-layout animate-fade-in">
        <aside class="sidebar">
          <ControlPanel
            :default-info="defaultInfo" :auto="auto" :interval-ms="intervalMs"
            :include-block="includeBlock" :include-frost="includeFrost"
            @refresh="refreshSummary" @update:auto="handleAutoChange"
            @update:interval-ms="handleIntervalChange"
            @update:include-block="v => includeBlock = v"
            @update:include-frost="v => includeFrost = v"
            @add-node="handleAddNode" @select-all="handleSelectAll" @clear="handleClear"
          />
          <NodeList :nodes="nodes" :selected="selected" @toggle="handleToggleNode" />
        </aside>
        <div class="p-content">
          <section class="panel main-stage">
            <header class="stage-header">
              <div class="sh-title">
                 <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M14.5 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7.5L14.5 2z"/><polyline points="14 2 14 8 20 8"/></svg>
                 <h2>Cluster Snapshot</h2>
              </div>
              <div class="sh-meta">
                 <span class="pulse-badge">{{ lastUpdate }}</span>
              </div>
            </header>
            <NodeCards :nodes="summaryNodes" @card-click="handleCardClick" />
          </section>
        </div>
      </main>

      <main v-else-if="activeTab === 'search'" class="p-layout search-mode animate-fade-in">
        <SearchPanel :nodes="nodes" :default-node="firstSelectedNode" :predefined-tx="pendingSearchTx" @select-tx="handleSelectTx" />
      </main>


      <main v-else-if="activeTab === 'trading'" class="p-layout trade-mode animate-fade-in">
        <TradingPanel :nodes="nodes" :default-node="firstSelectedNode" />
      </main>

      <main v-else-if="activeTab === 'withdrawals'" class="p-layout protocol-mode animate-fade-in">
        <div class="protocol-wrap">
          <div class="p-selector">
            <label>Bridge Node Context</label>
            <select v-model="selectedProtocolNode" class="p-select">
              <option v-for="node in nodes" :key="node" :value="node">{{ node }}</option>
            </select>
          </div>
          <WithdrawQueue v-if="selectedProtocolNode" :node="selectedProtocolNode" @select-tx="handleSelectTx" />

        </div>
      </main>

      <main v-else-if="activeTab === 'recharges'" class="p-layout protocol-mode animate-fade-in">
        <div class="protocol-wrap">
          <div class="p-selector">
            <label>Witness Node Context</label>
            <select v-model="selectedProtocolNode" class="p-select">
              <option v-for="node in nodes" :key="node" :value="node">{{ node }}</option>
            </select>
          </div>
          <WitnessList v-if="selectedProtocolNode" :node="selectedProtocolNode" />
          <div style="margin-top: 32px"></div>
          <WitnessFlow v-if="selectedProtocolNode" :node="selectedProtocolNode" @select-tx="handleSelectTx" />
        </div>
      </main>

      <main v-else-if="activeTab === 'dkg'" class="p-layout protocol-mode animate-fade-in">
        <div class="protocol-wrap">
          <div class="p-selector">
            <label>Dkg Target Node</label>
            <select v-model="selectedProtocolNode" class="p-select">
              <option v-for="node in nodes" :key="node" :value="node">{{ node }}</option>
            </select>
          </div>
          <DkgTimeline v-if="selectedProtocolNode" :node="selectedProtocolNode" />
        </div>
      </main>
    </div>

    <footer class="p-status-bar">
      <div class="s-left">
        <div class="heartbeat" :class="{ 'pulse': auto }"></div>
        <span>{{ statusText }}</span>
      </div>
      <div class="s-right">
        <span v-if="elapsedMs" class="mono text-gray-500">RTT: {{ elapsedMs }}ms</span>
      </div>
    </footer>

    <NodeModal :visible="modalVisible" :address="modalAddress" @close="modalVisible = false" />
    
    <TxDetail 
      :show="txModalVisible" 
      :node="txModalNode" 
      :tx-id="txModalId"
      @close="txModalVisible = false"
      @address-click="handleAddressClick"
    />
  </div>
</template>

<style scoped>
@import url('https://fonts.googleapis.com/css2?family=Outfit:wght@400;500;600;700;800&family=JetBrains+Mono:wght@400;500&display=swap');

.pro-container {
  min-height: 100vh; background: #020617; font-family: 'Outfit', sans-serif; color: #fff;
  display: flex; flex-direction: column;
}

.main-hero { position: relative; padding: 40px 60px; overflow: hidden; }
.hero-bg-blur {
  position: absolute; top: -50%; right: -20%; width: 800px; height: 800px;
  background: radial-gradient(circle, rgba(99, 102, 241, 0.1) 0%, transparent 70%);
  filter: blur(80px); pointer-events: none;
}

.hero-content { display: flex; justify-content: space-between; align-items: center; position: relative; z-index: 2; }
.brand-block { display: flex; align-items: center; gap: 24px; }
.p-brand-orb {
  width: 72px; height: 72px; background: linear-gradient(135deg, #6366f1, #3b82f6);
  border-radius: 22px; display: flex; align-items: center; justify-content: center;
  box-shadow: 0 20px 40px rgba(99, 102, 241, 0.4); transform: rotate(-5deg);
}

.p-title { margin: 0; font-size: 1.8rem; font-weight: 900; letter-spacing: -0.04em; display: flex; align-items: center; gap: 16px; }
.v-pill { background: rgba(255, 255, 255, 0.1); padding: 4px 12px; border-radius: 8px; font-size: 0.75rem; vertical-align: middle; border: 1px solid rgba(255,255,255,0.1); }
.p-tagline { margin: 4px 0 0; font-size: 0.95rem; color: #475569; font-weight: 500; }

.pulse-status { display: flex; align-items: center; gap: 12px; background: rgba(16, 185, 129, 0.05); padding: 8px 16px; border-radius: 99px; border: 1px solid rgba(16, 185, 129, 0.2); }
.p-dot { width: 8px; height: 8px; background: #10b981; border-radius: 50%; box-shadow: 0 0 10px #10b981; }

.p-nav-bar { background: rgba(15, 23, 42, 0.5); backdrop-filter: blur(20px); border-bottom: 1px solid rgba(255,255,255,0.05); padding: 0 60px; position: sticky; top: 0; z-index: 100; }
.nav-inner { display: flex; gap: 40px; }
.nav-btn {
  background: none; border: none; padding: 24px 0; color: #475569; font-size: 0.9rem; font-weight: 700;
  display: flex; align-items: center; gap: 10px; cursor: pointer; transition: all 0.3s; position: relative;
}
.nav-btn:hover { color: #818cf8; }
.nav-btn.active { color: #fff; }
.nav-btn.active::after { content: ''; position: absolute; bottom: -1px; left: 0; width: 100%; height: 2px; background: #6366f1; box-shadow: 0 0 10px #6366f1; }

.p-layout { display: grid; grid-template-columns: 340px 1fr; gap: 40px; padding: 40px 60px; flex: 1; }
.sidebar { display: flex; flex-direction: column; gap: 24px; }
.p-content { min-width: 0; }

.stage-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 24px; }
.sh-title { display: flex; align-items: center; gap: 12px; }
.sh-title h2 { margin: 0; font-size: 1.1rem; color: #fff; }
.pulse-badge { font-size: 0.7rem; font-weight: 800; color: #6366f1; padding: 4px 10px; background: rgba(99, 102, 241, 0.05); border-radius: 6px; }

.search-mode, .trade-mode { grid-template-columns: 1fr; }
.protocol-mode { grid-template-columns: 1fr; max-width: 1200px; margin: 0 auto; width: 100%; }

.protocol-wrap { display: flex; flex-direction: column; gap: 24px; }
.p-selector { display: flex; align-items: center; gap: 16px; background: rgba(15, 23, 42, 0.5); padding: 12px 24px; border-radius: 16px; border: 1px solid rgba(255,255,255,0.05); }
.p-selector label { font-size: 0.75rem; font-weight: 800; color: #475569; text-transform: uppercase; }
.p-select { background: none; border: none; color: #818cf8; font-family: 'JetBrains Mono', monospace; font-size: 0.85rem; outline: none; cursor: pointer; }

.p-status-bar { padding: 12px 60px; background: #0f172a; border-top: 1px solid rgba(255,255,255,0.05); display: flex; justify-content: space-between; font-size: 0.7rem; font-weight: 600; color: #334155; }
.s-left { display: flex; align-items: center; gap: 12px; text-transform: uppercase; letter-spacing: 0.05em; }
.heartbeat { width: 6px; height: 6px; background: #334155; border-radius: 50%; }
.heartbeat.pulse { background: #10b981; box-shadow: 0 0 10px #10b981; animation: blink 2s infinite; }

@keyframes blink { 0% { opacity: 0.5; } 50% { opacity: 1; } 100% { opacity: 0.5; } }
@keyframes fadeIn { from { opacity: 0; transform: translateY(10px); } to { opacity: 1; transform: translateY(0); } }
.animate-fade-in { animation: fadeIn 0.6s cubic-bezier(0.4, 0, 0.2, 1); }

@media (max-width: 1200px) {
  .p-layout { grid-template-columns: 1fr; }
  .p-nav-bar { padding: 0 20px; }
  .main-hero { padding: 30px 20px; }
  .p-layout { padding: 20px; }
  .nav-inner { gap: 20px; overflow-x: auto; }
  .nav-btn span { display: none; }
}
</style>
