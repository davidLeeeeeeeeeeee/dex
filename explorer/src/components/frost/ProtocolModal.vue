<script setup lang="ts">
import { ref, watch, onMounted, onUnmounted, nextTick } from 'vue'
import mermaid from 'mermaid'

const props = defineProps<{
  show: boolean
  title: string
  definition: string
}>()

const emit = defineEmits(['close'])

const svgContent = ref('')
const isRendering = ref(false)
const renderError = ref('')
const rawDefinition = ref('')
const initialized = ref(false)

const initMermaid = () => {
  if (initialized.value) return
  try {
    mermaid.initialize({
      startOnLoad: false,
      theme: 'dark',
      securityLevel: 'loose',
      fontFamily: 'ui-monospace, monospace',
      flowchart: { htmlLabels: true, curve: 'basis' },
    })
    initialized.value = true
    console.log('[Mermaid] Initialized successfully')
  } catch (e) {
    console.error('[Mermaid] Init failed', e)
    renderError.value = 'Failed to initialize Mermaid'
  }
}

const renderChart = async () => {
  console.log('[Mermaid] renderChart called, show:', props.show, 'definition:', props.definition?.substring(0, 50))

  if (!props.definition) {
    renderError.value = 'No diagram definition provided'
    return
  }
  if (!props.show) return

  initMermaid()

  if (!initialized.value) {
    renderError.value = 'Mermaid not initialized'
    return
  }

  isRendering.value = true
  renderError.value = ''
  svgContent.value = ''
  rawDefinition.value = props.definition

  await nextTick()

  try {
    const id = 'mermaid-svg-' + Math.random().toString(36).substring(2, 9)
    console.log('[Mermaid] Rendering with id:', id)
    const result = await mermaid.render(id, props.definition)
    console.log('[Mermaid] Render result:', result ? 'success' : 'empty')
    svgContent.value = result.svg
  } catch (e: any) {
    console.error('[Mermaid] Render failed:', e)
    renderError.value = String(e.message || e || 'Unknown render error')
  } finally {
    isRendering.value = false
  }
}

onMounted(() => {
  initMermaid()
})

// 确保组件销毁时恢复滚动
onUnmounted(() => {
  document.body.style.overflow = ''
})

watch(() => props.show, (val) => {
  if (val) {
    nextTick(() => renderChart())
  } else {
    document.body.style.overflow = ''
    svgContent.value = ''
    renderError.value = ''
  }
})

watch(() => props.definition, () => {
  if (props.show) {
    renderChart()
  }
})
</script>

<template>
  <Teleport to="body">
    <Transition name="fade">
      <div v-if="show" class="modal-root" @click.self="emit('close')">
        <div class="modal-backdrop"></div>

        <div class="modal-container" @click.stop>
          <!-- Header -->
          <div class="modal-header">
            <h3 class="modal-title">{{ title }}</h3>
            <button @click="emit('close')" class="close-btn">&times;</button>
          </div>

          <!-- Content -->
          <div class="modal-body">
            <div v-if="isRendering" class="status-msg">
              <span class="spinner"></span>
              <span>Rendering diagram...</span>
            </div>
            <div v-else-if="renderError" class="error-container">
              <div class="error-msg">
                <strong>Render Error:</strong> {{ renderError }}
              </div>
              <div v-if="rawDefinition" class="raw-definition">
                <p class="raw-label">Raw Mermaid Definition:</p>
                <pre>{{ rawDefinition }}</pre>
              </div>
            </div>
            <div v-else-if="svgContent" class="mermaid-output" v-html="svgContent"></div>
            <div v-else class="status-msg">
              <span>⏳ Waiting for diagram...</span>
            </div>
          </div>

          <!-- Footer -->
          <div class="modal-footer">
            <button @click="emit('close')" class="close-button">Close</button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.modal-root {
  position: fixed;
  inset: 0;
  z-index: 9999;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
}

.modal-backdrop {
  position: absolute;
  inset: 0;
  background: rgba(0, 0, 0, 0.85);
  backdrop-filter: blur(8px);
}

.modal-container {
  position: relative;
  width: 100%;
  max-width: 1000px;
  max-height: 90vh;
  background: #111827;
  border: 1px solid rgba(255, 255, 255, 0.1);
  border-radius: 16px;
  box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.5);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.modal-header {
  padding: 20px 24px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.05);
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.modal-title {
  font-size: 1.25rem;
  font-weight: 700;
  color: white;
  margin: 0;
}

.close-btn {
  background: transparent;
  border: none;
  color: #9ca3af;
  font-size: 2rem;
  line-height: 1;
  cursor: pointer;
  transition: color 0.2s;
}

.close-btn:hover {
  color: white;
}

.modal-body {
  flex: 1;
  padding: 32px;
  overflow: auto;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(0, 0, 0, 0.2);
}

.mermaid-output {
  width: 100%;
  display: flex;
  justify-content: center;
}

:deep(.mermaid-output svg) {
  max-width: 100%;
  height: auto;
}

.status-msg {
  color: #9ca3af;
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 1rem;
}

.error-container {
  width: 100%;
  max-width: 600px;
}

.error-msg {
  color: #f87171;
  background: rgba(239, 68, 68, 0.1);
  padding: 16px;
  border-radius: 8px;
  border: 1px solid rgba(239, 68, 68, 0.2);
  margin-bottom: 16px;
}

.raw-definition {
  background: rgba(0, 0, 0, 0.3);
  border-radius: 8px;
  padding: 16px;
  border: 1px solid rgba(255, 255, 255, 0.1);
}

.raw-label {
  color: #9ca3af;
  font-size: 0.75rem;
  margin-bottom: 8px;
  text-transform: uppercase;
}

.raw-definition pre {
  color: #d1d5db;
  font-size: 0.75rem;
  white-space: pre-wrap;
  word-break: break-all;
  margin: 0;
  font-family: ui-monospace, monospace;
}

.modal-footer {
  padding: 16px;
  text-align: center;
  background: rgba(0, 0, 0, 0.3);
  border-top: 1px solid rgba(255, 255, 255, 0.05);
}

.close-button {
  background: linear-gradient(135deg, #4b5563 0%, #374151 100%);
  color: white;
  border: none;
  padding: 10px 24px;
  border-radius: 8px;
  font-size: 0.875rem;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.2s;
}

.close-button:hover {
  background: linear-gradient(135deg, #6b7280 0%, #4b5563 100%);
}

.spinner {
  width: 20px;
  height: 20px;
  border: 2px solid rgba(255, 255, 255, 0.1);
  border-top-color: #6366f1;
  border-radius: 50%;
  animation: spin 1s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

.fade-enter-active, .fade-leave-active {
  transition: opacity 0.3s ease;
}

.fade-enter-from, .fade-leave-to {
  opacity: 0;
}
</style>
