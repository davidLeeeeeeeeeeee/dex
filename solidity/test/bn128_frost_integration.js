// solidity/test/bn128_frost_integration.js
//
// Go↔Solidity FROST BN128 签名跨语言集成测试
// 读取 Go 端生成的 FROST alt_bn128 签名向量，
// 在 SchnorrExample 合约中验证签名。
//
// 运行:
//   cd solidity && npx hardhat test

const { expect } = require("chai");
const { ethers } = require("hardhat");
const fs = require("fs");
const path = require("path");

describe("SchnorrExample FROST BN128 Integration", function () {
  let contract;
  let vectors;

  before(async function () {
    const vectorsPath = path.join(__dirname, "bn128_frost_vectors.json");
    if (!fs.existsSync(vectorsPath)) {
      throw new Error(
        "bn128_frost_vectors.json not found. Run Go test first:\n" +
          "  cd <project_root> && go test -v -run TestGenerateFROSTVectorsForSolidity ./frost/runtime/roast/ -count=1"
      );
    }
    vectors = JSON.parse(fs.readFileSync(vectorsPath, "utf-8")).vectors;

    // 用第一个向量的公钥部署合约
    const v = vectors[0];
    const SchnorrExample = await ethers.getContractFactory("SchnorrExample");
    contract = await SchnorrExample.deploy(
      "0x" + v.px,
      "0x" + v.py
    );
    await contract.waitForDeployment();

    console.log("SchnorrExample deployed at:", await contract.getAddress());
    console.log("Initial pubkey: Px=" + v.px + " Py=" + v.py);
  });

  it("verifySignature: FROST BN128 threshold signature", async function () {
    const v = vectors.find((x) => x.name === "verifySignature");
    expect(v).to.not.be.undefined;

    const result = await contract.verifySignature(
      "0x" + v.px,
      "0x" + v.py,
      "0x" + v.rx,
      "0x" + v.ry,
      "0x" + v.s,
      "0x" + v.msg_hash
    );

    console.log("  verifySignature result:", result);
    expect(result).to.be.true;
    console.log("  ✅ FROST BN128 签名在 Solidity 合约中验证通过!");
  });

  it("changePub: FROST BN128 key rotation", async function () {
    const v = vectors.find((x) => x.name === "changePub");
    expect(v).to.not.be.undefined;

    // 调用 changePub，使用 FROST 签名进行密钥轮换
    const tx = await contract.changePub(
      "0x" + v.new_px,
      "0x" + v.new_py,
      "0x" + v.rx,
      "0x" + v.ry,
      "0x" + v.s
    );
    await tx.wait();

    // 验证公钥已更新
    const newP = await contract.P();
    expect(newP[0].toString(16)).to.equal(BigInt("0x" + v.new_px).toString(16));
    expect(newP[1].toString(16)).to.equal(BigInt("0x" + v.new_py).toString(16));
    console.log("  ✅ FROST changePub 在 Solidity 合约中执行成功!");
  });

  it("withdraw: FROST BN128 ETH withdrawal", async function () {
    const v = vectors.find((x) => x.name === "withdraw");
    expect(v).to.not.be.undefined;

    // withdraw 使用的签名公钥是第一把（未经 changePub 切换的公钥）
    // 所以需要重新部署一个合约来测试 withdraw
    const SchnorrExample = await ethers.getContractFactory("SchnorrExample");
    const withdrawContract = await SchnorrExample.deploy(
      "0x" + v.px,
      "0x" + v.py
    );
    await withdrawContract.waitForDeployment();

    // 给合约充值 2 ETH
    const [signer] = await ethers.getSigners();
    await signer.sendTransaction({
      to: await withdrawContract.getAddress(),
      value: ethers.parseEther("2"),
    });

    const recipientAddr = v.recipient;
    const recipientBalanceBefore = await ethers.provider.getBalance(recipientAddr);

    // 构造 message = abi.encode(amount, token, to)
    const message = "0x" + v.msg;

    const tx = await withdrawContract.withdraw(
      message,
      "0x" + v.rx,
      "0x" + v.ry,
      "0x" + v.s
    );
    await tx.wait();

    const recipientBalanceAfter = await ethers.provider.getBalance(recipientAddr);
    const diff = recipientBalanceAfter - recipientBalanceBefore;
    expect(diff.toString()).to.equal(v.amount);
    console.log("  ✅ FROST withdraw 在 Solidity 合约中执行成功! 转账", ethers.formatEther(diff), "ETH");
  });
});
