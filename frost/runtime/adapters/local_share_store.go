package adapters

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"dex/db"
	"dex/frost/runtime"
	"dex/keys"
	"dex/logs"
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
	logs.Debug("[LocalShareStore] save local share chain=%s vault=%d epoch=%d key=%s share_fp=%s",
		chain, vaultID, epoch, key, localShareFingerprintForLog(shareCopy))

	s.dbManager.EnqueueSet(key, string(shareCopy))
	if err := s.dbManager.ForceFlush(); err != nil {
		logs.Warn("[LocalShareStore] save local share failed chain=%s vault=%d epoch=%d key=%s err=%v",
			chain, vaultID, epoch, key, err)
		return err
	}
	logs.Debug("[LocalShareStore] save local share completed chain=%s vault=%d epoch=%d key=%s",
		chain, vaultID, epoch, key)
	return nil
}

func (s *DBLocalShareStore) LoadLocalShare(chain string, vaultID uint32, epoch uint64) ([]byte, error) {
	if s == nil || s.dbManager == nil {
		return nil, errors.New("db manager not available")
	}

	key := keys.KeyFrostLocalShare(chain, vaultID, epoch)
	logs.Debug("[LocalShareStore] load local share chain=%s vault=%d epoch=%d key=%s",
		chain, vaultID, epoch, key)
	data, err := s.dbManager.Get(key)
	if err != nil {
		if isLocalShareNotFound(err) {
			logs.Debug("[LocalShareStore] local share not found chain=%s vault=%d epoch=%d key=%s",
				chain, vaultID, epoch, key)
			return nil, nil
		}
		logs.Warn("[LocalShareStore] load local share failed chain=%s vault=%d epoch=%d key=%s err=%v",
			chain, vaultID, epoch, key, err)
		return nil, err
	}
	if len(data) == 0 {
		logs.Debug("[LocalShareStore] local share empty chain=%s vault=%d epoch=%d key=%s",
			chain, vaultID, epoch, key)
		return nil, nil
	}

	out := make([]byte, len(data))
	copy(out, data)
	logs.Debug("[LocalShareStore] loaded local share chain=%s vault=%d epoch=%d key=%s share_fp=%s",
		chain, vaultID, epoch, key, localShareFingerprintForLog(out))
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

func localShareFingerprintForLog(data []byte) string {
	if len(data) == 0 {
		return "len=0"
	}
	sum := sha256.Sum256(data)
	prefixLen := 8
	if len(data) < prefixLen {
		prefixLen = len(data)
	}
	return fmt.Sprintf("len=%d,prefix=%x,sha256=%x", len(data), data[:prefixLen], sum[:8])
}

var _ runtime.LocalShareStore = (*DBLocalShareStore)(nil)
