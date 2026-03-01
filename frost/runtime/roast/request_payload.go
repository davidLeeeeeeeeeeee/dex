package roast

import (
	"encoding/binary"
)

var roastRequestPayloadMagic = [4]byte{'R', 'R', 'Q', '1'}

const roastRequestPayloadVersion byte = 1

// encodeRoastRequestPayload packs messages and aggregated nonces into one
// payload for coordinator -> participant requests.
func encodeRoastRequestPayload(messages [][]byte, aggregatedNonces []byte) []byte {
	msgCount := len(messages)
	headerLen := 4 + 1 + 2 + 4 // magic + version + msg_count + nonce_len

	totalMsgsLen := 0
	for _, m := range messages {
		totalMsgsLen += 2 + len(m) // msg_len + msg_bytes
	}

	out := make([]byte, headerLen+totalMsgsLen+len(aggregatedNonces))
	copy(out[:4], roastRequestPayloadMagic[:])
	out[4] = roastRequestPayloadVersion
	binary.BigEndian.PutUint16(out[5:7], uint16(msgCount))

	off := 7
	for _, m := range messages {
		binary.BigEndian.PutUint16(out[off:off+2], uint16(len(m)))
		off += 2
		copy(out[off:off+len(m)], m)
		off += len(m)
	}

	binary.BigEndian.PutUint32(out[off:off+4], uint32(len(aggregatedNonces)))
	off += 4
	copy(out[off:], aggregatedNonces)
	return out
}

// decodeRoastRequestPayload decodes payload produced by
// encodeRoastRequestPayload. Returns ok=false for legacy payloads.
func decodeRoastRequestPayload(data []byte) (messages [][]byte, aggregatedNonces []byte, ok bool) {
	if len(data) < 11 {
		return nil, nil, false
	}
	if data[0] != roastRequestPayloadMagic[0] ||
		data[1] != roastRequestPayloadMagic[1] ||
		data[2] != roastRequestPayloadMagic[2] ||
		data[3] != roastRequestPayloadMagic[3] {
		return nil, nil, false
	}
	if data[4] != roastRequestPayloadVersion {
		return nil, nil, false
	}

	msgCount := int(binary.BigEndian.Uint16(data[5:7]))
	off := 7

	msgs := make([][]byte, 0, msgCount)
	for i := 0; i < msgCount; i++ {
		if off+2 > len(data) {
			return nil, nil, false
		}
		msgLen := int(binary.BigEndian.Uint16(data[off : off+2]))
		off += 2
		if msgLen < 0 || off+msgLen > len(data) {
			return nil, nil, false
		}
		msg := make([]byte, msgLen)
		copy(msg, data[off:off+msgLen])
		off += msgLen
		msgs = append(msgs, msg)
	}

	if off+4 > len(data) {
		return nil, nil, false
	}
	nonceLen := int(binary.BigEndian.Uint32(data[off : off+4]))
	off += 4
	if nonceLen < 0 || off+nonceLen > len(data) {
		return nil, nil, false
	}

	nonceBlob := make([]byte, nonceLen)
	copy(nonceBlob, data[off:off+nonceLen])
	return msgs, nonceBlob, true
}
