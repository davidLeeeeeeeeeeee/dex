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
export async function fetchFrostWithdrawQueue(node: string, chain: string = '', asset: string = ''): Promise<any[]> {
  const resp = await fetch(`/api/frost/withdraw/queue?node=${encodeURIComponent(node)}&chain=${chain}&asset=${asset}`)
  return resp.json()
}

// 获取见证者上账请求
export async function fetchWitnessRequests(node: string): Promise<any[]> {
  const resp = await fetch(`/api/witness/requests?node=${encodeURIComponent(node)}`)
  return resp.json()
}
// 获取 Frost DKG 会话
export async function fetchFrostDKGSessions(node: string): Promise<any[]> {
  const resp = await fetch(`/api/frost/dkg/list?node=${encodeURIComponent(node)}`)
  return resp.json()
}

// 获取地址交易历史
export async function fetchTxHistory(address: string, limit: number = 50): Promise<TxHistoryResponse> {
  const resp = await fetch(`/api/txhistory?address=${encodeURIComponent(address)}&limit=${limit}`)
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
    if (data.details) {
      return `${data.error}: ${data.details}`
    }
    return data.error
  } catch {
    return `Request failed with status ${resp.status}`
  }
}

// 获取订单簿数据
import type { OrderBookData, TradeRecord } from './types'

export async function fetchOrderBook(node: string, pair: string): Promise<OrderBookData> {
  const resp = await fetch(`/api/orderbook?node=${encodeURIComponent(node)}&pair=${encodeURIComponent(pair)}`)
  if (!resp.ok) {
    const errorMessage = await parseAPIError(resp)
    throw new Error(errorMessage)
  }
  return resp.json()
}

// 获取最近成交记录
export async function fetchRecentTrades(node: string, pair: string): Promise<TradeRecord[]> {
  const resp = await fetch(`/api/trades?node=${encodeURIComponent(node)}&pair=${encodeURIComponent(pair)}`)
  if (!resp.ok) {
    const errorMessage = await parseAPIError(resp)
    throw new Error(errorMessage)
  }
  const data = await resp.json()
  return data || [] // 确保返回数组而不是 null
}
