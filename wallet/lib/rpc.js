/**
 * lib/rpc.js
 * 通过 Explorer HTTP 服务（普通 JSON，无需 Protobuf 和 HTTP/3）与节点交互
 * 默认 Explorer 地址：http://127.0.0.1:8080
 */

import { toBase64 } from './crypto.js';

const TIMEOUT_MS = 15_000;

async function getExplorerUrl() {
    const { explorerUrl } = await chrome.storage.local.get('explorerUrl');
    return (explorerUrl || 'http://127.0.0.1:8080').replace(/\/$/, '');
}

async function getDefaultNode() {
    const { nodeAddr } = await chrome.storage.local.get('nodeAddr');
    return nodeAddr || '';
}

async function postJSON(path, body) {
    const base = await getExplorerUrl();
    const res = await fetch(`${base}${path}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
        signal: AbortSignal.timeout(TIMEOUT_MS),
    });
    if (!res.ok) throw new Error(`HTTP ${res.status}: ${await res.text()}`);
    return res.json();
}

/**
 * 获取账户 nonce（下一笔交易用）
 * @param {string} address
 * @returns {Promise<{address, nonce}>}
 */
export async function getAccount(address) {
    const node = await getDefaultNode();
    return postJSON('/api/wallet/nonce', { node, address });
}

/**
 * 获取账户所有余额
 * 使用 Explorer 现有的 /api/address 接口
 * @returns {Promise<{account: {balances, nonce, ...}}>}
 */
export async function getAccountBalances(address) {
    const node = await getDefaultNode();
    const res = await postJSON('/api/address', { node, address });
    // Explorer /api/address 返回 { account: { balances: {...}, nonce, ... } }
    return { balances: res.account?.balances ?? {}, error: res.error };
}

/**
 * 发送已签名的 AnyTx（Uint8Array protobuf 字节）
 * 钱包把 bytes 转为 base64 POST 给 Explorer，Explorer 转发给节点
 * @param {Uint8Array} anyTxBytes
 */
export async function sendTx(anyTxBytes) {
    const node = await getDefaultNode();
    const tx = toBase64(anyTxBytes);
    return postJSON('/api/wallet/submittx', { node, tx });
}

/**
 * 查询交易收据
 * @param {string} txId
 */
export async function getTxReceipt(txId) {
    const node = await getDefaultNode();
    return postJSON('/api/wallet/receipt', { node, tx_id: txId });
}

/** 设置 Explorer 地址（从设置页保存） */
export async function setExplorerUrl(url) {
    await chrome.storage.local.set({ explorerUrl: url });
}

/** 设置默认节点地址（用于 Explorer 转发到哪个节点） */
export async function setDefaultNode(addr) {
    await chrome.storage.local.set({ nodeAddr: addr });
}
