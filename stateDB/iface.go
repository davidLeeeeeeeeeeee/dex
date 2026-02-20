package statedb

import "errors"

const (
	BackendPebble = "pebble"
	BackendBadger = "badger"
)

var ErrKVNotFound = errors.New("kv key not found")
var commitSeqKey = []byte("v1_sdb_meta_commit_seq")

type kvReader interface {
	Get(key []byte) ([]byte, error)
	IteratePrefix(prefix []byte, startAfter []byte, fn func(key []byte, value []byte) error) error
}

type kvWriter interface {
	kvReader
	Set(key []byte, value []byte) error
	Delete(key []byte) error
}

type kvStore interface {
	Get(key []byte) ([]byte, error)
	View(fn func(tx kvReader) error) error
	Update(fn func(tx kvWriter) error) error
	NextSequence() (uint64, error)
	Checkpoint(destDir string, flushWAL bool) error
	IsNotFound(err error) bool
	Close() error
}

func prefixUpperBound(prefix []byte) []byte {
	upper := append([]byte(nil), prefix...)
	for i := len(upper) - 1; i >= 0; i-- {
		upper[i]++
		if upper[i] != 0 {
			return upper
		}
	}
	return nil
}
