<script setup lang="ts">
import { ref, onMounted } from 'vue'

const fileInputRef = ref<HTMLInputElement | null>(null)

// ─── Chrome 消息通信 ──────────────────────────────────────────────────────────
function bg(msg: any): Promise<any> {
  return new Promise((resolve, reject) => {
    chrome.runtime.sendMessage(msg, (res: any) => {
      if (chrome.runtime.lastError) return reject(new Error(chrome.runtime.lastError.message))
      if (res?.error) return reject(new Error(res.error))
      resolve(res)
    })
  })
}

// ─── 页面路由 ─────────────────────────────────────────────────────────────────
type Page = 'lock' | 'setup' | 'main' | 'send' | 'history' | 'settings' | 'approve'
const page = ref<Page>('lock')
const setupTab = ref<'create' | 'import'>('create')

// ─── 全局状态 ─────────────────────────────────────────────────────────────────
const address = ref('')
const accounts = ref<string[]>([])
const balances = ref<Record<string, any>>({})
const loadingBalance = ref(false)
const generatedMnemonic = ref('')
const showAccountList = ref(false)

// ─── 表单字段 ─────────────────────────────────────────────────────────────────
const unlockPassword = ref('')
const lockError = ref('')

const createPassword = ref('')
const createPassword2 = ref('')
const setupError = ref('')

const importMnemonic = ref('')
const importPassword = ref('')
const importFileStatus = ref('')
const importFileError = ref('')
const importKeysText = ref('')

const sendToken = ref('FB')
const sendTo = ref('')
const sendAmount = ref('')
const sendStatus = ref('')
const sendError = ref('')
const sendTxId = ref('')
const sendLoading = ref(false)
const txHistory = ref<any[]>([])
const txHistoryLoading = ref(false)
const txHistoryError = ref('')

const explorerUrl = ref('http://127.0.0.1:8080')
const nodeAddr = ref('')

const pendingRequests = ref<any[]>([])

async function checkPendingRequests() {
  const res = await bg({ type: 'GET_PENDING_REQUESTS' })
  pendingRequests.value = res.requests || []
  if (pendingRequests.value.length > 0) {
    page.value = 'approve'
  }
}


// ─── 初始化 ──────────────────────────────────────────────────────────────────
onMounted(async () => {
  try {
    const { has } = await bg({ type: 'HAS_WALLET' })
    if (!has) { page.value = 'setup'; return }
    const { unlocked } = await bg({ type: 'IS_UNLOCKED' })
    if (unlocked) { 
      await loadMain()
      await checkPendingRequests()
      if (page.value !== 'approve') page.value = 'main'
    }
    else page.value = 'lock'
  } catch { page.value = 'lock' }
})

// ─── 解锁 ────────────────────────────────────────────────────────────────────
async function unlock() {
  lockError.value = ''
  try {
    const res = await bg({ type: 'UNLOCK', password: unlockPassword.value })
    address.value = res.address
    unlockPassword.value = ''
    await loadMain()
    await checkPendingRequests()
    if (page.value !== 'approve') page.value = 'main'
  } catch (e: any) { lockError.value = e.message }
}

// ─── 创建钱包 ─────────────────────────────────────────────────────────────────
async function generateMnemonic() {
  const res = await bg({ type: 'GENERATE_MNEMONIC' })
  generatedMnemonic.value = res.mnemonic
}

async function createWallet() {
  setupError.value = ''
  if (!generatedMnemonic.value) return (setupError.value = '请先生成助记词')
  if (createPassword.value.length < 8) return (setupError.value = '密码至少8位')
  if (createPassword.value !== createPassword2.value) return (setupError.value = '两次密码不一致')
  try {
    const res = await bg({ type: 'CREATE_WALLET', mnemonic: generatedMnemonic.value, password: createPassword.value })
    address.value = res.address
    await loadMain()
    await checkPendingRequests()
    if (page.value !== 'approve') page.value = 'main'
  } catch (e: any) { setupError.value = e.message }
}

async function importWallet() {
  setupError.value = ''
  if (!importMnemonic.value.trim()) return (setupError.value = '请输入助记词')
  if (importPassword.value.length < 8) return (setupError.value = '密码至少8位')
  try {
    const res = await bg({ type: 'IMPORT_WALLET', mnemonic: importMnemonic.value.trim(), password: importPassword.value })
    address.value = res.address
    await loadMain()
    await checkPendingRequests()
    if (page.value !== 'approve') page.value = 'main'
  } catch (e: any) { setupError.value = e.message }
}

// ─── 主页逻辑 ─────────────────────────────────────────────────────────────────
async function loadMain() {
  loadingBalance.value = true
  try {
    const res = await bg({ type: 'GET_ACCOUNTS' })
    accounts.value = res.accounts ?? []
    // 保留当前选中的地址；只有地址不在列表中时才切到第 0 个
    if (!address.value || !accounts.value.includes(address.value))
      address.value = accounts.value[0] ?? ''
    if (!address.value) return
    const bal = await bg({ type: 'GET_BALANCES', address: address.value })
    balances.value = bal.balances ?? {}
    normalizeSendToken()
    if (page.value === 'history') {
      await loadLocalTxHistory()
    }
  } catch (e: any) {
    if (e.message.includes('locked')) { page.value = 'lock'; return }
  } finally { loadingBalance.value = false }
}

async function selectAccount(index: number) {
  await bg({ type: 'SELECT_ACCOUNT', index })
  address.value = accounts.value[index]
  showAccountList.value = false
  loadingBalance.value = true
  try {
    const bal = await bg({ type: 'GET_BALANCES', address: address.value })
    balances.value = bal.balances ?? {}
    normalizeSendToken()
    if (page.value === 'history') {
      await loadLocalTxHistory()
    }
  } finally { loadingBalance.value = false }
}

// ─── 从文本批量导入账户 ──────────────────────────────────────────────────────
async function importAccountsFromText() {
  importFileStatus.value = ''
  importFileError.value = ''
  const hexKeys = importKeysText.value.split('\n')
    .map(l => l.trim())
    .filter(l => l && !l.startsWith('#') && /^[0-9a-fA-F]{64}$/.test(l))
  if (!hexKeys.length) { importFileError.value = '未找到有效私钥，请粘贴 miner_keys.txt 内容'; return }
  if (!importPassword.value) { importFileError.value = '请先输入钱包密码'; return }
  try {
    const res = await bg({ type: 'IMPORT_ACCOUNTS_FILE', hexKeys, password: importPassword.value })
    importKeysText.value = ''
    importFileStatus.value = `✅ 成功导入 ${res.added.length} 个账户`
    await loadMain()
    page.value = 'main'
  } catch (e: any) { importFileError.value = `导入失败：${e.message}` }
}

async function lock() {
  await bg({ type: 'LOCK' })
  address.value = ''; balances.value = {}
  page.value = 'lock'
}

// ─── 发送 ────────────────────────────────────────────────────────────────────
async function confirmSend() {
  sendError.value = ''
  sendStatus.value = ''
  sendTxId.value = ''
  if (!sendTo.value.trim()) return (sendError.value = '请输入接收地址')
  const amt = parseFloat(sendAmount.value)
  if (!amt || amt <= 0) return (sendError.value = '请输入有效金额')
  if (!sendToken.value) return (sendError.value = '请选择代币')
  sendLoading.value = true
  try {
    const res = await bg({
      type: 'SEND_TX',
      txDesc: {
        type: 'transaction',
        to: sendTo.value.trim(),
        tokenAddress: sendToken.value,
        amount: sendAmount.value,
      },
    })
    sendTxId.value = res.txId || ''
    sendStatus.value = '✅ 交易已提交'
    sendTo.value = ''
    sendAmount.value = ''
    await loadLocalTxHistory()
  } catch (e: any) {
    sendError.value = `发送失败：${e.message}`
  } finally {
    sendLoading.value = false
  }
}

// ─── 审批请求管理 ─────────────────────────────────────────────────────────────
async function approveRequest(id: number) {
  try {
    await bg({ type: 'APPROVE_REQUEST', id })
    await checkPendingRequests()
    if (pendingRequests.value.length === 0) window.close()
  } catch (e: any) { alert('Approve Failed: ' + e.message) }
}

async function rejectRequest(id: number) {
  try {
    await bg({ type: 'REJECT_REQUEST', id })
    await checkPendingRequests()
    if (pendingRequests.value.length === 0) window.close()
  } catch (e: any) { alert('Reject Failed: ' + e.message) }
}


// ─── 设置 ────────────────────────────────────────────────────────────────────
async function openSettings() {
  const res = await bg({ type: 'GET_SETTINGS' })
  explorerUrl.value = res.explorerUrl || 'http://127.0.0.1:8080'
  nodeAddr.value = res.nodeAddr || ''
  page.value = 'settings'
}

async function saveSettings() {
  await bg({ type: 'SET_SETTINGS', explorerUrl: explorerUrl.value, nodeAddr: nodeAddr.value })
  page.value = 'main'
}

async function resetWallet() {
  if (!confirm('确定重置？助记词丢失将无法恢复！')) return
  await bg({ type: 'RESET_WALLET' })
  address.value = ''; balances.value = {}
  page.value = 'setup'
}

// ─── 工具函数 ─────────────────────────────────────────────────────────────────
function availableTokens() {
  const keys = Object.keys(balances.value || {})
  if (!keys.length) return ['FB']
  return keys.sort((a, b) => {
    if (a === 'FB') return -1
    if (b === 'FB') return 1
    return a.localeCompare(b)
  })
}

function normalizeSendToken() {
  const tokens = availableTokens()
  if (!tokens.length) {
    sendToken.value = 'FB'
    return
  }
  if (!tokens.includes(sendToken.value)) {
    sendToken.value = tokens[0]
  }
}

async function loadLocalTxHistory() {
  if (!address.value) {
    txHistory.value = []
    return
  }
  txHistoryLoading.value = true
  txHistoryError.value = ''
  try {
    const res = await bg({ type: 'GET_LOCAL_TX_HISTORY', address: address.value, limit: 200 })
    txHistory.value = res.items || []
  } catch (e: any) {
    txHistoryError.value = e.message || '加载历史失败'
  } finally {
    txHistoryLoading.value = false
  }
}

function openSendPage() {
  normalizeSendToken()
  page.value = 'send'
}

async function openHistoryPage() {
  page.value = 'history'
  await loadLocalTxHistory()
}

function formatHistoryTime(ts: number) {
  if (!ts) return '-'
  return new Date(ts).toLocaleString()
}

function copyText(text: string) {
  if (!text) return
  navigator.clipboard.writeText(text)
}

function shortAddr(addr: string) {
  return addr.length > 14 ? `${addr.slice(0, 8)}…${addr.slice(-6)}` : addr
}

function copyAddr() {
  if (address.value) navigator.clipboard.writeText(address.value)
}

function copyTxId() {
  if (sendTxId.value) {
    navigator.clipboard.writeText(sendTxId.value)
    const old = sendStatus.value
    sendStatus.value = '✅ 已复制完整 TxID'
    setTimeout(() => { if (sendStatus.value === '✅ 已复制完整 TxID') sendStatus.value = old }, 2000)
  }
}

function fbBalance() {
  return balances.value['FB']?.balance ?? '0'
}

function otherBalances() {
  return Object.entries(balances.value).filter(([k]) => k !== 'FB')
}
</script>

<template>
  <div class="wallet-shell">

    <!-- ── 锁定页 ──────────────────────────────────────────── -->
    <div v-if="page === 'lock'" class="page">
      <div class="logo-area">
        <div class="logo-snowflake">❄</div>
        <h1>FrostBit</h1>
        <p class="tagline">去中心化交易所钱包</p>
      </div>
      <div class="card">
        <label class="field-label">密码</label>
        <input class="field-input" type="password" v-model="unlockPassword"
          placeholder="输入钱包密码" @keyup.enter="unlock" autocomplete="current-password" />
        <p v-if="lockError" class="msg-error">{{ lockError }}</p>
        <button class="btn-primary mt-3" @click="unlock">解锁钱包</button>
        <div class="divider">或</div>
        <button class="btn-ghost" @click="page = 'setup'">创建 / 导入钱包</button>
      </div>
    </div>

    <!-- ── 创建/导入页 ──────────────────────────────────────── -->
    <div v-else-if="page === 'setup'" class="page">
      <nav class="top-nav">
        <button class="btn-icon" @click="page = 'lock'">←</button>
        <span class="nav-title">设置钱包</span>
        <span></span>
      </nav>
      <div class="tab-bar">
        <button :class="['tab', setupTab === 'create' && 'tab-active']" @click="setupTab = 'create'">新建</button>
        <button :class="['tab', setupTab === 'import' && 'tab-active']" @click="setupTab = 'import'">导入</button>
      </div>

      <!-- 新建 -->
      <div v-if="setupTab === 'create'" class="card">
        <div :class="['mnemonic-box', generatedMnemonic && 'mnemonic-filled']">
          {{ generatedMnemonic || '点击下方按钮生成助记词' }}
        </div>
        <button class="btn-secondary" @click="generateMnemonic">🎲 生成助记词</button>
        <label class="field-label">密码</label>
        <input class="field-input" type="password" v-model="createPassword" placeholder="至少8位" />
        <label class="field-label">确认密码</label>
        <input class="field-input" type="password" v-model="createPassword2" placeholder="再次输入" />
        <p v-if="setupError" class="msg-error">{{ setupError }}</p>
        <button class="btn-primary mt-2" :disabled="!generatedMnemonic" @click="createWallet">创建钱包</button>
        <p class="hint">⚠️ 请务必抄写助记词，丢失不可恢复</p>
      </div>

      <!-- 导入 -->
      <div v-else class="card">
        <label class="field-label">助记词（12个，空格分隔）</label>
        <textarea class="field-input" rows="3" v-model="importMnemonic" placeholder="word1 word2 word3 ..."></textarea>
        <label class="field-label">密码</label>
        <input class="field-input" type="password" v-model="importPassword" placeholder="至少8位" />
        <p v-if="setupError" class="msg-error">{{ setupError }}</p>
        <button class="btn-primary mt-2" @click="importWallet">导入钱包</button>
        <div class="divider">或</div>
        <label class="field-label">批量导入账户（粘贴 miner_keys.txt 内容）</label>
        <p class="hint">将 miner_keys.txt 内容全部复制后粘贴到下方，支持 # 注释行</p>
        <textarea class="field-input" rows="4" v-model="importKeysText" placeholder="# index: 0  address: bc1q...&#10;aabb...hex...&#10;# index: 1..." style="font-family: monospace; font-size: 11px;"></textarea>
        <button class="btn-secondary mt-2" @click="importAccountsFromText">📥 导入账户</button>
        <p v-if="importFileError" class="msg-error">{{ importFileError }}</p>
        <p v-if="importFileStatus" class="msg-success">{{ importFileStatus }}</p>
      </div>
    </div>

    <!-- ── 主页 ───────────────────────────────────────────────── -->
    <div v-else-if="page === 'main'" class="page">
      <nav class="top-nav">
        <div class="addr-dropdown">
          <div class="addr-row" @click="showAccountList = !showAccountList" title="切换账户">
            <code class="addr-code">{{ shortAddr(address) }}</code>
            <span class="copy-icon">{{ showAccountList ? '▲' : '▼' }}</span>
          </div>
          <div v-if="showAccountList" class="account-list">
            <div v-for="(addr, idx) in accounts" :key="addr"
              :class="['account-item', addr === address && 'account-item-active']"
              @click="selectAccount(idx)">
              <code>{{ shortAddr(addr) }}</code>
              <span v-if="addr === address" class="checkmark">✓</span>
            </div>
          </div>
        </div>
        <div class="nav-actions">
          <button class="btn-icon" @click="copyAddr" title="复制地址">⎘</button>
          <button class="btn-icon" @click="openSettings" title="设置">⚙</button>
          <button class="btn-icon" @click="lock" title="锁定">🔒</button>
        </div>
      </nav>

      <div class="balance-card">
        <div class="balance-main">
          <span class="balance-val">{{ loadingBalance ? '…' : fbBalance() }}</span>
          <span class="balance-unit">FB</span>
        </div>
        <div class="balance-list">
          <div v-for="[token, bal] in otherBalances()" :key="token" class="balance-row">
            <span class="balance-token">{{ token }}</span>
            <span>{{ bal.balance ?? '0' }}</span>
          </div>
        </div>
        <button class="btn-refresh" @click="loadMain" :disabled="loadingBalance">
          {{ loadingBalance ? '刷新中…' : '↻ 刷新余额' }}
        </button>
      </div>

      <div class="action-row">
        <button class="action-btn" @click="openSendPage">
          <span class="action-icon">↑</span>
          <span>发送</span>
        </button>
        <button class="action-btn" @click="openHistoryPage">
          <span class="action-icon">🕘</span>
          <span>发送历史</span>
        </button>
      </div>
    </div>

    <!-- ── 发送页 ──────────────────────────────────────────────── -->
    <div v-else-if="page === 'send'" class="page">
      <nav class="top-nav">
        <button class="btn-icon" @click="page = 'main'">←</button>
        <span class="nav-title">发送代币</span>
        <span></span>
      </nav>
      <div class="card">
        <label class="field-label">代币</label>
        <select class="field-input" v-model="sendToken">
          <option v-for="token in availableTokens()" :key="token" :value="token">{{ token }}</option>
        </select>
        <label class="field-label">接收地址</label>
        <input class="field-input" type="text" v-model="sendTo" placeholder="bc1q..." />
        <label class="field-label">金额</label>
        <input class="field-input" type="text" v-model="sendAmount" placeholder="0.00" />
        <p v-if="sendError" class="msg-error">{{ sendError }}</p>
        <p v-if="sendStatus" class="msg-success">{{ sendStatus }}</p>
        <div v-if="sendTxId" class="hint" style="display: flex; gap: 8px; align-items: center; margin-top: -6px;">
          <span>TxID: {{ shortAddr(sendTxId) }}</span>
          <button class="btn-ghost" style="padding: 2px 6px; font-size: 11px; margin: 0; min-width: auto;" @click="copyTxId">复制完整 TxID</button>
        </div>
        <button class="btn-primary mt-2" @click="confirmSend" :disabled="sendLoading">
          {{ sendLoading ? '发送中…' : '确认发送' }}
        </button>
      </div>
    </div>

    <!-- ── 设置页 ──────────────────────────────────────────────── -->
    <div v-else-if="page === 'history'" class="page">
      <nav class="top-nav">
        <button class="btn-icon" @click="page = 'main'">⬅</button>
        <span class="nav-title">本地发送历史</span>
        <button class="btn-icon" @click="loadLocalTxHistory" :disabled="txHistoryLoading" title="刷新">↻</button>
      </nav>
      <div class="card">
        <p class="hint">仅显示本钱包本地发送记录（当前账户）</p>
        <p class="hint mono-mini">{{ shortAddr(address) }}</p>
        <p v-if="txHistoryError" class="msg-error">{{ txHistoryError }}</p>
        <div v-if="txHistoryLoading" class="hint">加载中...</div>
        <div v-else-if="txHistory.length === 0" class="hint">暂无发送记录</div>
        <div v-else class="tx-history-list">
          <div v-for="item in txHistory" :key="item.localId" class="tx-history-item">
            <div class="tx-history-row">
              <span class="balance-token">{{ item.tokenAddress || '-' }}</span>
              <span>{{ item.amount || '-' }}</span>
            </div>
            <div class="tx-history-row mono-mini">
              <span>To: {{ shortAddr(item.toAddress || '-') }}</span>
              <span>{{ formatHistoryTime(item.createdAt) }}</span>
            </div>
            <div class="tx-history-row mono-mini">
              <span>TxID: {{ item.txId ? shortAddr(item.txId) : '-' }}</span>
              <button v-if="item.txId" class="btn-ghost tx-copy-btn" @click="copyText(item.txId)">复制</button>
            </div>
          </div>
        </div>
      </div>
    </div>
    <div v-else-if="page === 'settings'" class="page">
      <nav class="top-nav">
        <button class="btn-icon" @click="page = 'main'">←</button>
        <span class="nav-title">设置</span>
        <span></span>
      </nav>
      <div class="card">
        <label class="field-label">Explorer 地址</label>
        <input class="field-input" type="text" v-model="explorerUrl" placeholder="http://127.0.0.1:8080" />
        <label class="field-label">节点地址（转发用）</label>
        <input class="field-input" type="text" v-model="nodeAddr" placeholder="127.0.0.1:6000" />
        <button class="btn-primary mt-2" @click="saveSettings">保存</button>
      </div>
      <div class="card danger-card">
        <p class="danger-hint">危险操作 — 不可撤销</p>
        <button class="btn-danger" @click="resetWallet">重置钱包</button>
      </div>
    </div>

    <!-- ── 审批页 ──────────────────────────────────────────────── -->
    <div v-else-if="page === 'approve'" class="page">
      <nav class="top-nav">
        <span class="nav-title">签名授权请求</span>
        <span></span>
      </nav>
      <div v-if="pendingRequests.length > 0" class="card">
        <p class="hint">来源: {{ pendingRequests[0].url }}</p>
        <div style="display: flex; flex-direction: column; gap: 8px;">
          <label class="field-label">操作类型</label>
          <div class="field-input">{{ pendingRequests[0].txDesc.type }}</div>
          
          <label class="field-label">操作详情</label>
          <pre class="field-input" style="white-space: pre-wrap; word-break: break-all; font-size: 11px; max-height: 200px; overflow-y: auto;">{{ JSON.stringify(pendingRequests[0].txDesc, null, 2) }}</pre>
        </div>

        <div style="display: flex; gap: 10px; margin-top: 15px;">
          <button class="btn-ghost" style="flex:1" @click="rejectRequest(pendingRequests[0].id)">拒绝</button>
          <button class="btn-primary" style="flex:1" @click="approveRequest(pendingRequests[0].id)">批准</button>
        </div>
      </div>
    </div>

  </div>
</template>

<style scoped>
@import url('https://fonts.googleapis.com/css2?family=Outfit:wght@400;500;600;700;800&family=JetBrains+Mono:wght@400;500&display=swap');

.wallet-shell {
  font-family: 'Outfit', system-ui, sans-serif;
  width: 360px;
  min-height: 480px;
  background: #0d1117;
  color: #e6edf3;
}

.page {
  display: flex;
  flex-direction: column;
  min-height: 480px;
  padding: 0 0 16px;
}

/* ── Logo ─────────────────────────────────────────────── */
.logo-area {
  text-align: center;
  padding: 40px 16px 20px;
}

.logo-snowflake {
  font-size: 56px;
  line-height: 1;
  margin-bottom: 8px;
  background: linear-gradient(135deg, #58a6ff, #a5d6ff);
  -webkit-background-clip: text;
  background-clip: text;
  -webkit-text-fill-color: transparent;
}

h1 {
  font-size: 26px;
  font-weight: 800;
  background: linear-gradient(135deg, #58a6ff, #a5d6ff);
  -webkit-background-clip: text;
  background-clip: text;
  -webkit-text-fill-color: transparent;
  margin-bottom: 4px;
}

.tagline { color: #8b949e; font-size: 12px; }

/* ── Card ─────────────────────────────────────────────── */
.card {
  margin: 0 12px 12px;
  background: #161b22;
  border: 1px solid #30363d;
  border-radius: 12px;
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

/* ── Nav ──────────────────────────────────────────────── */
.top-nav {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 12px 10px;
  border-bottom: 1px solid #21262d;
  margin-bottom: 12px;
}

.nav-title { font-size: 15px; font-weight: 600; }
.nav-actions { display: flex; gap: 4px; }

.addr-row {
  display: flex;
  align-items: center;
  gap: 6px;
  cursor: pointer;
  padding: 4px 8px;
  border-radius: 8px;
  transition: background 0.15s;
}

.addr-row:hover { background: #21262d; }
.addr-code { font-family: 'JetBrains Mono', monospace; font-size: 12px; color: #8b949e; }
.copy-icon { font-size: 14px; color: #8b949e; }

/* ── Buttons ──────────────────────────────────────────── */
.btn-primary {
  width: 100%;
  background: linear-gradient(135deg, #1f6feb, #58a6ff);
  color: #fff;
  border: none;
  border-radius: 8px;
  padding: 10px 16px;
  font-size: 14px;
  font-weight: 600;
  cursor: pointer;
  transition: opacity 0.15s;
  font-family: inherit;
}
.btn-primary:hover:not(:disabled) { opacity: 0.85; }
.btn-primary:disabled { opacity: 0.4; cursor: not-allowed; }

.btn-secondary {
  background: #21262d;
  color: #58a6ff;
  border: 1px solid #30363d;
  border-radius: 8px;
  padding: 8px 14px;
  font-size: 13px;
  cursor: pointer;
  transition: border-color 0.15s;
  font-family: inherit;
}
.btn-secondary:hover { border-color: #58a6ff; }

.btn-ghost {
  background: none;
  border: 1px solid #30363d;
  color: #8b949e;
  border-radius: 8px;
  padding: 8px 14px;
  font-size: 13px;
  cursor: pointer;
  transition: all 0.15s;
  font-family: inherit;
}
.btn-ghost:hover { border-color: #58a6ff; color: #58a6ff; }

.btn-icon {
  background: none;
  border: none;
  color: #8b949e;
  font-size: 17px;
  padding: 4px 8px;
  border-radius: 6px;
  cursor: pointer;
  transition: color 0.15s;
  font-family: inherit;
}
.btn-icon:hover { color: #e6edf3; }

.btn-danger {
  background: rgba(248, 81, 73, 0.1);
  border: 1px solid #f85149;
  color: #f85149;
  border-radius: 8px;
  padding: 10px 16px;
  font-size: 14px;
  cursor: pointer;
  font-family: inherit;
  width: 100%;
}
.btn-danger:hover { background: rgba(248, 81, 73, 0.2); }

.btn-refresh {
  background: #21262d;
  border: 1px solid #30363d;
  color: #8b949e;
  border-radius: 6px;
  padding: 5px 10px;
  font-size: 12px;
  cursor: pointer;
  transition: all 0.15s;
  font-family: inherit;
  align-self: flex-start;
}
.btn-refresh:hover { border-color: #58a6ff; color: #58a6ff; }
.btn-refresh:disabled { opacity: 0.5; cursor: not-allowed; }

/* ── Form ─────────────────────────────────────────────── */
.field-label { font-size: 12px; color: #8b949e; font-weight: 500; margin-bottom: -4px; }

.field-input {
  background: #21262d;
  border: 1px solid #30363d;
  border-radius: 8px;
  padding: 9px 12px;
  color: #e6edf3;
  font-size: 13px;
  outline: none;
  transition: border-color 0.15s;
  font-family: inherit;
  width: 100%;
}
.field-input:focus { border-color: #58a6ff; }
textarea.field-input { resize: none; line-height: 1.5; }

/* ── Tabs ─────────────────────────────────────────────── */
.tab-bar {
  display: flex;
  margin: 0 12px 8px;
  background: #161b22;
  border: 1px solid #30363d;
  border-radius: 8px;
  padding: 3px;
  gap: 2px;
}

.tab {
  flex: 1;
  background: none;
  border: none;
  color: #8b949e;
  padding: 7px;
  border-radius: 6px;
  cursor: pointer;
  font-size: 13px;
  font-weight: 500;
  font-family: inherit;
  transition: all 0.15s;
}

.tab-active {
  background: #21262d;
  color: #e6edf3;
}

/* ── Mnemonic ─────────────────────────────────────────── */
.mnemonic-box {
  background: #21262d;
  border: 1px dashed #30363d;
  border-radius: 8px;
  padding: 12px;
  font-family: 'JetBrains Mono', monospace;
  font-size: 13px;
  line-height: 1.8;
  color: #8b949e;
  text-align: center;
  min-height: 64px;
  word-break: break-word;
}

.mnemonic-filled { color: #58a6ff; border-color: #58a6ff; }

/* ── Balance ──────────────────────────────────────────── */
.balance-card {
  margin: 0 12px 12px;
  background: linear-gradient(135deg, #0c1e35, #0d1a2d);
  border: 1px solid #1f4068;
  border-radius: 12px;
  padding: 20px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.balance-main { display: flex; align-items: baseline; gap: 8px; }
.balance-val { font-size: 36px; font-weight: 800; }
.balance-unit { font-size: 15px; color: #8b949e; }

.balance-list { display: flex; flex-direction: column; gap: 4px; }
.balance-row {
  display: flex;
  justify-content: space-between;
  font-size: 13px;
  padding: 4px 0;
  border-bottom: 1px solid rgba(255, 255, 255, 0.04);
  color: #8b949e;
}
.balance-row:last-child { border-bottom: none; }
.balance-token { font-weight: 500; color: #e6edf3; }

/* ── Actions ──────────────────────────────────────────── */
.action-row { display: flex; gap: 10px; padding: 0 12px; }

.action-btn {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
  flex: 1;
  background: #161b22;
  border: 1px solid #30363d;
  border-radius: 12px;
  padding: 14px 10px;
  color: #e6edf3;
  cursor: pointer;
  font-size: 13px;
  font-weight: 500;
  font-family: inherit;
  transition: border-color 0.15s;
}
.action-btn:hover { border-color: #58a6ff; }
.action-icon { font-size: 20px; }

.tx-history-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.tx-history-item {
  border: 1px solid #30363d;
  border-radius: 10px;
  padding: 10px;
  background: #11161d;
}

.tx-history-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.mono-mini {
  font-family: 'JetBrains Mono', monospace;
  font-size: 11px;
  color: #8b949e;
}

.tx-copy-btn {
  padding: 2px 8px;
  font-size: 11px;
  min-width: auto;
}

/* ── Messages ─────────────────────────────────────────── */
.msg-error { color: #f85149; font-size: 12px; }
.msg-success { color: #3fb950; font-size: 12px; }
.hint { color: #e3b341; font-size: 11px; }

/* ── Danger ───────────────────────────────────────────── */
.danger-card { border-color: rgba(248, 81, 73, 0.3); }
.danger-hint { color: #8b949e; font-size: 12px; }

/* ── Utils ────────────────────────────────────────────── */
.divider {
  text-align: center;
  color: #8b949e;
  font-size: 12px;
  margin: 2px 0;
}
.mt-2 { margin-top: 4px; }
.mt-3 { margin-top: 6px; }

/* ── Account Dropdown ────────────────────────────────────── */
.addr-dropdown { position: relative; }

.account-list {
  position: absolute;
  top: calc(100% + 4px);
  left: 0;
  background: #1c2128;
  border: 1px solid #30363d;
  border-radius: 8px;
  min-width: 200px;
  z-index: 100;
  box-shadow: 0 8px 24px rgba(0,0,0,0.5);
  overflow: hidden;
}

.account-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 9px 12px;
  cursor: pointer;
  font-size: 12px;
  color: #8b949e;
  font-family: 'JetBrains Mono', monospace;
  transition: background 0.12s;
}
.account-item:hover { background: #21262d; color: #e6edf3; }
.account-item-active { color: #58a6ff; }
.checkmark { color: #3fb950; font-weight: 700; }

/* ── File Input ────────────────────────────────────────── */
.file-input {
  background: #21262d;
  border: 1px dashed #30363d;
  border-radius: 8px;
  padding: 9px 12px;
  color: #8b949e;
  font-size: 12px;
  cursor: pointer;
  width: 100%;
  transition: border-color 0.15s;
}
.file-input:hover { border-color: #58a6ff; }
</style>
