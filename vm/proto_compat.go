package vm

import (
	"bytes"
	"fmt"

	"google.golang.org/protobuf/proto"
)

// unmarshalProtoCompat tolerates zero-padded values from StateDB by retrying
// after trimming trailing NUL bytes when the first unmarshal fails.
func unmarshalProtoCompat(data []byte, msg proto.Message) error {
	if err := proto.Unmarshal(data, msg); err == nil {
		return nil
	} else {
		trimmed := bytes.TrimRight(data, "\x00")
		if len(trimmed) == 0 || len(trimmed) == len(data) {
			return err
		}
		proto.Reset(msg)
		if err2 := proto.Unmarshal(trimmed, msg); err2 == nil {
			return nil
		} else {
			return fmt.Errorf("proto unmarshal failed (raw=%v, trimmed=%v)", err, err2)
		}
	}
}
