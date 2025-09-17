package db

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/dgraph-io/badger/v4"
	"google.golang.org/protobuf/proto"
	"strconv"
)

func ProtoMarshal(m proto.Message) ([]byte, error) {
	return proto.Marshal(m)
}

func ProtoUnmarshal(data []byte, m proto.Message) error {
	return proto.Unmarshal(data, m)
}

// DeleteKey 一个DeleteKey工具
func (manager *Manager) DeleteKey(key string) error {
	manager.mu.RLock()
	db := manager.Db
	manager.mu.RUnlock()

	if db == nil {
		return fmt.Errorf("database is not initialized or closed")
	}

	return db.Update(func(txn *badger.Txn) error {
		return txn.Delete([]byte(key))
	})
}
func (m *AnyTx) GetBase() *BaseMessage {
	switch tx := m.GetContent().(type) {
	case *AnyTx_IssueTokenTx:
		return tx.IssueTokenTx.Base
	case *AnyTx_FreezeTx:
		return tx.FreezeTx.Base
	case *AnyTx_Transaction:
		return tx.Transaction.Base
	case *AnyTx_OrderTx:
		return tx.OrderTx.Base
	case *AnyTx_AddressTx:
		return tx.AddressTx.Base
	case *AnyTx_CandidateTx:
		return tx.CandidateTx.Base
	default:
		return nil
	}
}

func (m *AnyTx) GetTxId() string {
	base := m.GetBase()
	if base == nil {
		return ""
	}
	return base.TxId
}

// getNewIndex 只做只读操作，计算“应该用哪个下标”
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

	// ① 只读事务：尝试寻找可复用的 free_idx_*
	err := mgr.Db.View(func(txn *badger.Txn) error {
		itOpts := badger.DefaultIteratorOptions
		itOpts.Prefix = []byte("free_idx_")
		it := txn.NewIterator(itOpts)
		defer it.Close()

		it.Rewind()
		if it.Valid() {
			// 找到了可复用的 index
			freeIdxKey = it.Item().KeyCopy(nil)
			idxStr := bytes.TrimPrefix(freeIdxKey, []byte("free_idx_"))
			i, _ := strconv.ParseUint(string(idxStr), 10, 64)
			idx = i
			return nil
		}

		// 没有空闲 -> 尝试读取 meta:max_index
		idx = 0

		return nil

	})
	if err != nil {
		return 0, nil, err
	}
	if idx == 0 {
		idx, _ = mgr.NextIndex()
	}
	// ② 生成写入任务
	if freeIdxKey != nil {
		// 复用 index，需要删除 free_idx_<idx>
		tasks = append(tasks, WriteTask{
			Key: freeIdxKey,
			Op:  OpDelete,
		})
	}

	return idx, tasks, nil
}

func removeIndex(idx uint64) WriteTask {
	k := []byte(fmt.Sprintf("free_idx_%020d", idx))
	return WriteTask{Key: k, Op: OpSet} // 值为空即可，也可以存时间戳
}

// HashTx 接受任意交易消息（proto.Message），并返回其哈希值（排除 BaseMessage.TxId 字段）。

// HashTx 接受任意交易消息（proto.Message），并返回其哈希值。
// 在计算 hash 时，排除了 BaseMessage 中的 tx_id 和 signature 字段。
func HashTx(tx proto.Message) (string, error) {
	// 克隆一份消息，避免修改原始对象
	txCopy := proto.Clone(tx)

	// 根据不同的 tx 类型，清空其中 BaseMessage 的 tx_id 和 signature 字段
	switch t := txCopy.(type) {
	case *IssueTokenTx:
		if t.Base != nil {
			t.Base.TxId = ""
			t.Base.Signature = ""
		}
	case *FreezeTx:
		if t.Base != nil {
			t.Base.TxId = ""
			t.Base.Signature = ""
		}
	case *Transaction:
		if t.Base != nil {
			t.Base.TxId = ""
			t.Base.Signature = ""
		}
	case *OrderTx:
		if t.Base != nil {
			t.Base.TxId = ""
			t.Base.Signature = ""
		}
	case *CandidateTx:
		if t.Base != nil {
			t.Base.TxId = ""
			t.Base.Signature = ""
		}
	case *MinerTx:
		if t.Base != nil {
			t.Base.TxId = ""
			t.Base.Signature = ""
		}

	case *AnyTx:
		// 对于 AnyTx，需要判断当前存放的是哪种 tx，并清空其中的 BaseMessage 中相关字段
		switch content := t.Content.(type) {
		case *AnyTx_IssueTokenTx:
			if content.IssueTokenTx.Base != nil {
				content.IssueTokenTx.Base.TxId = ""
				content.IssueTokenTx.Base.Signature = ""
			}
		case *AnyTx_FreezeTx:
			if content.FreezeTx.Base != nil {
				content.FreezeTx.Base.TxId = ""
				content.FreezeTx.Base.Signature = ""
			}
		case *AnyTx_Transaction:
			if content.Transaction.Base != nil {
				content.Transaction.Base.TxId = ""
				content.Transaction.Base.Signature = ""
			}
		case *AnyTx_OrderTx:
			if content.OrderTx.Base != nil {
				content.OrderTx.Base.TxId = ""
				content.OrderTx.Base.Signature = ""
			}
		case *AnyTx_AddressTx:
			if content.AddressTx.Base != nil {
				content.AddressTx.Base.TxId = ""
				content.AddressTx.Base.Signature = ""
			}
		case *AnyTx_CandidateTx:
			if content.CandidateTx.Base != nil {
				content.CandidateTx.Base.TxId = ""
				content.CandidateTx.Base.Signature = ""
			}

		default:
			return "", fmt.Errorf("不支持的 AnyTx 内部类型")
		}
	default:
		return "", fmt.Errorf("不支持的交易类型")
	}

	// 使用 proto.Marshal 序列化为字节数组
	data, err := proto.Marshal(txCopy)
	if err != nil {
		return "", err
	}

	// 计算 SHA256 哈希值，并以十六进制字符串返回
	hashBytes := sha256.Sum256(data)
	return hex.EncodeToString(hashBytes[:]), nil
}
