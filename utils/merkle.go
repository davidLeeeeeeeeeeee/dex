package utils

import "crypto/sha256"

const TxPerMerkleTree = 1000

// BuildBlockHash 根据所有交易构建最终的Block哈希(多棵1000笔交易的Merkle树)
func BuildBlockHash(allTxs [][]byte) []byte {
	if len(allTxs) == 0 {
		// 没有交易时可定义一个默认值
		return Sha256Hash([]byte("empty_block"))
	}

	// 分片
	chunks := chunkTransactions(allTxs, TxPerMerkleTree)

	// 对每个chunk构建默克尔树获得merkle root
	var merkleRoots [][]byte
	for _, c := range chunks {
		root := buildMerkleRoot(c)
		merkleRoots = append(merkleRoots, root)
	}

	// 如果只有一个root，就直接返回
	if len(merkleRoots) == 1 {
		return merkleRoots[0]
	}

	// 否则对这些root再次构建默克尔树，直到只剩一个root
	return buildMerkleRoot(merkleRoots)
}

func chunkTransactions(allTxs [][]byte, chunkSize int) [][][]byte {
	var chunks [][][]byte
	total := len(allTxs)
	for start := 0; start < total; start += chunkSize {
		end := start + chunkSize
		if end > total {
			end = total
		}
		chunk := allTxs[start:end]

		// 不足 chunkSize 时补齐
		if len(chunk) < chunkSize {
			lastTx := chunk[len(chunk)-1]
			for len(chunk) < chunkSize {
				chunk = append(chunk, lastTx)
			}
		}
		chunks = append(chunks, chunk)
	}
	return chunks
}

func buildMerkleRoot(leaves [][]byte) []byte {
	if len(leaves) == 1 {
		// 对单leaf进行hash，也可直接返回leaf，看需求
		return Sha256Hash(leaves[0])
	}
	level := hashLeaves(leaves)
	for len(level) > 1 {
		level = buildMerkleLevel(level)
	}
	return level[0]
}

func hashLeaves(data [][]byte) [][]byte {
	hashed := make([][]byte, len(data))
	for i, d := range data {
		hashed[i] = Sha256Hash(d)
	}
	return hashed
}
func sha256Hash(data []byte) []byte {
	sum := sha256.Sum256(data)
	// sum 是 [32]byte，转成切片返回
	return sum[:]
}
func buildMerkleLevel(nodes [][]byte) [][]byte {
	// 如果节点数是奇数，重复最后一个节点
	if len(nodes)%2 != 0 {
		nodes = append(nodes, nodes[len(nodes)-1])
	}
	nextLevel := make([][]byte, 0, len(nodes)/2)
	for i := 0; i < len(nodes); i += 2 {
		// 拼接左右节点后用 sha256
		combined := append(nodes[i], nodes[i+1]...)
		h := sha256Hash(combined)
		nextLevel = append(nextLevel, h)
	}
	return nextLevel
}
