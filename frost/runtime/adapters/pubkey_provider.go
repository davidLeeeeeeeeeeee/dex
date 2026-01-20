package adapters

import (
	"dex/frost/runtime"
	"dex/keys"
	"dex/pb"
	"fmt"
	"sync"

	"google.golang.org/protobuf/proto"
)

// StatePubKeyProvider reads miner public keys from the Top10000 snapshot.
type StatePubKeyProvider struct {
	reader runtime.ChainStateReader
	mu     sync.RWMutex
	cache  map[string][]byte
}

func NewStatePubKeyProvider(reader runtime.ChainStateReader) *StatePubKeyProvider {
	return &StatePubKeyProvider{
		reader: reader,
		cache:  make(map[string][]byte),
	}
}

func (p *StatePubKeyProvider) GetMinerSigningPubKey(minerID string, signAlgo pb.SignAlgo) ([]byte, error) {
	if minerID == "" {
		return nil, fmt.Errorf("empty miner id")
	}
	if p == nil || p.reader == nil {
		return nil, fmt.Errorf("state reader not available")
	}

	if pub := p.getCached(minerID); pub != nil {
		return pub, nil
	}
	if err := p.reload(); err != nil {
		return nil, err
	}
	if pub := p.getCached(minerID); pub != nil {
		return pub, nil
	}

	return nil, fmt.Errorf("pubkey not found for miner %s", minerID)
}

func (p *StatePubKeyProvider) getCached(minerID string) []byte {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if pub, ok := p.cache[minerID]; ok {
		out := make([]byte, len(pub))
		copy(out, pub)
		return out
	}
	return nil
}

func (p *StatePubKeyProvider) reload() error {
	data, exists, err := p.reader.Get(keys.KeyFrostTop10000())
	if err != nil {
		return fmt.Errorf("read top10000: %w", err)
	}
	if !exists || len(data) == 0 {
		return fmt.Errorf("top10000 not found")
	}

	var top10000 pb.FrostTop10000
	if err := proto.Unmarshal(data, &top10000); err != nil {
		return fmt.Errorf("unmarshal top10000: %w", err)
	}

	cache := make(map[string][]byte, len(top10000.Addresses))
	for i, addr := range top10000.Addresses {
		if i >= len(top10000.PublicKeys) {
			break
		}
		if len(addr) == 0 || len(top10000.PublicKeys[i]) == 0 {
			continue
		}
		pub := make([]byte, len(top10000.PublicKeys[i]))
		copy(pub, top10000.PublicKeys[i])
		cache[addr] = pub
	}

	p.mu.Lock()
	p.cache = cache
	p.mu.Unlock()
	return nil
}

var _ runtime.MinerPubKeyProvider = (*StatePubKeyProvider)(nil)
