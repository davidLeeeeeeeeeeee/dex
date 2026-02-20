package types

// SyncBlocksRequest requests a sync block range from a peer.
type SyncBlocksRequest struct {
	FromHeight    uint64 `json:"from_height"`
	ToHeight      uint64 `json:"to_height"`
	SyncShortMode bool   `json:"sync_short_mode,omitempty"`
}

// SyncBlockBundle carries one finalized block and its VRF signature set proof.
// BlockBytes is proto.Marshal(pb.Block)
// SignatureSetBytes is proto.Marshal(pb.ConsensusSignatureSet)
type SyncBlockBundle struct {
	Height            uint64 `json:"height"`
	BlockID           string `json:"block_id"`
	BlockBytes        []byte `json:"block_bytes"`
	SignatureSetBytes []byte `json:"signature_set_bytes"`
}

// SyncBlocksResponse returns a list of block bundles for a requested range.
type SyncBlocksResponse struct {
	Blocks []SyncBlockBundle `json:"blocks,omitempty"`
	Error  string            `json:"error,omitempty"`
}
