// 节点摘要信息
export interface NodeSummary {
  address: string
  status?: string
  info?: string
  current_height?: number
  last_accepted_height?: number
  latency_ms?: number
  error?: string
  block?: BlockSummary
  frost_metrics?: FrostMetrics
}

// 节点详情（包含日志和最近区块）
export interface NodeDetails extends NodeSummary {
  logs?: LogLine[]
  recent_blocks?: BlockHeader[]
}

// 区块头信息
export interface BlockHeader {
  height: number
  block_hash: string
  txs_hash: string
  miner: string
  tx_count: number
  accumulated_reward: string
  window: number
}

// 日志行
export interface LogLine {
  timestamp: string
  level: string
  message: string
}

// 区块摘要
export interface BlockSummary {
  height: number
  block_hash?: string
  prev_block_hash?: string
  txs_hash?: string
  miner?: string
  tx_count: number
  tx_type_counts?: Record<string, number>
  accumulated_reward?: string
  window?: number
}

// FROST 指标
export interface FrostMetrics {
  heap_alloc: number
  heap_sys: number
  num_goroutine: number
  frost_jobs: number
  frost_withdraws: number
  api_call_stats?: Record<string, number>
}

// 节点列表响应
export interface NodesResponse {
  base_port: number
  count: number
  nodes: string[]
}

// 摘要请求
export interface SummaryRequest {
  nodes: string[]
  include_block: boolean
  include_frost: boolean
}

// 摘要响应
export interface SummaryResponse {
  generated_at?: string
  nodes: NodeSummary[]
  errors?: string[]
  selected?: string[]
  elapsed_ms?: number
}

// Explorer 状态
export interface ExplorerState {
  nodes: string[]
  customNodes: string[]
  selected: Set<string>
  auto: boolean
  includeBlock: boolean
  includeFrost: boolean
  intervalMs: number
  timer: number | null
}

// 区块详情（包含交易列表）
export interface BlockInfo {
  height: number
  block_hash: string
  prev_block_hash?: string
  txs_hash?: string
  miner?: string
  tx_count: number
  accumulated_reward?: string
  window?: number
  transactions?: TxSummary[]
}

// 交易摘要（区块内的交易列表项）
export interface TxSummary {
  tx_id: string
  tx_type?: string
  from_address?: string
  status?: string
  summary?: string
}

// 交易详情
export interface TxInfo {
  tx_id: string
  tx_type?: string
  from_address?: string
  to_address?: string
  status?: string
  executed_height?: number
  fee?: string
  nonce?: number
  details?: Record<string, unknown>
}

// 区块查询请求
export interface BlockRequest {
  node: string
  height?: number
  hash?: string
}

// 区块查询响应
export interface BlockResponse {
  block?: BlockInfo
  error?: string
}

// 交易查询请求
export interface TxRequest {
  node: string
  tx_id: string
}

// 交易查询响应
export interface TxResponse {
  transaction?: TxInfo
  error?: string
}

// FROST 提现队列项
export interface FrostWithdrawQueueItem {
  withdraw_id: string
  chain: string
  asset: string
  to: string
  amount: string
  status: string
  vault_id?: number
  request_height?: number
  job_id?: string
}

// 见证人上账请求
export interface WitnessRequest {
  request_id: string
  native_chain: string
  native_tx_hash: string
  token_address: string
  amount: string
  receiver_address: string
  requester_address?: string
  status: string
  create_height?: number
  pass_count?: number
  fail_count?: number
  abstain_count?: number
  vault_id?: number
}

// DKG 会话
export interface DKGSession {
  chain: string
  vault_id: number
  epoch_id: number
  sign_algo: string
  trigger_height: number
  old_committee_members: string[]
  new_committee_members: string[]
  dkg_status: string
  dkg_session_id: string
  dkg_threshold_t: number
  dkg_n: number
  dkg_commit_deadline: number
  dkg_dispute_deadline: number
  validation_status: string
  lifecycle: string
}
