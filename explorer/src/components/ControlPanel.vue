<script setup lang="ts">
import { ref } from 'vue'

defineProps<{
  defaultInfo: string
  auto: boolean
  intervalMs: number
  includeBlock: boolean
  includeFrost: boolean
}>()

const emit = defineEmits<{
  refresh: []
  'update:auto': [value: boolean]
  'update:intervalMs': [value: number]
  'update:includeBlock': [value: boolean]
  'update:includeFrost': [value: boolean]
  addNode: [node: string]
  selectAll: []
  clear: []
}>()

const nodeInput = ref('')

function handleAddNode() {
  if (nodeInput.value.trim()) {
    emit('addNode', nodeInput.value.trim())
    nodeInput.value = ''
  }
}
</script>

<template>
  <section class="panel control-panel">
    <div class="panel-header">
      <div class="header-title">
        <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="text-indigo-500"><path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/></svg>
        <h2>Control Center</h2>
      </div>
      <span class="info-tag">{{ defaultInfo }}</span>
    </div>
    
    <div class="control-rows">
      <!-- Sync Row -->
      <div class="control-row">
        <button class="action-btn primary-glow" @click="emit('refresh')">
          <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/></svg>
          Snapshot
        </button>
        
        <div class="toggle-group">
          <label class="premium-toggle">
            <input 
              type="checkbox" 
              :checked="auto"
              @change="emit('update:auto', ($event.target as HTMLInputElement).checked)"
            />
            <div class="toggle-track">
              <div class="toggle-thumb"></div>
            </div>
            <span>Auto Sync</span>
          </label>
        </div>

        <div class="select-wrapper">
          <select 
            :value="intervalMs"
            @change="emit('update:intervalMs', Number(($event.target as HTMLSelectElement).value))"
          >
            <option value="3000">3s</option>
            <option value="5000">5s</option>
            <option value="10000">10s</option>
          </select>
          <svg class="select-icon" xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><path d="m6 9 6 6 6-6"/></svg>
        </div>
      </div>
      
      <!-- Options Row -->
      <div class="options-row">
        <label class="premium-checkbox">
          <input 
            type="checkbox" 
            :checked="includeBlock"
            @change="emit('update:includeBlock', ($event.target as HTMLInputElement).checked)"
          />
          <div class="check-box">
             <svg xmlns="http://www.w3.org/2000/svg" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="4" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>
          </div>
          <span>Block Meta</span>
        </label>
        <label class="premium-checkbox">
          <input 
            type="checkbox" 
            :checked="includeFrost"
            @change="emit('update:includeFrost', ($event.target as HTMLInputElement).checked)"
          />
          <div class="check-box">
             <svg xmlns="http://www.w3.org/2000/svg" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="4" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>
          </div>
          <span>Frost Metrics</span>
        </label>
      </div>
      
      <div class="divider"></div>

      <!-- Add Node Row -->
      <div class="input-group">
        <div class="input-orb">
          <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" x2="12" y1="8" y2="16"/><line x1="8" x2="16" y1="12" y2="12"/></svg>
        </div>
        <input 
          v-model="nodeInput"
          type="text" 
          placeholder="Enter Node Endpoint..."
          @keyup.enter="handleAddNode"
        />
        <button class="add-btn" @click="handleAddNode">Connect</button>
      </div>
      
      <div class="bulk-actions">
        <button class="action-link" @click="emit('selectAll')">Select All</button>
        <button class="action-link danger" @click="emit('clear')">Clear Nodes</button>
      </div>
    </div>
  </section>
</template>

<style scoped>
.control-panel {
  padding: 28px;
}

.header-title {
  display: flex;
  align-items: center;
  gap: 12px;
}

.header-title h2 { margin: 0; font-size: 1.1rem; font-weight: 700; color: #fff; }

.info-tag {
  font-size: 0.65rem;
  font-weight: 700;
  background: rgba(255, 255, 255, 0.03);
  padding: 4px 10px;
  border-radius: 6px;
  color: #64748b;
  border: 1px solid rgba(255, 255, 255, 0.05);
}

.control-rows {
  display: flex;
  flex-direction: column;
  gap: 24px;
  margin-top: 24px;
}

.control-row {
  display: flex;
  align-items: center;
  gap: 16px;
}

.action-btn {
  background: #6366f1;
  color: #fff;
  border: none;
  padding: 10px 18px;
  border-radius: 10px;
  font-size: 0.85rem;
  font-weight: 700;
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  transition: all 0.3s;
}

.action-btn:hover {
  transform: translateY(-2px);
  box-shadow: 0 8px 20px rgba(99, 102, 241, 0.4);
}

/* Premium Toggle */
.premium-toggle {
  display: flex;
  align-items: center;
  gap: 10px;
  cursor: pointer;
  font-size: 0.85rem;
  color: #94a3b8;
  font-weight: 600;
}

.premium-toggle input { display: none; }
.toggle-track {
  width: 36px; height: 18px;
  background: rgba(255, 255, 255, 0.1);
  border-radius: 20px;
  position: relative;
  transition: background 0.3s;
}
.toggle-thumb {
  width: 14px; height: 14px;
  background: #fff;
  border-radius: 50%;
  position: absolute;
  top: 2px; left: 2px;
  transition: transform 0.3s;
}
.premium-toggle input:checked + .toggle-track { background: #10b981; }
.premium-toggle input:checked + .toggle-track .toggle-thumb { transform: translateX(18px); }

/* Custom Select */
.select-wrapper { position: relative; }
.select-wrapper select {
  padding: 8px 32px 8px 12px;
  background: rgba(255, 255, 255, 0.03);
  border: 1px solid rgba(255, 255, 255, 0.05);
  border-radius: 8px;
  font-size: 0.85rem; color: #fff;
  appearance: none;
  cursor: pointer;
}
.select-icon { position: absolute; right: 10px; top: 12px; color: #475569; pointer-events: none; }

/* Options */
.options-row { display: flex; gap: 20px; }

.premium-checkbox {
  display: flex; align-items: center; gap: 10px;
  cursor: pointer; font-size: 0.8rem; color: #64748b; font-weight: 600;
}
.premium-checkbox input { display: none; }
.check-box {
  width: 16px; height: 16px;
  background: rgba(255, 255, 255, 0.05);
  border: 1px solid rgba(255, 255, 255, 0.1);
  border-radius: 4px;
  display: flex; align-items: center; justify-content: center;
  color: transparent; transition: all 0.2s;
}
.premium-checkbox input:checked + .check-box { background: #6366f1; border-color: #6366f1; color: #fff; }

.divider { height: 1px; background: rgba(255, 255, 255, 0.05); }

/* Input Group */
.input-group {
  display: flex; align-items: center;
  background: rgba(0, 0, 0, 0.2);
  border: 1px solid rgba(255, 255, 255, 0.05);
  border-radius: 12px;
  padding: 4px 4px 4px 16px;
  transition: border-color 0.3s;
}
.input-group:focus-within { border-color: #6366f1; }
.input-orb { color: #475569; display: flex; align-items: center; }
.input-group input {
  background: transparent; border: none; flex: 1; padding: 10px;
  font-size: 0.85rem; color: #fff;
}
.input-group input:focus { outline: none; }

.add-btn {
  background: rgba(99, 102, 241, 0.1);
  color: #818cf8;
  border: none;
  padding: 8px 16px;
  border-radius: 8px;
  font-size: 0.8rem;
  font-weight: 700;
  cursor: pointer;
  transition: all 0.2s;
}
.add-btn:hover { background: #6366f1; color: #fff; }

.bulk-actions { display: flex; gap: 16px; justify-content: flex-end; }
.action-link {
  background: none; border: none; padding: 0;
  font-size: 0.75rem; font-weight: 700; color: #475569;
  cursor: pointer; transition: color 0.2s;
}
.action-link:hover { color: #818cf8; }
.action-link.danger:hover { color: #ef4444; }
</style>
