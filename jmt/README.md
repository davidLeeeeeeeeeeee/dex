# smt

A Go library that implements a Sparse Merkle tree for a key-value map. The tree implements the same optimisations specified in the [Libra whitepaper][libra whitepaper], to reduce the number of hash operations required per tree operation to O(k) where k is the number of non-empty elements in the tree.

[![Tests](https://github.com/celestiaorg/smt/actions/workflows/test.yml/badge.svg)](https://github.com/celestiaorg/smt/actions/workflows/test.yml)
[![codecov](https://codecov.io/gh/celestiaorg/smt/branch/master/graph/badge.svg?token=U3GGEDSA94)](https://codecov.io/gh/celestiaorg/smt)
[![GoDoc](https://godoc.org/github.com/celestiaorg/smt?status.svg)](https://godoc.org/github.com/celestiaorg/smt)

## Features

- **Standard SMT Operations**: Support for `Get`, `Update`, `Delete`, and `Prove`.
- **Thread Safety**: `SparseMerkleTree` is thread-safe, supporting concurrent reads and safe writes using `sync.RWMutex`.
- **Thread-safe Hashing**: Internal hashing uses `sync.Pool` to manage `hash.Hash` instances efficiently and safely across goroutines.
- **Batch Updates**: Support for updating multiple keys in a single operation via `UpdateBatch`, providing optimized performance for bulk updates.
- **Compact Proofs**: Efficient proof representation using bitmasks for placeholder nodes.

## Example

```go
package main

import (
	"crypto/sha256"
	"fmt"

	"github.com/celestiaorg/smt"
)

func main() {
	// Initialise two new key-value store to store the nodes and values of the tree
	nodeStore := smt.NewSimpleMap()
	valueStore := smt.NewSimpleMap()
	// Initialise the tree
	tree := smt.NewSparseMerkleTree(nodeStore, valueStore, sha256.New())

	// Update the key "foo" with the value "bar"
	_, _ = tree.Update([]byte("foo"), []byte("bar"))

	// Batch update multiple keys
	keys := [][]byte{[]byte("key1"), []byte("key2")}
	values := [][]byte{[]byte("val1"), []byte("val2")}
	_, _ = tree.UpdateBatch(keys, values)

	// Generate a Merkle proof for foo=bar
	proof, _ := tree.Prove([]byte("foo"))
	root := tree.Root() // We also need the current tree root for the proof

	// Verify the Merkle proof for foo=bar
	if smt.VerifyProof(proof, root, []byte("foo"), []byte("bar"), sha256.New()) {
		fmt.Println("Proof verification succeeded.")
	} else {
		fmt.Println("Proof verification failed.")
	}
}
```

[libra whitepaper]: https://diem-developers-components.netlify.app/papers/the-diem-blockchain/2020-05-26.pdf
