---
name: cross-chain-frost-test
description: Go↔链上合约 FROST 门限签名跨语言集成测试，覆盖 Solana Ed25519 和 EVM alt_bn128 两条链。
---

# 跨链 FROST 签名集成测试 Skill

## Trigger Cues

- `跨链测试`、`集成测试`
- `FROST 签名验证`、`门限签名合约验证`
- `Go 签名 Solidity 验证`、`Go 签名 Solana 验证`

## 测试架构

```
Go 端 (frost/runtime/roast/)         链上合约
┌─────────────────────────┐      ┌────────────────────────┐
│ 1. DKG 份额生成          │      │ Solana: frost-vault    │
│ 2. FROST 门限签名        │─JSON→│  Ed25519SigVerify 验签  │
│ 3. 写 test vectors JSON │      │                        │
│                         │      │ EVM: SchnorrExample    │
│                         │─JSON→│  ecAdd/ecMul 预编译验签  │
└─────────────────────────┘      └────────────────────────┘
```

## 文件索引

### Go 向量生成器

| 文件 | 链 | 说明 |
|------|---|------|
| `frost/runtime/roast/roast_solana_vectors_test.go` | SOL | 生成 Ed25519 向量 → `solana/frost-vault/tests/frost_vectors.json` |
| `frost/runtime/roast/roast_solidity_vectors_test.go` | EVM | 生成 BN128 向量 → `solidity/test/bn128_frost_vectors.json` |

### 链上测试

| 文件 | 链 | 框架 |
|------|---|------|
| `solana/frost-vault/tests/frost-integration.ts` | SOL | Anchor (Mocha) |
| `solidity/test/bn128_frost_integration.js` | EVM | Hardhat (Mocha) |

## 快速运行

```bash
# === Solana Ed25519 ===
# Step 1: 生成向量
go test -v -run TestGenerateFROSTVectorsForSolana ./frost/runtime/roast/ -count=1
# Step 2: 合约验证
cd solana/frost-vault && anchor test

# === EVM BN128 ===
# Step 1: 生成向量
go test -v -run TestGenerateFROSTVectorsForSolidity ./frost/runtime/roast/ -count=1
# Step 2: 合约验证
cd solidity && npx hardhat test
```

## 向量 JSON 格式

### Ed25519 (SOL)

```json
{
  "name": "changePub | withdraw",
  "pubkey": "32 bytes hex (Ed25519 compressed point)",
  "message": "原始消息 hex",
  "msg_hash": "sha256(message) hex",
  "signature": "64 bytes hex (R_compressed || z_le)"
}
```

### BN128 (EVM)

```json
{
  "name": "verifySignature | changePub | withdraw",
  "px": "32 bytes hex (公钥 X)", "py": "32 bytes hex (公钥 Y)",
  "rx": "32 bytes hex (R.X)",    "ry": "32 bytes hex (R.Y)",
  "s": "32 bytes hex (标量 s, big-endian)",
  "msg_hash": "keccak256(msg) hex"
}
```

## 关键差异对照

| 维度 | Solana Ed25519 | EVM BN128 |
|------|---------------|-----------|
| Challenge | `SHA-512(R‖pk‖msg) mod L` | `keccak256(Rx‖Ry‖Px‖Py‖mHash) mod n` |
| 签名格式 | `R(32) ‖ z_le(32)` | `R.x(32) ‖ z_be(32)` |
| z 字节序 | 小端 | 大端 |
| 验签方式 | Ed25519SigVerify 预编译 | ecAdd(0x06) + ecMul(0x07) 预编译 |
| 合约验签 | 用 `instruction_sysvar` 反查指令 | 直接在 `view` 函数内计算 |
| 签名对象 | `sha256(message)` | `keccak256(message)` |

## 常见陷阱

1. **Solidity challenge 的 msg 参数是 mHash 不是原始消息** — `verify(P, R, s, mHash)` 中 challenge = `keccak256(Rx‖Ry‖Px‖Py‖mHash)`，所以 Go 端签名时必须对 `keccak256(msg)` 签名
2. **Solana Ed25519 instruction data 偏移量** — `signatureOffset` 在 `d[2..3]`（非 `d[4..5]`），详见 `verify_ed25519_ix` 中的注释
3. **带 data 的 PDA 不能用 `system_instruction::transfer`** — 需用 `try_borrow_mut_lamports()` 直接操作
