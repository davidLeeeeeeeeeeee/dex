<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  type: string
  details: Record<string, any>
}>()

const isWitness = computed(() => props.type.includes('Witness'))
const isFrost = computed(() => props.type.includes('Frost'))
const isDKG = computed(() => props.type.includes('Dkg'))

function formatVal(val: any): string {
  if (val === null || val === undefined) return '-'
  if (typeof val === 'string') return val
  return JSON.stringify(val)
}
</script>

<template>
  <div class="tx-type-renderer mt-4">
    <!-- Witness Specific Render -->
    <div v-if="isWitness" class="type-box witness-box">
      <div class="flex items-center mb-2">
        <span class="text-xl mr-2">👁️</span>
        <h4 class="font-bold text-blue-300">Witness Protocol Module</h4>
      </div>
      <div class="grid grid-cols-2 gap-x-4 gap-y-2 text-sm">
        <div v-for="(val, key) in details" :key="key" class="detail-item">
          <span class="text-gray-500 capitalize">{{ key.replace(/_/g, ' ') }}:</span>
          <span class="text-white ml-2 font-mono">{{ formatVal(val) }}</span>
        </div>
      </div>
    </div>

    <!-- Frost Specific Render -->
    <div v-else-if="isFrost || isDKG" class="type-box frost-box">
      <div class="flex items-center mb-2">
        <span class="text-xl mr-2">❄️</span>
        <h4 class="font-bold text-indigo-300">Frost Threshold Protocol</h4>
      </div>
      <div class="grid grid-cols-1 gap-2 text-sm">
        <div v-for="(val, key) in details" :key="key" class="detail-item border-l-2 border-indigo-500/30 pl-3">
          <span class="text-gray-400 text-xs block mb-1 uppercase tracking-wider">{{ key.replace(/_/g, ' ') }}</span>
          <span class="text-indigo-100 break-all font-mono bg-black/30 p-1 rounded">{{ formatVal(val) }}</span>
        </div>
      </div>
    </div>

    <!-- Generic Fallback -->
    <div v-else class="type-box generic-box">
      <h4 class="text-gray-400 mb-2 text-sm uppercase">Raw Data Body</h4>
      <pre class="bg-black/50 p-3 rounded text-xs text-green-400 overflow-x-auto">{{ details }}</pre>
    </div>
  </div>
</template>

<style scoped>
.type-box {
  background: rgba(0, 0, 0, 0.2);
  border-radius: 8px;
  padding: 16px;
  border: 1px solid rgba(255, 255, 255, 0.05);
}

.witness-box { border-left: 4px solid #3b82f6; }
.frost-box { border-left: 4px solid #6366f1; }
.generic-box { border-left: 4px solid #4b5563; }

.detail-item {
  display: flex;
  flex-direction: column;
}
</style>
