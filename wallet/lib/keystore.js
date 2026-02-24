/**
 * lib/keystore.js
 * 助记词生成、BIP-32 推导 P-256 密钥、AES-GCM 加密存储
 *
 * 推导路径：m/44'/9999'/0'/0/<index>（9999 = FrostBit coin type）
 *
 * 技术路线：
 *   1. @scure/bip32 推导 BIP-32 子节点 → 得到 32 字节 raw 私钥
 *   2. @noble/curves/p256 计算对应的公钥点 (x, y)
 *   3. 构造完整 P-256 JWK 导入 WebCrypto — 可直接用于签名和导出 spki
 */

import { generateMnemonic, mnemonicToSeedSync, validateMnemonic } from '@scure/bip39';
import { wordlist } from '@scure/bip39/wordlists/english';
import { HDKey } from '@scure/bip32';
import { p256 } from '@noble/curves/p256';
import { exportPubKeyDer, toHex } from './crypto.js';

const COIN_TYPE = 9999;
const BASE_PATH = `m/44'/${COIN_TYPE}'/0'/0`;

// ─── 助记词 ───────────────────────────────────────────────────────────────

export const generateMnemonicPhrase = () => generateMnemonic(wordlist, 128);
export const isMnemonicValid = (phrase) => validateMnemonic(phrase, wordlist);

// ─── 密钥推导 ─────────────────────────────────────────────────────────────

/**
 * 从助记词推导第 index 个账户
 * @returns {Promise<{privateKey: CryptoKey, pubKeyDer: Uint8Array, address: string}>}
 */
export async function deriveAccount(phrase, index = 0) {
    const seed = mnemonicToSeedSync(phrase);
    const child = HDKey.fromMasterSeed(seed).derive(`${BASE_PATH}/${index}`);
    const rawPriv = child.privateKey; // Uint8Array 32字节

    // 用 @noble/curves 从私钥原始字节计算 P-256 公钥点 (uncompressed: 04||x||y)
    const pubPoint = p256.getPublicKey(rawPriv, false); // 65字节 04||x||y
    const x = pubPoint.slice(1, 33);
    const y = pubPoint.slice(33, 65);

    const toB64u = (buf) => btoa(String.fromCharCode(...buf))
        .replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '');

    const ALGO = { name: 'ECDSA', namedCurve: 'P-256' };
    const privateKey = await crypto.subtle.importKey('jwk', {
        kty: 'EC', crv: 'P-256',
        d: toB64u(rawPriv), x: toB64u(x), y: toB64u(y),
        key_ops: ['sign'],
    }, ALGO, true, ['sign']);

    const publicKey = await crypto.subtle.importKey('jwk', {
        kty: 'EC', crv: 'P-256', x: toB64u(x), y: toB64u(y), key_ops: ['verify'],
    }, ALGO, true, ['verify']);

    const pubKeyDer = await exportPubKeyDer(publicKey);
    const address = deriveAddress(pubKeyDer);
    return { privateKey, pubKeyDer, address };
}

/**
 * 从公钥 spki DER 字节派生地址
 * 取未压缩点的 x 分量前 20 字节，以 0x 为前缀
 */
export function deriveAddress(pubKeyDer) {
    const point = pubKeyDer.slice(-65); // 04 || x(32) || y(32)
    return '0x' + toHex(point.slice(1, 21));
}

// ─── AES-GCM 加密存储 ─────────────────────────────────────────────────────

const STORAGE_KEY = 'frostbit_keystore';

export async function saveKeystore(phrase, password) {
    const enc = new TextEncoder();
    const km = await crypto.subtle.importKey('raw', enc.encode(password), 'PBKDF2', false, ['deriveKey']);
    const salt = crypto.getRandomValues(new Uint8Array(16));
    const aesKey = await crypto.subtle.deriveKey(
        { name: 'PBKDF2', salt, iterations: 100_000, hash: 'SHA-256' },
        km, { name: 'AES-GCM', length: 256 }, false, ['encrypt']
    );
    const iv = crypto.getRandomValues(new Uint8Array(12));
    const ct = await crypto.subtle.encrypt({ name: 'AES-GCM', iv }, aesKey, enc.encode(phrase));
    await chrome.storage.local.set({
        [STORAGE_KEY]: {
            salt: Array.from(salt), iv: Array.from(iv),
            ct: Array.from(new Uint8Array(ct)), version: 1,
        }
    });
}

export async function loadKeystore(password) {
    const { [STORAGE_KEY]: ks } = await chrome.storage.local.get(STORAGE_KEY);
    if (!ks) throw new Error('No keystore found');
    const enc = new TextEncoder();
    const km = await crypto.subtle.importKey('raw', enc.encode(password), 'PBKDF2', false, ['deriveKey']);
    const aesKey = await crypto.subtle.deriveKey(
        { name: 'PBKDF2', salt: new Uint8Array(ks.salt), iterations: 100_000, hash: 'SHA-256' },
        km, { name: 'AES-GCM', length: 256 }, false, ['decrypt']
    );
    const plain = await crypto.subtle.decrypt(
        { name: 'AES-GCM', iv: new Uint8Array(ks.iv) }, aesKey, new Uint8Array(ks.ct)
    );
    return new TextDecoder().decode(plain);
}

export async function hasKeystore() {
    const { [STORAGE_KEY]: ks } = await chrome.storage.local.get(STORAGE_KEY);
    return !!ks;
}

export async function clearKeystore() {
    await chrome.storage.local.remove(STORAGE_KEY);
}
