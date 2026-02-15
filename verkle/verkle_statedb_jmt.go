package verkle

import (
	cfg_default "dex/config"
	jmt "dex/jmt"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dgraph-io/badger/v2"
)

// KVUpdate keeps compatibility with existing callers and maps directly to JMT.
type KVUpdate = jmt.KVUpdate

// VerkleConfig is kept for backward compatibility at call sites.
type VerkleConfig struct {
	DataDir           string
	Prefix            []byte
	EnableKVLog       bool
	DisableRootCommit bool
	Database          *cfg_default.DatabaseConfig
}

// VerkleStateDB is now a compatibility wrapper over JMTStateDB.
type VerkleStateDB struct {
	inner *jmt.JMTStateDB
}

// VerkleStateDBSession wraps JMTStateDBSession.
type VerkleStateDBSession struct {
	inner *jmt.JMTStateDBSession
}

var errVerkleProofUnavailable = errors.New("verkle proof is unavailable in JMT mode")

func normalizeJMTDataDir(dataDir string) string {
	if dataDir == "" {
		return dataDir
	}
	clean := filepath.Clean(dataDir)
	if strings.HasSuffix(clean, "verkle_state") {
		return strings.TrimSuffix(clean, "verkle_state") + "jmt_state"
	}
	return dataDir
}

func normalizeJMTPrefix(prefix []byte) []byte {
	if len(prefix) == 0 || string(prefix) == "verkle:" {
		return []byte("jmt:")
	}
	return prefix
}

func NewVerkleStateDB(cfg VerkleConfig) (*VerkleStateDB, error) {
	prefix := normalizeJMTPrefix(cfg.Prefix)
	inner, err := jmt.NewJMTStateDB(jmt.JMTConfig{
		DataDir:     normalizeJMTDataDir(cfg.DataDir),
		Prefix:      prefix,
		EnableKVLog: cfg.EnableKVLog,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create JMT-backed StateDB: %w", err)
	}
	return &VerkleStateDB{inner: inner}, nil
}

func NewVerkleStateDBWithDB(db *badger.DB, cfg VerkleConfig) (*VerkleStateDB, error) {
	prefix := normalizeJMTPrefix(cfg.Prefix)
	inner, err := jmt.NewJMTStateDBWithDB(db, jmt.JMTConfig{
		DataDir:     normalizeJMTDataDir(cfg.DataDir),
		Prefix:      prefix,
		EnableKVLog: cfg.EnableKVLog,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create JMT-backed StateDB with existing DB: %w", err)
	}
	return &VerkleStateDB{inner: inner}, nil
}

func (s *VerkleStateDB) Get(key string) ([]byte, bool, error) {
	return s.inner.Get(key)
}

func (s *VerkleStateDB) GetAtVersion(key string, version uint64) ([]byte, error) {
	return s.inner.GetAtVersion(key, version)
}

func (s *VerkleStateDB) Exists(key string) (bool, error) {
	return s.inner.Exists(key)
}

func (s *VerkleStateDB) ApplyAccountUpdate(height uint64, kvs ...KVUpdate) error {
	return s.inner.ApplyAccountUpdate(height, kvs...)
}

func (s *VerkleStateDB) NewSession() (*VerkleStateDBSession, error) {
	sess, err := s.inner.NewSession()
	if err != nil {
		return nil, err
	}
	return &VerkleStateDBSession{inner: sess}, nil
}

func (s *VerkleStateDBSession) Get(key string) ([]byte, bool, error) {
	return s.inner.Get(key)
}

func (s *VerkleStateDBSession) GetKV(key string) ([]byte, error) {
	return s.inner.GetKV(key)
}

func (s *VerkleStateDBSession) ApplyUpdate(height uint64, kvs ...KVUpdate) error {
	return s.inner.ApplyUpdate(height, kvs...)
}

func (s *VerkleStateDBSession) Commit() error {
	return s.inner.Commit()
}

func (s *VerkleStateDBSession) Rollback() error {
	return s.inner.Rollback()
}

func (s *VerkleStateDBSession) Close() error {
	return s.inner.Close()
}

func (s *VerkleStateDBSession) Root() []byte {
	return s.inner.Root()
}

func (s *VerkleStateDB) Root() []byte {
	return s.inner.Root()
}

func (s *VerkleStateDB) CommitRoot(version uint64, root []byte) {
	s.inner.CommitRoot(version, root)
}

func (s *VerkleStateDB) Version() uint64 {
	return s.inner.Version()
}

func (s *VerkleStateDB) GetRootHash(version uint64) ([]byte, error) {
	return s.inner.GetRootHash(version)
}

func (s *VerkleStateDB) Prove(key string) (*VerkleProof, error) {
	return nil, errVerkleProofUnavailable
}

func (s *VerkleStateDB) VerifyProof(proof *VerkleProof, root []byte) bool {
	return false
}

func (s *VerkleStateDB) Prune(version uint64) error {
	return s.inner.Prune(version)
}

func (s *VerkleStateDB) Close() error {
	return s.inner.Close()
}

func (s *VerkleStateDB) IterateLatestSnapshot(fn func(key string, value []byte) error) error {
	return s.inner.IterateLatestSnapshot(fn)
}

func (s *VerkleStateDB) FlushAndRotate(epochEnd uint64) error {
	return s.inner.FlushAndRotate(epochEnd)
}
