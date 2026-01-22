package utils

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"dex/types"
	"encoding/binary"
	"fmt"

	"go.dedis.ch/kyber/v3"
	"go.dedis.ch/kyber/v3/pairing/bn256"
	"go.dedis.ch/kyber/v3/sign/bls"
)

// VRFProvider VRF提供者实现
type VRFProvider struct{}

// NewVRFProvider 创建VRF提供者
func NewVRFProvider() *VRFProvider {
	return &VRFProvider{}
}

// GenerateVRF 生成VRF证明和输出
// 基于BLS签名实现VRF功能
func (v *VRFProvider) GenerateVRF(privateKey *ecdsa.PrivateKey, height uint64, window int, parentBlockHash string, nodeID types.NodeID) ([]byte, []byte, error) {
	if privateKey == nil {
		return nil, nil, fmt.Errorf("private key is nil")
	}

	// 1. 构造VRF输入
	vrfInput := v.constructVRFInput(height, window, parentBlockHash, nodeID)

	// 2. 使用BLS签名作为VRF
	suite := bn256.NewSuite()

	// 将ECDSA私钥转换为BLS私钥
	blsPrivateKey, err := GetBLSPrivateKey(privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get BLS private key: %w", err)
	}

	// 3. 生成BLS签名（作为VRF证明）
	vrfProof, err := bls.Sign(suite, blsPrivateKey, vrfInput)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate VRF proof: %w", err)
	}

	// 4. 从证明中派生VRF输出（对证明进行哈希）
	vrfOutput := sha256.Sum256(vrfProof)

	return vrfOutput[:], vrfProof, nil
}

// VerifyVRF 验证VRF证明
// 注意：这个方法需要BLS公钥，而不是ECDSA公钥
// 在实际使用中，应该从区块或矿工信息中获取已序列化的BLS公钥
func (v *VRFProvider) VerifyVRF(publicKey *ecdsa.PublicKey, height uint64, window int, parentBlockHash string, nodeID types.NodeID, proof []byte, output []byte) error {
	if publicKey == nil {
		return fmt.Errorf("public key is nil")
	}

	// 1. 构造VRF输入（必须与生成时一致）
	vrfInput := v.constructVRFInput(height, window, parentBlockHash, nodeID)

	// 2. 从ECDSA公钥派生BLS公钥（使用相同的哈希方法）
	// 注意：这种方法只在私钥所有者生成BLS公钥时使用相同的派生方法才有效
	// 实际应用中，应该在链上存储BLS公钥
	pubKeyBytes := append(publicKey.X.Bytes(), publicKey.Y.Bytes()...)
	hash := sha256.Sum256(pubKeyBytes)

	suite := bn256.NewSuite()
	blsPrivScalar := suite.G2().Scalar().SetBytes(hash[:])
	blsPublicKey := suite.G2().Point().Mul(blsPrivScalar, nil)

	// 3. 验证BLS签名
	err := bls.Verify(suite, blsPublicKey, vrfInput, proof)
	if err != nil {
		return fmt.Errorf("VRF proof verification failed: %w", err)
	}

	// 4. 验证输出是否正确（输出应该是证明的哈希）
	expectedOutput := sha256.Sum256(proof)
	if len(output) != len(expectedOutput) {
		return fmt.Errorf("VRF output length mismatch")
	}
	for i := range output {
		if output[i] != expectedOutput[i] {
			return fmt.Errorf("VRF output mismatch")
		}
	}

	return nil
}

// VerifyVRFWithBLSPublicKey 使用BLS公钥验证VRF证明
// 这是推荐的验证方法，因为它直接使用BLS公钥
func (v *VRFProvider) VerifyVRFWithBLSPublicKey(blsPublicKey kyber.Point, height uint64, window int, parentBlockHash string, nodeID types.NodeID, proof []byte, output []byte) error {
	// 1. 构造VRF输入
	vrfInput := v.constructVRFInput(height, window, parentBlockHash, nodeID)

	// 2. 验证BLS签名
	suite := bn256.NewSuite()
	err := bls.Verify(suite, blsPublicKey, vrfInput, proof)
	if err != nil {
		return fmt.Errorf("VRF proof verification failed: %w", err)
	}

	// 3. 验证输出是否正确
	expectedOutput := sha256.Sum256(proof)
	if len(output) != len(expectedOutput) {
		return fmt.Errorf("VRF output length mismatch")
	}
	for i := range output {
		if output[i] != expectedOutput[i] {
			return fmt.Errorf("VRF output mismatch")
		}
	}

	return nil
}

// constructVRFInput 构造VRF输入
// 输入格式: Hash(height || window || parentBlockHash || nodeID)
func (v *VRFProvider) constructVRFInput(height uint64, window int, parentBlockHash string, nodeID types.NodeID) []byte {
	hasher := sha256.New()

	// 写入高度
	heightBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(heightBytes, height)
	hasher.Write(heightBytes)

	// 写入window
	windowBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(windowBytes, uint32(window))
	hasher.Write(windowBytes)

	// 写入父区块哈希
	hasher.Write([]byte(parentBlockHash))

	// 写入节点ID
	hasher.Write([]byte(nodeID))

	return hasher.Sum(nil)
}

// CalculateBlockProbability 根据VRF输出计算是否应该出块
// threshold: 出块概率阈值（0.0-1.0）
// vrfOutput: VRF输出
// 返回: true表示应该出块
func CalculateBlockProbability(vrfOutput []byte, threshold float64) bool {
	if len(vrfOutput) == 0 {
		return false
	}

	// 将VRF输出的前8字节转换为uint64
	var outputValue uint64
	if len(vrfOutput) >= 8 {
		outputValue = binary.BigEndian.Uint64(vrfOutput[:8])
	} else {
		// 如果输出不足8字节，用0填充
		temp := make([]byte, 8)
		copy(temp, vrfOutput)
		outputValue = binary.BigEndian.Uint64(temp)
	}

	// 计算概率：outputValue / MaxUint64 < threshold
	// 为避免浮点运算，我们比较: outputValue < threshold * MaxUint64
	maxUint64 := uint64(0xFFFFFFFFFFFFFFFF)
	thresholdValue := uint64(threshold * float64(maxUint64))

	return outputValue < thresholdValue
}

// GetBLSPublicKeyFromECDSAPublic 从ECDSA公钥派生BLS公钥
// 注意：这种派生方法只在私钥所有者使用相同方法生成BLS密钥对时才有效
// 实际应用中，建议在链上存储矿工的BLS公钥
func GetBLSPublicKeyFromECDSAPublic(pub *ecdsa.PublicKey) (kyber.Point, error) {
	if pub == nil {
		return nil, fmt.Errorf("public key is nil")
	}

	// 使用与私钥派生相同的方法
	pubKeyBytes := append(pub.X.Bytes(), pub.Y.Bytes()...)
	hash := sha256.Sum256(pubKeyBytes)

	suite := bn256.NewSuite()
	blsPrivScalar := suite.G2().Scalar().SetBytes(hash[:])
	blsPublicKey := suite.G2().Point().Mul(blsPrivScalar, nil)

	return blsPublicKey, nil
}
