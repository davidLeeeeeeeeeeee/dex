/**
 * content/content_script.js
 * 注入到所有页面，在 window 上暴露 frostbit Provider 对象
 * DApp 通过 window.frostbit 与钱包交互
 */

(function () {
    if (window.frostbit) return; // 防止重复注入

    // 向 background 发消息的辅助函数
    function send(msg) {
        return new Promise((resolve, reject) => {
            chrome.runtime.sendMessage(msg, (res) => {
                if (chrome.runtime.lastError) return reject(new Error(chrome.runtime.lastError.message));
                if (res?.error) return reject(new Error(res.error));
                resolve(res);
            });
        });
    }

    // 等待用户在 popup 中点击 "授权" 的辅助（简化版：直接检查锁定状态）
    async function requireConnected() {
        const { unlocked } = await send({ type: 'IS_UNLOCKED' });
        if (!unlocked) throw new Error('FrostBit Wallet is locked. Please open the extension and unlock first.');
    }

    const frostbit = {
        isFrostBit: true,
        version: '0.1.0',

        /**
         * 请求连接，返回当前账户地址列表
         * DApp 应在用户交互时调用（符合浏览器手势限制）
         */
        requestAccounts: async () => {
            await requireConnected();
            const { accounts } = await send({ type: 'GET_ACCOUNTS' });
            return accounts;
        },

        /** 获取当前账户（不弹窗） */
        getAccounts: async () => {
            const { accounts } = await send({ type: 'GET_ACCOUNTS' });
            return accounts ?? [];
        },

        /**
         * 签名并广播交易，返回 txId（哈希）
         * @param {object} txDesc 交易描述
         *   { type: 'transaction', to: string, tokenAddress: string, amount: string }
         */
        sendTransaction: async (txDesc) => {
            await requireConnected();
            return send({ type: 'SEND_TX', txDesc });
        },

        /**
         * 仅签名，返回已签名 AnyTx 的 Base64 字节码（DApp 自行广播）
         * @param {object} txDesc 同 sendTransaction
         */
        signTransaction: async (txDesc) => {
            await requireConnected();
            const { signedTx } = await send({ type: 'SIGN_TX', txDesc });
            return signedTx;
        },

        /**
         * 对任意字符串签名（用于身份验证），返回 Base64 编码的 P-256 DER 签名
         * @param {string} message
         */
        signMessage: async (message) => {
            await requireConnected();
            const { signature } = await send({ type: 'SIGN_MESSAGE', message });
            return signature;
        },

        /**
         * 查询账户余额
         * @param {string} address 可选，默认当前账户
         */
        getBalance: async (address) => {
            const { balances } = await send({ type: 'GET_BALANCES', address });
            return balances ?? {};
        },
    };

    Object.defineProperty(window, 'frostbit', {
        value: Object.freeze(frostbit),
        writable: false,
        configurable: false,
    });

    // 通知 DApp 钱包已就绪
    window.dispatchEvent(new CustomEvent('frostbit#initialized'));
})();
