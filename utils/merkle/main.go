package main

import (
	cryptoRand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"time"

	"github.com/celestiaorg/smt"
)

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = cryptoRand.Read(b)
	return b
}

func main() {
	updates := flag.Int("updates", 100000, "number of updates")
	keyLen := flag.Int("keylen", 32, "key length in bytes")
	valLen := flag.Int("vallen", 32, "value length in bytes")
	flag.Parse()

	// 内存 map 存储（节点/值），sha256 作为哈希器
	nodeStore := smt.NewSimpleMap()
	valStore := smt.NewSimpleMap()
	tree := smt.NewSparseMerkleTree(nodeStore, valStore, sha256.New())

	start := time.Now()
	for i := 0; i < *updates; i++ {
		k := randBytes(*keyLen)
		v := randBytes(*valLen)
		if _, err := tree.Update(k, v); err != nil {
			panic(err)
		}
	}
	elapsed := time.Since(start)

	root := tree.Root()
	fmt.Printf("[SMT] updates=%d, total=%v, per_update=%.3f ms, root=%s\n",
		*updates, elapsed, float64(elapsed.Microseconds())/1000.0/float64(*updates),
		hex.EncodeToString(root[:]))
}
