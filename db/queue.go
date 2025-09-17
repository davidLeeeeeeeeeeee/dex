package db

type WriteTask struct {
	Key   []byte
	Value []byte
	Op    WriteOp // 可以是 “Set” or “Delete”
}

type WriteOp int

const (
	OpSet WriteOp = iota
	OpDelete
)
