/**
 * lib/keystore.js
 * 助记词生成、BIP-32 推导 P-256 密钥、AES-GCM 加密存储
 *
 * 推导路径：m/44'/9999'/0'/0/<index>（9999 = FrostBit coin type）
 *
 * 地址格式：P2WPKH bech32（bc1q...），与节点 DeriveBtcBech32Address 一致
 *   Hash160(压缩公钥) → bech32 encode，hrp='bc', witness_ver=0
 */

import { generateMnemonic, mnemonicToSeedSync, validateMnemonic } from '@scure/bip39';
import { wordlist } from '@scure/bip39/wordlists/english';
import { HDKey } from '@scure/bip32';
import { p256 } from '@noble/curves/p256';
import { secp256k1 } from '@noble/curves/secp256k1';
import { sha256 } from '@noble/hashes/sha256';
import { ripemd160 } from '@noble/hashes/ripemd160';
import { bech32 } from '@scure/base';
import { exportPubKeyDer, toHex, fromHex } from './crypto.js';

const COIN_TYPE = 9999;
const BASE_PATH = `m/44'/${COIN_TYPE}'/0'/0`;

// ─── 助记词 ───────────────────────────────────────────────────────────────

export const generateMnemonicPhrase = () => generateMnemonic(wordlist, 128);
export const isMnemonicValid = (phrase) => validateMnemonic(phrase, wordlist);

// ─── 地址推导（P2WPKH bech32 bc1q...）────────────────────────────────────

/**
 * 从 secp256k1 压缩公钥字节（33字节）派生 bc1q 地址
 * Hash160 = RIPEMD160(SHA256(compressedPubKey))
 * 与 Go 的 btcutil.Hash160 + NewAddressWitnessPubKeyHash 完全对齐
 */
export function deriveAddress(compressedPubKey) {
    const hash160 = ripemd160(sha256(compressedPubKey));
    const words = bech32.toWords(hash160);
    return bech32.encode('bc', new Uint8Array([0, ...words]));
}

// ─── 密钥推导（BIP-32，助记词）───────────────────────────────────────────

export async function deriveAccount(phrase, index = 0) {
    const seed = mnemonicToSeedSync(phrase);
    const child = HDKey.fromMasterSeed(seed).derive(`${BASE_PATH}/${index}`);
    return _accountFromRawPriv(child.privateKey);
}

export async function deriveAccountFromRawHex(hexKey) {
    const raw = fromHex(hexKey.trim().replace(/^0x/, ''));
    if (raw.length !== 32) throw new Error(`Invalid key length: ${raw.length}`);
    return _accountFromRawPriv(raw);
}

/** 内部：从 32字节私钥字节同时派生 secp256k1 地址 + P-256 签名密钥 */
async function _accountFromRawPriv(rawPriv) {
    // ── 地址：secp256k1 压缩公钥 → Hash160 → bc1q（与节点 Go 端完全一致）
    const pubCompressed = secp256k1.getPublicKey(rawPriv, true); // 33字节

    // ── 签名：P-256（WebCrypto 原生支持）
    const pubUncompressed = p256.getPublicKey(rawPriv, false); // 65字节 04||x||y
    const x = pubUncompressed.slice(1, 33);
    const y = pubUncompressed.slice(33, 65);
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
    const address = deriveAddress(pubCompressed);
    return { privateKey, pubKeyDer, rawPriv, address };
}

// ─── AES-GCM 加密存储（助记词）──────────────────────────────────────────

const STORAGE_KEY = 'frostbit_keystore';
const IMPORTED_KEY = 'frostbit_imported_keys';

async function _aesEncrypt(plaintext, password) {
    const enc = new TextEncoder();
    const km = await crypto.subtle.importKey('raw', enc.encode(password), 'PBKDF2', false, ['deriveKey']);
    const salt = crypto.getRandomValues(new Uint8Array(16));
    const aesKey = await crypto.subtle.deriveKey(
        { name: 'PBKDF2', salt, iterations: 100_000, hash: 'SHA-256' },
        km, { name: 'AES-GCM', length: 256 }, false, ['encrypt']
    );
    const iv = crypto.getRandomValues(new Uint8Array(12));
    const ct = await crypto.subtle.encrypt({ name: 'AES-GCM', iv }, aesKey, enc.encode(plaintext));
    return { salt: Array.from(salt), iv: Array.from(iv), ct: Array.from(new Uint8Array(ct)), version: 1 };
}

async function _aesDecrypt(ks, password) {
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

export async function saveKeystore(phrase, password) {
    await chrome.storage.local.set({ [STORAGE_KEY]: await _aesEncrypt(phrase, password) });
}

export async function loadKeystore(password) {
    const { [STORAGE_KEY]: ks } = await chrome.storage.local.get(STORAGE_KEY);
    if (!ks) throw new Error('No keystore found');
    return _aesDecrypt(ks, password);
}

export async function hasKeystore() {
    const { [STORAGE_KEY]: ks } = await chrome.storage.local.get(STORAGE_KEY);
    return !!ks;
}

export async function clearKeystore() {
    await chrome.storage.local.remove([STORAGE_KEY, IMPORTED_KEY]);
}

// ─── 导入账户存储（hex 私钥列表）────────────────────────────────────────

/**
 * 将 hex 私钥数组加密保存（JSON 序列化后 AES-GCM）
 * @param {string[]} hexKeys
 */
export async function saveImportedKeys(hexKeys, password) {
    const payload = JSON.stringify(hexKeys);
    await chrome.storage.local.set({ [IMPORTED_KEY]: await _aesEncrypt(payload, password) });
}

/**
 * 解密还原 hex 私钥数组，再派生账户
 * @returns {Promise<Array>} 账户对象数组
 */
export async function loadImportedAccounts(password) {
    const { [IMPORTED_KEY]: ks } = await chrome.storage.local.get(IMPORTED_KEY);
    if (!ks) return [];
    const json = await _aesDecrypt(ks, password);
    const hexKeys = JSON.parse(json);
    return Promise.all(hexKeys.map(k => deriveAccountFromRawHex(k)));
}

export async function hasImportedKeys() {
    const { [IMPORTED_KEY]: ks } = await chrome.storage.local.get(IMPORTED_KEY);
    return !!ks;
}
