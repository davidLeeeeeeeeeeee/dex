<script setup lang="ts">
defineProps<{
  nodes: string[]
  selected: Set<string>
}>()

const emit = defineEmits<{
  toggle: [node: string, checked: boolean]
}>()
</script>

<template>
  <section class="panel pool-panel">
    <div class="panel-header">
      <div class="title-meta">
        <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" class="text-indigo-500"><path d="M12 2v8"/><path d="m4.93 10.93 1.41 1.41"/><path d="M2 18h2"/><path d="M20 18h2"/><path d="m19.07 10.93-1.41 1.41"/><path d="M22 22H2"/><path d="m8 22 4-10 4 10"/></svg>
        <h2 class="text-sm">Available Pool</h2>
      </div>
      <span class="selection-count">{{ selected.size }} Active</span>
    </div>
    <div class="pool-scroll">
      <div 
        v-for="node in nodes" 
        :key="node" 
        :class="['pool-item', { selected: selected.has(node) }]"
        @click="emit('toggle', node, !selected.has(node))"
      >
        <div class="check-box">
          <svg v-if="selected.has(node)" xmlns="http://www.w3.org/2000/svg" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="4" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>
        </div>
        <span class="mono">{{ node }}</span>
      </div>
      <div v-if="nodes.length === 0" class="empty-pool">No nodes detected...</div>
    </div>
  </section>
</template>

<style scoped>
.pool-panel { padding: 24px; }

.title-meta { display: flex; align-items: center; gap: 10px; }
.title-meta h2 { margin: 0; font-size: 0.85rem; font-weight: 700; color: #fff; }

.selection-count {
  font-size: 0.65rem; font-weight: 800; background: rgba(99, 102, 241, 0.1);
  color: #818cf8; padding: 2px 8px; border-radius: 4px; border: 1px solid rgba(99, 102, 241, 0.2);
}

.pool-scroll {
  display: flex; flex-direction: column; gap: 8px; margin-top: 20px;
  max-height: 400px; overflow-y: auto; padding-right: 4px;
}

.pool-scroll::-webkit-scrollbar { width: 4px; }
.pool-scroll::-webkit-scrollbar-track { background: transparent; }
.pool-scroll::-webkit-scrollbar-thumb { background: rgba(255, 255, 255, 0.05); border-radius: 2px; }

.pool-item {
  display: flex; align-items: center; gap: 12px; padding: 12px;
  background: rgba(255, 255, 255, 0.02); border: 1px solid rgba(255, 255, 255, 0.05);
  border-radius: 12px; cursor: pointer; transition: all 0.2s;
}

.pool-item:hover { background: rgba(255, 255, 255, 0.04); border-color: rgba(255, 255, 255, 0.1); }
.pool-item.selected { background: rgba(99, 102, 241, 0.05); border-color: rgba(99, 102, 241, 0.2); }

.check-box {
  width: 14px; height: 14px; background: rgba(0, 0, 0, 0.2);
  border: 1px solid rgba(255, 255, 255, 0.1); border-radius: 4px;
  display: flex; align-items: center; justify-content: center;
  color: transparent; transition: all 0.2s;
}

.selected .check-box { background: #6366f1; border-color: #6366f1; color: #fff; }
.pool-item span { font-size: 0.75rem; color: #94a3b8; }
.selected span { color: #e2e8f0; font-weight: 600; }

.empty-pool { text-align: center; padding: 32px; font-size: 0.75rem; color: #334155; }
.mono { font-family: 'JetBrains Mono', monospace; }
</style>
