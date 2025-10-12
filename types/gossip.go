// types/gossip.go 或 types/network.go
package types

import "dex/db"

// GossipPayload 用于节点间的区块传播
type GossipPayload struct {
	Block     *db.Block `json:"block"`
	RequestID uint32    `json:"request_id"`
}
