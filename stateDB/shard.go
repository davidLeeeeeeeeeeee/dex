// statedb/shard.go
package statedb

import (
	"crypto/sha256"
	"encoding/hex"
)

// ====== Sharding ======

func shardOf(addr string, width int) string {
	sum := sha256.Sum256([]byte(addr))
	hexed := hex.EncodeToString(sum[:])
	return hexed[:width]
}

