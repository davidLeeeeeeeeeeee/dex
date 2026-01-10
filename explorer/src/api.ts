import type {
  NodesResponse,
  SummaryRequest,
  SummaryResponse,
  NodeDetails,
  BlockRequest,
  BlockResponse,
  TxRequest,
  TxResponse,
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

