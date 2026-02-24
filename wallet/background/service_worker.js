/**
 * background/service_worker.js
 * Chrome Extension Service Worker — 密钥管理中枢 & 消息路由
 *
 * 所有涉及私钥的操作在此处执行，popup 和 content_script 通过
 * chrome.runtime.sendMessage 与其通信。
 *
 * 会话内存（内存，不落盘）：
 *   _session.phrase   解锁后的明文助记词
 *   _session.accounts [{address, privateKey(CryptoKey), pubKeyDer}]
 *   _session.unlocked bool
 */

import {
    generateMnemonicPhrase, isMnemonicValid, deriveAccount,
    saveKeystore, loadKeystore, hasKeystore, clearKeystore,
} from '../lib/keystore.js';
import { buildAndSign } from '../lib/proto.js';
import { getAccount, getAccountBalances, sendTx, getTxReceipt } from '../lib/rpc.js';
import { toBase64 } from '../lib/crypto.js';

// ─── 会话状态（内存）────────────────────────────────────────────────────────

const _session = { unlocked: false, phrase: null, accounts: [], selectedIndex: 0, pendingRequests: [] };

let _reqIdCounter = 0;

function requireUnlocked() {
    if (!_session.unlocked) throw new Error('Wallet is locked');
}

function selectedAccount() {
    return _session.accounts[_session.selectedIndex];
}

// ─── 消息处理 ────────────────────────────────────────────────────────────────

chrome.runtime.onMessage.addListener((msg, sender, reply) => {
    // 拦截来自 DApp 的交易请求
    if (sender.tab && (msg.type === 'SEND_TX' || msg.type === 'SIGN_TX')) {
        const reqId = ++_reqIdCounter;
        _session.pendingRequests.push({
            id: reqId,
            type: msg.type,
            txDesc: msg.txDesc,
            reply,
            url: sender.tab.url || sender.url
        });

        chrome.windows.create({
            url: 'popup/index.html',
            type: 'popup',
            width: 360,
            height: 600,
            focused: true
        });
        return true; // Wait for async reply
    }

    handleMessage(msg).then(reply).catch(e => reply({ error: e.message }));
    return true; // 异步应答
});

async function handleMessage(msg) {
    switch (msg.type) {

        // ── 签名请求审批 ────────────────────────────────────────────────────────
        case 'GET_PENDING_REQUESTS':
            return { requests: _session.pendingRequests.map(r => ({ id: r.id, type: r.type, txDesc: r.txDesc, url: r.url })) };

        case 'APPROVE_REQUEST': {
            const reqIdx = _session.pendingRequests.findIndex(r => r.id === msg.id);
            if (reqIdx === -1) throw new Error('Request not found');
            const req = _session.pendingRequests[reqIdx];
            _session.pendingRequests.splice(reqIdx, 1);

            try {
                // 内部直接调用，跳过 tab 检查
                const res = await handleMessage({ type: req.type, txDesc: req.txDesc });
                req.reply(res);
                return { ok: true };
            } catch (e) {
                req.reply({ error: e.message });
                throw e;
            }
        }

        case 'REJECT_REQUEST': {
            const reqIdx = _session.pendingRequests.findIndex(r => r.id === msg.id);
            if (reqIdx === -1) throw new Error('Request not found');
            const req = _session.pendingRequests[reqIdx];
            _session.pendingRequests.splice(reqIdx, 1);
            req.reply({ error: 'User rejected the request' });
            return { ok: true };
        }

        // ── 钱包初始化 ──────────────────────────────────────────────────────────

        case 'HAS_WALLET':
            return { has: await hasKeystore() };

        case 'GENERATE_MNEMONIC':
            return { mnemonic: generateMnemonicPhrase() };

        case 'CREATE_WALLET': {
            const { mnemonic, password } = msg;
            if (!isMnemonicValid(mnemonic)) throw new Error('Invalid mnemonic');
            await saveKeystore(mnemonic, password);
            // 自动解锁
            _session.phrase = mnemonic;
            _session.accounts = [await deriveAccount(mnemonic, 0)];
            _session.selectedIndex = 0;
            _session.unlocked = true;
            return { address: _session.accounts[0].address };
        }

        case 'IMPORT_WALLET': {
            const { mnemonic, password } = msg;
            if (!isMnemonicValid(mnemonic)) throw new Error('Invalid mnemonic');
            await saveKeystore(mnemonic, password);
            _session.phrase = mnemonic;
            _session.accounts = [await deriveAccount(mnemonic, 0)];
            _session.selectedIndex = 0;
            _session.unlocked = true;
            return { address: _session.accounts[0].address };
        }

        // ── 锁定/解锁 ──────────────────────────────────────────────────────────

        case 'UNLOCK': {
            const phrase = await loadKeystore(msg.password); // 密码错误会抛出
            _session.phrase = phrase;
            _session.accounts = [await deriveAccount(phrase, 0)];
            _session.selectedIndex = 0;
            _session.unlocked = true;
            return { address: _session.accounts[0].address };
        }

        case 'LOCK':
            _session.unlocked = false;
            _session.phrase = null;
            _session.accounts = [];
            return { ok: true };

        case 'IS_UNLOCKED':
            return { unlocked: _session.unlocked };

        // ── 账户 ───────────────────────────────────────────────────────────────

        case 'GET_ACCOUNTS':
            requireUnlocked();
            return { accounts: _session.accounts.map(a => a.address) };

        case 'ADD_ACCOUNT': {
            requireUnlocked();
            const idx = _session.accounts.length;
            const acc = await deriveAccount(_session.phrase, idx);
            _session.accounts.push(acc);
            return { address: acc.address, index: idx };
        }

        case 'SELECT_ACCOUNT':
            requireUnlocked();
            if (msg.index < 0 || msg.index >= _session.accounts.length)
                throw new Error('Invalid account index');
            _session.selectedIndex = msg.index;
            return { address: selectedAccount().address };

        // ── 余额查询 ───────────────────────────────────────────────────────────

        case 'GET_BALANCES': {
            requireUnlocked();
            const addr = msg.address || selectedAccount().address;
            return getAccountBalances(addr);
        }

        // ── 转账 ───────────────────────────────────────────────────────────────

        case 'SEND_TX': {
            requireUnlocked();
            const acc = selectedAccount();
            const { txDesc } = msg;

            // 拉取最新 nonce
            const accountInfo = await getAccount(acc.address);
            const nonce = (accountInfo?.account?.nonce ?? 0) + 1;

            const txBytes = await buildAndSign(txDesc, {
                fromAddress: acc.address,
                nonce,
                privateKey: acc.privateKey,
                pubKeyDer: acc.pubKeyDer,
            });

            await sendTx(txBytes);
            // txId 已在 buildAndSign 内计算并嵌入 base，此处简单返回已发送
            return { ok: true };
        }

        // ── 签名（DApp 调用）──────────────────────────────────────────────────

        case 'SIGN_TX': {
            requireUnlocked();
            const acc = selectedAccount();
            const { txDesc } = msg;
            const accountInfo = await getAccount(acc.address);
            const nonce = (accountInfo?.account?.nonce ?? 0) + 1;
            const txBytes = await buildAndSign(txDesc, {
                fromAddress: acc.address,
                nonce,
                privateKey: acc.privateKey,
                pubKeyDer: acc.pubKeyDer,
            });
            return { signedTx: toBase64(txBytes) };
        }

        case 'SIGN_MESSAGE': {
            requireUnlocked();
            const acc = selectedAccount();
            const msgBytes = new TextEncoder().encode(msg.message);
            const { sign } = await import('../lib/crypto.js');
            const sig = await sign(acc.privateKey, msgBytes);
            return { signature: toBase64(sig) };
        }

        // ── 收据查询 ──────────────────────────────────────────────────────────

        case 'GET_RECEIPT':
            return getTxReceipt(msg.txId);

        // ── 节点设置 ──────────────────────────────────────────────────────────

        case 'SET_SETTINGS':
            await chrome.storage.local.set({
                explorerUrl: msg.explorerUrl || 'http://127.0.0.1:8080',
                nodeAddr: msg.nodeAddr || '',
            });
            return { ok: true };

        case 'GET_SETTINGS': {
            const { explorerUrl, nodeAddr } = await chrome.storage.local.get(['explorerUrl', 'nodeAddr']);
            return { explorerUrl: explorerUrl || 'http://127.0.0.1:8080', nodeAddr: nodeAddr || '' };
        }

        // ── 重置 ──────────────────────────────────────────────────────────────

        case 'RESET_WALLET':
            await clearKeystore();
            _session.unlocked = false;
            _session.phrase = null;
            _session.accounts = [];
            return { ok: true };

        default:
            throw new Error(`Unknown message type: ${msg.type}`);
    }
}

// 自动锁定：5 分钟无操作
chrome.alarms.create('auto_lock', { periodInMinutes: 5 });
chrome.alarms.onAlarm.addListener(a => {
    if (a.name === 'auto_lock' && _session.unlocked) {
        _session.unlocked = false;
        _session.phrase = null;
        _session.accounts = [];
    }
});
