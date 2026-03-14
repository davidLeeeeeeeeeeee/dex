// frost/runtime/roast/roast_solana_vectors_test.go
// 生成 FROST Ed25519 签名测试向量，供 Solana frost-vault 集成测试使用。
// 运行后将向量写入 solana/frost-vault/tests/frost_vectors.json

package roast

import (
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"dex/frost/core/curve"
	"dex/frost/core/dkg"
	coreRoast "dex/frost/core/roast"
)

// FROSTVector 单个 FROST 签名测试向量
type FROSTVector struct {
	Name      string `json:"name"`
	Pubkey    string `json:"pubkey"`     // 群公钥 (32 bytes hex)
	Message   string `json:"message"`    // 原始消息 (hex)
	MsgHash   string `json:"msg_hash"`   // sha256(message) (32 bytes hex)
	Signature string `json:"signature"`  // FROST 聚合签名 (64 bytes hex)
	// changePub 专用
	NewPubkey string `json:"new_pubkey,omitempty"` // 新公钥 (32 bytes hex)
	// withdraw 专用
	Amount    uint64 `json:"amount,omitempty"`
	Recipient string `json:"recipient,omitempty"` // 接收者地址 (32 bytes hex)
}

// FROSTVectors 测试向量集合
type FROSTVectors struct {
	Vectors []FROSTVector `json:"vectors"`
}

// TestGenerateFROSTVectorsForSolana 生成 FROST 签名测试向量写入 JSON 文件
func TestGenerateFROSTVectorsForSolana(t *testing.T) {
	group := curve.NewEd25519Group()

	// === 确定性 masterSecret 和参数 ===
	masterSecret := new(big.Int).SetBytes([]byte{
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
		0xfe, 0xdc, 0xba, 0x98, 0x76, 0x54, 0x32, 0x10,
	})
	threshold, numSigners := 3, 5
	signerIDs := []int{1, 2, 3, 4} // t+1 个签名者

	// DKG 份额
	groupPub := group.ScalarBaseMult(masterSecret)
	shares, _ := generateTestShares(masterSecret, threshold, numSigners, group)
	groupPubSerialized := group.SerializePoint(groupPub)

	t.Logf("FROST group pubkey: %s", hex.EncodeToString(groupPubSerialized))

	// === 生成第二把密钥（用于 changePub 的新公钥）===
	masterSecret2 := new(big.Int).SetBytes([]byte{
		0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x11, 0x22,
		0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0x00,
	})
	groupPub2 := group.ScalarBaseMult(masterSecret2)
	groupPub2Serialized := group.SerializePoint(groupPub2)

	var vectors []FROSTVector

	// === Vector 1: changePub ===
	// 消息 = sha256(new_pubkey_bytes)，签名者使用旧公钥签名
	changePubMsg := groupPub2Serialized // 新公钥原始字节
	changePubMsgHash := sha256.Sum256(changePubMsg)

	changePubSig := generateFROSTSignature(t, group, signerIDs, shares, groupPub, groupPubSerialized, changePubMsgHash[:])
	// 验证 Go 端签名可以通过
	if !ed25519.Verify(groupPubSerialized, changePubMsgHash[:], changePubSig) {
		t.Fatal("❌ changePub FROST 签名 Go 端验证失败")
	}
	t.Logf("✅ changePub FROST 签名 Go 端验证通过")

	vectors = append(vectors, FROSTVector{
		Name:      "changePub",
		Pubkey:    hex.EncodeToString(groupPubSerialized),
		Message:   hex.EncodeToString(changePubMsg),
		MsgHash:   hex.EncodeToString(changePubMsgHash[:]),
		Signature: hex.EncodeToString(changePubSig),
		NewPubkey: hex.EncodeToString(groupPub2Serialized),
	})

	// === Vector 2: withdraw ===
	// 构造提现消息：amount(8 bytes BE) + zeros(32 bytes token) + recipient(32 bytes)
	amount := uint64(500_000_000)                    // 0.5 SOL
	recipientPub := groupPub2Serialized              // 用第二把公钥作为 recipient
	withdrawMsg := buildWithdrawMessage(amount, recipientPub)
	withdrawMsgHash := sha256.Sum256(withdrawMsg)

	withdrawSig := generateFROSTSignature(t, group, signerIDs, shares, groupPub, groupPubSerialized, withdrawMsgHash[:])
	if !ed25519.Verify(groupPubSerialized, withdrawMsgHash[:], withdrawSig) {
		t.Fatal("❌ withdraw FROST 签名 Go 端验证失败")
	}
	t.Logf("✅ withdraw FROST 签名 Go 端验证通过")

	vectors = append(vectors, FROSTVector{
		Name:      "withdraw",
		Pubkey:    hex.EncodeToString(groupPubSerialized),
		Message:   hex.EncodeToString(withdrawMsg),
		MsgHash:   hex.EncodeToString(withdrawMsgHash[:]),
		Signature: hex.EncodeToString(withdrawSig),
		Amount:    amount,
		Recipient: hex.EncodeToString(recipientPub),
	})

	// === 写入 JSON 文件 ===
	output := FROSTVectors{Vectors: vectors}
	jsonData, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		t.Fatalf("JSON marshal failed: %v", err)
	}

	// 定位 solana/frost-vault/tests/ 目录
	_, thisFile, _, _ := runtime.Caller(0)
	projectRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	outputPath := filepath.Join(projectRoot, "solana", "frost-vault", "tests", "frost_vectors.json")

	if err := os.WriteFile(outputPath, jsonData, 0644); err != nil {
		t.Fatalf("写入 %s 失败: %v", outputPath, err)
	}
	t.Logf("✅ 测试向量已写入: %s", outputPath)
	t.Logf("JSON:\n%s", string(jsonData))
}

// generateFROSTSignature 用 FROST 门限签名流程生成 Ed25519 签名
func generateFROSTSignature(
	t *testing.T, group curve.Group,
	signerIDs []int, shares map[int]*big.Int,
	groupPub curve.Point, groupPubSerialized []byte,
	msgHash []byte,
) []byte {
	t.Helper()

	// 生成 nonce 对
	type nonceData struct {
		hidingK, bindingK   *big.Int
		hidingPt, bindingPt curve.Point
	}
	signerNonces := make(map[int]*nonceData)
	for _, sid := range signerIDs {
		hk := dkg.RandomScalar(group.Order())
		bk := dkg.RandomScalar(group.Order())
		hp := group.ScalarBaseMult(hk)
		bp := group.ScalarBaseMult(bk)
		signerNonces[sid] = &nonceData{hk, bk, hp, bp}
	}

	// 群承诺 R
	coreNonces := make([]coreRoast.SignerNonce, len(signerIDs))
	for i, sid := range signerIDs {
		n := signerNonces[sid]
		coreNonces[i] = coreRoast.SignerNonce{
			SignerID: sid, HidingNonce: n.hidingK, BindingNonce: n.bindingK,
			HidingPoint: n.hidingPt, BindingPoint: n.bindingPt,
		}
	}

	R, err := coreRoast.ComputeGroupCommitment(coreNonces, msgHash, group)
	if err != nil {
		t.Fatalf("ComputeGroupCommitment failed: %v", err)
	}
	rSerialized := group.SerializePoint(R)

	// Challenge: SHA-512(R || pk || msg) mod L
	e := computeEd25519Challenge(rSerialized, groupPubSerialized, msgHash, group.Order())
	lambdas := coreRoast.ComputeLagrangeCoefficientsForSet(signerIDs, group.Order())

	// Partial signatures
	partialSigs := make(map[int]*big.Int)
	for _, sid := range signerIDs {
		n := signerNonces[sid]
		rho := coreRoast.ComputeBindingCoefficient(sid, msgHash, coreNonces, group)
		z := coreRoast.ComputePartialSignature(sid,
			n.hidingK, n.bindingK, shares[sid],
			rho, lambdas[sid], e, group)
		partialSigs[sid] = z
	}

	// 聚合
	zAgg := big.NewInt(0)
	for _, sid := range signerIDs {
		zAgg.Add(zAgg, partialSigs[sid])
	}
	zAgg.Mod(zAgg, group.Order())

	// 签名: R(32) || z_le(32)
	sig := make([]byte, 64)
	copy(sig[:32], rSerialized)
	zBytes := make([]byte, 32)
	zAgg.FillBytes(zBytes)
	copy(sig[32:], reverseBytes(zBytes))

	return sig
}

// computeEd25519Challenge 计算 Ed25519 Schnorr challenge
func computeEd25519Challenge(rCompressed, pkCompressed, msg []byte, order *big.Int) *big.Int {
	h := crypto.SHA512.New()
	h.Write(rCompressed)
	h.Write(pkCompressed)
	h.Write(msg)
	digest := h.Sum(nil)
	e := new(big.Int).SetBytes(reverseBytes(digest))
	return e.Mod(e, order)
}

// buildWithdrawMessage 构造提现消息（与 TS 测试一致）
// 格式: amount(8 bytes BE) + zeros(32 bytes) + recipient(32 bytes) = 72 bytes
func buildWithdrawMessage(amount uint64, recipient []byte) []byte {
	msg := make([]byte, 72)
	// amount BE 8 bytes
	msg[0] = byte(amount >> 56)
	msg[1] = byte(amount >> 48)
	msg[2] = byte(amount >> 40)
	msg[3] = byte(amount >> 32)
	msg[4] = byte(amount >> 24)
	msg[5] = byte(amount >> 16)
	msg[6] = byte(amount >> 8)
	msg[7] = byte(amount)
	// bytes 8..39 = zeros (token = SOL)
	// bytes 40..71 = recipient
	copy(msg[40:], recipient[:32])
	return msg
}
