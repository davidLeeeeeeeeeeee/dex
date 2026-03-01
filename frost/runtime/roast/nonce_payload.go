package roast

import (
	"encoding/binary"
)

var aggregatedNoncePayloadMagic = [4]byte{'R', 'N', 'O', 'N'}

const aggregatedNoncePayloadVersion byte = 1

// encodeAggregatedNoncePayload encodes signer IDs and raw nonce blob into a
// self-describing payload so participants can recover real signer IDs even
// after selected-set rotation.
func encodeAggregatedNoncePayload(signerIDs []int, nonceBlob []byte) []byte {
	count := len(signerIDs)
	if count == 0 {
		return nil
	}

	headerLen := 4 + 1 + 2 + count*2
	out := make([]byte, headerLen+len(nonceBlob))
	copy(out[:4], aggregatedNoncePayloadMagic[:])
	out[4] = aggregatedNoncePayloadVersion
	binary.BigEndian.PutUint16(out[5:7], uint16(count))

	off := 7
	for _, id := range signerIDs {
		if id < 0 {
			id = 0
		}
		if id > 0xFFFF {
			id = 0xFFFF
		}
		binary.BigEndian.PutUint16(out[off:off+2], uint16(id))
		off += 2
	}

	copy(out[off:], nonceBlob)
	return out
}

// decodeAggregatedNoncePayload decodes payload produced by
// encodeAggregatedNoncePayload.
// Returns ok=false for legacy payloads (without header).
func decodeAggregatedNoncePayload(data []byte) (signerIDs []int, nonceBlob []byte, ok bool) {
	if len(data) < 7 {
		return nil, nil, false
	}
	if data[0] != aggregatedNoncePayloadMagic[0] ||
		data[1] != aggregatedNoncePayloadMagic[1] ||
		data[2] != aggregatedNoncePayloadMagic[2] ||
		data[3] != aggregatedNoncePayloadMagic[3] {
		return nil, nil, false
	}
	if data[4] != aggregatedNoncePayloadVersion {
		return nil, nil, false
	}

	count := int(binary.BigEndian.Uint16(data[5:7]))
	headerLen := 7 + count*2
	if count <= 0 || len(data) < headerLen {
		return nil, nil, false
	}

	ids := make([]int, count)
	off := 7
	for i := 0; i < count; i++ {
		ids[i] = int(binary.BigEndian.Uint16(data[off : off+2]))
		off += 2
	}

	return ids, data[off:], true
}
