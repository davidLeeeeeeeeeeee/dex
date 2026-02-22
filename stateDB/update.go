package statedb

import (
	"dex/keys"
	"dex/logs"
	"encoding/binary"
	"time"
)

const (
	stateDBApplyProbeWarnThreshold      = 300 * time.Millisecond
	stateDBApplyCheckpointInfoThreshold = 80 * time.Millisecond
	stateDBApplyLockWaitWarnThreshold   = 20 * time.Millisecond
)

// ApplyAccountUpdate applies state updates at the given block height.
// New architecture: write-through into live DB, no in-memory diff window.
func (s *DB) ApplyAccountUpdate(height uint64, kvs ...KVUpdate) (err error) {
	startAt := time.Now()
	checkpointBoundary := height > 0 && checkpointOf(height, s.checkpointEvery())
	var (
		durLockWait        time.Duration
		durNextSeq         time.Duration
		durWriteBatch      time.Duration
		durCheckpointQueue time.Duration
		statefulCount      int
		checkpointQueued   bool
	)
	defer func() {
		total := time.Since(startAt)
		if err == nil &&
			total < stateDBApplyCheckpointInfoThreshold &&
			durLockWait < stateDBApplyLockWaitWarnThreshold {
			return
		}
		if err != nil ||
			total >= stateDBApplyProbeWarnThreshold ||
			durLockWait >= stateDBApplyLockWaitWarnThreshold ||
			(checkpointBoundary && total >= stateDBApplyCheckpointInfoThreshold) {
			logs.Warn(
				"[StateDB][Apply] height=%d updates=%d stateful=%d total=%s lockWait=%s nextSeq=%s writeBatch=%s checkpoint=%s boundary=%t queued=%t err=%v",
				height, len(kvs), statefulCount, total, durLockWait, durNextSeq, durWriteBatch, durCheckpointQueue, checkpointBoundary, checkpointQueued, err,
			)
		}
	}()

	lockWaitAt := time.Now()
	s.mu.Lock()
	durLockWait = time.Since(lockWaitAt)
	locked := true
	defer func() {
		if locked {
			s.mu.Unlock()
		}
	}()

	seq := uint64(0)
	if height > 0 {
		nextSeqAt := time.Now()
		nextSeq, err := s.store.NextSequence()
		durNextSeq = time.Since(nextSeqAt)
		if err != nil {
			return err
		}
		seq = nextSeq
	}

	writeBatchAt := time.Now()
	err = s.store.Update(func(tx kvWriter) error {
		for _, kv := range kvs {
			if !keys.IsStatefulKey(kv.Key) {
				continue
			}
			statefulCount++
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

		if height > 0 {
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
		}
		return nil
	})
	durWriteBatch = time.Since(writeBatchAt)
	if err != nil {
		return err
	}
	s.mu.Unlock()
	locked = false

	if checkpointBoundary {
		checkpointAt := time.Now()
		checkpointQueued = s.enqueueCheckpoint(height)
		durCheckpointQueue = time.Since(checkpointAt)
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
