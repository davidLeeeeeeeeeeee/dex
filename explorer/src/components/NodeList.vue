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
  <section class="panel nodes">
    <div class="panel-header">
      <h2>Nodes</h2>
      <span class="muted">{{ selected.size }} selected</span>
    </div>
    <div class="node-list">
      <label 
        v-for="node in nodes" 
        :key="node" 
        class="node-item"
      >
        <input 
          type="checkbox" 
          :checked="selected.has(node)"
          @change="emit('toggle', node, ($event.target as HTMLInputElement).checked)"
        />
        <span>{{ node }}</span>
      </label>
    </div>
  </section>
</template>

