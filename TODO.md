### 1. eth\sol\bnb模块的见证，vault，提取整个流程走通
### 2. vault的权利交接集成测试、单元测试
### 3. eth\sol\bnb模块的配套合约编写和测试网测试
### 4. 思考怎么添加eth等合约的资产，USDT,USDC等如何映射到DEX资产。
### 5. 调研SOL技术栈。
### 6. 本链的token发布流程，包括前端UI等
### 要点
链	模型	FROST 签名的性质	需要 RawTx？
BTC	无合约，签名即交易	Taproot Schnorr 签名直接解锁 UTXO	✅ 需要，RawTx 就是可广播的完整交易
ETH/BNB/SOL	合约验签	签名只是合约的一个参数	❌ 不需要，用户自己构造合约调用

RawTx 保留，BTC adapter 填它，合约链 adapter 留空就行。SignedPackage不用改。


