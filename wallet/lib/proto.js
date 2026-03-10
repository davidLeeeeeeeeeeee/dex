/**
 * lib/proto.js
 * 基于 pbjs 静态生成的序列化模块封装 AnyTx 的序列化与签名组装
 * 彻底移除了运行时的 JSON load 以及 Root 反射，避免 Chrome MV3 里的 unsafe-eval 报错。
 */

import { sign, exportPubKeyDer, toHex } from './crypto.js';

// 直接导入通过 npx pbjs -t static-module 提前静态生成的类代码
import { pb } from './data_proto.js';

const AnyTx = () => pb.AnyTx;
const Transaction = () => pb.Transaction;
const BaseMessage = () => pb.BaseMessage;
const PublicKeys = () => pb.PublicKeys;

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
    const base = () => {
        const pubKeysMsg = PublicKeys().create({ keys: { 1: pubKeyDer } });
        return BaseMessage().create({ fromAddress, nonce, publicKeys: pubKeysMsg, fee: '0' });
    };
    const signAndWrap = async (anyTxContent) => {
        const draft = AnyTx().encode(AnyTx().create(anyTxContent)).finish();
        const hashBuf = await crypto.subtle.digest('SHA-256', draft);
        const txId = toHex(new Uint8Array(hashBuf));
        const sig = await sign(privateKey, draft);
        // 设置 txId 和 signature
        Object.values(anyTxContent)[0].base.txId = txId;
        Object.values(anyTxContent)[0].base.signature = sig;
        return AnyTx().encode(AnyTx().create(anyTxContent)).finish();
    };

    switch (txDesc.type) {
        case 'transaction':
            return buildAndSignTransferTx({
                fromAddress, toAddress: txDesc.to, tokenAddress: txDesc.tokenAddress,
                amount: txDesc.amount, nonce, privateKey, pubKeyDer,
            });

        case 'order': {
            const OrderTx = () => pb.OrderTx;
            const OrderOp = pb.OrderOp;
            const OrderSide = pb.OrderSide;
            const tx = OrderTx().create({
                base: base(),
                baseToken: txDesc.baseToken,
                quoteToken: txDesc.quoteToken,
                op: OrderOp.ADD,
                amount: txDesc.amount,
                price: txDesc.price,
                side: txDesc.side === 'SELL' ? OrderSide.SELL : OrderSide.BUY,
            });
            return signAndWrap({ orderTx: tx });
        }

        case 'witness_vote': {
            const WitnessVoteTx = () => pb.WitnessVoteTx;
            const WitnessVote = () => pb.WitnessVote;
            const WitnessVoteType = pb.WitnessVoteType;
            const voteType = txDesc.vote === 'FAIL' ? WitnessVoteType.VOTE_FAIL : WitnessVoteType.VOTE_PASS;
            const tx = WitnessVoteTx().create({
                base: base(),
                vote: WitnessVote().create({ requestId: txDesc.requestId, voteType }),
            });
            return signAndWrap({ witnessVoteTx: tx });
        }

        case 'witness_challenge': {
            const WitnessChallengeTx = () => pb.WitnessChallengeTx;
            const tx = WitnessChallengeTx().create({
                base: base(),
                requestId: txDesc.requestId,
                stakeAmount: txDesc.stakeAmount || '100',
                reason: txDesc.reason || '',
                evidence: txDesc.evidence || '',
            });
            return signAndWrap({ witnessChallengeTx: tx });
        }

        case 'recharge_request': {
            const WitnessRequestTx = () => pb.WitnessRequestTx;
            if (!txDesc.tokenAddress) throw new Error('recharge_request: tokenAddress is required');
            // nativeScript: hex string → Uint8Array（BTC P2TR scriptPubKey，34字节）
            const scriptHex = (txDesc.nativeScript || '').replace(/^0x/i, '');
            const nativeScript = scriptHex.length > 0
                ? new Uint8Array(scriptHex.match(/.{1,2}/g).map(b => parseInt(b, 16)))
                : new Uint8Array(0);
            const tx = WitnessRequestTx().create({
                base: base(),
                nativeChain: txDesc.chain,
                nativeTxHash: txDesc.nativeTxHash,
                nativeVout: txDesc.nativeVout ?? 0,
                nativeScript,
                tokenAddress: txDesc.tokenAddress,
                amount: txDesc.amount || '',
                receiverAddress: txDesc.receiverAddress,
                memo: txDesc.memo || '',
                rechargeFee: txDesc.rechargeFee || '0',
            });
            return signAndWrap({ witnessRequestTx: tx });
        }

        case 'frost_withdraw': {
            const FrostWithdrawRequestTx = () => pb.FrostWithdrawRequestTx;
            const tx = FrostWithdrawRequestTx().create({
                base: base(),
                chain: txDesc.chain,
                asset: txDesc.asset,
                to: txDesc.toAddress,
                amount: txDesc.amount,
            });
            return signAndWrap({ frostWithdrawRequestTx: tx });
        }

        default:
            throw new Error(`Unsupported tx type: ${txDesc.type}`);
    }
}


/** 解析 AnyTx 字节（调试用） */
export function decodeAnyTx(bytes) {
    return AnyTx().decode(bytes);
}

