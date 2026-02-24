/**
 * lib/proto.js
 * 基于 protobufjs 封装 AnyTx 的序列化与签名组装
 *
 * 注意：此文件在构建时被 esbuild bundle，会一并打包 protobufjs 和 data.proto 的 JSON 描述符
 */

import protobuf from 'protobufjs';
import { sign, exportPubKeyDer, toHex } from './crypto.js';

// data.proto 的 JSON descriptor（由 pbjs -t json pb/data.proto 生成并内联）
// 在执行 npm run build 之前须先运行：
//   npx pbjs -t json ../pb/data.proto -o lib/data_proto.json
import protoJson from './data_proto.json' assert { type: 'json' };

let _root = null;
function root() {
    if (!_root) _root = protobuf.Root.fromJSON(protoJson);
    return _root;
}

const AnyTx = () => root().lookupType('pb.AnyTx');
const Transaction = () => root().lookupType('pb.Transaction');
const BaseMessage = () => root().lookupType('pb.BaseMessage');
const PublicKeys = () => root().lookupType('pb.PublicKeys');

/**
 * 构造并签名一笔 Transaction（转账），返回已签名的 AnyTx protobuf 字节
 *
 * @param {object} params
 * @param {string}      params.fromAddress  发送方地址
 * @param {string}      params.toAddress    接收方地址
 * @param {string}      params.tokenAddress 代币地址（如 "FB" / "USDT" / token addr）
 * @param {string}      params.amount       金额字符串（整数或小数）
 * @param {number}      params.nonce        当前 nonce
 * @param {CryptoKey}   params.privateKey   P-256 CryptoKey（sign 权限）
 * @param {Uint8Array}  params.pubKeyDer    公钥 DER 字节
 * @returns {Promise<Uint8Array>} 序列化后的 AnyTx 字节
 */
export async function buildAndSignTransferTx({ fromAddress, toAddress, tokenAddress, amount, nonce, privateKey, pubKeyDer }) {
    // 1. 构造 BaseMessage（signature 先留空，用于计算 txId）
    const pubKeysMsg = PublicKeys().create({
        keys: { 1: pubKeyDer }, // key=1 即 SIGN_ALGO_ECDSA_P256
    });

    const baseDraft = BaseMessage().create({
        fromAddress,
        nonce,
        publicKeys: pubKeysMsg,
        fee: '0',
    });

    // 2. 构造 Transaction
    const txMsg = Transaction().create({
        base: baseDraft,
        to: toAddress,
        tokenAddress,
        amount,
    });

    // 3. 包入 AnyTx
    const anyTxDraft = AnyTx().create({ transaction: txMsg });

    // 4. 序列化草稿（不含签名），计算 txId
    const draftBytes = AnyTx().encode(anyTxDraft).finish();
    const hashBuf = await crypto.subtle.digest('SHA-256', draftBytes);
    const txId = toHex(new Uint8Array(hashBuf));

    // 5. 签名
    const sigBytes = await sign(privateKey, draftBytes);

    // 6. 填入 txId 和签名，重新序列化
    baseDraft.txId = txId;
    baseDraft.signature = sigBytes;

    const finalAnyTx = AnyTx().create({ transaction: txMsg });
    return AnyTx().encode(finalAnyTx).finish();
}

/**
 * 对任意 AnyTx JSON 描述（DApp 传入）构造 + 签名
 * txDesc 格式：{ type: 'transaction', to, tokenAddress, amount }
 */
export async function buildAndSign(txDesc, { fromAddress, nonce, privateKey, pubKeyDer }) {
    switch (txDesc.type) {
        case 'transaction':
            return buildAndSignTransferTx({
                fromAddress,
                toAddress: txDesc.to,
                tokenAddress: txDesc.tokenAddress,
                amount: txDesc.amount,
                nonce,
                privateKey,
                pubKeyDer,
            });
        default:
            throw new Error(`Unsupported tx type: ${txDesc.type}`);
    }
}

/** 解析 AnyTx 字节（调试用） */
export function decodeAnyTx(bytes) {
    return AnyTx().decode(bytes);
}
