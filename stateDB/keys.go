package statedb

import "fmt"

func kState(shard, bizKey string) []byte {
	return []byte(fmt.Sprintf("v1_sdb_state_%s_%s", shard, bizKey))
}

func kStatePrefix(shard string) []byte {
	return []byte(fmt.Sprintf("v1_sdb_state_%s_", shard))
}

func kMetaH2Seq(h uint64) []byte {
	return []byte(fmt.Sprintf("v1_sdb_meta_h2seq_%020d", h))
}

func kMetaSeq2H(seq uint64) []byte {
	return []byte(fmt.Sprintf("v1_sdb_meta_seq2h_%020d", seq))
}

func kMetaLastAppliedHeight() []byte {
	return []byte("v1_sdb_meta_last_applied_height")
}

func kCpLatest() []byte {
	return []byte("v1_sdb_cp_latest")
}

func kCpIndex(height uint64) []byte {
	return []byte(fmt.Sprintf("v1_sdb_cp_h_%020d", height))
}
