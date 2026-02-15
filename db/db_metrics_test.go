package db

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestNormalizeWriteQueueKeyBucket(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{
			name: "empty",
			key:  "",
			want: "empty",
		},
		{
			name: "pending anytx strips version and hash",
			key:  "v1_pending_anytx_0xabc123",
			want: "pending_anytx",
		},
		{
			name: "block data strips version and hash",
			key:  "v1_blockdata_0xdeadbeef",
			want: "blockdata",
		},
		{
			name: "frost planning log family",
			key:  "v1_frost_planning_log_123",
			want: "frost_planning_log",
		},
		{
			name: "unknown key falls back to first two parts",
			key:  "foo_bar_baz",
			want: "foo_bar",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeWriteQueueKeyBucket(tt.key)
			if got != tt.want {
				t.Fatalf("normalizeWriteQueueKeyBucket(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestSnapshotCounterMapDeltaResetsCounters(t *testing.T) {
	manager := &Manager{}
	var counterMap sync.Map

	counter := &atomic.Uint64{}
	counter.Store(3)
	counterMap.Store("pending_anytx", counter)

	first := manager.snapshotCounterMapDelta(&counterMap)
	if first["pending_anytx"] != 3 {
		t.Fatalf("first snapshot delta = %d, want 3", first["pending_anytx"])
	}

	second := manager.snapshotCounterMapDelta(&counterMap)
	if len(second) != 0 {
		t.Fatalf("second snapshot should be empty, got %v", second)
	}

	counter.Add(2)
	third := manager.snapshotCounterMapDelta(&counterMap)
	if third["pending_anytx"] != 2 {
		t.Fatalf("third snapshot delta = %d, want 2", third["pending_anytx"])
	}
}

func TestTopCounterValuesOrder(t *testing.T) {
	cur := map[string]uint64{
		"bucket_b": 2,
		"bucket_a": 2,
		"bucket_c": 5,
	}

	top := topCounterValues(cur, 2)
	if len(top) != 2 {
		t.Fatalf("top len = %d, want 2", len(top))
	}
	if top[0] != "bucket_c=5" {
		t.Fatalf("top[0] = %q, want %q", top[0], "bucket_c=5")
	}
	if top[1] != "bucket_a=2" {
		t.Fatalf("top[1] = %q, want %q", top[1], "bucket_a=2")
	}
}
