// statedb/types.go
package statedb

type Config struct {
	Backend         string // "pebble" (default) / "badger"
	DataDir         string // e.g. "stateDB/data"
	ShardHexWidth   int    // 1 => 16 shards, 2 => 256 shards
	PageSize        int
	CheckpointKeep  int
	CheckpointEvery uint64 // 0 means use default interval (1000)
}

type KVUpdate struct {
	Key     string
	Value   []byte
	Deleted bool
}

type Page struct {
	Items         []KVUpdate
	Page          int
	TotalPages    int
	NextPageToken string
}

type ShardInfo struct {
	Shard string
	Count int64
}
