package runtime

import (
	"context"

	"dex/pb"
)

// NodeID 表示网络节点标识
type NodeID string

// SignerInfo 表示签名者信息
type SignerInfo struct {
	ID        NodeID // 节点 ID
	Index     uint32 // 在 Top10000 中的索引
	PublicKey []byte // 公钥
	Weight    uint64 // 权重（可选）
}

// ChainStateReader 读链上最终化状态（来自 StateDB/DB overlay 的只读视图）
type ChainStateReader interface {
	Get(key string) ([]byte, bool, error)
	Scan(prefix string, fn func(k string, v []byte) bool) error
}

// TxSubmitter 提交"回写交易"（进入 txpool/广播/共识）
type TxSubmitter interface {
	Submit(tx any) (txID string, err error)
	SubmitDkgCommitTx(ctx context.Context, tx *pb.FrostVaultDkgCommitTx) error
	SubmitDkgShareTx(ctx context.Context, tx *pb.FrostVaultDkgShareTx) error
	SubmitDkgValidationSignedTx(ctx context.Context, tx *pb.FrostVaultDkgValidationSignedTx) error
}

// StateReader 状态读取接口（ChainStateReader 的别名）
type StateReader = ChainStateReader

// MinerPubKeyProvider 矿工公钥提供者
type MinerPubKeyProvider interface {
	// GetMinerSigningPubKey 获取矿工的签名公钥
	// minerID: 矿工地址
	// signAlgo: 签名算法（决定返回哪种曲线的公钥）
	GetMinerSigningPubKey(minerID string, signAlgo pb.SignAlgo) ([]byte, error)
}

// FinalityNotifier 订阅最终化事件（仅作为唤醒，不是唯一触发源）
type FinalityNotifier interface {
	SubscribeBlockFinalized(fn func(height uint64))
}

// FrostEnvelope P2P 消息封装
type FrostEnvelope struct {
	SessionID string
	Kind      string // "NonceCommit" | "SigShare" | "Abort" | "CoordinatorAnnounce" | ...
	From      NodeID
	Chain     string // 目标链
	VaultID   uint32 // 目标 Vault
	SignAlgo  int32  // pb.SignAlgo 枚举值，决定 FROST 变体与曲线
	Epoch     uint64 // key_epoch
	Round     uint32
	Payload   []byte // protobuf / json（承诺点/份额格式由 SignAlgo 决定）
	Sig       []byte // 消息签名（防伪造/重放）
}

// P2P 网络接口（复用现有 Transport）
type P2P interface {
	Send(to NodeID, msg *FrostEnvelope) error
	Broadcast(peers []NodeID, msg *FrostEnvelope) error
	SamplePeers(n int, role string) []NodeID
}

// SignerSetProvider 当前高度下 signer set（Top10000 bitmap）提供者
type SignerSetProvider interface {
	Top10000(height uint64) ([]SignerInfo, error) // height = committee_ref snapshot height
	CurrentEpoch(height uint64) uint64
}

// VaultCommitteeProvider Vault 委员会提供者（按 Vault 分片）
type VaultCommitteeProvider interface {
	// VaultCommittee 获取指定 Vault 的委员会成员（K 个）
	VaultCommittee(chain string, vaultID uint32, epoch uint64) ([]SignerInfo, error)
	// VaultCurrentEpoch 获取指定 Vault 的当前 epoch
	VaultCurrentEpoch(chain string, vaultID uint32) uint64
	// VaultGroupPubkey 获取指定 Vault 的 group_pubkey
	VaultGroupPubkey(chain string, vaultID uint32, epoch uint64) ([]byte, error)
}

// ChainAdapter 和 ChainAdapterFactory 接口定义在 frost/chain/adapter.go
// 这里不再重复定义，直接使用 frost/chain 包中的类型：
//   - chain.ChainAdapter
//   - chain.ChainAdapterFactory
//
// 使用示例：
//   import "dex/frost/chain"
//   func NewWorker(factory chain.ChainAdapterFactory) { ... }
