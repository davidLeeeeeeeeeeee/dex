/**
 * content/inpage.js  (MAIN world)
 * 运行在页面 JS 主上下文，暴露 window.frostbit Provider
 * 通过 window.postMessage 与 ISOLATED world 的 content_script.js 通信
 */
(function () {
    if (window.frostbit) return;

    let _reqId = 0;
    const _pending = {};

    // 监听 content_script 中转回来的响应
    window.addEventListener('message', (e) => {
        if (e.source !== window || !e.data || e.data.__frostbit_dir !== 'from_cs') return;
        const { id, result, error } = e.data;
        if (!_pending[id]) return;
        const { resolve, reject } = _pending[id];
        delete _pending[id];
        error ? reject(new Error(error)) : resolve(result);
    });

    function send(msg) {
        return new Promise((resolve, reject) => {
            const id = ++_reqId;
            _pending[id] = { resolve, reject };
            window.postMessage({ __frostbit_dir: 'to_bg', id, msg }, '*');
        });
    }

    const frostbit = {
        isFrostBit: true,
        version: '0.1.0',

        requestAccounts: async () => {
            const res = await send({ type: 'GET_ACCOUNTS' });
            if (!res?.accounts?.length) throw new Error('钱包未解锁，请先点击扩展图标并解锁');
            return res.accounts;
        },

        getAccounts: async () => {
            const res = await send({ type: 'GET_ACCOUNTS' });
            return res?.accounts ?? [];
        },

        sendTransaction: async (txDesc) => {
            const unlocked = await send({ type: 'IS_UNLOCKED' });
            if (!unlocked?.unlocked) throw new Error('钱包未解锁，请先点击扩展图标并解锁');
            return send({ type: 'SEND_TX', txDesc });
        },

        signTransaction: async (txDesc) => {
            const res = await send({ type: 'SIGN_TX', txDesc });
            return res?.signedTx;
        },

        signMessage: async (message) => {
            const res = await send({ type: 'SIGN_MESSAGE', message });
            return res?.signature;
        },

        getBalance: async (address) => {
            const res = await send({ type: 'GET_BALANCES', address });
            return res?.balances ?? {};
        },
    };

    Object.defineProperty(window, 'frostbit', {
        value: Object.freeze(frostbit),
        writable: false,
        configurable: false,
    });

    window.dispatchEvent(new CustomEvent('frostbit#initialized'));
})();
