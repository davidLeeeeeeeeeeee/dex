// frost/runtime/adapters/signer_provider.go
// SignerSetProvider 适配器实现（从 consensus 获取 Top10000）
package adapters

import (
	"dex/db"
	"dex/frost/runtime"
	"dex/pb"
	"fmt"

	"google.golang.org/protobuf/proto"
)

// Top10000Reader Top10000 读取接口（由外部实现）
type Top10000Reader interface {
	// GetTop10000 获取 Top10000 矿工列表
	GetTop10000() ([]byte, error)
	// GetCurrentHeight 获取当前高度
	GetCurrentHeight() uint64
}

// DBManagerAdapter 适配 db.Manager 到 Top10000Reader 接口
type DBManagerAdapter struct {
	dbManager *db.Manager
}

// NewDBManagerAdapter 创建新的 DBManagerAdapter
func NewDBManagerAdapter(dbManager *db.Manager) *DBManagerAdapter {
	return &DBManagerAdapter{
		dbManager: dbManager,
	}
}

// GetTop10000 获取 Top10000 矿工列表
func (a *DBManagerAdapter) GetTop10000() ([]byte, error) {
	key := "v1_frost_top10000"
	val, err := a.dbManager.Read(key)
	if err != nil {
		return nil, err
	}
	return []byte(val), nil
}

// GetCurrentHeight 获取当前高度
func (a *DBManagerAdapter) GetCurrentHeight() uint64 {
	// TODO: 从共识层获取实际高度
	return 0
}

// ConsensusSignerProvider 基于共识层的 SignerSetProvider 实现
type ConsensusSignerProvider struct {
	reader Top10000Reader
}

// NewConsensusSignerProvider 创建新的 ConsensusSignerProvider
func NewConsensusSignerProvider(reader Top10000Reader) *ConsensusSignerProvider {
	return &ConsensusSignerProvider{
		reader: reader,
	}
}

// Top10000 获取指定高度的 Top10000 矿工列表
func (p *ConsensusSignerProvider) Top10000(height uint64) ([]runtime.SignerInfo, error) {
	if p.reader == nil {
		return nil, fmt.Errorf("reader not available")
	}

	// 读取 Top10000 数据
	data, err := p.reader.GetTop10000()
	if err != nil {
		return nil, fmt.Errorf("get top10000: %w", err)
	}

	// 解析 protobuf
	var top10000 pb.FrostTop10000
	if err := proto.Unmarshal(data, &top10000); err != nil {
		return nil, fmt.Errorf("unmarshal top10000: %w", err)
	}

	// 转换为 SignerInfo 列表
	result := make([]runtime.SignerInfo, 0, len(top10000.Indices))
	for i, idx := range top10000.Indices {
		if i >= len(top10000.Addresses) {
			break
		}
		var pubKey []byte
		if i < len(top10000.PublicKeys) {
			pubKey = top10000.PublicKeys[i]
		}

		result = append(result, runtime.SignerInfo{
			ID:        runtime.NodeID(top10000.Addresses[i]),
			Index:     uint32(idx),
			PublicKey: pubKey,
			Weight:    1, // TODO: 从实际数据获取权重
		})
	}

	return result, nil
}

// CurrentEpoch 获取当前 epoch（简化实现，返回高度）
func (p *ConsensusSignerProvider) CurrentEpoch(height uint64) uint64 {
	if p.reader == nil {
		return height / 1000 // 默认每 1000 个区块一个 epoch
	}
	// TODO: 从共识层获取实际的 epoch
	return height / 1000
}

// Ensure ConsensusSignerProvider implements runtime.SignerSetProvider
var _ runtime.SignerSetProvider = (*ConsensusSignerProvider)(nil)
