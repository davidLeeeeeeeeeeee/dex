/**
 * content/content_script.js  (ISOLATED world)
 * 1. 把 inpage.js 作为 <script> 标签注入页面主上下文（MAIN world），暴露 window.frostbit
 * 2. 桥接：监听 inpage.js 发来的 postMessage，转发到 background service worker
 */
(function () {
    // ── Step 1: 注入 inpage.js 到页面主 JS 上下文 ──────────────────────────
    const script = document.createElement('script');
    script.src = chrome.runtime.getURL('content/inpage.js');
    script.onload = () => script.remove();
    (document.head || document.documentElement).appendChild(script);

    // ── Step 2: 桥接 postMessage ↔ chrome.runtime.sendMessage ──────────────
    window.addEventListener('message', (e) => {
        if (e.source !== window || !e.data || e.data.__frostbit_dir !== 'to_bg') return;
        const { id, msg } = e.data;
        try {
            chrome.runtime.sendMessage(msg, (res) => {
                const error = chrome.runtime.lastError?.message || res?.error || null;
                window.postMessage({
                    __frostbit_dir: 'from_cs',
                    id,
                    result: error ? null : res,
                    error,
                }, '*');
            });
        } catch (err) {
            // Extension context invalidated (用户重载了扩展但没刷新页面)
            window.postMessage({
                __frostbit_dir: 'from_cs',
                id,
                result: null,
                error: '插件已更新，请按 F5 刷新当前网页后再重试！',
            }, '*');
        }
    });
})();
