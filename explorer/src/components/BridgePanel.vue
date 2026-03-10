<script setup lang="ts">
import { ref } from 'vue'
import { wallet } from '../walletStore'
import { fetchDepositAddress, type DepositAddressResponse } from '../api'

const props = defineProps<{ node: string }>()

type Mode = 'recharge' | 'withdraw'
const mode = ref<Mode>('recharge')

// 跨入表单
const rChain = ref('BTC')
const rReceiverAddress = ref('')
const rNativeTxHash = ref('')
const rNativeVout = ref(0)
const rAmount = ref('')
const rFee = ref('0')
const rLoading = ref(false)
const rFetchingAddr = ref(false)
const rResult = ref('')
const depositInfo = ref<DepositAddressResponse | null>(null)

async function fetchAddr() {
  const receiver = rReceiverAddress.value || wallet.address
  if (!receiver) return alert('请先连接钱包或填写接收地址')
  rFetchingAddr.value = true
  depositInfo.value = null
  try {
    const res = await fetchDepositAddress(props.node, rChain.value, receiver)
    if (res.error) throw new Error(res.error)
    depositInfo.value = res
    if (!rReceiverAddress.value) rReceiverAddress.value = receiver
  } catch (e: any) { alert('获取充值地址失败: ' + (e.message || e)) }
  finally { rFetchingAddr.value = false }
}

async function copy(text: string) {
  await navigator.clipboard.writeText(text)
}

async function submitRecharge() {
  if (!wallet.connected) return alert('请先连接钱包')
  if (!depositInfo.value) return alert('请先点击"获取充值地址"')
  if (!rNativeTxHash.value) return alert('请输入外链交易哈希')
  rLoading.value = true
  rResult.value = ''
  try {
    const res = await window.frostbit.sendTransaction({
      type: 'recharge_request',
      nativeTxHash: rNativeTxHash.value,
      nativeScript: depositInfo.value.script_pubkey,
      nativeVout: rNativeVout.value,
      receiverAddress: rReceiverAddress.value || wallet.address,
      chain: rChain.value,
      tokenAddress: rChain.value,
      amount: rAmount.value,
      rechargeFee: rFee.value,
    })
    rResult.value = `跨入请求已广播${res?.txId ? '，TxID: ' + res.txId : ''}`
    rNativeTxHash.value = ''
  } catch (e: any) { rResult.value = '错误: ' + (e.message || e) }
  finally { rLoading.value = false }
}

// 跨出表单
const wChain = ref('BTC')
const wAsset = ref('native')
const wToAddress = ref('')
const wAmount = ref('')
const wLoading = ref(false)
const wResult = ref('')

const chains = ['BTC', 'ETH', 'SOL']

async function submitWithdraw() {
  if (!wallet.connected) return alert('请先连接钱包')
  if (!wToAddress.value || !wAmount.value) return alert('请填写目标地址和金额')
  wLoading.value = true
  wResult.value = ''
  try {
    const res = await window.frostbit.sendTransaction({
      type: 'frost_withdraw',
      chain: wChain.value,
      asset: wAsset.value,
      toAddress: wToAddress.value,
      amount: wAmount.value,
    })
    wResult.value = `跨出请求已广播${res?.txId ? '，TxID: ' + res.txId : ''}`
    wToAddress.value = ''
    wAmount.value = ''
  } catch (e: any) { wResult.value = '错误: ' + (e.message || e) }
  finally { wLoading.value = false }
}
</script>

<template>
  <div class="bridge-panel">
    <header class="bp-header">
      <div class="bp-title-group">
        <div class="icon-orb indigo">
          <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 3v7a6 6 0 0 0 6 6 6 6 0 0 0 6-6V3"/><line x1="4" y1="21" x2="20" y2="21"/></svg>
        </div>
        <div>
          <h3 class="bp-title">Cross-Chain Bridge</h3>
          <p class="bp-sub">资产跨链入金 / 出金</p>
        </div>
      </div>
    </header>

    <!-- 模式切换 -->
    <div class="mode-tabs">
      <button :class="['mode-btn', { active: mode === 'recharge' }]" @click="mode = 'recharge'">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 5v14"/><path d="m19 12-7 7-7-7"/></svg>
        跨入（Deposit）
      </button>
      <button :class="['mode-btn', { active: mode === 'withdraw' }]" @click="mode = 'withdraw'">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 19V5"/><path d="m5 12 7-7 7 7"/></svg>
        跨出（Withdraw）
      </button>
    </div>

    <!-- 钱包未连接提示 -->
    <div v-if="!wallet.connected" class="notice amber">
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>
      请先连接钱包以使用 Bridge 功能
    </div>

    <!-- 跨入表单 -->
    <div v-if="mode === 'recharge'" class="form-card">
      <div class="form-title">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#10b981" stroke-width="2"><path d="M12 5v14"/><path d="m19 12-7 7-7-7"/></svg>
        资产跨入 — 将外链资产充值到本链
      </div>

      <!-- Step 1: 选链 + 接收地址 + 获取充值地址 -->
      <div class="field-row">
        <div class="field-group" style="flex:1">
          <label>链</label>
          <div class="select-wrap">
            <select v-model="rChain" @change="depositInfo = null">
              <option v-for="c in chains" :key="c" :value="c">{{ c }}</option>
            </select>
          </div>
        </div>
        <div class="field-group" style="flex:3">
          <label>接收地址（本链）<span class="hint">留空则使用当前钱包地址</span></label>
          <input v-model="rReceiverAddress" class="field-input mono" :placeholder="wallet.address || '本链地址'" @input="depositInfo = null" />
        </div>
      </div>

      <button class="submit-btn fetch-btn" @click="fetchAddr" :disabled="rFetchingAddr">
        <svg v-if="rFetchingAddr" class="spin" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/></svg>
        <svg v-else width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="11" cy="11" r="8"/><path d="m21 21-4.35-4.35"/></svg>
        {{ rFetchingAddr ? '查询中…' : '获取充值地址' }}
      </button>

      <!-- Vault 信息卡片 -->
      <div v-if="depositInfo" class="vault-card">
        <div class="vault-header">
          <span class="vault-badge">Vault #{{ depositInfo.vault_id }}</span>
          <span class="vault-label">已为你分配充值地址</span>
        </div>
        <div class="vault-row">
          <span class="vault-key">Vault 公钥</span>
          <span class="vault-val mono">{{ depositInfo.vault_pubkey.slice(0, 20) }}…</span>
          <button class="copy-btn" @click="copy(depositInfo!.vault_pubkey)">复制完整</button>
        </div>
        <div class="vault-row">
          <span class="vault-key">充值地址</span>
          <span class="vault-val mono addr">{{ depositInfo.deposit_address }}</span>
          <button class="copy-btn green" @click="copy(depositInfo!.deposit_address)">复制</button>
        </div>
        <p class="vault-hint">⚠️ 将 {{ rChain }} 转到上方地址，完成后填写下方信息并提交</p>
      </div>

      <!-- Step 2: 填充交易信息（仅获取到充值地址后显示）-->
      <template v-if="depositInfo">
        <div class="field-row" style="margin-top:8px">
          <div class="field-group" style="flex:3">
            <label>外链交易哈希 (TxHash)</label>
            <input v-model="rNativeTxHash" class="field-input mono" placeholder="0x... 或 BTC txid" />
          </div>
          <div class="field-group" style="flex:1">
            <label>Vout <span class="hint">输出索引</span></label>
            <input v-model.number="rNativeVout" class="field-input" type="number" min="0" placeholder="0" />
          </div>
        </div>

        <div class="field-row">
          <div class="field-group half">
            <label>金额</label>
            <input v-model="rAmount" class="field-input" placeholder="0.001" type="number" min="0" step="any" />
          </div>
          <div class="field-group half">
            <label>见证费</label>
            <input v-model="rFee" class="field-input" placeholder="0" type="number" min="0" step="any" />
          </div>
        </div>

        <button class="submit-btn green" @click="submitRecharge" :disabled="rLoading || !wallet.connected">
          <svg v-if="rLoading" class="spin" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/></svg>
          {{ rLoading ? '提交中…' : '发起跨入请求' }}
        </button>
        <div v-if="rResult" class="tx-result" :class="{ error: rResult.startsWith('错误') }">{{ rResult }}</div>
      </template>
    </div>


    <!-- 跨出表单 -->
    <div v-if="mode === 'withdraw'" class="form-card">
      <div class="form-title">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#f59e0b" stroke-width="2"><path d="M12 19V5"/><path d="m5 12 7-7 7 7"/></svg>
        资产跨出 — 将本链资产提现到外链
      </div>

      <div class="field-row">
        <div class="field-group half">
          <label>目标链</label>
          <div class="select-wrap">
            <select v-model="wChain">
              <option v-for="c in chains" :key="c" :value="c">{{ c }}</option>
            </select>
          </div>
        </div>
        <div class="field-group half">
          <label>资产</label>
          <input v-model="wAsset" class="field-input" placeholder="native / token地址" />
        </div>
      </div>

      <div class="field-group">
        <label>目标地址（外链）</label>
        <input v-model="wToAddress" class="field-input mono" placeholder="bc1q... 或 0x..." />
      </div>

      <div class="field-group">
        <label>金额</label>
        <input v-model="wAmount" class="field-input" placeholder="0.001" type="number" min="0" step="any" />
      </div>

      <button class="submit-btn amber" @click="submitWithdraw" :disabled="wLoading || !wallet.connected">
        <svg v-if="wLoading" class="spin" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/></svg>
        {{ wLoading ? '提交中…' : '发起跨出请求' }}
      </button>
      <div v-if="wResult" class="tx-result" :class="{ error: wResult.startsWith('错误') }">{{ wResult }}</div>
    </div>
  </div>
</template>

<style scoped>
@import url('https://fonts.googleapis.com/css2?family=Outfit:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap');
.bridge-panel { font-family: 'Outfit', sans-serif; color: #e2e8f0; max-width: 680px; margin: 0 auto; }
.bp-header { display: flex; align-items: center; margin-bottom: 28px; }
.bp-title-group { display: flex; align-items: center; gap: 16px; }
.icon-orb { width: 48px; height: 48px; border-radius: 14px; display: flex; align-items: center; justify-content: center; }
.icon-orb.indigo { background: rgba(99,102,241,0.12); border: 1px solid rgba(99,102,241,0.3); color: #818cf8; }
.bp-title { font-size: 1.25rem; font-weight: 700; margin: 0; color: #fff; }
.bp-sub { font-size: 0.8rem; color: #64748b; margin: 2px 0 0; }

.mode-tabs { display: flex; gap: 0; margin-bottom: 24px; background: rgba(0,0,0,0.3); border-radius: 12px; padding: 4px; }
.mode-btn { flex: 1; border: none; background: transparent; color: #64748b; padding: 10px 16px; border-radius: 9px; font-size: 0.9rem; font-weight: 600; cursor: pointer; transition: all 0.2s; display: flex; align-items: center; justify-content: center; gap: 8px; }
.mode-btn.active { background: rgba(99,102,241,0.2); color: #818cf8; box-shadow: 0 0 0 1px rgba(99,102,241,0.3); }
.mode-btn:hover:not(.active) { color: #94a3b8; }

.notice { display: flex; align-items: center; gap: 10px; padding: 14px 18px; border-radius: 12px; font-size: 0.85rem; margin-bottom: 16px; }
.notice.amber { background: rgba(245,158,11,0.08); border: 1px solid rgba(245,158,11,0.2); color: #fbbf24; }

.form-card { background: rgba(15,23,42,0.5); border: 1px solid rgba(255,255,255,0.06); border-radius: 20px; padding: 28px; }
.form-title { display: flex; align-items: center; gap: 10px; font-size: 0.9rem; font-weight: 700; color: #94a3b8; margin-bottom: 24px; }

.field-group { display: flex; flex-direction: column; gap: 7px; margin-bottom: 18px; }
.field-group label { font-size: 0.7rem; font-weight: 800; color: #475569; text-transform: uppercase; letter-spacing: 0.04em; }
.hint { font-size: 0.65rem; font-weight: 500; color: #334155; text-transform: none; margin-left: 8px; }
.field-row { display: flex; gap: 16px; }
.field-group.half { flex: 1; }
.field-input {
  background: rgba(0,0,0,0.3); border: 1px solid rgba(255,255,255,0.08); border-radius: 10px;
  color: #e2e8f0; padding: 10px 14px; font-size: 0.9rem; outline: none; transition: border-color 0.2s;
  width: 100%; box-sizing: border-box;
}
.field-input:focus { border-color: rgba(99,102,241,0.5); }
.field-input.mono { font-family: 'JetBrains Mono', monospace; font-size: 0.82rem; }
.select-wrap { position: relative; }
.select-wrap select { appearance: none; width: 100%; background: rgba(0,0,0,0.3); border: 1px solid rgba(255,255,255,0.08); border-radius: 10px; color: #e2e8f0; padding: 10px 14px; font-size: 0.9rem; outline: none; cursor: pointer; }

.submit-btn { width: 100%; padding: 13px; border: none; border-radius: 12px; font-size: 0.95rem; font-weight: 700; cursor: pointer; transition: all 0.2s; display: flex; align-items: center; justify-content: center; gap: 10px; margin-top: 8px; }
.submit-btn:disabled { opacity: 0.4; cursor: not-allowed; }
.submit-btn.green { background: linear-gradient(135deg,#10b981,#34d399); color: #fff; box-shadow: 0 4px 20px rgba(16,185,129,0.3); }
.submit-btn.green:hover:not(:disabled) { opacity: 0.85; transform: translateY(-1px); }
.submit-btn.amber { background: linear-gradient(135deg,#f59e0b,#fbbf24); color: #111; box-shadow: 0 4px 20px rgba(245,158,11,0.3); }
.submit-btn.amber:hover:not(:disabled) { opacity: 0.85; transform: translateY(-1px); }

.tx-result { margin-top: 12px; font-size: 0.8rem; color: #34d399; padding: 10px 14px; background: rgba(16,185,129,0.08); border-radius: 10px; }
.tx-result.error { color: #f87171; background: rgba(239,68,68,0.08); }

/* Vault 卡片 */
.fetch-btn { background: rgba(99,102,241,0.12); color: #818cf8; border: 1px solid rgba(99,102,241,0.3); margin-bottom: 16px; }
.fetch-btn:hover:not(:disabled) { background: rgba(99,102,241,0.2); }
.vault-card { background: rgba(16,185,129,0.06); border: 1px solid rgba(16,185,129,0.2); border-radius: 14px; padding: 18px; margin-bottom: 20px; }
.vault-header { display: flex; align-items: center; gap: 10px; margin-bottom: 14px; }
.vault-badge { background: rgba(99,102,241,0.2); color: #818cf8; font-size: 0.72rem; font-weight: 700; padding: 3px 10px; border-radius: 20px; }
.vault-label { font-size: 0.8rem; color: #34d399; font-weight: 600; }
.vault-row { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; flex-wrap: wrap; }
.vault-key { font-size: 0.68rem; font-weight: 800; color: #475569; text-transform: uppercase; letter-spacing: 0.04em; min-width: 60px; }
.vault-val { font-size: 0.8rem; color: #94a3b8; flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.vault-val.addr { color: #e2e8f0; }
.copy-btn { font-size: 0.7rem; font-weight: 700; padding: 4px 10px; border-radius: 6px; border: 1px solid rgba(99,102,241,0.3); background: rgba(99,102,241,0.1); color: #818cf8; cursor: pointer; white-space: nowrap; transition: all 0.15s; }
.copy-btn:hover { background: rgba(99,102,241,0.2); }
.copy-btn.green { border-color: rgba(16,185,129,0.4); background: rgba(16,185,129,0.1); color: #34d399; }
.copy-btn.green:hover { background: rgba(16,185,129,0.2); }
.vault-hint { font-size: 0.75rem; color: #64748b; margin: 10px 0 0; }

@keyframes spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }
.spin { animation: spin 1s linear infinite; }
</style>
