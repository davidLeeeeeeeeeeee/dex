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
  <section class="panel controls">
    <div class="panel-header">
      <h2>Controls</h2>
      <span class="muted">{{ defaultInfo }}</span>
    </div>
    
    <div class="row">
      <button class="primary" @click="emit('refresh')">Refresh</button>
      <label class="toggle">
        <input 
          type="checkbox" 
          :checked="auto"
          @change="emit('update:auto', ($event.target as HTMLInputElement).checked)"
        />
        <span>Auto</span>
      </label>
      <select 
        :value="intervalMs"
        @change="emit('update:intervalMs', Number(($event.target as HTMLSelectElement).value))"
      >
        <option value="3000">3s</option>
        <option value="5000">5s</option>
        <option value="10000">10s</option>
      </select>
    </div>
    
    <div class="row">
      <label class="checkbox">
        <input 
          type="checkbox" 
          :checked="includeBlock"
          @change="emit('update:includeBlock', ($event.target as HTMLInputElement).checked)"
        />
        <span>Include latest block</span>
      </label>
      <label class="checkbox">
        <input 
          type="checkbox" 
          :checked="includeFrost"
          @change="emit('update:includeFrost', ($event.target as HTMLInputElement).checked)"
        />
        <span>Include Frost metrics</span>
      </label>
    </div>
    
    <div class="row">
      <input 
        v-model="nodeInput"
        type="text" 
        placeholder="127.0.0.1:6000"
        @keyup.enter="handleAddNode"
      />
      <button class="ghost" @click="handleAddNode">Add node</button>
    </div>
    
    <div class="row">
      <button class="ghost" @click="emit('selectAll')">Select all</button>
      <button class="ghost" @click="emit('clear')">Clear</button>
    </div>
  </section>
</template>

