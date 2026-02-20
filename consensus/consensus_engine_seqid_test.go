package consensus

import (
	"dex/types"
	"testing"
)

func TestCollectConsensusSignatureSet_UsesSuccessRoundSeqID(t *testing.T) {
	store := NewMemoryBlockStore()
	sb := NewSnowball(nil)
	sb.successHistory = []SuccessRound{
		{
			Round:     1,
			SeqID:     7,
			Timestamp: 123,
			Votes: []types.FinalizationChit{
				{NodeID: "n1", PreferredID: "block-10"},
			},
		},
	}

	engine := &SnowmanEngine{
		nodeID:    "node-a",
		store:     store,
		snowballs: map[uint64]*Snowball{10: sb},
	}

	sigSet := engine.collectConsensusSignatureSet(10, "block-10")
	if sigSet == nil {
		t.Fatal("expected non-nil signature set")
	}
	if len(sigSet.Rounds) != 1 {
		t.Fatalf("expected 1 round, got %d", len(sigSet.Rounds))
	}
	if got := sigSet.Rounds[0].SeqId; got != 7 {
		t.Fatalf("unexpected seq_id: got %d want 7", got)
	}
}
