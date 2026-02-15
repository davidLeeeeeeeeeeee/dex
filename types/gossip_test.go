package types

import (
	"dex/pb"
	"testing"
)

func TestEncodeDecodeGossipPayloadWithOneofTx(t *testing.T) {
	tx := &pb.AnyTx{
		Content: &pb.AnyTx_Transaction{
			Transaction: &pb.Transaction{
				To:     "addr-1",
				Amount: "42",
			},
		},
	}
	payload := &GossipPayload{
		Block: &pb.Block{
			BlockHash: "block-1",
			Header: &pb.BlockHeader{
				Height: 1,
			},
			Body: []*pb.AnyTx{tx},
		},
		RequestID: 99,
	}

	data, err := EncodeGossipPayload(payload)
	if err != nil {
		t.Fatalf("EncodeGossipPayload() error = %v", err)
	}

	got, err := DecodeGossipPayload(data)
	if err != nil {
		t.Fatalf("DecodeGossipPayload() error = %v", err)
	}

	if got.RequestID != payload.RequestID {
		t.Fatalf("requestID mismatch: got %d want %d", got.RequestID, payload.RequestID)
	}
	if got.Block == nil {
		t.Fatal("decoded block is nil")
	}
	if got.Block.BlockHash != payload.Block.BlockHash {
		t.Fatalf("block hash mismatch: got %s want %s", got.Block.BlockHash, payload.Block.BlockHash)
	}
	if len(got.Block.Body) != 1 {
		t.Fatalf("tx body count mismatch: got %d want 1", len(got.Block.Body))
	}
	if got.Block.Body[0].GetTransaction() == nil {
		t.Fatal("decoded oneof transaction is nil")
	}
	if got.Block.Body[0].GetTransaction().Amount != "42" {
		t.Fatalf("decoded transaction amount mismatch: got %s want 42", got.Block.Body[0].GetTransaction().Amount)
	}
}

func TestEncodeGossipPayloadMissingBlock(t *testing.T) {
	_, err := EncodeGossipPayload(&GossipPayload{RequestID: 1})
	if err == nil {
		t.Fatal("expected error for missing block")
	}
}
