<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import ControlPanel from './components/ControlPanel.vue'
import NodeList from './components/NodeList.vue'
import NodeCards from './components/NodeCards.vue'
import NodeModal from './components/NodeModal.vue'
import SearchPanel from './components/SearchPanel.vue'
import type { NodeSummary, NodesResponse } from './types'
import { fetchNodes, fetchSummary } from './api'
import FrostDashboard from './components/frost/FrostDashboard.vue'

// Tab 状态
const activeTab = ref<'overview' | 'search' | 'frost'>('overview')

// 状态
const nodes = ref<string[]>([])
const customNodes = ref<string[]>([])
const selected = ref<Set<string>>(new Set())
const summaryNodes = ref<NodeSummary[]>([])
const lastUpdate = ref<string>('Not loaded')
const elapsedMs = ref<number>(0)
const statusText = ref<string>('Idle')
const defaultInfo = ref<string>('Loading defaults...')

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

// 存储键
const storageKeys = {
  custom: 'dex_explorer_custom_nodes',
  selected: 'dex_explorer_selected_nodes',
}

// 加载本地存储的状态
function loadStoredState() {
  try {
    const storedCustom = localStorage.getItem(storageKeys.custom)
    if (storedCustom) customNodes.value = JSON.parse(storedCustom)
    
    const storedSelected = localStorage.getItem(storageKeys.selected)
    if (storedSelected) selected.value = new Set(JSON.parse(storedSelected))
  } catch {
    // 忽略解析错误
  }
}

// 合并节点列表
function mergeNodes(defaults: string[], custom: string[]): string[] {
  const set = new Set<string>()
  defaults.forEach(n => set.add(normalizeNode(n)))
  custom.forEach(n => set.add(normalizeNode(n)))
  return Array.from(set).filter(Boolean)
}

function normalizeNode(value: string): string {
  return String(value || '').trim().replace(/^https?:\/\//, '').replace(/\/$/, '')
}

// 加载默认节点
async function loadDefaults() {
  statusText.value = 'Loading defaults...'
  try {
    const data: NodesResponse = await fetchNodes()
    const defaults = Array.isArray(data.nodes) ? data.nodes : []
    nodes.value = mergeNodes(defaults, customNodes.value)
    
    if (selected.value.size === 0 && nodes.value.length > 0) {
      nodes.value.slice(0, 3).forEach(n => selected.value.add(n))
    }
    
    defaultInfo.value = `Default ${data.base_port} + ${data.count}`
    statusText.value = 'Defaults ready'
  } catch (err: any) {
    statusText.value = `Failed to load defaults: ${err.message}`
  }
}

// 刷新摘要
async function refreshSummary() {
  if (selected.value.size === 0) {
    summaryNodes.value = []
    return
  }
  
  statusText.value = 'Refreshing...'
  try {
    const data = await fetchSummary({
      nodes: Array.from(selected.value),
      include_block: includeBlock.value,
      include_frost: includeFrost.value,
    })
    
    summaryNodes.value = data.nodes || []
    lastUpdate.value = data.generated_at ? `Updated ${data.generated_at}` : 'Updated'
    elapsedMs.value = data.elapsed_ms || 0
    statusText.value = 'Snapshot updated'
  } catch (err: any) {
    statusText.value = `Snapshot failed: ${err.message}`
  }
}

// 自动刷新调度
function scheduleAuto() {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
  if (auto.value) {
    timer = window.setInterval(refreshSummary, intervalMs.value)
  }
}

// 事件处理
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
  if (checked) {
    selected.value.add(node)
  } else {
    selected.value.delete(node)
  }
  persistSelection()
}

function persistSelection() {
  localStorage.setItem(storageKeys.selected, JSON.stringify(Array.from(selected.value)))
}

function handleCardClick(address: string) {
  modalAddress.value = address
  modalVisible.value = true
}

function handleCloseModal() {
  modalVisible.value = false
}

onMounted(() => {
  loadStoredState()
  loadDefaults()
})
</script>

<template>
  <header class="hero">
    <div class="hero-content">
      <p class="eyebrow">Local Observer</p>
      <h1>Dex Node Explorer</h1>
      <p class="subtle">
        Select multiple nodes and pull live status from the HTTP/3 protobuf
        APIs without adding a separate backend.
      </p>
    </div>
    <div class="hero-badge">
      <div class="badge-dot"></div>
      <span>Local only</span>
    </div>
  </header>

  <nav class="tab-nav">
    <button
      :class="['tab-btn', { active: activeTab === 'overview' }]"
      @click="activeTab = 'overview'"
    >节点总览</button>
    <button
      :class="['tab-btn', { active: activeTab === 'search' }]"
      @click="activeTab = 'search'"
    >Search</button>
    <button
      :class="['tab-btn', { active: activeTab === 'frost' }]"
      @click="activeTab = 'frost'"
    >Protocol</button>
  </nav>

  <main v-if="activeTab === 'overview'" class="layout">
    <div class="left">
      <ControlPanel
        :default-info="defaultInfo"
        :auto="auto"
        :interval-ms="intervalMs"
        :include-block="includeBlock"
        :include-frost="includeFrost"
        @refresh="refreshSummary"
        @update:auto="handleAutoChange"
        @update:interval-ms="handleIntervalChange"
        @update:include-block="v => includeBlock = v"
        @update:include-frost="v => includeFrost = v"
        @add-node="handleAddNode"
        @select-all="handleSelectAll"
        @clear="handleClear"
      />

      <NodeList
        :nodes="nodes"
        :selected="selected"
        @toggle="handleToggleNode"
      />
    </div>

    <div class="right">
      <section class="panel summary">
        <div class="panel-header">
          <h2>Live Snapshot</h2>
          <span class="muted">{{ lastUpdate }}</span>
        </div>
        <NodeCards
          :nodes="summaryNodes"
          @card-click="handleCardClick"
        />
      </section>
    </div>
  </main>

  <main v-else-if="activeTab === 'search'" class="layout search-layout">
    <SearchPanel
      :nodes="nodes"
      :default-node="firstSelectedNode"
    />
  </main>

  <main v-else-if="activeTab === 'frost'" class="layout frost-layout">
    <FrostDashboard
      :nodes="nodes"
      :default-node="firstSelectedNode"
    />
  </main>

  <footer class="footer">
    <span>{{ statusText }}</span>
    <span class="muted">{{ elapsedMs ? `Elapsed ${elapsedMs} ms` : '' }}</span>
  </footer>

  <NodeModal
    :visible="modalVisible"
    :address="modalAddress"
    @close="handleCloseModal"
  />
</template>
