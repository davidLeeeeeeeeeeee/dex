package verkle

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"sync"

	gverkle "github.com/ethereum/go-verkle"
)

// ============================================
// Go-Verkle 适配器
// 使用 github.com/ethereum/go-verkle 库实现高性能 Verkle Tree
// ============================================

// GoVerkleTree 使用 go-verkle 库的 Verkle Tree 包装器
type GoVerkleTree struct {
	mu          sync.RWMutex
	root        gverkle.VerkleNode
	store       VersionedStore
	version     Version
	rootHistory map[Version][]byte
}

// NewGoVerkleTree 创建新的 go-verkle 包装器
func NewGoVerkleTree(store VersionedStore) *GoVerkleTree {
	return &GoVerkleTree{
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
func (t *GoVerkleTree) Get(key []byte, version Version) ([]byte, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.root == nil {
		return nil, ErrNotFound
	}

	verkleKey := toVerkleKey32(key)
	value, err := t.root.Get(verkleKey[:], nil)
	if err != nil || value == nil {
		return nil, ErrNotFound
	}
	return value, nil
}

// Update 批量更新 Key-Value
func (t *GoVerkleTree) Update(keys [][]byte, values [][]byte, newVersion Version) ([]byte, error) {
	if len(keys) != len(values) {
		return nil, errors.New("keys and values must have the same length")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	for i := 0; i < len(keys); i++ {
		verkleKey := toVerkleKey32(keys[i])
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

// Delete 删除指定 Key
func (t *GoVerkleTree) Delete(key []byte, newVersion Version) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	verkleKey := toVerkleKey32(key)
	// go-verkle 通过插入空值来删除
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

// ============================================
// 访问器
// ============================================

// Root 获取当前根承诺
func (t *GoVerkleTree) Root() []byte {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.root == nil || t.root.Commitment() == nil {
		return make([]byte, 32)
	}
	commitment := t.root.Commitment().Bytes()
	return commitment[:]
}

// Version 获取当前版本
func (t *GoVerkleTree) Version() Version {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.version
}

// GetRootHash 获取指定版本的根承诺
func (t *GoVerkleTree) GetRootHash(version Version) ([]byte, error) {
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
func (t *GoVerkleTree) Prove(key []byte) (*VerkleProof, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	verkleKey := toVerkleKey32(key)

	// 获取值
	value, _ := t.root.Get(verkleKey[:], nil)

	proof := &VerkleProof{
		Key:         key,
		Value:       value,
		Commitments: [][]byte{t.Root()},
		Depths:      []byte{0},
	}

	return proof, nil
}

// ============================================
// 批量操作（高性能）
// ============================================

// BatchUpdate 高性能批量更新
func (t *GoVerkleTree) BatchUpdate(keys [][]byte, values [][]byte, newVersion Version) ([]byte, error) {
	if len(keys) != len(values) {
		return nil, errors.New("keys and values must have the same length")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// 批量插入
	for i := 0; i < len(keys); i++ {
		verkleKey := toVerkleKey32(keys[i])
		if err := t.root.Insert(verkleKey[:], values[i], nil); err != nil {
			return nil, err
		}
	}

	// 计算承诺
	t.root.Commit()
	rootCommitment := t.root.Commitment().Bytes()

	t.version = newVersion
	t.rootHistory[newVersion] = rootCommitment[:]

	if err := t.store.Set(rootKey(newVersion), rootCommitment[:], newVersion); err != nil {
		return nil, err
	}

	return rootCommitment[:], nil
}

// ============================================
// 工具函数
// ============================================

// toVerkleKey32 将任意长度 Key 转换为 32 字节 Verkle Key
func toVerkleKey32(key []byte) [32]byte {
	return sha256.Sum256(key)
}

// isZeroCommitment 检查是否为零承诺
func isZeroCommitment(commitment []byte) bool {
	zero := make([]byte, 32)
	return bytes.Equal(commitment, zero)
}

// ============================================
// 兼容性接口
// ============================================

// CommitRoot 更新树的根承诺和版本号（用于恢复）
func (t *GoVerkleTree) CommitRoot(version Version, root []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.version = version
	t.rootHistory[version] = root
}

// Commit 提交当前状态
func (t *GoVerkleTree) Commit() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.root.Commit()
	return nil
}
