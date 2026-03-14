// frost/runtime/roast/roast_solidity_vectors_test.go
// 生成 FROST BN128 签名测试向量，供 Solidity schnorr_verify.sol 集成测试使用。
// 运行后将向量写入 solidity/test/bn128_frost_vectors.json

package roast

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"dex/frost/core/curve"
	"dex/frost/core/dkg"
	frost "dex/frost/core/frost"
	coreRoast "dex/frost/core/roast"

	"golang.org/x/crypto/sha3"
)

// BN128FROSTVector 单个 BN128 FROST 签名测试向量
type BN128FROSTVector struct {
	Name string `json:"name"`
	Px   string `json:"px"` // 群公钥 X 坐标（32 bytes hex, big-endian）
	Py   string `json:"py"` // 群公钥 Y 坐标
	Rx   string `json:"rx"` // R 点 X 坐标
	Ry   string `json:"ry"` // R 点 Y 坐标
	S    string `json:"s"`  // 标量 s（32 bytes hex, big-endian）
	Msg  string `json:"msg"`      // 原始消息 hex
	MsgHash string `json:"msg_hash"` // keccak256(msg) hex
	// changePub 专用
	NewPx string `json:"new_px,omitempty"`
	NewPy string `json:"new_py,omitempty"`
	// withdraw 专用
	Amount    string `json:"amount,omitempty"`
	Token     string `json:"token,omitempty"`
	Recipient string `json:"recipient,omitempty"`
}

type BN128FROSTVectors struct {
	Vectors []BN128FROSTVector `json:"vectors"`
}

// TestGenerateFROSTVectorsForSolidity 生成 FROST BN128 签名测试向量写入 JSON
func TestGenerateFROSTVectorsForSolidity(t *testing.T) {
	group := curve.NewBN256Group()

	masterSecret := new(big.Int).SetBytes([]byte{
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
		0xfe, 0xdc, 0xba, 0x98, 0x76, 0x54, 0x32, 0x10,
	})
	threshold, numSigners := 3, 5
	signerIDs := []int{1, 2, 3, 4}

	groupPub := group.ScalarBaseMult(masterSecret)
	shares, _ := generateTestShares(masterSecret, threshold, numSigners, group)

	t.Logf("BN128 group pubkey: Px=%s Py=%s", pad32Hex(groupPub.X), pad32Hex(groupPub.Y))

	// 第二把密钥（用于 changePub）
	masterSecret2 := new(big.Int).SetBytes([]byte{
		0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x11, 0x22,
		0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0x00,
	})
	groupPub2 := group.ScalarBaseMult(masterSecret2)

	var vectors []BN128FROSTVector

	// === Vector 1: verifySignature（纯签名验证）===
	// Solidity verify(P, R, s, mHash) 计算 challenge = keccak256(Rx||Ry||Px||Py||mHash)
	// 所以签名时必须对 keccak256(msg) 签名
	plainMsg := []byte("FROST BN128 signature test for Solidity")
	msgHash := keccak256(plainMsg)
	R1, s1 := generateBN128FROSTSignatureForHash(t, group, signerIDs, shares, groupPub, msgHash)

	// Go 端验证
	pubKey := make([]byte, 64)
	groupPub.X.FillBytes(pubKey[:32])
	groupPub.Y.FillBytes(pubKey[32:])
	sig1 := make([]byte, 64)
	R1.X.FillBytes(sig1[:32])
	s1.FillBytes(sig1[32:])
	valid, err := frost.VerifyBN128(pubKey, msgHash, sig1)
	if err != nil || !valid {
		t.Fatalf("❌ verifySignature Go 端验证失败: valid=%v err=%v", valid, err)
	}
	t.Logf("✅ verifySignature Go 端验证通过")

	vectors = append(vectors, BN128FROSTVector{
		Name: "verifySignature",
		Px: pad32Hex(groupPub.X), Py: pad32Hex(groupPub.Y),
		Rx: pad32Hex(R1.X), Ry: pad32Hex(R1.Y),
		S:   pad32Hex(s1),
		Msg: hex.EncodeToString(plainMsg), MsgHash: hex.EncodeToString(msgHash),
	})

	// === Vector 2: changePub ===
	// Solidity: hPub = keccak256(abi.encodePacked(newPx, newPy))
	changePubPayload := make([]byte, 64)
	groupPub2.X.FillBytes(changePubPayload[:32])
	groupPub2.Y.FillBytes(changePubPayload[32:])
	changePubHash := keccak256(changePubPayload)

	R2, s2 := generateBN128FROSTSignatureForHash(t, group, signerIDs, shares, groupPub, changePubHash)
	sig2 := make([]byte, 64)
	R2.X.FillBytes(sig2[:32])
	s2.FillBytes(sig2[32:])
	valid2, err2 := frost.VerifyBN128(pubKey, changePubHash, sig2)
	if err2 != nil || !valid2 {
		t.Fatalf("❌ changePub Go 端验证失败: valid=%v err=%v", valid2, err2)
	}
	t.Logf("✅ changePub Go 端验证通过")

	vectors = append(vectors, BN128FROSTVector{
		Name: "changePub",
		Px: pad32Hex(groupPub.X), Py: pad32Hex(groupPub.Y),
		Rx: pad32Hex(R2.X), Ry: pad32Hex(R2.Y),
		S:   pad32Hex(s2),
		Msg: hex.EncodeToString(changePubPayload), MsgHash: hex.EncodeToString(changePubHash),
		NewPx: pad32Hex(groupPub2.X), NewPy: pad32Hex(groupPub2.Y),
	})

	// === Vector 3: withdraw ===
	// Solidity: message = abi.encode(amount, token, to)
	amount := new(big.Int).SetUint64(1_000_000_000_000_000_000) // 1 ETH in wei
	token := big.NewInt(0) // address(0) = ETH
	recipient := new(big.Int).SetBytes([]byte{0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42})
	// abi.encode packs each to 32 bytes
	withdrawMsg := make([]byte, 96)
	amount.FillBytes(withdrawMsg[:32])
	token.FillBytes(withdrawMsg[32:64])
	recipient.FillBytes(withdrawMsg[64:96])
	withdrawHash := keccak256(withdrawMsg)

	R3, s3 := generateBN128FROSTSignatureForHash(t, group, signerIDs, shares, groupPub, withdrawHash)
	sig3 := make([]byte, 64)
	R3.X.FillBytes(sig3[:32])
	s3.FillBytes(sig3[32:])
	valid3, err3 := frost.VerifyBN128(pubKey, withdrawHash, sig3)
	if err3 != nil || !valid3 {
		t.Fatalf("❌ withdraw Go 端验证失败: valid=%v err=%v", valid3, err3)
	}
	t.Logf("✅ withdraw Go 端验证通过")

	vectors = append(vectors, BN128FROSTVector{
		Name: "withdraw",
		Px: pad32Hex(groupPub.X), Py: pad32Hex(groupPub.Y),
		Rx: pad32Hex(R3.X), Ry: pad32Hex(R3.Y),
		S:   pad32Hex(s3),
		Msg: hex.EncodeToString(withdrawMsg), MsgHash: hex.EncodeToString(withdrawHash),
		Amount:    amount.String(),
		Token:     "0x0000000000000000000000000000000000000000",
		Recipient: "0x4242424242424242424242424242424242424242",
	})

	// 写入 JSON
	output := BN128FROSTVectors{Vectors: vectors}
	jsonData, _ := json.MarshalIndent(output, "", "  ")

	_, thisFile, _, _ := runtime.Caller(0)
	projectRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	outputPath := filepath.Join(projectRoot, "solidity", "test", "bn128_frost_vectors.json")
	os.MkdirAll(filepath.Dir(outputPath), 0755)

	if err := os.WriteFile(outputPath, jsonData, 0644); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	t.Logf("✅ 测试向量已写入: %s", outputPath)
}

// generateBN128FROSTSignature 生成对原始消息的 FROST BN128 签名
func generateBN128FROSTSignature(
	t *testing.T, group curve.Group,
	signerIDs []int, shares map[int]*big.Int,
	groupPub curve.Point, msg []byte,
) (curve.Point, *big.Int) {
	t.Helper()
	// 生成 nonce
	type nd struct { hk, bk *big.Int; hp, bp curve.Point }
	nonces := make(map[int]*nd)
	for _, sid := range signerIDs {
		hk, bk := dkg.RandomScalar(group.Order()), dkg.RandomScalar(group.Order())
		nonces[sid] = &nd{hk, bk, group.ScalarBaseMult(hk), group.ScalarBaseMult(bk)}
	}

	coreNonces := make([]coreRoast.SignerNonce, len(signerIDs))
	for i, sid := range signerIDs {
		n := nonces[sid]
		coreNonces[i] = coreRoast.SignerNonce{
			SignerID: sid, HidingNonce: n.hk, BindingNonce: n.bk,
			HidingPoint: n.hp, BindingPoint: n.bp,
		}
	}

	R, _ := coreRoast.ComputeGroupCommitment(coreNonces, msg, group)
	e := computeBN128ChallengeForTest(R.X, R.Y, groupPub.X, groupPub.Y, msg, group.Order())
	lambdas := coreRoast.ComputeLagrangeCoefficientsForSet(signerIDs, group.Order())

	zAgg := big.NewInt(0)
	for _, sid := range signerIDs {
		n := nonces[sid]
		rho := coreRoast.ComputeBindingCoefficient(sid, msg, coreNonces, group)
		z := coreRoast.ComputePartialSignature(sid, n.hk, n.bk, shares[sid], rho, lambdas[sid], e, group)
		zAgg.Add(zAgg, z)
	}
	zAgg.Mod(zAgg, group.Order())
	return R, zAgg
}

// generateBN128FROSTSignatureForHash 生成对已哈希消息的 FROST BN128 签名
// Solidity 合约的 verifySignature 接收 messageHash（32 bytes），
// challenge = keccak256(Rx||Ry||Px||Py||messageHash)
// 所以签名时 msg 参数直接传 hash（32 bytes）
func generateBN128FROSTSignatureForHash(
	t *testing.T, group curve.Group,
	signerIDs []int, shares map[int]*big.Int,
	groupPub curve.Point, msgHash []byte,
) (curve.Point, *big.Int) {
	return generateBN128FROSTSignature(t, group, signerIDs, shares, groupPub, msgHash)
}

func keccak256(data []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)
	return h.Sum(nil)
}
