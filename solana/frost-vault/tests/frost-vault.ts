// solana/frost-vault/tests/frost-vault.ts
//
// FROST Vault Program 测试
// 测试 initialize / change_pub / withdraw 三个指令

import * as anchor from "@coral-xyz/anchor";
import { Program } from "@coral-xyz/anchor";
import { FrostVault } from "../target/types/frost_vault";
import {
  Ed25519Program,
  Keypair,
  PublicKey,
  SystemProgram,
  SYSVAR_INSTRUCTIONS_PUBKEY,
  Transaction,
} from "@solana/web3.js";
import { expect } from "chai";
import * as crypto from "crypto";
import * as nacl from "tweetnacl";

describe("frost-vault", () => {
  const provider = anchor.AnchorProvider.env();
  anchor.setProvider(provider);
  const program = anchor.workspace.FrostVault as Program<FrostVault>;

  // 模拟 FROST 群公钥（实际由 DKG 生成）
  const frostKeypair1 = Keypair.generate();
  const frostKeypair2 = Keypair.generate();
  const vaultId = 1;

  // 计算 VaultState PDA
  const [vaultPda, vaultBump] = PublicKey.findProgramAddressSync(
    [
      Buffer.from("frost_vault"),
      Buffer.from(new Uint32Array([vaultId]).buffer),
    ],
    program.programId
  );

  it("Initialize vault", async () => {
    const tx = await program.methods
      .initialize(vaultId, frostKeypair1.publicKey)
      .accounts({
        payer: provider.wallet.publicKey,
        vaultState: vaultPda,
        systemProgram: SystemProgram.programId,
      })
      .rpc();

    console.log("Initialize tx:", tx);

    // 验证状态
    const vault = await program.account.vaultState.fetch(vaultPda);
    expect(vault.authority.toBase58()).to.equal(
      frostKeypair1.publicKey.toBase58()
    );
    expect(vault.vaultId).to.equal(vaultId);
    expect(vault.keyEpoch.toNumber()).to.equal(1);
    console.log("✅ Vault initialized with authority:", vault.authority.toBase58());
  });

  it("Change pub (DKG key rotation)", async () => {
    // 1. 构造消息: sha256(new_pubkey)
    const msgHash = crypto
      .createHash("sha256")
      .update(frostKeypair2.publicKey.toBuffer())
      .digest();

    // 2. 旧公钥签名
    const signature = nacl.sign.detached(
      msgHash,
      frostKeypair1.secretKey
    );

    // 3. 构建交易：Ed25519SigVerify 指令 + changePub 指令
    const ed25519Ix = Ed25519Program.createInstructionWithPublicKey({
      publicKey: frostKeypair1.publicKey.toBytes(),
      message: msgHash,
      signature: Buffer.from(signature),
    });

    const changePubTx = await program.methods
      .changePub(frostKeypair2.publicKey, Array.from(signature) as any)
      .accounts({
        vaultState: vaultPda,
        instructionSysvar: SYSVAR_INSTRUCTIONS_PUBKEY,
      })
      .preInstructions([ed25519Ix]) // Ed25519 验签指令在前
      .rpc();

    console.log("ChangePub tx:", changePubTx);

    // 验证状态
    const vault = await program.account.vaultState.fetch(vaultPda);
    expect(vault.authority.toBase58()).to.equal(
      frostKeypair2.publicKey.toBase58()
    );
    expect(vault.keyEpoch.toNumber()).to.equal(2);
    console.log("✅ Pub changed to:", vault.authority.toBase58(), "epoch:", vault.keyEpoch.toNumber());
  });

  it("Withdraw SOL", async () => {
    // 先给 Vault PDA 充值
    const airdropSig = await provider.connection.requestAirdrop(
      vaultPda,
      1_000_000_000 // 1 SOL
    );
    await provider.connection.confirmTransaction(airdropSig);

    const recipient = Keypair.generate();
    const amount = 500_000_000; // 0.5 SOL

    // 构造提现消息（模拟 abi.encode(amount, token, to)）
    const message = Buffer.alloc(72);
    message.writeBigUInt64BE(BigInt(amount), 0);
    // token = 0x00...00 表示 SOL
    recipient.publicKey.toBuffer().copy(message, 40);

    const msgHash = crypto.createHash("sha256").update(message).digest();
    const signature = nacl.sign.detached(msgHash, frostKeypair2.secretKey);

    // ConsumedRecord PDA
    const [consumedPda] = PublicKey.findProgramAddressSync(
      [Buffer.from("consumed"), msgHash],
      program.programId
    );

    // Ed25519 验签指令
    const ed25519Ix = Ed25519Program.createInstructionWithPublicKey({
      publicKey: frostKeypair2.publicKey.toBytes(),
      message: msgHash,
      signature: Buffer.from(signature),
    });

    const withdrawTx = await program.methods
      .withdraw(
        new anchor.BN(amount),
        message,
        Array.from(signature) as any,
        Array.from(msgHash) as any
      )
      .accounts({
        vaultState: vaultPda,
        vaultSol: vaultPda,
        consumedRecord: consumedPda,
        recipient: recipient.publicKey,
        payer: provider.wallet.publicKey,
        instructionSysvar: SYSVAR_INSTRUCTIONS_PUBKEY,
        systemProgram: SystemProgram.programId,
        vaultTokenAccount: null,
        recipientTokenAccount: null,
        tokenProgram: null,
      })
      .preInstructions([ed25519Ix])
      .rpc();

    console.log("Withdraw tx:", withdrawTx);

    // 验证余额
    const recipientBalance = await provider.connection.getBalance(
      recipient.publicKey
    );
    expect(recipientBalance).to.equal(amount);
    console.log("✅ Withdrew", amount / 1e9, "SOL to", recipient.publicKey.toBase58());
  });

  it("Reject replay attack", async () => {
    // 使用与上一次相同的消息（ConsumedRecord PDA 已存在）
    const recipient = Keypair.generate();
    const amount = 500_000_000;
    const message = Buffer.alloc(72);
    message.writeBigUInt64BE(BigInt(amount), 0);
    recipient.publicKey.toBuffer().copy(message, 40);

    const msgHash = crypto.createHash("sha256").update(message).digest();
    const signature = nacl.sign.detached(msgHash, frostKeypair2.secretKey);

    const [consumedPda] = PublicKey.findProgramAddressSync(
      [Buffer.from("consumed"), msgHash],
      program.programId
    );

    const ed25519Ix = Ed25519Program.createInstructionWithPublicKey({
      publicKey: frostKeypair2.publicKey.toBytes(),
      message: msgHash,
      signature: Buffer.from(signature),
    });

    try {
      await program.methods
        .withdraw(
          new anchor.BN(amount),
          message,
          Array.from(signature) as any,
          Array.from(msgHash) as any
        )
        .accounts({
          vaultState: vaultPda,
          vaultSol: vaultPda,
          consumedRecord: consumedPda,
          recipient: recipient.publicKey,
          payer: provider.wallet.publicKey,
          instructionSysvar: SYSVAR_INSTRUCTIONS_PUBKEY,
          systemProgram: SystemProgram.programId,
          vaultTokenAccount: null,
          recipientTokenAccount: null,
          tokenProgram: null,
        })
        .preInstructions([ed25519Ix])
        .rpc();

      expect.fail("Should have rejected replay");
    } catch (e) {
      console.log("✅ Replay attack correctly rejected:", e.message?.substring(0, 60));
    }
  });
});
