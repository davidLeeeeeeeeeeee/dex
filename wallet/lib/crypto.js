/**
 * lib/crypto.js
 * 基于 WebCrypto API 的 ECDSA P-256 工具
 * - 生成密钥对
 * - 导入/导出私钥（raw 32字节 / JWK）
 * - 导出公钥（DER PKIX SubjectPublicKeyInfo，节点要求格式）
 * - 签名：SHA-256 + ECDSA P-256，输出 DER 编码签名
 */

const ALGO = { name: 'ECDSA', namedCurve: 'P-256' };
const SIGN_ALGO = { name: 'ECDSA', hash: 'SHA-256' };

/** 生成新的 P-256 密钥对，返回 {privateKey: CryptoKey, publicKey: CryptoKey} */
export async function generateKeyPair() {
    return crypto.subtle.generateKey(ALGO, true, ['sign', 'verify']);
}

/** 将 CryptoKey 私钥导出为 JWK */
export async function exportPrivKeyJwk(privateKey) {
    return crypto.subtle.exportKey('jwk', privateKey);
}

/** 从 JWK 重新导入私钥 */
export async function importPrivKeyJwk(jwk) {
    return crypto.subtle.importKey('jwk', jwk, ALGO, true, ['sign']);
}

/** 从 JWK 重新导入公钥 */
export async function importPubKeyJwk(jwk) {
    const pubJwk = { kty: jwk.kty, crv: jwk.crv, x: jwk.x, y: jwk.y };
    return crypto.subtle.importKey('jwk', pubJwk, ALGO, true, ['verify']);
}

/**
 * 导出公钥为 DER PKIX SubjectPublicKeyInfo（65字节未压缩点，带固定ASN.1 header）
 * 这是节点 ECDSA_P256 验签要求的格式
 */
export async function exportPubKeyDer(publicKey) {
    // spki 格式即为 DER SubjectPublicKeyInfo
    const buf = await crypto.subtle.exportKey('spki', publicKey);
    return new Uint8Array(buf);
}

/** 从 DER spki 字节导入公钥 */
export async function importPubKeyDer(derBytes) {
    return crypto.subtle.importKey('spki', derBytes, ALGO, true, ['verify']);
}

/**
 * 签名：对 data (Uint8Array) 用 P-256 + SHA-256 签名
 * 返回 Uint8Array（DER 编码）
 */
export async function sign(privateKey, data) {
    const sig = await crypto.subtle.sign(SIGN_ALGO, privateKey, data);
    return new Uint8Array(sig);
}

/**
 * 从私钥 JWK 推导公钥 JWK（利用曲线点恢复，实际上直接从 JWK 读 x, y 即可）
 */
export function pubJwkFromPrivJwk(privJwk) {
    return { kty: privJwk.kty, crv: privJwk.crv, x: privJwk.x, y: privJwk.y };
}

/** Uint8Array <-> hex 工具 */
export const toHex = (buf) => Array.from(buf).map(b => b.toString(16).padStart(2, '0')).join('');
export const fromHex = (hex) => new Uint8Array(hex.match(/.{1,2}/g).map(b => parseInt(b, 16)));

/** Uint8Array -> base64 */
export const toBase64 = (buf) => btoa(String.fromCharCode(...buf));
export const fromBase64 = (s) => new Uint8Array(atob(s).split('').map(c => c.charCodeAt(0)));
