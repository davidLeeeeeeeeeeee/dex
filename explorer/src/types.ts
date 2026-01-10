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

