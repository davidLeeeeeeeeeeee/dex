package statedb

import (
	"dex/keys"
	"encoding/binary"
)

// ApplyAccountUpdate applies state updates at the given block height.
// New architecture: write-through into live DB, no in-memory diff window.
func (s *DB) ApplyAccountUpdate(height uint64, kvs ...KVUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	seq, err := s.store.NextSequence()
	if err != nil {
		return err
	}

	err = s.store.Update(func(tx kvWriter) error {
		for _, kv := range kvs {
			if !keys.IsStatefulKey(kv.Key) {
				continue
			}
			sh := shardOf(kv.Key, s.conf.ShardHexWidth)
			stateKey := kState(sh, kv.Key)
			if kv.Deleted {
				if err := tx.Delete(stateKey); err != nil && !s.store.IsNotFound(err) {
					return err
				}
				continue
			}
			if err := tx.Set(stateKey, kv.Value); err != nil {
				return err
			}
		}

		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, seq)
		hb := make([]byte, 8)
		binary.BigEndian.PutUint64(hb, height)

		if err := tx.Set(kMetaH2Seq(height), buf); err != nil {
			return err
		}
		if err := tx.Set(kMetaSeq2H(seq), hb); err != nil {
			return err
		}
		if err := tx.Set(kMetaLastAppliedHeight(), hb); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	if checkpointOf(height, s.checkpointEvery()) {
		return s.createCheckpointLocked(height)
	}
	return nil
}

// FlushAndRotate is preserved for compatibility.
// In the new architecture it simply forces checkpoint creation at the supplied height.
func (s *DB) FlushAndRotate(epochEnd uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createCheckpointLocked(epochEnd)
}
