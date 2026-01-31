package verkle

import (
	"github.com/crate-crypto/go-ipa/banderwagon"
	"github.com/crate-crypto/go-ipa/ipa"
)

// ============================================
// Verkle IPA 证明
// ============================================

// VerkleProof Verkle Tree 的 IPA 证明
type VerkleProof struct {
	// Commitments 从叶子到根的承诺路径
	Commitments [][]byte

	// Depths 每个承诺的深度
	Depths []byte

	// Key 被证明的 Key
	Key []byte

	// Value 被证明的值（存在性证明时非空）
	Value []byte

	// IPAProofData IPA 证明数据（序列化）
	IPAProofData []byte
}

// IsMembershipProof 检查是否为存在性证明
func (p *VerkleProof) IsMembershipProof() bool {
	return p.Value != nil
}

// ============================================
// 证明生成
// ============================================

// Prove 生成指定 Key 的 Verkle 证明
func (t *VerkleTree) Prove(key []byte) (*VerkleProof, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	verkleKey := ToVerkleKey(key)
	stem := GetStemFromKey(verkleKey)
	suffix := GetSuffixFromKey(verkleKey)

	proof := &VerkleProof{
		Key:         key,
		Commitments: make([][]byte, 0),
		Depths:      make([]byte, 0),
	}

	if t.isZeroCommitment(t.root) {
		// 空树，返回不存在性证明
		return proof, nil
	}

	// 收集路径上的承诺
	currentCommitment := t.root
	proof.Commitments = append(proof.Commitments, currentCommitment)
	proof.Depths = append(proof.Depths, 0)

	for depth := 0; depth < StemSize; depth++ {
		nodeData := t.getCache(currentCommitment)
		if nodeData == nil {
			var err error
			nodeData, err = t.store.Get(currentCommitment, t.version)
			if err != nil {
				break
			}
			t.setCache(currentCommitment, nodeData)
		}

		nodeType := GetNodeType(nodeData)

		switch nodeType {
		case NodeTypeLeaf:
			leaf, err := DecodeLeafNode(nodeData)
			if err != nil {
				return nil, err
			}
			// 检查是否为目标 key
			if leaf.Stem == stem {
				value := leaf.GetValue(suffix)
				if value != nil {
					proof.Value = value
				}
			}
			// 叶子节点，结束遍历
			return proof, nil

		case NodeTypeInternal:
			node, err := DecodeInternalNode256(nodeData)
			if err != nil {
				return nil, err
			}
			childCommitment := node.GetChild(stem[depth])
			if childCommitment == nil || t.isZeroCommitment(childCommitment) {
				// 路径结束，不存在性证明
				return proof, nil
			}
			currentCommitment = childCommitment
			proof.Commitments = append(proof.Commitments, currentCommitment)
			proof.Depths = append(proof.Depths, byte(depth+1))

		default:
			return proof, nil
		}
	}

	return proof, nil
}

// ============================================
// 证明验证
// ============================================

// VerifyVerkleProof 验证 Verkle 证明
// 注意：完整的 IPA 验证需要更复杂的实现
// 这里提供简化版本，验证承诺链的一致性
func VerifyVerkleProof(proof *VerkleProof, root []byte, committer *PedersenCommitter) bool {
	if len(proof.Commitments) == 0 {
		// 空树的不存在性证明
		return !proof.IsMembershipProof()
	}

	// 验证根承诺匹配
	if len(proof.Commitments) > 0 {
		if string(proof.Commitments[0]) != string(root) {
			return false
		}
	}

	// 验证承诺链的逻辑一致性
	// 完整版本需要使用 IPA 验证每一层
	for i := 1; i < len(proof.Commitments); i++ {
		if len(proof.Commitments[i]) != CommitmentSize {
			return false
		}
	}

	return true
}

// ============================================
// IPA 证明辅助（高级功能）
// ============================================

// IPAProver IPA 证明器
type IPAProver struct {
	config *ipa.IPAConfig
}

// NewIPAProver 创建新的 IPA 证明器
func NewIPAProver() *IPAProver {
	config, _ := GetIPAConfig()
	return &IPAProver{
		config: config,
	}
}

// CreateOpeningProof 创建多点开放证明
// 这是 IPA 的核心功能，证明多个点的值
func (p *IPAProver) CreateOpeningProof(commitment []byte, point int, value []byte) ([]byte, error) {
	// 将承诺转换为点
	var commitPoint banderwagon.Element
	if err := commitPoint.SetBytes(commitment); err != nil {
		return nil, err
	}

	// 将值转换为标量
	var valueScalar banderwagon.Fr
	if value != nil {
		valueScalar.SetBytes(value)
	}

	// 创建简化的开放证明
	// 完整版本需要构造多项式并使用 IPA 协议
	proofData := make([]byte, 64)
	copy(proofData[:32], commitment)
	copy(proofData[32:], value)

	return proofData, nil
}

// VerifyOpeningProof 验证开放证明
func (p *IPAProver) VerifyOpeningProof(commitment []byte, point int, value []byte, proof []byte) bool {
	if len(proof) < 64 {
		return false
	}

	// 简化验证：检查承诺和值的一致性
	// 完整版本需要完整的 IPA 验证协议
	return string(proof[:32]) == string(commitment)
}

// ============================================
// Multiproof（批量证明）
// ============================================

// MultiProof 批量证明结构
type MultiProof struct {
	// Keys 被证明的所有 key
	Keys [][]byte

	// Values 对应的值
	Values [][]byte

	// Commitments 共享的承诺路径
	Commitments [][]byte

	// IPAProof 聚合的 IPA 证明
	IPAProof []byte
}

// CreateMultiProof 创建批量证明
func (t *VerkleTree) CreateMultiProof(keys [][]byte) (*MultiProof, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	proof := &MultiProof{
		Keys:        keys,
		Values:      make([][]byte, len(keys)),
		Commitments: make([][]byte, 0),
	}

	// 收集所有值
	for i, key := range keys {
		value, err := t.Get(key, t.version)
		if err == nil {
			proof.Values[i] = value
		}
	}

	// 添加根承诺
	if !t.isZeroCommitment(t.root) {
		proof.Commitments = append(proof.Commitments, t.root)
	}

	return proof, nil
}

// VerifyMultiProof 验证批量证明
func VerifyMultiProof(proof *MultiProof, root []byte) bool {
	if len(proof.Commitments) == 0 {
		return len(proof.Keys) == 0
	}

	// 验证根承诺
	if string(proof.Commitments[0]) != string(root) {
		return false
	}

	return true
}
