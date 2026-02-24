<script setup lang="ts">
import { ref } from 'vue'
import { wallet, connectWallet, disconnectWallet, shortAddr } from '../walletStore'

const connecting = ref(false)

async function handleConnect() {
  connecting.value = true
  try { await connectWallet() } catch (e: any) { alert(e.message) } finally { connecting.value = false }
}
</script>

<template>
  <div class="wallet-connector">
    <template v-if="wallet.connected">
      <div class="wallet-badge">
        <div class="w-dot"></div>
        <span class="w-addr">{{ shortAddr(wallet.address) }}</span>
        <button class="w-btn disconnect" @click="disconnectWallet">断开</button>
      </div>
    </template>
    <template v-else>
      <button class="w-btn connect" @click="handleConnect" :disabled="connecting">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <rect width="20" height="14" x="2" y="5" rx="2"/>
          <path d="M2 10h20"/>
        </svg>
        {{ connecting ? '连接中…' : '连接钱包' }}
      </button>
    </template>
  </div>
</template>

<style scoped>
.wallet-connector { display: flex; align-items: center; }
.wallet-badge {
  display: flex; align-items: center; gap: 8px;
  background: rgba(16,185,129,0.08); border: 1px solid rgba(16,185,129,0.25);
  padding: 6px 14px; border-radius: 99px;
}
.w-dot { width: 7px; height: 7px; background: #10b981; border-radius: 50%; box-shadow: 0 0 8px #10b981; }
.w-addr { font-family: 'JetBrains Mono', monospace; font-size: 0.8rem; color: #34d399; }
.w-btn {
  border: none; cursor: pointer; border-radius: 99px; font-weight: 700;
  font-size: 0.82rem; display: flex; align-items: center; gap: 8px; transition: all 0.2s;
}
.w-btn.connect {
  background: linear-gradient(135deg,#6366f1,#3b82f6); color:#fff; padding: 8px 18px;
  box-shadow: 0 4px 16px rgba(99,102,241,0.35);
}
.w-btn.connect:hover:not(:disabled) { opacity: 0.85; transform: translateY(-1px); }
.w-btn.connect:disabled { opacity: 0.5; cursor: not-allowed; }
.w-btn.disconnect { background: rgba(239,68,68,0.12); color:#f87171; padding: 4px 10px; border: 1px solid rgba(239,68,68,0.2); }
.w-btn.disconnect:hover { background: rgba(239,68,68,0.2); }
</style>
