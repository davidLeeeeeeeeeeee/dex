package consensus

import (
	"dex/config"
	"dex/db"
	"dex/keys"
	"dex/logs"
	"dex/types"
	"fmt"
	"sync"
	"testing"
)

type mockStateSnapshotTransport struct {
	samplePeers []types.NodeID

	shardRespByPeer map[types.NodeID]*types.StateSnapshotShardsResponse
	shardErrByPeer  map[types.NodeID]error
	pageResp        map[string]*types.StateSnapshotPageResponse
	pageErr         map[string]error

	mu              sync.Mutex
	pageCallsByPeer map[types.NodeID]int
}

func snapshotPageKey(peer types.NodeID, shard, pageToken string) string {
	return fmt.Sprintf("%s|%s|%s", peer, shard, pageToken)
}

func (m *mockStateSnapshotTransport) Send(to types.NodeID, msg types.Message) error { return nil }
func (m *mockStateSnapshotTransport) Receive() <-chan types.Message                  { return nil }
func (m *mockStateSnapshotTransport) Broadcast(msg types.Message, peers []types.NodeID) {
}
func (m *mockStateSnapshotTransport) SamplePeers(exclude types.NodeID, count int) []types.NodeID {
	if count <= 0 {
		return nil
	}
	out := make([]types.NodeID, 0, count)
	for _, peer := range m.samplePeers {
		if peer == exclude {
			continue
		}
		out = append(out, peer)
		if len(out) >= count {
			break
		}
	}
	return out
}
func (m *mockStateSnapshotTransport) GetAllPeers(exclude types.NodeID) []types.NodeID {
	out := make([]types.NodeID, 0, len(m.samplePeers))
	for _, peer := range m.samplePeers {
		if peer == exclude {
			continue
		}
		out = append(out, peer)
	}
	return out
}

func (m *mockStateSnapshotTransport) FetchStateSnapshotShards(peer types.NodeID, targetHeight uint64) (*types.StateSnapshotShardsResponse, error) {
	if err := m.shardErrByPeer[peer]; err != nil {
		return nil, err
	}
	resp := m.shardRespByPeer[peer]
	if resp == nil {
		return nil, nil
	}

	out := &types.StateSnapshotShardsResponse{
		SnapshotHeight: resp.SnapshotHeight,
		Error:          resp.Error,
	}
	if len(resp.Shards) > 0 {
		out.Shards = make([]types.StateSnapshotShard, len(resp.Shards))
		copy(out.Shards, resp.Shards)
	}
	return out, nil
}

func (m *mockStateSnapshotTransport) FetchStateSnapshotPage(peer types.NodeID, snapshotHeight uint64, shard string, pageSize int, pageToken string) (*types.StateSnapshotPageResponse, error) {
	key := snapshotPageKey(peer, shard, pageToken)

	m.mu.Lock()
	if m.pageCallsByPeer == nil {
		m.pageCallsByPeer = make(map[types.NodeID]int)
	}
	m.pageCallsByPeer[peer]++
	m.mu.Unlock()

	if err := m.pageErr[key]; err != nil {
		return nil, err
	}
	resp := m.pageResp[key]
	if resp == nil {
		return nil, nil
	}

	out := &types.StateSnapshotPageResponse{
		SnapshotHeight: resp.SnapshotHeight,
		Shard:          resp.Shard,
		NextPageToken:  resp.NextPageToken,
		Error:          resp.Error,
	}
	if len(resp.Items) > 0 {
		out.Items = make([]types.StateSnapshotItem, len(resp.Items))
		for i, item := range resp.Items {
			valCopy := make([]byte, len(item.Value))
			copy(valCopy, item.Value)
			out.Items[i] = types.StateSnapshotItem{
				Key:   item.Key,
				Value: valCopy,
			}
		}
	}
	return out, nil
}

func (m *mockStateSnapshotTransport) pageCallCount(peer types.NodeID) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pageCallsByPeer[peer]
}

func TestSelectStateSyncPeers_PrioritizesHeightsThenSampling(t *testing.T) {
	transport := &mockStateSnapshotTransport{
		samplePeers: []types.NodeID{"node-self", "peer-2", "peer-5"},
	}
	sm := &SyncManager{
		nodeID:    "node-self",
		transport: transport,
		config:    &SyncConfig{StateSyncPeers: 3},
		PeerHeights: map[types.NodeID]uint64{
			"peer-1": 140,
			"peer-2": 130,
			"peer-3": 120,
			"peer-4": 90,
		},
	}

	peers := sm.selectStateSyncPeers(125, 3)
	want := []types.NodeID{"peer-1", "peer-2", "peer-5"}
	if len(peers) != len(want) {
		t.Fatalf("expected %d peers, got %d: %v", len(want), len(peers), peers)
	}
	for i := range want {
		if peers[i] != want[i] {
			t.Fatalf("peer[%d] mismatch: want=%s got=%s (all=%v)", i, want[i], peers[i], peers)
		}
	}
}

func TestFetchSnapshotShardWithFallback_SwitchesPeerOnError(t *testing.T) {
	transport := &mockStateSnapshotTransport{
		pageErr: map[string]error{
			snapshotPageKey("peer-1", "shard-a", ""): fmt.Errorf("peer-1 unavailable"),
		},
		pageResp: map[string]*types.StateSnapshotPageResponse{
			snapshotPageKey("peer-2", "shard-a", ""): {
				SnapshotHeight: 500,
				Shard:          "shard-a",
				Items: []types.StateSnapshotItem{
					{Key: keys.KeyAccount("alice"), Value: []byte("A")},
				},
				NextPageToken: "next-1",
			},
			snapshotPageKey("peer-2", "shard-a", "next-1"): {
				SnapshotHeight: 500,
				Shard:          "shard-a",
				Items: []types.StateSnapshotItem{
					{Key: keys.KeyBalance("alice", "USDT"), Value: []byte("B")},
				},
			},
		},
	}

	sm := &SyncManager{}
	updates, peer, err := sm.fetchSnapshotShardWithFallback(
		transport,
		[]types.NodeID{"peer-1", "peer-2"},
		0,
		500,
		"shard-a",
		100,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if peer != "peer-2" {
		t.Fatalf("expected fallback to peer-2, got %s", peer)
	}
	if len(updates) != 2 {
		t.Fatalf("expected 2 updates, got %d", len(updates))
	}
	if got := transport.pageCallCount("peer-1"); got == 0 {
		t.Fatalf("expected peer-1 to be attempted at least once")
	}
	if got := transport.pageCallCount("peer-2"); got < 2 {
		t.Fatalf("expected peer-2 to serve two pages, got %d", got)
	}
}

func TestPerformDistributedStateDBSync_UsesMultiplePeersAndBuildsSnapshot(t *testing.T) {
	cfg := config.DefaultConfig()
	logger := logs.NewNodeLogger("state-sync-test", 0)
	manager, err := db.NewManagerWithConfig(t.TempDir(), logger, cfg)
	if err != nil {
		t.Fatalf("create db manager: %v", err)
	}
	defer manager.Close()

	transport := &mockStateSnapshotTransport{
		samplePeers: []types.NodeID{"peer-1", "peer-2"},
		shardRespByPeer: map[types.NodeID]*types.StateSnapshotShardsResponse{
			"peer-1": {
				SnapshotHeight: 777,
				Shards: []types.StateSnapshotShard{
					{Shard: "shard-a", Count: 3},
					{Shard: "shard-b", Count: 1},
				},
			},
		},
		pageResp: map[string]*types.StateSnapshotPageResponse{
			snapshotPageKey("peer-1", "shard-a", ""): {
				SnapshotHeight: 777,
				Shard:          "shard-a",
				Items: []types.StateSnapshotItem{
					{Key: keys.KeyAccount("alice"), Value: []byte("acc-alice")},
					{Key: keys.KeyBalance("alice", "USDT"), Value: []byte("1000")},
				},
				NextPageToken: "a-2",
			},
			snapshotPageKey("peer-1", "shard-a", "a-2"): {
				SnapshotHeight: 777,
				Shard:          "shard-a",
				Items: []types.StateSnapshotItem{
					{Key: keys.KeyFrostConfig(), Value: []byte("cfg-1")},
				},
			},
			snapshotPageKey("peer-2", "shard-b", ""): {
				SnapshotHeight: 777,
				Shard:          "shard-b",
				Items: []types.StateSnapshotItem{
					{Key: keys.KeyAccount("bob"), Value: []byte("acc-bob")},
				},
			},
		},
	}

	sm := &SyncManager{
		nodeID:    "node-self",
		transport: transport,
		store: &RealBlockStore{
			dbManager: manager,
		},
		config: &SyncConfig{
			StateSyncPeers:            2,
			StateSyncShardConcurrency: 2,
			StateSyncPageSize:         2,
		},
		Logger: logger,
		PeerHeights: map[types.NodeID]uint64{
			"peer-1": 900,
			"peer-2": 860,
		},
	}

	if ok := sm.performDistributedStateDBSync(800); !ok {
		t.Fatalf("performDistributedStateDBSync returned false")
	}

	if got := transport.pageCallCount("peer-1"); got == 0 {
		t.Fatalf("expected peer-1 to serve state snapshot page(s)")
	}
	if got := transport.pageCallCount("peer-2"); got == 0 {
		t.Fatalf("expected peer-2 to serve state snapshot page(s)")
	}

	got := collectSnapshotAtHeight(t, manager, 777)
	want := map[string]string{
		keys.KeyAccount("alice"):        "acc-alice",
		keys.KeyBalance("alice", "USDT"): "1000",
		keys.KeyFrostConfig():           "cfg-1",
		keys.KeyAccount("bob"):          "acc-bob",
	}
	for k, v := range want {
		if gotVal, ok := got[k]; !ok || gotVal != v {
			t.Fatalf("snapshot key mismatch for %s: want=%q got=%q present=%v", k, v, gotVal, ok)
		}
	}
}

func collectSnapshotAtHeight(t *testing.T, manager *db.Manager, height uint64) map[string]string {
	t.Helper()

	shards, err := manager.ListStateSnapshotShards(height)
	if err != nil {
		t.Fatalf("list snapshot shards: %v", err)
	}

	out := make(map[string]string)
	for _, shard := range shards {
		if shard.Count <= 0 {
			continue
		}

		token := ""
		for i := 0; i < 1000; i++ {
			page, err := manager.PageStateSnapshotShard(height, shard.Shard, 256, token)
			if err != nil {
				t.Fatalf("page snapshot shard=%s token=%s: %v", shard.Shard, token, err)
			}
			for _, item := range page.Items {
				out[item.Key] = string(item.Value)
			}
			if page.NextPageToken == "" || page.NextPageToken == token {
				break
			}
			token = page.NextPageToken
		}
	}
	return out
}
