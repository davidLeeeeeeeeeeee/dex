package verkle

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"sync"

	gverkle "github.com/ethereum/go-verkle"
)

// ============================================
// Verkle Tree 适配层
// 使用 github.com/ethereum/go-verkle 库实现高性能 Verkle Tree
// ============================================

const (
	// CommitmentSize Pedersen 承诺大小 (32 字节)
	CommitmentSize = 32
	// StemSize Stem 大小 (31 字节)
	StemSize = 31

	// 大值存储标记
	markerDirect   byte = 0x00 // 直接存储
	markerIndirect byte = 0x01 // 间接引用

	// 直接存储的最大值长度 (32 - 1字节标记 = 31)
	maxDirectValueSize = 31
)

// VerkleProof Verkle Tree 的证明结构
type VerkleProof struct {
	// Commitments 从叶子到根的承诺路径
	Commitments [][]byte
	// Depths 每个承诺的深度
	Depths []byte
	// Key 被证明的 Key
	Key []byte
	// Value 被证明的值
	Value []byte
}

// IsMembershipProof 检查是否为存在性证明
func (p *VerkleProof) IsMembershipProof() bool {
	return p.Value != nil
}

// VerkleTree 使用 go-verkle 库的 Verkle Tree 包装器
type VerkleTree struct {
	mu          sync.RWMutex
	root        gverkle.VerkleNode
	store       VersionedStore
	version     Version
	rootHistory map[Version][]byte
}

// NewVerkleTree 创建新的 Verkle Tree 包装器
func NewVerkleTree(store VersionedStore) *VerkleTree {
	return &VerkleTree{
		root:        gverkle.New(),
		store:       store,
		version:     0,
		rootHistory: make(map[Version][]byte),
	}
}

// ============================================
// 核心操作
// ============================================

// Get 获取指定版本的值
func (t *VerkleTree) Get(key []byte, version Version) ([]byte, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.root == nil || t.root.Commitment() == nil {
		return nil, ErrNotFound
	}

	verkleKey := ToVerkleKey(key)
	data, err := t.root.Get(verkleKey[:], nil)
	if err != nil || data == nil || len(data) == 0 {
		return nil, ErrNotFound
	}

	marker := data[0]
	payload := data[1:]

	switch marker {
	case markerDirect:
		// 直接存储的值
		return payload, nil
	case markerIndirect:
		// 从 KV 获取大值
		val, err := t.store.Get(largeValueKey(payload), version)
		if err != nil {
			return nil, err
		}
		return val, nil
	default:
		// 兼容旧数据或未知格式
		return data, nil
	}
}

// GetWithSession 在会话中获取值
func (t *VerkleTree) GetWithSession(sess VersionedStoreSession, key []byte, version Version) ([]byte, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.root == nil || t.root.Commitment() == nil {
		return nil, ErrNotFound
	}

	verkleKey := ToVerkleKey(key)
	data, err := t.root.Get(verkleKey[:], nil)
	if err != nil || data == nil || len(data) == 0 {
		return nil, ErrNotFound
	}

	marker := data[0]
	payload := data[1:]

	switch marker {
	case markerDirect:
		// 直接存储的值
		return payload, nil
	case markerIndirect:
		// 从 KV 获取大值（通过 Session）
		val, err := sess.Get(largeValueKey(payload), version)
		if err != nil {
			return nil, err
		}
		return val, nil
	default:
		// 兼容旧数据或未知格式
		return data, nil
	}
}

// Update 批量更新 Key-Value
func (t *VerkleTree) Update(keys [][]byte, values [][]byte, newVersion Version) ([]byte, error) {
	if len(keys) != len(values) {
		return nil, errors.New("keys and values must have the same length")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	for i := 0; i < len(keys); i++ {
		verkleKey := ToVerkleKey(keys[i])
		if err := t.root.Insert(verkleKey[:], values[i], nil); err != nil {
			return nil, err
		}
	}

	// 计算并返回根承诺
	t.root.Commit()
	rootCommitment := t.root.Commitment().Bytes()

	t.version = newVersion
	t.rootHistory[newVersion] = rootCommitment[:]

	// 保存到存储
	if err := t.store.Set(rootKey(newVersion), rootCommitment[:], newVersion); err != nil {
		return nil, err
	}

	return rootCommitment[:], nil
}

// UpdateWithSession 在会话中批量更新（支持大值分片存储）
func (t *VerkleTree) UpdateWithSession(sess VersionedStoreSession, keys [][]byte, values [][]byte, newVersion Version) ([]byte, error) {
	if len(keys) != len(values) {
		return nil, errors.New("keys and values must have the same length")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	for i := 0; i < len(keys); i++ {
		verkleKey := ToVerkleKey(keys[i])
		val := values[i]
		var treeVal []byte

		if len(val) <= maxDirectValueSize {
			// 小值：直接存储 [0x00] + value
			treeVal = make([]byte, 1+len(val))
			treeVal[0] = markerDirect
			copy(treeVal[1:], val)
		} else {
			// 大值：存储引用 [0x01] + hash[:31]
			h := sha256.Sum256(val)
			hashPart := h[:31]

			// 使用 Session 原子写入大值到 KV
			if err := sess.Set(largeValueKey(hashPart), val, newVersion); err != nil {
				return nil, err
			}

			treeVal = make([]byte, 32)
			treeVal[0] = markerIndirect
			copy(treeVal[1:], hashPart)
		}

		if err := t.root.Insert(verkleKey[:], treeVal, nil); err != nil {
			return nil, err
		}
	}

	// 计算根承诺
	t.root.Commit()
	rootCommitment := t.root.Commitment().Bytes()

	t.version = newVersion
	t.rootHistory[newVersion] = rootCommitment[:]

	// 存储根承诺（也通过 session）
	if err := sess.Set(rootKey(newVersion), rootCommitment[:], newVersion); err != nil {
		return nil, err
	}

	return rootCommitment[:], nil
}

// Delete 删除指定 Key
func (t *VerkleTree) Delete(key []byte, newVersion Version) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	verkleKey := ToVerkleKey(key)
	if _, err := t.root.Delete(verkleKey[:], nil); err != nil {
		return nil, err
	}

	t.root.Commit()
	rootCommitment := t.root.Commitment().Bytes()

	t.version = newVersion
	t.rootHistory[newVersion] = rootCommitment[:]

	if err := t.store.Set(rootKey(newVersion), rootCommitment[:], newVersion); err != nil {
		return nil, err
	}

	return rootCommitment[:], nil
}

// DeleteWithSession 在会话中删除
func (t *VerkleTree) DeleteWithSession(sess VersionedStoreSession, key []byte, newVersion Version) ([]byte, error) {
	return t.Delete(key, newVersion)
}

// ============================================
// 访问器
// ============================================

// Root 获取当前根承诺
func (t *VerkleTree) Root() []byte {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.root == nil || t.root.Commitment() == nil {
		return make([]byte, 32)
	}
	commitment := t.root.Commitment().Bytes()
	return commitment[:]
}

// Version 获取当前版本
func (t *VerkleTree) Version() Version {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.version
}

// GetRootHash 获取指定版本的根承诺
func (t *VerkleTree) GetRootHash(version Version) ([]byte, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if version == 0 || version == t.version {
		return t.Root(), nil
	}
	if root, ok := t.rootHistory[version]; ok {
		return root, nil
	}
	return nil, ErrVersionNotFound
}

// ============================================
// 证明
// ============================================

// Prove 生成指定 Key 的 Verkle 证明
func (t *VerkleTree) Prove(key []byte) (*VerkleProof, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	verkleKey := ToVerkleKey(key)
	value, _ := t.root.Get(verkleKey[:], nil)

	proof := &VerkleProof{
		Key:         key,
		Value:       value,
		Commitments: [][]byte{t.Root()},
		Depths:      []byte{0},
	}

	return proof, nil
}

// VerifyVerkleProof 验证 Verkle 证明（简化实现）
func VerifyVerkleProof(proof *VerkleProof, root []byte) bool {
	if len(proof.Commitments) == 0 {
		return !proof.IsMembershipProof()
	}
	if !bytes.Equal(proof.Commitments[0], root) {
		return false
	}
	return true
}

// ============================================
// 工具函数
// ============================================

// ToVerkleKey 将任意长度 Key 转换为 32 字节 Verkle Key
func ToVerkleKey(key []byte) [32]byte {
	return sha256.Sum256(key)
}

// largeValueKey 生成大值的存储 Key
func largeValueKey(hash []byte) []byte {
	k := make([]byte, len(hash)+7)
	copy(k, "vlarge:")
	copy(k[7:], hash)
	return k
}

// rootKey 生成根承诺的存储 Key
func rootKey(version Version) []byte {
	k := make([]byte, 14)
	copy(k, "root:v")
	k[6] = byte(version >> 56)
	k[7] = byte(version >> 48)
	k[8] = byte(version >> 40)
	k[9] = byte(version >> 32)
	k[10] = byte(version >> 24)
	k[11] = byte(version >> 16)
	k[12] = byte(version >> 8)
	k[13] = byte(version)
	return k
}

// ============================================
// 兼容性接口
// ============================================

// CommitRoot 更新树的根承诺和版本号
func (t *VerkleTree) CommitRoot(version Version, root []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.version = version
	t.rootHistory[version] = root
}

// Commit 提交当前状态
func (t *VerkleTree) Commit() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.root.Commit()
	return nil
}

// Committer 返回承诺器（兼容性接口，返回 nil 或内部对象）
func (t *VerkleTree) Committer() interface{} {
	return nil
}
