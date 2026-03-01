package adapters

import (
	"errors"
	"strings"

	"dex/db"
	"dex/frost/runtime"
	"dex/keys"
)

// DBLocalShareStore persists local FROST shares into the node's local DB.
// Data written here is local-only and does not go through consensus.
type DBLocalShareStore struct {
	dbManager *db.Manager
}

func NewDBLocalShareStore(dbManager *db.Manager) *DBLocalShareStore {
	return &DBLocalShareStore{dbManager: dbManager}
}

func (s *DBLocalShareStore) SaveLocalShare(chain string, vaultID uint32, epoch uint64, share []byte) error {
	if s == nil || s.dbManager == nil {
		return errors.New("db manager not available")
	}
	if len(share) == 0 {
		return errors.New("empty local share")
	}

	key := keys.KeyFrostLocalShare(chain, vaultID, epoch)
	shareCopy := make([]byte, len(share))
	copy(shareCopy, share)

	s.dbManager.EnqueueSet(key, string(shareCopy))
	return s.dbManager.ForceFlush()
}

func (s *DBLocalShareStore) LoadLocalShare(chain string, vaultID uint32, epoch uint64) ([]byte, error) {
	if s == nil || s.dbManager == nil {
		return nil, errors.New("db manager not available")
	}

	key := keys.KeyFrostLocalShare(chain, vaultID, epoch)
	data, err := s.dbManager.Get(key)
	if err != nil {
		if isLocalShareNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}

	out := make([]byte, len(data))
	copy(out, data)
	return out, nil
}

func isLocalShareNotFound(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "not found") ||
		strings.Contains(errStr, "key not found") ||
		strings.Contains(errStr, "does not exist")
}

var _ runtime.LocalShareStore = (*DBLocalShareStore)(nil)
