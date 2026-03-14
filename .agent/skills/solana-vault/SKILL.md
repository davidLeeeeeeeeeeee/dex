---
name: solana-vault
description: Solana Anchor program implementing FROST Ed25519 threshold-signature vault (equivalent to solidity/schnorr_verify.sol for SOL chain).
---

# Solana FROST Vault Skill

## Trigger Cues

- `solana vault`
- `sol withdraw`
- `sol changePub`
- `Ed25519SigVerify`
- `SPL Token vault`

Use this skill when working on the Solana on-chain vault program, its deployment, or its integration with the Go-side `SolanaAdapter`.

## Source Map

- Program entry: `solana/frost-vault/programs/frost-vault/src/lib.rs`
- State (PDA accounts): `solana/frost-vault/programs/frost-vault/src/state.rs`
- Error codes: `solana/frost-vault/programs/frost-vault/src/error.rs`
- Instructions:
  - `src/instructions/initialize.rs` — create VaultState PDA, set initial FROST group pubkey
  - `src/instructions/change_pub.rs` — DKG key rotation via Ed25519SigVerify precompile
  - `src/instructions/withdraw.rs` — SOL / SPL Token withdrawal with replay protection
- Tests: `solana/frost-vault/tests/frost-vault.ts`
- Go adapter: `frost/chain/solana/adapter.go`
- Solidity counterpart: `solidity/schnorr_verify.sol`

## Key Design Decisions

1. **Ed25519SigVerify Precompile** — signatures are verified by Solana runtime (zero CU), program only checks instruction data matches expected pubkey/msg/sig.
2. **Replay protection** — `ConsumedRecord` PDA with `init` constraint; duplicate PDA creation fails automatically.
3. **PDA as vault** — VaultState PDA holds SOL and acts as SPL Token authority; transfers use `invoke_signed`.

## Typical Tasks

1. Deploy & test: `anchor build && anchor test`
2. Integrate with Go `SolanaAdapter`: update `BuildWithdrawTemplate`, `PackageSigned`, `VerifySignature`
3. Add SPL Token mint support: extend `withdraw.rs` token path

## Quick Commands

```bash
# Build
cd solana/frost-vault && anchor build

# Test
anchor test

# Find Ed25519 verification logic
rg "verify_ed25519_ix|Ed25519SigVerify|ed25519_program" solana/frost-vault
```
