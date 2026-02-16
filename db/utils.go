package db

import (
	"bytes"
	"crypto/sha256"
	"dex/pb"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"
)

func ProtoMarshal(m proto.Message) ([]byte, error)      { return proto.Marshal(m) }
func ProtoUnmarshal(data []byte, m proto.Message) error { return proto.Unmarshal(data, m) }

// DeleteKey 一个DeleteKey工具
func (manager *Manager) DeleteKey(key string) error {
	manager.mu.RLock()
	db := manager.Db
	manager.mu.RUnlock()
	if db == nil {
		return fmt.Errorf("database is not initialized or closed")
	}
	return db.Delete([]byte(key), pebble.Sync)
}

// getNewIndex 只做只读操作，计算"应该用哪个下标"
// 并返回：
//
//	idx      -> 新分配的下标
//	tasks    -> 为保持一致性必须追加到写队列里的 WriteTask 切片
func getNewIndex(mgr *Manager) (uint64, []WriteTask, error) {
	var (
		idx        uint64
		tasks      []WriteTask
		freeIdxKey []byte
	)

	// 使用 Pebble Iterator 寻找可复用的 free_idx_*
	prefix := []byte(KeyFreeIdx())
	iter, err := mgr.Db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixUpperBound(prefix),
	})
	if err != nil {
		return 0, nil, err
	}

	if iter.SeekGE(prefix); iter.Valid() {
		freeIdxKey = make([]byte, len(iter.Key()))
		copy(freeIdxKey, iter.Key())
		idxStr := bytes.TrimPrefix(freeIdxKey, prefix)
		i, _ := strconv.ParseUint(string(idxStr), 10, 64)
		idx = i
	}
	iter.Close()

	if idx == 0 {
		idx, _ = mgr.NextIndex()
	}

	if freeIdxKey != nil {
		tasks = append(tasks, WriteTask{Key: freeIdxKey, Op: OpDelete})
	}
	return idx, tasks, nil
}

func removeIndex(idx uint64) WriteTask {
	k := []byte(fmt.Sprintf(KeyFreeIdx()+"%020d", idx))
	return WriteTask{Key: k, Op: OpSet}
}

// HashTx 接受任意交易消息（proto.Message），并返回其哈希值（排除 BaseMessage.TxId 字段）。

// HashTx 接受任意交易消息（proto.Message），并返回其哈希值。
// 在计算 hash 时，排除了 BaseMessage 中的 tx_id 和 signature 字段。
func HashTx(tx proto.Message) (string, error) {
	txCopy := proto.Clone(tx)
	switch t := txCopy.(type) {
	case *pb.IssueTokenTx:
		if t.Base != nil {
			t.Base.TxId = ""
			t.Base.Signature = nil
		}
	case *pb.FreezeTx:
		if t.Base != nil {
			t.Base.TxId = ""
			t.Base.Signature = nil
		}
	case *pb.Transaction:
		if t.Base != nil {
			t.Base.TxId = ""
			t.Base.Signature = nil
		}
	case *pb.OrderTx:
		if t.Base != nil {
			t.Base.TxId = ""
			t.Base.Signature = nil
		}
	case *pb.MinerTx:
		if t.Base != nil {
			t.Base.TxId = ""
			t.Base.Signature = nil
		}
	case *pb.AnyTx:
		switch content := t.Content.(type) {
		case *pb.AnyTx_IssueTokenTx:
			if content.IssueTokenTx.Base != nil {
				content.IssueTokenTx.Base.TxId = ""
				content.IssueTokenTx.Base.Signature = nil
			}
		case *pb.AnyTx_FreezeTx:
			if content.FreezeTx.Base != nil {
				content.FreezeTx.Base.TxId = ""
				content.FreezeTx.Base.Signature = nil
			}
		case *pb.AnyTx_Transaction:
			if content.Transaction.Base != nil {
				content.Transaction.Base.TxId = ""
				content.Transaction.Base.Signature = nil
			}
		case *pb.AnyTx_OrderTx:
			if content.OrderTx.Base != nil {
				content.OrderTx.Base.TxId = ""
				content.OrderTx.Base.Signature = nil
			}
		case *pb.AnyTx_MinerTx:
			if content.MinerTx.Base != nil {
				content.MinerTx.Base.TxId = ""
				content.MinerTx.Base.Signature = nil
			}
		case *pb.AnyTx_WitnessStakeTx:
			if content.WitnessStakeTx.Base != nil {
				content.WitnessStakeTx.Base.TxId = ""
				content.WitnessStakeTx.Base.Signature = nil
			}
		case *pb.AnyTx_WitnessRequestTx:
			if content.WitnessRequestTx.Base != nil {
				content.WitnessRequestTx.Base.TxId = ""
				content.WitnessRequestTx.Base.Signature = nil
			}
		case *pb.AnyTx_WitnessVoteTx:
			if content.WitnessVoteTx.Base != nil {
				content.WitnessVoteTx.Base.TxId = ""
				content.WitnessVoteTx.Base.Signature = nil
			}
		case *pb.AnyTx_WitnessChallengeTx:
			if content.WitnessChallengeTx.Base != nil {
				content.WitnessChallengeTx.Base.TxId = ""
				content.WitnessChallengeTx.Base.Signature = nil
			}
		case *pb.AnyTx_ArbitrationVoteTx:
			if content.ArbitrationVoteTx.Base != nil {
				content.ArbitrationVoteTx.Base.TxId = ""
				content.ArbitrationVoteTx.Base.Signature = nil
			}
		case *pb.AnyTx_WitnessClaimRewardTx:
			if content.WitnessClaimRewardTx.Base != nil {
				content.WitnessClaimRewardTx.Base.TxId = ""
				content.WitnessClaimRewardTx.Base.Signature = nil
			}
		default:
			return "", fmt.Errorf("不支持的 AnyTx 内部类型")
		}
	default:
		return "", fmt.Errorf("不支持的交易类型")
	}
	data, err := proto.Marshal(txCopy)
	if err != nil {
		return "", err
	}
	hashBytes := sha256.Sum256(data)
	return hex.EncodeToString(hashBytes[:]), nil
}

// restoreSeqFromDB 从 DB 恢复 sequence counter（用于测试或初始化后）
func restoreSeqFromDB(db *pebble.DB) uint64 {
	raw, closer, err := db.Get([]byte("meta:seq_counter"))
	if err != nil {
		return 0
	}
	defer closer.Close()
	if len(raw) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(raw)
}
