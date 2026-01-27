// statedb/utils.go
package statedb

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
)

// ====== 工具 ======
func u64ToBytes(u uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, u)
	return b
}

func bytesToU64(b []byte) uint64 {
	if len(b) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}

func encodeToken(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

func decodeToken(tok string) string {
	if tok == "" {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return ""
	}
	return string(b)
}

// ====== 你可能会用到的错误 ======
var (
	ErrNotFound = errors.New("not found")
)
