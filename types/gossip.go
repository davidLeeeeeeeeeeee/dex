package types

import (
	"dex/pb"
	"encoding/binary"
	"fmt"

	"google.golang.org/protobuf/proto"
)

// GossipPayload carries block gossip between nodes.
type GossipPayload struct {
	Block     *pb.Block `json:"block"`
	RequestID uint32    `json:"request_id"`
}

var gossipWireMagic = [4]byte{'G', 'P', 'B', '1'}

const gossipWireHeaderLen = 8 // 4 bytes magic + 4 bytes request_id

func EncodeGossipPayload(payload *GossipPayload) ([]byte, error) {
	if payload == nil {
		return nil, fmt.Errorf("nil gossip payload")
	}
	if payload.Block == nil {
		return nil, fmt.Errorf("gossip payload missing block")
	}

	blockBytes, err := proto.Marshal(payload.Block)
	if err != nil {
		return nil, fmt.Errorf("marshal gossip block proto: %w", err)
	}

	data := make([]byte, gossipWireHeaderLen+len(blockBytes))
	copy(data[:4], gossipWireMagic[:])
	binary.BigEndian.PutUint32(data[4:8], payload.RequestID)
	copy(data[gossipWireHeaderLen:], blockBytes)

	return data, nil
}

func DecodeGossipPayload(data []byte) (*GossipPayload, error) {
	if len(data) < gossipWireHeaderLen {
		return nil, fmt.Errorf("gossip payload too short: got %d bytes", len(data))
	}
	if data[0] != gossipWireMagic[0] || data[1] != gossipWireMagic[1] || data[2] != gossipWireMagic[2] || data[3] != gossipWireMagic[3] {
		return nil, fmt.Errorf("unsupported gossip payload wire format")
	}
	if len(data) == gossipWireHeaderLen {
		return nil, fmt.Errorf("gossip payload missing block")
	}

	requestID := binary.BigEndian.Uint32(data[4:8])
	block := &pb.Block{}
	if err := proto.Unmarshal(data[gossipWireHeaderLen:], block); err != nil {
		return nil, fmt.Errorf("unmarshal gossip block proto: %w", err)
	}

	return &GossipPayload{
		Block:     block,
		RequestID: requestID,
	}, nil
}
