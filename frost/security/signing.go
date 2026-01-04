// frost/security/signing.go
// 消息签名与验证

package security

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"dex/pb"
	"fmt"
	"math/big"
)

// SignMessage 对消息进行签名
func SignMessage(privateKey *ecdsa.PrivateKey, msg []byte) ([]byte, error) {
	hash := sha256.Sum256(msg)

	r, s, err := ecdsa.Sign(nil, privateKey, hash[:])
	if err != nil {
		return nil, fmt.Errorf("sign failed: %w", err)
	}

	// 将 r, s 编码为 64 字节签名 (32 + 32)
	sig := make([]byte, 64)
	rBytes := r.Bytes()
	sBytes := s.Bytes()
	copy(sig[32-len(rBytes):32], rBytes)
	copy(sig[64-len(sBytes):64], sBytes)

	return sig, nil
}

// VerifyMessage 验证消息签名
func VerifyMessage(publicKey *ecdsa.PublicKey, msg, sig []byte) bool {
	if len(sig) != 64 {
		return false
	}

	hash := sha256.Sum256(msg)

	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])

	return ecdsa.Verify(publicKey, hash[:], r, s)
}

// SignFrostEnvelope 对 FrostEnvelope 进行签名
func SignFrostEnvelope(privateKey *ecdsa.PrivateKey, env *pb.FrostEnvelope) error {
	// 清空签名字段
	env.Sig = nil

	// 序列化消息（不含签名）
	msgToSign := serializeEnvelopeForSigning(env)

	sig, err := SignMessage(privateKey, msgToSign)
	if err != nil {
		return err
	}

	env.Sig = sig
	return nil
}

// VerifyFrostEnvelope 验证 FrostEnvelope 签名
func VerifyFrostEnvelope(publicKey *ecdsa.PublicKey, env *pb.FrostEnvelope) bool {
	if len(env.Sig) == 0 {
		return false
	}

	// 保存签名
	sig := env.Sig
	env.Sig = nil

	// 序列化消息（不含签名）
	msgToSign := serializeEnvelopeForSigning(env)

	// 恢复签名
	env.Sig = sig

	return VerifyMessage(publicKey, msgToSign, sig)
}

// serializeEnvelopeForSigning 序列化 envelope 用于签名
func serializeEnvelopeForSigning(env *pb.FrostEnvelope) []byte {
	h := sha256.New()
	h.Write([]byte(env.From))
	h.Write([]byte(env.To))
	h.Write([]byte(fmt.Sprintf("%d", env.Kind)))
	h.Write(env.Payload)
	h.Write([]byte(env.JobId))
	h.Write([]byte(fmt.Sprintf("%d", env.Seq)))
	return h.Sum(nil)
}

// BindTemplateHash 绑定 template_hash 到签名请求
func BindTemplateHash(jobID string, templateHash []byte) []byte {
	h := sha256.New()
	h.Write([]byte("frost_template_binding"))
	h.Write([]byte(jobID))
	h.Write(templateHash)
	return h.Sum(nil)
}

// VerifyTemplateBinding 验证 template_hash 绑定
func VerifyTemplateBinding(jobID string, expectedHash, actualHash []byte) bool {
	expected := BindTemplateHash(jobID, expectedHash)
	actual := BindTemplateHash(jobID, actualHash)

	if len(expected) != len(actual) {
		return false
	}

	for i := range expected {
		if expected[i] != actual[i] {
			return false
		}
	}
	return true
}
