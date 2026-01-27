package smt

import (
	"bytes"
	"hash"
	"reflect"
	"sync"
)

var leafPrefix = []byte{0}
var nodePrefix = []byte{1}

type treeHasher struct {
	hasherPool *sync.Pool
	zeroValue  []byte
}

func newTreeHasher(hasher hash.Hash) *treeHasher {
	th := treeHasher{}
	th.hasherPool = &sync.Pool{
		New: func() interface{} {
			return reflect.New(reflect.TypeOf(hasher).Elem()).Interface().(hash.Hash)
		},
	}
	th.zeroValue = make([]byte, th.pathSize())

	return &th
}

func (th *treeHasher) digest(data []byte) []byte {
	h := th.hasherPool.Get().(hash.Hash)
	defer th.hasherPool.Put(h)
	h.Reset()
	h.Write(data)
	return h.Sum(nil)
}

func (th *treeHasher) path(key []byte) []byte {
	return th.digest(key)
}

func (th *treeHasher) digestLeaf(path []byte, leafData []byte) ([]byte, []byte) {
	value := make([]byte, 0, len(leafPrefix)+len(path)+len(leafData))
	value = append(value, leafPrefix...)
	value = append(value, path...)
	value = append(value, leafData...)

	h := th.hasherPool.Get().(hash.Hash)
	defer th.hasherPool.Put(h)
	h.Reset()
	h.Write(value)
	sum := h.Sum(nil)

	return sum, value
}

func (th *treeHasher) parseLeaf(data []byte) ([]byte, []byte) {
	if len(data) < len(leafPrefix)+th.pathSize() {
		return nil, nil
	}
	return data[len(leafPrefix) : th.pathSize()+len(leafPrefix)], data[len(leafPrefix)+th.pathSize():]
}

func (th *treeHasher) isLeaf(data []byte) bool {
	if len(data) < len(leafPrefix) {
		return false
	}
	return bytes.Equal(data[:len(leafPrefix)], leafPrefix)
}

func (th *treeHasher) digestNode(leftData []byte, rightData []byte) ([]byte, []byte) {
	value := make([]byte, 0, len(nodePrefix)+len(leftData)+len(rightData))
	value = append(value, nodePrefix...)
	value = append(value, leftData...)
	value = append(value, rightData...)

	h := th.hasherPool.Get().(hash.Hash)
	defer th.hasherPool.Put(h)
	h.Reset()
	h.Write(value)
	sum := h.Sum(nil)

	return sum, value
}

func (th *treeHasher) parseNode(data []byte) ([]byte, []byte) {
	if len(data) < len(nodePrefix)+2*th.pathSize() {
		return nil, nil
	}
	return data[len(nodePrefix) : th.pathSize()+len(nodePrefix)], data[len(nodePrefix)+th.pathSize():]
}

func (th *treeHasher) pathSize() int {
	h := th.hasherPool.Get().(hash.Hash)
	defer th.hasherPool.Put(h)
	return h.Size()
}

func (th *treeHasher) placeholder() []byte {
	return th.zeroValue
}
