// statedb/keys.go
package statedb

import "fmt"

// ====== Keys（仅 stateDB 内部使用）======
//
// s1|snap|<E>|<shard>|<addr> -> value       // Base 或者是"全量快照"的载体
// s1|ovl|<E>|<shard>|<addr>  -> value       // 当前 Epoch 覆盖写的 overlay
// i1|snap|<E>|<shard>        -> count       // 快照分片计数
// i1|ovl|<E>|<shard>         -> count       // overlay 分片计数
// meta:snap:base:<E>         -> <E'>        // E 的 base（如果是叠加式快照）
// meta:h2seq:<H>             -> <seq>       // 高度到提交序号
// meta:seq2h:<seq>           -> <H>         // 反向映射
// wal|<E>|<block>|<seq>      -> batch diff  // 可选 WAL

func kSnap(E uint64, shard, addr string) []byte {
	return []byte(fmt.Sprintf("s1|snap|%020d|%s|%s", E, shard, addr))
}

func kOvl(E uint64, shard, addr string) []byte {
	return []byte(fmt.Sprintf("s1|ovl|%020d|%s|%s", E, shard, addr))
}

func kIdxSnap(E uint64, shard string) []byte {
	return []byte(fmt.Sprintf("i1|snap|%020d|%s", E, shard))
}

func kIdxOvl(E uint64, shard string) []byte {
	return []byte(fmt.Sprintf("i1|ovl|%020d|%s", E, shard))
}

func kMetaSnapBase(E uint64) []byte {
	return []byte(fmt.Sprintf("meta:snap:base:%020d", E))
}

func kMetaH2Seq(h uint64) []byte {
	return []byte(fmt.Sprintf("meta:h2seq:%020d", h))
}

func kMetaSeq2H(seq uint64) []byte {
	return []byte(fmt.Sprintf("meta:seq2h:%020d", seq))
}

