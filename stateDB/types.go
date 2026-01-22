// statedb/types.go
package statedb

// ====== Config & Types ======

type Config struct {
	DataDir         string // e.g. "stateDB/data"
	EpochSize       uint64 // 40000
	ShardHexWidth   int    // 1 => 16片, 2 => 256片
	PageSize        int    // 默认分页大小
	UseWAL          bool   // 是否启用可选 WAL 以防宕机丢内存 diff
	VersionsToKeep  int    // e.g. 10
	AccountNSPrefix string // "v1_account_"
}

type KVUpdate struct {
	// 这里的 Key 必须是主库使用的 "v1_account_<addr>"
	Key     string
	Value   []byte // 删除使用空切片+Deleted
	Deleted bool
}

type Page struct {
	Items         []KVUpdate
	Page          int
	TotalPages    int
	NextPageToken string // 供游标分页
}

type ShardInfo struct {
	Shard string // "0f" / "a" / "7c" ...
	Count int64  // 该 shard 的键数量（快照或 overlay 维度）
}
