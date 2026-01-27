package smt

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

func TestUpdateBatchCorrectness(t *testing.T) {
	smn, smv := NewSimpleMap(), NewSimpleMap()
	smt := NewSparseMerkleTree(smn, smv, sha256.New())

	keys := [][]byte{
		[]byte("key1"),
		[]byte("key2"),
		[]byte("key3"),
	}
	values := [][]byte{
		[]byte("value1"),
		[]byte("value2"),
		[]byte("value3"),
	}

	// Update batch
	rootBatch, err := smt.UpdateBatch(keys, values)
	if err != nil {
		t.Fatalf("UpdateBatch failed: %v", err)
	}

	// Verify all keys
	for i, key := range keys {
		val, err := smt.Get(key)
		if err != nil {
			t.Errorf("Get failed for %s: %v", string(key), err)
		}
		if string(val) != string(values[i]) {
			t.Errorf("Expected %s, got %s", string(values[i]), string(val))
		}
	}

	// Compare with sequential updates
	smn2, smv2 := NewSimpleMap(), NewSimpleMap()
	smt2 := NewSparseMerkleTree(smn2, smv2, sha256.New())
	var rootSeq []byte
	for i := 0; i < len(keys); i++ {
		rootSeq, _ = smt2.Update(keys[i], values[i])
	}

	if fmt.Sprintf("%x", rootBatch) != fmt.Sprintf("%x", rootSeq) {
		t.Errorf("Batch root %x != Sequential root %x", rootBatch, rootSeq)
	}
}
