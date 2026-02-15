package handlers

import (
	"dex/pb"
	"fmt"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/reflect/protoreflect"
)

const maxInvalidUTF8Paths = 8

func buildGetBlockMarshalDetail(blockID, source string, block *pb.Block, marshalErr error) string {
	height := uint64(0)
	txs := 0
	shortTxBytes := 0
	if block != nil {
		if block.Header != nil {
			height = block.Header.Height
		}
		txs = len(block.Body)
		shortTxBytes = len(block.ShortTxs)
	}

	invalidPaths := collectInvalidUTF8Paths(&pb.GetBlockResponse{Block: block}, maxInvalidUTF8Paths)
	invalidUTF8 := "none"
	if len(invalidPaths) > 0 {
		invalidUTF8 = strings.Join(invalidPaths, ",")
	}

	return fmt.Sprintf(
		"block_id=%s source=%s height=%d txs=%d short_txs_bytes=%d marshal_err=%v invalid_utf8_paths=%s",
		blockID, source, height, txs, shortTxBytes, marshalErr, invalidUTF8,
	)
}

func collectInvalidUTF8Paths(msg protoreflect.ProtoMessage, limit int) []string {
	if msg == nil || limit <= 0 {
		return nil
	}
	out := make([]string, 0, limit)
	collectInvalidUTF8(msg.ProtoReflect(), msg.ProtoReflect().Descriptor().Name(), &out, limit)
	return out
}

func collectInvalidUTF8(m protoreflect.Message, prefix protoreflect.Name, out *[]string, limit int) {
	if !m.IsValid() || len(*out) >= limit {
		return
	}

	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len() && len(*out) < limit; i++ {
		fd := fields.Get(i)
		if !m.Has(fd) {
			continue
		}

		fieldPath := string(prefix) + "." + string(fd.Name())
		val := m.Get(fd)

		switch {
		case fd.IsList():
			list := val.List()
			for j := 0; j < list.Len() && len(*out) < limit; j++ {
				itemPath := fmt.Sprintf("%s[%d]", fieldPath, j)
				item := list.Get(j)
				switch fd.Kind() {
				case protoreflect.StringKind:
					if !utf8.ValidString(item.String()) {
						*out = append(*out, itemPath)
					}
				case protoreflect.MessageKind:
					child := item.Message()
					if child.IsValid() {
						collectInvalidUTF8(child, protoreflect.Name(itemPath), out, limit)
					}
				}
			}
		case fd.IsMap():
			mv := val.Map()
			mv.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
				if len(*out) >= limit {
					return false
				}
				keyText := truncateForPath(fmt.Sprintf("%v", k.Interface()), 64)
				itemPath := fmt.Sprintf("%s[%s]", fieldPath, keyText)

				if fd.MapKey().Kind() == protoreflect.StringKind && !utf8.ValidString(k.String()) {
					*out = append(*out, itemPath+"{key}")
				}

				switch fd.MapValue().Kind() {
				case protoreflect.StringKind:
					if !utf8.ValidString(v.String()) {
						*out = append(*out, itemPath)
					}
				case protoreflect.MessageKind:
					child := v.Message()
					if child.IsValid() {
						collectInvalidUTF8(child, protoreflect.Name(itemPath), out, limit)
					}
				}
				return true
			})
		default:
			switch fd.Kind() {
			case protoreflect.StringKind:
				if !utf8.ValidString(val.String()) {
					*out = append(*out, fieldPath)
				}
			case protoreflect.MessageKind:
				child := val.Message()
				if child.IsValid() {
					collectInvalidUTF8(child, protoreflect.Name(fieldPath), out, limit)
				}
			}
		}
	}
}

func truncateForPath(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

