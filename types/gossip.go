package types

import (
	"bytes"
	"dex/pb"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
)

// GossipPayload carries block gossip between nodes.
type GossipPayload struct {
	Block     *pb.Block `json:"block"`
	RequestID uint32    `json:"request_id"`
}

// gossipPayloadJSON is the wire envelope used on /gossipAnyMsg.
// Block uses protobuf-JSON to support oneof fields inside pb.AnyTx.
type gossipPayloadJSON struct {
	Block     json.RawMessage `json:"block"`
	RequestID uint32          `json:"request_id"`
}

func EncodeGossipPayload(payload *GossipPayload) ([]byte, error) {
	if payload == nil {
		return nil, fmt.Errorf("nil gossip payload")
	}
	if payload.Block == nil {
		return nil, fmt.Errorf("gossip payload missing block")
	}

	blockJSON, err := protojson.Marshal(payload.Block)
	if err != nil {
		return nil, fmt.Errorf("marshal gossip block: %w", err)
	}

	wire := gossipPayloadJSON{
		Block:     blockJSON,
		RequestID: payload.RequestID,
	}
	data, err := json.Marshal(&wire)
	if err != nil {
		return nil, fmt.Errorf("marshal gossip payload: %w", err)
	}
	return data, nil
}

func DecodeGossipPayload(data []byte) (*GossipPayload, error) {
	var wire gossipPayloadJSON
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("parse gossip payload JSON: %w", err)
	}

	trimmed := bytes.TrimSpace(wire.Block)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, fmt.Errorf("gossip payload missing block")
	}

	block := &pb.Block{}
	if err := protojson.Unmarshal(wire.Block, block); err == nil {
		return &GossipPayload{
			Block:     block,
			RequestID: wire.RequestID,
		}, nil
	}

	// Backward-compatible fallback for historical JSON encoding path.
	var legacy GossipPayload
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, fmt.Errorf("parse legacy gossip payload: %w", err)
	}
	if legacy.Block == nil {
		return nil, fmt.Errorf("legacy gossip payload missing block")
	}

	return &legacy, nil
}
