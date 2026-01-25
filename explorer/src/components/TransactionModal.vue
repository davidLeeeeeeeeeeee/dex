<script setup lang="ts">
import { ref, watch } from 'vue'
import type { TxInfo } from '../types'
import { fetchTx } from '../api'
import TxDetail from './TxDetail.vue'

const props = defineProps<{
  show: boolean
  node: string
  txId: string
}>()

const emit = defineEmits(['close', 'address-click'])

const tx = ref<TxInfo | null>(null)
const loading = ref(false)
const error = ref('')

const loadTx = async () => {
  if (!props.node || !props.txId) return
  loading.value = true
  error.value = ''
  tx.value = null
  try {
    const resp = await fetchTx({ node: props.node, tx_id: props.txId })
    if (resp.error) {
      error.value = resp.error
    } else if (resp.transaction) {
      tx.value = resp.transaction
    } else {
      error.value = 'Transaction not found'
    }
  } catch (e: any) {
    error.value = e.message || 'Failed to fetch transaction'
  } finally {
    loading.value = false
  }
}

watch(() => [props.show, props.txId, props.node], ([show]) => {
  if (show && props.txId) {
    loadTx()
  }
})
</script>

<template>
  <Teleport to="body">
    <Transition name="fade">
      <div v-if="show" class="modal-root" @click.self="emit('close')">
        <div class="modal-backdrop"></div>
        
        <div class="modal-container" @click.stop>
          <div class="modal-header">
            <div class="header-left">
              <div class="header-icon">
                <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M7 11V7a5 5 0 0 1 10 0v4"/><rect width="18" height="11" x="3" y="11" rx="2" ry="2"/></svg>
              </div>
              <h3 class="modal-title">Quick Inspection</h3>
            </div>
            <button @click="emit('close')" class="close-btn">&times;</button>
          </div>

          <div class="modal-body custom-scrollbar">
            <div v-if="loading" class="state-msg">
              <span class="spinner"></span>
              <span>Retrieving transaction data...</span>
            </div>
            
            <div v-else-if="error" class="error-msg">
              <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" x2="12" y1="8" y2="12"/><line x1="12" x2="12.01" y1="16" y2="16"/></svg>
              <span>{{ error }}</span>
            </div>

            <div v-else-if="tx" class="tx-content">
              <TxDetail :tx="tx" @address-click="addr => emit('address-click', addr)" @back="emit('close')" />
            </div>
            
            <div v-else class="state-msg">
              <span>Ready for query</span>
            </div>
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
  z-index: 10000;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 40px;
}

.modal-backdrop {
  position: absolute;
  inset: 0;
  background: rgba(2, 6, 23, 0.85);
  backdrop-filter: blur(12px);
}

.modal-container {
  position: relative;
  width: 100%;
  max-width: 900px;
  max-height: 85vh;
  background: #0f172a;
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 24px;
  box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.7);
  display: flex;
  flex-direction: column;
  overflow: hidden;
  animation: modalSlide 0.4s cubic-bezier(0.4, 0, 0.2, 1);
}

@keyframes modalSlide {
  from { opacity: 0; transform: translateY(20px) scale(0.98); }
  to { opacity: 1; transform: translateY(0) scale(1); }
}

.modal-header {
  padding: 24px 32px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.05);
  display: flex;
  justify-content: space-between;
  align-items: center;
  background: rgba(255, 255, 255, 0.01);
}

.header-left {
  display: flex;
  align-items: center;
  gap: 16px;
}

.header-icon {
  width: 40px;
  height: 40px;
  background: rgba(99, 102, 241, 0.1);
  color: #6366f1;
  border-radius: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
}

.modal-title {
  font-size: 1.1rem;
  font-weight: 800;
  color: #fff;
  margin: 0;
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

.close-btn {
  background: transparent;
  border: none;
  color: #475569;
  font-size: 2rem;
  line-height: 1;
  cursor: pointer;
  transition: all 0.2s;
}

.close-btn:hover {
  color: #fff;
  transform: rotate(90deg);
}

.modal-body {
  flex: 1;
  padding: 32px;
  overflow-y: auto;
  position: relative;
}

.state-msg {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 80px 0;
  gap: 20px;
  color: #475569;
  font-weight: 500;
}

.spinner {
  width: 32px;
  height: 32px;
  border: 3px solid rgba(99, 102, 241, 0.1);
  border-top-color: #6366f1;
  border-radius: 50%;
  animation: spin 1s linear infinite;
}

.error-msg {
  display: flex;
  align-items: center;
  gap: 12px;
  color: #ef4444;
  background: rgba(239, 68, 68, 0.05);
  padding: 20px;
  border-radius: 16px;
  border: 1px solid rgba(239, 68, 68, 0.1);
}

@keyframes spin { to { transform: rotate(360deg); } }

.fade-enter-active, .fade-leave-active { transition: opacity 0.3s ease; }
.fade-enter-from, .fade-leave-to { opacity: 0; }

:deep(.tx-view .action-header) { display: none; } /* Hide the back button inside modal */
:deep(.tx-view .panel) { background: rgba(0, 0, 0, 0.15); }

/* Custom scrollbar */
.custom-scrollbar::-webkit-scrollbar { width: 6px; }
.custom-scrollbar::-webkit-scrollbar-track { background: transparent; }
.custom-scrollbar::-webkit-scrollbar-thumb { background: rgba(255, 255, 255, 0.1); border-radius: 10px; }
.custom-scrollbar::-webkit-scrollbar-thumb:hover { background: rgba(255, 255, 255, 0.2); }
</style>
