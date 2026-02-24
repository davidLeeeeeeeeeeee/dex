# FrostBit Wallet — Chrome 插件

## 目录结构

```
wallet/
├── manifest.json          # Chrome Extension Manifest V3
├── package.json           # 依赖（protobufjs、bip39、bip32、esbuild）
├── build.js               # 构建脚本
├── background/
│   └── service_worker.js  # 密钥管理、签名、RPC 中枢
├── content/
│   └── content_script.js  # 注入 window.frostbit Provider
├── popup/
│   ├── index.html
│   ├── popup.js
│   └── popup.css
├── lib/
│   ├── crypto.js          # WebCrypto P-256 签名工具
│   ├── keystore.js        # BIP-39/32 推导 + AES-GCM 加密存储
│   ├── proto.js           # protobufjs AnyTx 序列化 + 签名组装
│   ├── rpc.js             # 节点 HTTP API 封装
│   └── data_proto.json    # proto 编译产物（见下方步骤生成）
├── pb/
│   └── data.proto         # 从 ../pb/data.proto 复制
└── icons/
    ├── icon16.png
    ├── icon48.png
    └── icon128.png
```

## 快速开始

### 1. 安装依赖

```bash
cd wallet
npm install
```

### 2. 编译 proto 描述符

```bash
# 安装 pbjs 工具（已随 protobufjs 安装）
npx pbjs -t json pb/data.proto -o lib/data_proto.json
```

### 3. 构建插件

```bash
node build.js
# 产物在 dist/ 目录
```

### 4. 加载到 Chrome

1. 打开 `chrome://extensions/`
2. 开启右上角「开发者模式」
3. 点击「加载已解压的扩展程序」
4. 选择 `wallet/dist/` 目录（或直接选 `wallet/` 目录，若直接使用源文件）

## 注意事项

### P-256 私钥推导

`lib/keystore.js` 通过 `@scure/bip32` 推导 BIP-32 子节点的 32 字节私钥原始值，
然后将其作为 P-256 曲线的私钥种子导入 WebCrypto。
推导路径：`m/44'/9999'/0'/0/<index>`（9999 = FrostBit coin type）。

### 签名格式

节点要求：
- 公钥：DER PKIX SubjectPublicKeyInfo（WebCrypto `exportKey('spki', ...)` 直接输出）
- 签名：对 `protobuf.encode(AnyTx)` 字节的 SHA-256 哈希做 ECDSA P-256 签名

### 节点通信

默认节点地址：`https://127.0.0.1:6000`，可在设置页修改。
所有写操作（`/tx`）使用 `Content-Type: application/x-protobuf`，
查询操作（`/getaccount` 等）使用 JSON。

## DApp 接入示例

```javascript
// 等待钱包就绪
window.addEventListener('frostbit#initialized', async () => {
  // 请求连接
  const [address] = await window.frostbit.requestAccounts();

  // 发送转账
  await window.frostbit.sendTransaction({
    type: 'transaction',
    to: '0xReceiverAddress',
    tokenAddress: 'FB',
    amount: '100',
  });
});
```
