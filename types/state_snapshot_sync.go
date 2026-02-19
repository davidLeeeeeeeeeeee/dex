package types

// StateSnapshotShard describes one shard and its item count.
type StateSnapshotShard struct {
	Shard string `json:"shard"`
	Count int64  `json:"count"`
}

// StateSnapshotItem is one key/value entry transferred during state snapshot sync.
// Value uses JSON base64 encoding when marshaled.
type StateSnapshotItem struct {
	Key   string `json:"key"`
	Value []byte `json:"value"`
}

// StateSnapshotShardsRequest asks a peer for shard metadata at target height.
// The peer may downshift to its local accepted height and returns SnapshotHeight.
type StateSnapshotShardsRequest struct {
	TargetHeight uint64 `json:"target_height"`
}

// StateSnapshotShardsResponse returns shard metadata and the snapshot height used.
type StateSnapshotShardsResponse struct {
	SnapshotHeight uint64               `json:"snapshot_height"`
	Shards         []StateSnapshotShard `json:"shards"`
	Error          string               `json:"error,omitempty"`
}

// StateSnapshotPageRequest asks one page of one shard from a snapshot height.
type StateSnapshotPageRequest struct {
	SnapshotHeight uint64 `json:"snapshot_height"`
	Shard          string `json:"shard"`
	PageSize       int    `json:"page_size"`
	PageToken      string `json:"page_token"`
}

// StateSnapshotPageResponse returns one page and next page token.
type StateSnapshotPageResponse struct {
	SnapshotHeight uint64              `json:"snapshot_height"`
	Shard          string              `json:"shard"`
	Items          []StateSnapshotItem `json:"items"`
	NextPageToken  string              `json:"next_page_token"`
	Error          string              `json:"error,omitempty"`
}
