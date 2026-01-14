<script setup lang="ts">
import { ref, watch } from 'vue'
import WithdrawQueue from './WithdrawQueue.vue'
import WitnessFlow from './WitnessFlow.vue'
import DkgTimeline from './DkgTimeline.vue'

const props = defineProps<{
  nodes: string[]
  defaultNode: string
}>()

const selectedNode = ref(props.defaultNode || (props.nodes.length > 0 ? props.nodes[0] : ''))
const subTab = ref<'withdraw' | 'witness' | 'dkg'>('withdraw')

watch(() => props.defaultNode, (val) => {
  if (val && !selectedNode.value) selectedNode.value = val
})

const tabs = [
  { key: 'withdraw', label: 'Withdrawals', icon: '🏦', color: 'indigo' },
  { key: 'witness', label: 'Recharges', icon: '👁️', color: 'emerald' },
  { key: 'dkg', label: 'DKG Sessions', icon: '🔐', color: 'purple' },
] as const
</script>

<template>
  <div class="frost-dashboard w-full max-w-screen-2xl mx-auto">
    <!-- Header Card -->
    <div class="glass-panel p-6 mb-6">
      <div class="flex flex-col lg:flex-row lg:items-center lg:justify-between gap-4">
        <!-- Node Selector -->
        <div class="flex items-center gap-4">
          <div class="flex items-center gap-2">
            <span class="text-2xl">🌐</span>
            <div>
              <p class="text-xs text-gray-500 uppercase tracking-wide">Target Node</p>
              <select v-model="selectedNode" class="node-select mt-1">
                <option v-for="node in nodes" :key="node" :value="node">{{ node }}</option>
              </select>
            </div>
          </div>
        </div>

        <!-- Tab Switcher -->
        <div class="flex bg-black/30 p-1.5 rounded-xl gap-1">
          <button
            v-for="tab in tabs"
            :key="tab.key"
            @click="subTab = tab.key"
            :class="[
              'flex items-center gap-2 px-5 py-2.5 rounded-lg text-sm font-medium transition-all duration-200',
              subTab === tab.key
                ? 'bg-gradient-to-r from-indigo-500 to-purple-500 text-white shadow-lg shadow-indigo-500/30'
                : 'text-gray-400 hover:text-white hover:bg-white/5'
            ]"
          >
            <span>{{ tab.icon }}</span>
            <span>{{ tab.label }}</span>
          </button>
        </div>
      </div>
    </div>

    <!-- Content Area -->
    <div v-if="selectedNode" class="content-area">
      <WithdrawQueue v-if="subTab === 'withdraw'" :node="selectedNode" />
      <WitnessFlow v-if="subTab === 'witness'" :node="selectedNode" />
      <DkgTimeline v-if="subTab === 'dkg'" :node="selectedNode" />
    </div>

    <!-- Empty State -->
    <div v-else class="glass-panel p-16 text-center">
      <div class="text-6xl mb-4">🔗</div>
      <h3 class="text-xl font-semibold text-white mb-2">No Node Selected</h3>
      <p class="text-gray-400">Please select a node from the dropdown to view protocol details.</p>
    </div>
  </div>
</template>

<style scoped>
.node-select {
  background: linear-gradient(135deg, #1f2937 0%, #111827 100%);
  color: white;
  border: 1px solid rgba(255, 255, 255, 0.1);
  border-radius: 8px;
  padding: 8px 16px;
  min-width: 200px;
  outline: none;
  cursor: pointer;
  transition: all 0.2s;
}

.node-select:hover {
  border-color: rgba(99, 102, 241, 0.5);
}

.node-select:focus {
  border-color: #6366f1;
  box-shadow: 0 0 0 3px rgba(99, 102, 241, 0.2);
}
</style>
