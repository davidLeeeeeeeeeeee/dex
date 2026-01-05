// frost/runtime/roast_wire.go
// Wire helpers to encode/decode ROAST envelopes through pb.FrostEnvelope.

package runtime

import (
	"encoding/json"
	"errors"

	"dex/pb"
)

var (
	ErrInvalidPBEnvelope  = errors.New("invalid pb.FrostEnvelope")
	ErrEmptyRoastPayload  = errors.New("empty roast payload")
	ErrInvalidRoastKind   = errors.New("invalid roast kind")
	ErrUnknownRoastEncode = errors.New("unable to encode roast envelope")
)

// RoastEnvelopeFromPB decodes a RoastEnvelope from pb.FrostEnvelope payload.
func RoastEnvelopeFromPB(env *pb.FrostEnvelope) (*RoastEnvelope, error) {
	if env == nil {
		return nil, ErrInvalidPBEnvelope
	}
	if len(env.Payload) == 0 {
		return nil, ErrEmptyRoastPayload
	}

	var msg RoastEnvelope
	if err := json.Unmarshal(env.Payload, &msg); err != nil {
		return nil, err
	}

	if msg.SessionID == "" {
		msg.SessionID = env.JobId
	}
	if msg.From == "" && env.From != "" {
		msg.From = NodeID(env.From)
	}
	if msg.Kind == "" {
		kind := roastKindFromPB(env.Kind)
		if kind == "" {
			return nil, ErrInvalidRoastKind
		}
		msg.Kind = kind
	} else {
		expected := pbKindFromRoast(msg.Kind)
		if expected == pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_UNSPECIFIED {
			return nil, ErrInvalidRoastKind
		}
		if env.Kind != pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_UNSPECIFIED && env.Kind != expected {
			return nil, ErrInvalidRoastKind
		}
	}

	return &msg, nil
}

// PBEnvelopeFromRoast encodes a RoastEnvelope into pb.FrostEnvelope payload.
func PBEnvelopeFromRoast(msg *RoastEnvelope) (*pb.FrostEnvelope, error) {
	if msg == nil {
		return nil, ErrUnknownRoastEncode
	}
	kind := pbKindFromRoast(msg.Kind)
	if kind == pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_UNSPECIFIED {
		return nil, ErrInvalidRoastKind
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}

	return &pb.FrostEnvelope{
		From:    string(msg.From),
		Kind:    kind,
		Payload: payload,
		JobId:   msg.SessionID,
	}, nil
}

func pbKindFromRoast(kind string) pb.FrostEnvelopeKind {
	switch kind {
	case "NonceRequest", "SignRequest":
		return pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_ROAST_REQUEST
	case "NonceCommit", "SigShare":
		return pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_ROAST_RESPONSE
	default:
		return pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_UNSPECIFIED
	}
}

func roastKindFromPB(kind pb.FrostEnvelopeKind) string {
	switch kind {
	case pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_ROAST_REQUEST:
		return "NonceRequest"
	case pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_ROAST_RESPONSE:
		return "NonceCommit"
	default:
		return ""
	}
}
