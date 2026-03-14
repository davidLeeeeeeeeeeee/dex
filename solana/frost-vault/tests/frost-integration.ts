// solana/frost-vault/tests/frost-integration.ts
//
// Go↔Solana FROST 签名跨语言集成测试
// 读取 Go 端生成的 FROST Ed25519 门限签名向量（frost_vectors.json），
// 在 Solana frost-vault 合约中验证签名有效性。
//
// 运行前先执行 Go 端生成向量:
//   cd <project_root> && go test -v -run TestGenerateFROSTVectorsForSolana ./frost/runtime/roast/ -count=1
//
// 然后运行:
//   cd solana/frost-vault && anchor test

import * as anchor from "@coral-xyz/anchor";
import { Program } from "@coral-xyz/anchor";
import { FrostVault } from "../target/types/frost_vault";
import {
  Ed25519Program,
  Keypair,
  PublicKey,
  SystemProgram,
  SYSVAR_INSTRUCTIONS_PUBKEY,
} from "@solana/web3.js";
import { expect } from "chai";
import * as fs from "fs";
import * as path from "path";

interface FROSTVector {
  name: string;
  pubkey: string;
  message: string;
  msg_hash: string;
  signature: string;
  new_pubkey?: string;
  amount?: number;
  recipient?: string;
}

interface FROSTVectors {
  vectors: FROSTVector[];
}

describe("frost-vault FROST integration", () => {
  const provider = anchor.AnchorProvider.env();
  anchor.setProvider(provider);
  const program = anchor.workspace.FrostVault as Program<FrostVault>;

  // 读取 Go 生成的测试向量
  const vectorsPath = path.join(__dirname, "frost_vectors.json");
  let vectors: FROSTVectors;
  let changePubVector: FROSTVector;
  let withdrawVector: FROSTVector;

  // 使用独立的 vault ID 避免与其他测试冲突
  const vaultId = 42;

  let vaultPda: PublicKey;

  before(() => {
    if (!fs.existsSync(vectorsPath)) {
      throw new Error(
        `frost_vectors.json not found. Run Go test first:\n` +
          `  cd <project_root> && go test -v -run TestGenerateFROSTVectorsForSolana ./frost/runtime/roast/ -count=1`
      );
    }
    vectors = JSON.parse(fs.readFileSync(vectorsPath, "utf-8"));
    changePubVector = vectors.vectors.find((v) => v.name === "changePub")!;
    withdrawVector = vectors.vectors.find((v) => v.name === "withdraw")!;

    if (!changePubVector || !withdrawVector) {
      throw new Error("Missing changePub or withdraw vector in frost_vectors.json");
    }

    // 计算 VaultState PDA
    [vaultPda] = PublicKey.findProgramAddressSync(
      [
        Buffer.from("frost_vault"),
        Buffer.from(new Uint32Array([vaultId]).buffer),
      ],
      program.programId
    );

    console.log("=== FROST Integration Test Vectors ===");
    console.log("  FROST group pubkey:", changePubVector.pubkey);
    console.log("  New pubkey:", changePubVector.new_pubkey);
    console.log("  Vault PDA:", vaultPda.toBase58());
  });

  it("Initialize vault with FROST group pubkey", async () => {
    // 用 FROST 群公钥初始化 Vault
    const frostPubkey = new PublicKey(
      Buffer.from(changePubVector.pubkey, "hex")
    );

    const tx = await program.methods
      .initialize(vaultId, frostPubkey)
      .accounts({
        payer: provider.wallet.publicKey,
        vaultState: vaultPda,
        systemProgram: SystemProgram.programId,
      })
      .rpc();

    console.log("Initialize tx:", tx);

    const vault = await program.account.vaultState.fetch(vaultPda);
    expect(vault.authority.toBase58()).to.equal(frostPubkey.toBase58());
    expect(vault.vaultId).to.equal(vaultId);
    expect(vault.keyEpoch.toNumber()).to.equal(1);
    console.log(
      "✅ Vault initialized with FROST group pubkey:",
      vault.authority.toBase58()
    );
  });

  it("ChangePub with FROST threshold signature", async () => {
    const frostPubkeyBytes = Buffer.from(changePubVector.pubkey, "hex");
    const msgHash = Buffer.from(changePubVector.msg_hash, "hex");
    const signature = Buffer.from(changePubVector.signature, "hex");
    const newPubkey = new PublicKey(
      Buffer.from(changePubVector.new_pubkey!, "hex")
    );

    // 构建 Ed25519SigVerify 预编译指令
    const ed25519Ix = Ed25519Program.createInstructionWithPublicKey({
      publicKey: frostPubkeyBytes,
      message: msgHash,
      signature: signature,
    });

    const tx = await program.methods
      .changePub(newPubkey, Array.from(signature) as any)
      .accounts({
        vaultState: vaultPda,
        instructionSysvar: SYSVAR_INSTRUCTIONS_PUBKEY,
      })
      .preInstructions([ed25519Ix])
      .rpc();

    console.log("ChangePub tx:", tx);

    const vault = await program.account.vaultState.fetch(vaultPda);
    expect(vault.authority.toBase58()).to.equal(newPubkey.toBase58());
    expect(vault.keyEpoch.toNumber()).to.equal(2);
    console.log(
      "✅ FROST changePub succeeded! New authority:",
      vault.authority.toBase58(),
      "epoch:",
      vault.keyEpoch.toNumber()
    );
  });

  it("Withdraw SOL with FROST threshold signature", async () => {
    // 先给 Vault PDA 充值
    const airdropSig = await provider.connection.requestAirdrop(
      vaultPda,
      2_000_000_000 // 2 SOL
    );
    await provider.connection.confirmTransaction(airdropSig);

    // 注意：withdraw 向量是用**旧公钥**签名的，但 changePub 已经切到新公钥。
    // 所以我们需要用 withdrawVector 中的 pubkey 来验证——
    // 但合约验签时对比的是 vault.authority（已经是新公钥）。
    // 因此这里我们重新初始化一个新 vault 来测试 withdraw。
    const withdrawVaultId = 43;
    const [withdrawVaultPda] = PublicKey.findProgramAddressSync(
      [
        Buffer.from("frost_vault"),
        Buffer.from(new Uint32Array([withdrawVaultId]).buffer),
      ],
      program.programId
    );

    const frostPubkey = new PublicKey(
      Buffer.from(withdrawVector.pubkey, "hex")
    );

    // 初始化 withdraw 专用 vault
    await program.methods
      .initialize(withdrawVaultId, frostPubkey)
      .accounts({
        payer: provider.wallet.publicKey,
        vaultState: withdrawVaultPda,
        systemProgram: SystemProgram.programId,
      })
      .rpc();

    // 给 withdraw vault 充值
    const airdropSig2 = await provider.connection.requestAirdrop(
      withdrawVaultPda,
      2_000_000_000
    );
    await provider.connection.confirmTransaction(airdropSig2);

    const frostPubkeyBytes = Buffer.from(withdrawVector.pubkey, "hex");
    const message = Buffer.from(withdrawVector.message, "hex");
    const msgHash = Buffer.from(withdrawVector.msg_hash, "hex");
    const signature = Buffer.from(withdrawVector.signature, "hex");
    const amount = withdrawVector.amount!;
    const recipient = new PublicKey(
      Buffer.from(withdrawVector.recipient!, "hex")
    );

    // ConsumedRecord PDA
    const [consumedPda] = PublicKey.findProgramAddressSync(
      [Buffer.from("consumed"), msgHash],
      program.programId
    );

    // Ed25519SigVerify 预编译指令
    const ed25519Ix = Ed25519Program.createInstructionWithPublicKey({
      publicKey: frostPubkeyBytes,
      message: msgHash,
      signature: signature,
    });

    const tx = await program.methods
      .withdraw(
        new anchor.BN(amount),
        message,
        Array.from(signature) as any,
        Array.from(msgHash) as any
      )
      .accounts({
        vaultState: withdrawVaultPda,
        vaultSol: withdrawVaultPda,
        consumedRecord: consumedPda,
        recipient: recipient,
        payer: provider.wallet.publicKey,
        instructionSysvar: SYSVAR_INSTRUCTIONS_PUBKEY,
        systemProgram: SystemProgram.programId,
        vaultTokenAccount: null,
        recipientTokenAccount: null,
        tokenProgram: null,
      })
      .preInstructions([ed25519Ix])
      .rpc();

    console.log("Withdraw tx:", tx);

    // 验证余额
    const recipientBalance = await provider.connection.getBalance(recipient);
    expect(recipientBalance).to.equal(amount);
    console.log(
      "✅ FROST withdraw succeeded!",
      amount / 1e9,
      "SOL to",
      recipient.toBase58()
    );
  });
});
