<script setup lang="ts">
import type { NodeSummary } from '../types'

defineProps<{
  nodes: NodeSummary[]
}>()

const emit = defineEmits<{
  cardClick: [address: string]
}>()

const numberFormat = new Intl.NumberFormat('en-US')

function formatNumber(value?: number | string | null): string {
  if (value === undefined || value === null) return '-'
  if (typeof value === 'string') return value
  return numberFormat.format(value)
}

function truncate(value?: string, max = 16): string {
  if (!value) return '-'
  if (value.length <= max) return value
  return `${value.slice(0, max)}...`
}

function statusInfo(node: NodeSummary) {
  if (node.error) return { class: 'status-error', icon: 'error', label: 'Offline' }
  if (node.status?.toLowerCase() === 'ok') return { class: 'status-ok', icon: 'check', label: 'Online' }
  return { class: 'status-warn', icon: 'alert', label: 'Warning' }
}

function totalApiCalls(node: NodeSummary): number {
  const stats = node.frost_metrics?.api_call_stats
  if (!stats) return 0
  return Object.values(stats).reduce((sum, count) => sum + count, 0)
}

function topApiCalls(node: NodeSummary, n: number = 2): Array<{ name: string, count: number }> {
  const stats = node.frost_metrics?.api_call_stats
  if (!stats) return []
  return Object.entries(stats)
    .sort((a, b) => b[1] - a[1])
    .slice(0, n)
    .map(([name, count]) => ({ name, count }))
}
</script>

<template>
  <div class="node-grid">
    <div v-if="nodes.length === 0" class="empty-placeholder">
      <div class="empty-orb">
        <svg xmlns="http://www.w3.org/2000/svg" width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" x2="12" y1="8" y2="12"/><line x1="12" x2="12.01" y1="16" y2="16"/></svg>
      </div>
      <p>Select nodes to start monitoring</p>
    </div>
    
    <div 
      v-for="(node, index) in nodes" 
      :key="node.address"
      class="node-premium-card group"
      :style="{ animationDelay: `${index * 40}ms` }"
      @click="emit('cardClick', node.address)"
    >
      <div class="card-bg-glow"></div>
      
      <!-- Card Header -->
      <div class="node-header">
        <div class="node-title">
          <div class="node-icon" :class="statusInfo(node).class">
            <svg v-if="statusInfo(node).icon === 'check'" xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>
            <svg v-else-if="statusInfo(node).icon === 'error'" xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
            <svg v-else xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" x2="12" y1="8" y2="12"/><line x1="12" x2="12.01" y1="16" y2="16"/></svg>
          </div>
          <div class="title-stack">
            <h3>{{ node.address || 'unknown' }}</h3>
            <span :class="['status-label-text', statusInfo(node).class]">{{ statusInfo(node).label }}</span>
          </div>
        </div>
        <div class="latency-pill">
          <svg xmlns="http://www.w3.org/2000/svg" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><polyline points="22 12 18 12 15 21 9 3 6 12 2 12"/></svg>
          {{ node.latency_ms || 0 }}ms
        </div>
      </div>

      <!-- Main Height Stats -->
      <div class="height-grid">
        <div class="height-item">
          <span class="h-label">NET HEIGHT</span>
          <span class="h-value">{{ formatNumber(node.current_height) }}</span>
        </div>
        <div class="height-item highlight">
          <span class="h-label">ACCEPTED</span>
          <span class="h-value">{{ formatNumber(node.last_accepted_height) }}</span>
        </div>
      </div>

      <!-- Info Details -->
      <div class="details-stack">
        <div class="detail-row" v-if="node.error">
          <svg class="text-red-500" xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" x2="12" y1="8" y2="12"/><line x1="12" x2="12.01" y1="16" y2="16"/></svg>
          <span class="text-red-400 truncate">{{ node.error }}</span>
        </div>
        
        <div class="detail-row" v-if="node.block">
          <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="18" height="18" x="3" y="3" rx="2" ry="2"/><line x1="3" x2="21" y1="9" y2="9"/><line x1="3" x2="21" y1="15" y2="15"/><line x1="9" x2="9" y1="3" y2="21"/><line x1="15" x2="15" y1="3" y2="21"/></svg>
          <span class="mono">{{ truncate(node.block.block_hash, 12) }}</span>
          <span class="badge ml-auto">{{ formatNumber(node.block.tx_count) }} TXs</span>
        </div>

        <div class="detail-row" v-if="node.frost_metrics">
          <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/></svg>
          <span>FROST Jobs</span>
          <span class="text-indigo-400 font-bold ml-auto">{{ formatNumber(node.frost_metrics.frost_jobs) }}</span>
        </div>
      </div>

      <!-- Bottom API Stats -->
      <div v-if="totalApiCalls(node) > 0" class="api-footer">
        <div class="api-header">
           <span>API LOADS</span>
           <span class="text-white font-bold">{{ formatNumber(totalApiCalls(node)) }}</span>
        </div>
        <div class="api-bars">
           <div v-for="api in topApiCalls(node)" :key="api.name" class="api-bar-item">
              <span class="api-name truncate">{{ api.name }}</span>
              <div class="api-bar-bg">
                <div class="api-bar-fill" :style="{ width: (api.count / totalApiCalls(node) * 100) + '%' }"></div>
              </div>
              <span class="api-count">{{ formatNumber(api.count) }}</span>
           </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
@import url('https://fonts.googleapis.com/css2?family=Outfit:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap');

.node-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: 20px;
}

.node-premium-card {
  background: rgba(13, 17, 23, 0.4);
  border: 1px solid rgba(255, 255, 255, 0.05);
  border-radius: 20px;
  padding: 24px;
  position: relative;
  overflow: hidden;
  cursor: pointer;
  transition: all 0.3s cubic-bezier(0.4, 0, 0.2, 1);
  display: flex;
  flex-direction: column;
}

.node-premium-card:hover {
  background: rgba(255, 255, 255, 0.03);
  border-color: rgba(99, 102, 241, 0.3);
  transform: translateY(-4px);
  box-shadow: 0 10px 30px -5px rgba(0, 0, 0, 0.5);
}

.card-bg-glow {
  position: absolute;
  top: 0; right: 0;
  width: 150px; height: 150px;
  background: radial-gradient(circle at top right, rgba(99, 102, 241, 0.05), transparent 70%);
  pointer-events: none;
}

.node-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 24px;
}

.node-title {
  display: flex;
  align-items: center;
  gap: 12px;
}

.node-icon {
  width: 36px; height: 36px;
  border-radius: 10px;
  display: flex; align-items: center; justify-content: center;
  transition: all 0.3s;
}
.node-icon.status-ok { background: rgba(16, 185, 129, 0.1); color: #10b981; }
.node-icon.status-warn { background: rgba(245, 158, 11, 0.1); color: #f59e0b; }
.node-icon.status-error { background: rgba(239, 68, 68, 0.1); color: #ef4444; }

.title-stack h3 {
  margin: 0; font-size: 0.9rem; font-weight: 700; color: #fff;
  letter-spacing: -0.01em;
}

.status-label-text {
  font-size: 0.6rem; font-weight: 800; text-transform: uppercase;
  letter-spacing: 0.05em;
}
.status-label-text.status-ok { color: #10b981; }
.status-label-text.status-warn { color: #f59e0b; }
.status-label-text.status-error { color: #ef4444; }

.latency-pill {
  background: rgba(255, 255, 255, 0.03);
  padding: 4px 10px; border-radius: 99px;
  font-size: 0.65rem; font-weight: 700; color: #64748b;
  display: flex; align-items: center; gap: 6px;
  border: 1px solid rgba(255, 255, 255, 0.05);
}

.height-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
  margin-bottom: 24px;
}

.height-item {
  background: rgba(0, 0, 0, 0.2);
  padding: 12px; border-radius: 12px;
  display: flex; flex-direction: column;
  border: 1px solid rgba(255, 255, 255, 0.02);
}

.height-item.highlight {
  background: rgba(99, 102, 241, 0.03);
  border-color: rgba(99, 102, 241, 0.1);
}

.h-label { font-size: 0.55rem; font-weight: 800; color: #475569; letter-spacing: 0.05em; margin-bottom: 4px; }
.h-value { font-size: 1.1rem; font-weight: 800; color: #e2e8f0; font-family: 'JetBrains Mono', monospace; }
.highlight .h-value { color: #818cf8; }

.details-stack {
  display: flex; flex-direction: column; gap: 10px;
  padding: 16px; background: rgba(0, 0, 0, 0.1); border-radius: 12px;
  margin-bottom: 20px; flex: 1;
}

.detail-row {
  display: flex; align-items: center; gap: 10px;
  font-size: 0.75rem; color: #94a3b8;
}

.badge {
  background: rgba(255, 255, 255, 0.05);
  padding: 2px 6px; border-radius: 4px;
  font-size: 0.6rem; font-weight: 700; color: #64748b;
}

.mono { font-family: 'JetBrains Mono', monospace; }

.api-footer {
  border-top: 1px solid rgba(255, 255, 255, 0.05);
  padding-top: 16px;
}

.api-header {
  display: flex; justify-content: space-between;
  font-size: 0.6rem; font-weight: 800; color: #475569; margin-bottom: 12px;
  text-transform: uppercase; letter-spacing: 0.05em;
}

.api-bars { display: flex; flex-direction: column; gap: 10px; }

.api-bar-item {
  display: flex; align-items: center; gap: 8px;
  font-size: 0.65rem;
}

.api-name { width: 60px; color: #64748b; font-family: monospace; }
.api-bar-bg { flex: 1; height: 4px; background: rgba(255, 255, 255, 0.03); border-radius: 2px; position: relative; }
.api-bar-fill { height: 100%; background: #6366f1; border-radius: 2px; }
.api-count { font-weight: 700; color: #94a3b8; font-family: monospace; width: 30px; text-align: right; }

.empty-placeholder {
  text-align: center; padding: 60px; color: #475569;
}

.empty-orb {
  width: 64px; height: 64px; border-radius: 50%;
  background: rgba(255, 255, 255, 0.02);
  display: flex; align-items: center; justify-content: center;
  margin: 0 auto 20px;
}
</style>
