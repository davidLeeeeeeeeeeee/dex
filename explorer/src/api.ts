import type {
  NodesResponse,
  SummaryRequest,
  SummaryResponse,
  NodeDetails,
  BlockRequest,
  BlockResponse,
  TxRequest,
  TxResponse,
  AddressRequest,
  AddressResponse,
  TxHistoryResponse,
  SyncStatusResponse,
  FrostWithdrawQueueItem,
  FrostWithdrawJobItem,
  WitnessRequest,
  WitnessInfo,
  DKGSession,
  OrderBookData,
  TradeRecord,
} from './types'

// 获取默认节点列表
export async function fetchNodes(): Promise<NodesResponse> {
  const resp = await fetch('/api/nodes')
  return resp.json()
}

// 获取节点摘要信息
export async function fetchSummary(request: SummaryRequest): Promise<SummaryResponse> {
  const resp = await fetch('/api/summary', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(request),
  })
  return resp.json()
}

// 获取节点详情
export async function fetchNodeDetails(address: string): Promise<NodeDetails> {
  const resp = await fetch(`/api/node/details?address=${encodeURIComponent(address)}`)
  return resp.json()
}

// 查询区块（按高度或哈希）
export async function fetchBlock(request: BlockRequest): Promise<BlockResponse> {
  const resp = await fetch('/api/block', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(request),
  })
  return resp.json()
}

// 查询交易
export async function fetchTx(request: TxRequest): Promise<TxResponse> {
  const resp = await fetch('/api/tx', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(request),
  })
  return resp.json()
}

// 查询地址（账户）
export async function fetchAddress(request: AddressRequest): Promise<AddressResponse> {
  const resp = await fetch('/api/address', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(request),
  })
  return resp.json()
}

// 获取最近区块列表
export interface RecentBlocksRequest {
  node: string
  count?: number
}

export interface BlockHeaderInfo {
  height: number
  block_hash: string
  miner?: string
  tx_count: number
  accumulated_reward?: string
  window?: number
  state_root?: string
}

export interface RecentBlocksResponse {
  blocks: BlockHeaderInfo[]
  error?: string
}

export async function fetchRecentBlocks(request: RecentBlocksRequest): Promise<RecentBlocksResponse> {
  const resp = await fetch('/api/recentblocks', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(request),
  })
  return resp.json()
}

// 获取 Frost 提现队列
export async function fetchFrostWithdrawQueue(node: string, chain: string = '', asset: string = ''): Promise<FrostWithdrawQueueItem[]> {
  const resp = await fetch(`/api/frost/withdraw/queue?node=${encodeURIComponent(node)}&chain=${chain}&asset=${asset}`)
  return resp.json()
}

// 获取 Frost Job 列表
export async function fetchFrostWithdrawJobs(node: string, chain: string = '', asset: string = ''): Promise<FrostWithdrawJobItem[]> {
  const resp = await fetch(`/api/frost/withdraw/jobs?node=${encodeURIComponent(node)}&chain=${chain}&asset=${asset}`)
  return resp.json()
}

// 获取见证者上账请求
export async function fetchWitnessRequests(node: string): Promise<WitnessRequest[]> {
  const resp = await fetch(`/api/witness/requests?node=${encodeURIComponent(node)}`)
  return resp.json()
}

// 获取见证者列表
export async function fetchWitnessList(node: string): Promise<WitnessInfo[]> {
  const resp = await fetch(`/api/witness/list?node=${encodeURIComponent(node)}`)
  return resp.json()
}

// 获取 Frost DKG 会话
export async function fetchFrostDKGSessions(node: string): Promise<DKGSession[]> {
  const resp = await fetch(`/api/frost/dkg/list?node=${encodeURIComponent(node)}`)
  return resp.json()
}

// 获取地址交易历史
export async function fetchTxHistory(address: string, limit: number = 0): Promise<TxHistoryResponse> {
  let url = `/api/txhistory?address=${encodeURIComponent(address)}`
  if (limit > 0) url += `&limit=${limit}`
  const resp = await fetch(url)
  return resp.json()
}

// 获取同步状态
export async function fetchSyncStatus(): Promise<SyncStatusResponse> {
  const resp = await fetch('/api/sync/status')
  return resp.json()
}

// API 错误类型
interface APIError {
  error: string
  code?: string
  details?: string
}

// 解析 API 错误
async function parseAPIError(resp: Response): Promise<string> {
  try {
    const data: APIError = await resp.json()
    return data.details ? `${data.error}: ${data.details}` : data.error
  } catch {
    return `Request failed with status ${resp.status}`
  }
}

// 获取订单簿数据
export async function fetchOrderBook(node: string, pair: string): Promise<OrderBookData> {
  const resp = await fetch(`/api/orderbook?node=${encodeURIComponent(node)}&pair=${encodeURIComponent(pair)}`)
  if (!resp.ok) throw new Error(await parseAPIError(resp))
  return resp.json()
}

// 获取最近成交记录
export async function fetchRecentTrades(node: string, pair: string, limit: number = 100): Promise<TradeRecord[]> {
  const resp = await fetch(`/api/trades?node=${encodeURIComponent(node)}&pair=${encodeURIComponent(pair)}&limit=${limit}`)
  if (!resp.ok) throw new Error(await parseAPIError(resp))
  const data = await resp.json()
  return data || []
}

// 最近交易记录类型
export interface RecentTxRecord {
  tx_id: string
  tx_type: string
  from_address?: string
  to_address?: string
  value?: string
  status: string
  fee?: string
  nonce?: number
  height: number
  tx_index: number
}

export interface RecentTxsResponse {
  txs: RecentTxRecord[]
  error?: string
}

// 获取最近交易列表（支持 tx_type 过滤）
export async function fetchRecentTxs(node: string, txType: string = 'all', count: number = 50): Promise<RecentTxsResponse> {
  const resp = await fetch('/api/recenttxs', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ node, tx_type: txType, count }),
  })
  return resp.json()
}
