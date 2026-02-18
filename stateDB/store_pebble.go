package statedb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"sync"

	"github.com/cockroachdb/pebble"
)

type pebbleReadable interface {
	Get(key []byte) ([]byte, io.Closer, error)
	NewIter(o *pebble.IterOptions) (*pebble.Iterator, error)
}

type pebbleStore struct {
	db    *pebble.DB
	seqMu sync.Mutex
}

func newPebbleStore(path string) (kvStore, error) {
	db, err := pebble.Open(path, &pebble.Options{
		MaxOpenFiles: 500,
	})
	if err != nil {
		return nil, err
	}
	return &pebbleStore{db: db}, nil
}

func newPebbleStoreReadOnly(path string) (kvStore, error) {
	db, err := pebble.Open(path, &pebble.Options{
		MaxOpenFiles: 500,
		ReadOnly:     true,
	})
	if err != nil {
		return nil, err
	}
	return &pebbleStore{db: db}, nil
}

type pebbleReader struct {
	src pebbleReadable
}

func (r *pebbleReader) Get(key []byte) ([]byte, error) {
	v, closer, err := r.src.Get(key)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, ErrKVNotFound
		}
		return nil, err
	}
	defer closer.Close()
	out := append([]byte(nil), v...)
	return out, nil
}

func (r *pebbleReader) IteratePrefix(prefix []byte, startAfter []byte, fn func(key []byte, value []byte) error) error {
	opts := &pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixUpperBound(prefix),
	}
	iter, err := r.src.NewIter(opts)
	if err != nil {
		return err
	}
	defer iter.Close()

	seek := append([]byte(nil), prefix...)
	if len(startAfter) > 0 {
		seek = append(seek, startAfter...)
	}
	skipExact := len(startAfter) > 0

	for ok := iter.SeekGE(seek); ok; ok = iter.Next() {
		k := iter.Key()
		if skipExact && bytes.Equal(k, seek) {
			skipExact = false
			continue
		}
		skipExact = false

		key := append([]byte(nil), k...)
		val := append([]byte(nil), iter.Value()...)
		if err := fn(key, val); err != nil {
			return err
		}
	}
	return iter.Error()
}

type pebbleWriter struct {
	*pebbleReader
	batch *pebble.Batch
}

func (w *pebbleWriter) Set(key []byte, value []byte) error {
	return w.batch.Set(key, value, pebble.NoSync)
}

func (w *pebbleWriter) Delete(key []byte) error {
	return w.batch.Delete(key, pebble.NoSync)
}

func (s *pebbleStore) View(fn func(tx kvReader) error) error {
	snap := s.db.NewSnapshot()
	defer snap.Close()
	return fn(&pebbleReader{src: snap})
}

func (s *pebbleStore) Update(fn func(tx kvWriter) error) error {
	batch := s.db.NewIndexedBatch()
	defer batch.Close()

	writer := &pebbleWriter{
		pebbleReader: &pebbleReader{src: batch},
		batch:        batch,
	}
	if err := fn(writer); err != nil {
		return err
	}
	return batch.Commit(pebble.NoSync)
}

func (s *pebbleStore) NextSequence() (uint64, error) {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()

	var seq uint64
	raw, closer, err := s.db.Get(commitSeqKey)
	if err == nil {
		if len(raw) >= 8 {
			seq = binary.BigEndian.Uint64(raw)
		}
		closer.Close()
	} else if !errors.Is(err, pebble.ErrNotFound) {
		return 0, err
	}

	seq++
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, seq)
	if err := s.db.Set(commitSeqKey, buf, pebble.NoSync); err != nil {
		return 0, err
	}
	return seq, nil
}

func (s *pebbleStore) Checkpoint(destDir string, flushWAL bool) error {
	if flushWAL {
		return s.db.Checkpoint(destDir, pebble.WithFlushedWAL())
	}
	return s.db.Checkpoint(destDir)
}

func (s *pebbleStore) IsNotFound(err error) bool {
	return errors.Is(err, ErrKVNotFound) || errors.Is(err, pebble.ErrNotFound)
}

func (s *pebbleStore) Close() error {
	return s.db.Close()
}
