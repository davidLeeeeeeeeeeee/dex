/**
 * background/service_worker.js
 * Chrome Extension Service Worker — 密钥管理中枢 & 消息路由
 *
 * 会话内存（内存，不落盘）：
 *   _session.phrase       解锁后的明文助记词（可为 null，纯导入账户时）
 *   _session.accounts     [{address, privateKey(CryptoKey), pubKeyDer, rawPriv}]
 *   _session.unlocked     bool
 *   _session.selectedIndex int
 */

import {
    generateMnemonicPhrase, isMnemonicValid, deriveAccount, deriveAccountFromRawHex,
    saveKeystore, loadKeystore, hasKeystore, clearKeystore,
    saveImportedKeys, loadImportedAccounts, hasImportedKeys,
} from '../lib/keystore.js';
import { buildAndSign, decodeAnyTx } from '../lib/proto.js';
import { getAccount, getAccountBalances, sendTx, getTxReceipt } from '../lib/rpc.js';
import { toBase64 } from '../lib/crypto.js';

// ─── 会话状态（内存）────────────────────────────────────────────────────────

const _session = { unlocked: false, phrase: null, accounts: [], selectedIndex: 0, pendingRequests: [] };

let _reqIdCounter = 0;

async function tryRehydrateSession() {
    if (_session.unlocked) return true;
    const { sessionPassword } = await chrome.storage.session.get('sessionPassword');
    if (!sessionPassword) return false;

    try {
        const hasMain = await hasKeystore();
        if (hasMain) {
            const phrase = await loadKeystore(sessionPassword);
            _session.phrase = phrase;
            _session.accounts = [await deriveAccount(phrase, 0)];
        } else {
            _session.phrase = null;
            _session.accounts = [];
        }
        const imported = await loadImportedAccounts(sessionPassword);
        _session.accounts.push(...imported);
        _session.selectedIndex = 0;
        _session.unlocked = true;
        return true;
    } catch (e) {
        await chrome.storage.session.remove('sessionPassword');
        return false;
    }
}

async function requireUnlocked() {
    if (!(await tryRehydrateSession())) throw new Error('Wallet is locked');
}

function selectedAccount() {
    return _session.accounts[_session.selectedIndex];
}

function readAccountNonce(accountInfo) {
    if (typeof accountInfo?.nonce === 'number') return accountInfo.nonce;
    if (typeof accountInfo?.account?.nonce === 'number') return accountInfo.account.nonce;
    return 0;
}

// ─── 消息处理 ────────────────────────────────────────────────────────────────

chrome.runtime.onMessage.addListener((msg, sender, reply) => {
    // 每次收到请求时重置休眠锁定倒计时（实现“真实的无操作 5 分钟后锁定”）
    chrome.storage.session.get('sessionPassword').then(({ sessionPassword }) => {
        if (sessionPassword) {
            chrome.alarms.create('auto_lock', { delayInMinutes: 5 });
        }
    });

    // 拦截来自 DApp 的交易请求
    if (sender.tab && (msg.type === 'SEND_TX' || msg.type === 'SIGN_TX' || msg.type === 'SIGN_MESSAGE')) {
        const reqId = ++_reqIdCounter;
        _session.pendingRequests.push({
            id: reqId, type: msg.type, txDesc: msg.txDesc,
            reply, url: sender.tab.url || sender.url
        });
        chrome.windows.create({ url: 'popup/index.html', type: 'popup', width: 360, height: 600, focused: true });
        return true;
    }
    handleMessage(msg).then(reply).catch(e => reply({ error: e.message }));
    return true;
});

async function handleMessage(msg) {
    switch (msg.type) {

        // ── 签名请求审批 ────────────────────────────────────────────────────────
        case 'GET_PENDING_REQUESTS':
            return { requests: _session.pendingRequests.map(r => ({ id: r.id, type: r.type, txDesc: r.txDesc, url: r.url })) };

        case 'APPROVE_REQUEST': {
            const reqIdx = _session.pendingRequests.findIndex(r => r.id === msg.id);
            if (reqIdx === -1) throw new Error('Request not found');
            const req = _session.pendingRequests.splice(reqIdx, 1)[0];
            try {
                const res = await handleMessage({ type: req.type, txDesc: req.txDesc });
                req.reply(res);
                return { ok: true };
            } catch (e) { req.reply({ error: e.message }); throw e; }
        }

        case 'REJECT_REQUEST': {
            const reqIdx = _session.pendingRequests.findIndex(r => r.id === msg.id);
            if (reqIdx === -1) throw new Error('Request not found');
            _session.pendingRequests.splice(reqIdx, 1)[0].reply({ error: 'User rejected the request' });
            return { ok: true };
        }

        // ── 钱包初始化 ──────────────────────────────────────────────────────────

        case 'HAS_WALLET':
            return { has: (await hasKeystore()) || (await hasImportedKeys()) };

        case 'GENERATE_MNEMONIC':
            return { mnemonic: generateMnemonicPhrase() };

        case 'CREATE_WALLET': {
            const { mnemonic, password } = msg;
            if (!isMnemonicValid(mnemonic)) throw new Error('Invalid mnemonic');
            await saveKeystore(mnemonic, password);
            _session.phrase = mnemonic;
            _session.accounts = [await deriveAccount(mnemonic, 0)];
            _session.selectedIndex = 0;
            _session.unlocked = true;
            await chrome.storage.session.set({ sessionPassword: password });
            chrome.alarms.create('auto_lock', { delayInMinutes: 5 });
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
            await chrome.storage.session.set({ sessionPassword: password });
            chrome.alarms.create('auto_lock', { delayInMinutes: 5 });
            return { address: _session.accounts[0].address };
        }

        // ── 锁定/解锁 ──────────────────────────────────────────────────────────

        case 'UNLOCK': {
            const hasMain = await hasKeystore();
            if (hasMain) {
                const phrase = await loadKeystore(msg.password);
                _session.phrase = phrase;
                _session.accounts = [await deriveAccount(phrase, 0)];
            } else {
                _session.phrase = null;
                _session.accounts = [];
            }
            // 加载导入账户（这里卋 password 就是导入时用的密码）
            const imported = await loadImportedAccounts(msg.password);
            _session.accounts.push(...imported);
            _session.selectedIndex = 0;
            _session.unlocked = true;
            if (_session.accounts.length === 0) throw new Error('No accounts found for this password');
            await chrome.storage.session.set({ sessionPassword: msg.password });
            chrome.alarms.create('auto_lock', { delayInMinutes: 5 });
            return { address: _session.accounts[0].address };
        }

        case 'LOCK':
            _session.unlocked = false;
            _session.phrase = null;
            _session.accounts = [];
            await chrome.storage.session.remove('sessionPassword');
            chrome.alarms.clear('auto_lock');
            return { ok: true };

        case 'IS_UNLOCKED':
            return { unlocked: await tryRehydrateSession() };

        // ── 账户 ───────────────────────────────────────────────────────────────

        case 'GET_ACCOUNTS':
            await requireUnlocked();
            return { accounts: _session.accounts.map(a => a.address) };

        case 'GET_SELECTED_ACCOUNT':
            await requireUnlocked();
            return { address: selectedAccount().address };

        case 'ADD_ACCOUNT': {
            await requireUnlocked();
            if (!_session.phrase) throw new Error('No mnemonic (imported-key wallet cannot add derived accounts)');
            const idx = _session.accounts.length;
            const acc = await deriveAccount(_session.phrase, idx);
            _session.accounts.push(acc);
            return { address: acc.address, index: idx };
        }

        case 'SELECT_ACCOUNT':
            await requireUnlocked();
            if (msg.index < 0 || msg.index >= _session.accounts.length)
                throw new Error('Invalid account index');
            _session.selectedIndex = msg.index;
            return { address: selectedAccount().address };

        /**
         * 批量从 hex 私钥列表导入账户
         * 无需预先解锁；导入后直接建立会话
         */
        case 'IMPORT_ACCOUNTS_FILE': {
            const { hexKeys, password } = msg;
            if (!Array.isArray(hexKeys) || hexKeys.length === 0)
                throw new Error('No keys provided');

            // 派生新账户
            const existing = new Set(_session.unlocked ? _session.accounts.map(a => a.address) : []);
            const newAccounts = [];
            for (const k of hexKeys) {
                const acc = await deriveAccountFromRawHex(k);
                if (!existing.has(acc.address)) { existing.add(acc.address); newAccounts.push(acc); }
            }

            // 合并并保存全部 imported keys
            const allImportedAccounts = (_session.unlocked ? _session.accounts.filter(a => a.rawPriv) : []).concat(newAccounts);
            const allHex = allImportedAccounts.map(a =>
                Array.from(a.rawPriv).map(b => b.toString(16).padStart(2, '0')).join('')
            );
            await saveImportedKeys(allHex, password);

            // 建立/更新会话
            if (_session.unlocked) {
                _session.accounts.push(...newAccounts);
            } else {
                _session.accounts = newAccounts;
                _session.selectedIndex = 0;
                _session.unlocked = true;
            }
            return { added: newAccounts.map(a => a.address) };
        }

        // ── 余额查询 ───────────────────────────────────────────────────────────

        case 'GET_BALANCES': {
            await requireUnlocked();
            const addr = msg.address || selectedAccount().address;
            return getAccountBalances(addr);
        }

        // ── 转账 ───────────────────────────────────────────────────────────────

        case 'SEND_TX': {
            await requireUnlocked();
            const acc = selectedAccount();
            const { txDesc } = msg;
            const accountInfo = await getAccount(acc.address);
            const nonce = readAccountNonce(accountInfo) + 1;
            const txBytes = await buildAndSign(txDesc, {
                fromAddress: acc.address, nonce,
                privateKey: acc.privateKey, pubKeyDer: acc.pubKeyDer,
            });
            const submitRes = await sendTx(txBytes);
            if (submitRes?.ok !== true) throw new Error('submit tx failed');

            const anyTx = decodeAnyTx(txBytes);
            let txId = '';
            for (const key of Object.keys(anyTx)) {
                if (anyTx[key] && anyTx[key].base && anyTx[key].base.txId) {
                    txId = anyTx[key].base.txId;
                    break;
                }
            }
            return { ok: true, txId };
        }

        // ── 签名（DApp 调用）──────────────────────────────────────────────────

        case 'SIGN_TX': {
            await requireUnlocked();
            const acc = selectedAccount();
            const { txDesc } = msg;
            const accountInfo = await getAccount(acc.address);
            const nonce = readAccountNonce(accountInfo) + 1;
            const txBytes = await buildAndSign(txDesc, {
                fromAddress: acc.address, nonce,
                privateKey: acc.privateKey, pubKeyDer: acc.pubKeyDer,
            });
            return { signedTx: toBase64(txBytes) };
        }

        case 'SIGN_MESSAGE': {
            await requireUnlocked();
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
            await chrome.storage.session.remove('sessionPassword');
            chrome.alarms.clear('auto_lock');
            return { ok: true };

        default:
            throw new Error(`Unknown message type: ${msg.type}`);
    }
}

// 自动锁定：无操作后触发
chrome.alarms.onAlarm.addListener(async a => {
    if (a.name === 'auto_lock') {
        _session.unlocked = false;
        _session.phrase = null;
        _session.accounts = [];
        await chrome.storage.session.remove('sessionPassword');
    }
});
