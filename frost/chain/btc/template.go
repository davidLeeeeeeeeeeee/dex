package btc

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"dex/frost/chain"
)

// 常量定义
const (
	// DefaultSequence 默认序列号（RBF 禁用）
	DefaultSequence = 0xffffffff
	// DefaultLockTime 默认锁定时间
	DefaultLockTime = 0
	// DustLimit 粉尘限制（satoshi）
	DustLimit = 546
)

// TxInput 交易输入（规范化结构）
type TxInput struct {
	TxID     string `json:"txid"`     // 前序交易 ID（hex，小端）
	Vout     uint32 `json:"vout"`     // 输出索引
	Amount   uint64 `json:"amount"`   // 金额（用于签名）
	Sequence uint32 `json:"sequence"` // 序列号
}

// TxOutput 交易输出（规范化结构）
type TxOutput struct {
	Address string `json:"address"` // 目标地址
	Amount  uint64 `json:"amount"`  // 金额（satoshi）
}

// BTCTemplate BTC 交易模板（规范化结构）
// 用于确定性序列化和 template_hash 计算
type BTCTemplate struct {
	Version     int32      `json:"version"`      // 交易版本
	LockTime    uint32     `json:"lock_time"`    // 锁定时间
	Inputs      []TxInput  `json:"inputs"`       // 输入列表（已排序）
	Outputs     []TxOutput `json:"outputs"`      // 输出列表（已排序）
	VaultID     uint32     `json:"vault_id"`     // Vault ID
	KeyEpoch    uint64     `json:"key_epoch"`    // 密钥版本
	WithdrawIDs []string   `json:"withdraw_ids"` // 提现 ID 列表
}

// NewBTCTemplate 从参数创建 BTC 模板
// 注意：Fee 和 ChangeAmount 必须由 JobPlanner 确定性计算后传入，本函数不做任何费用推算
func NewBTCTemplate(params chain.WithdrawTemplateParams) (*BTCTemplate, error) {
	if len(params.Inputs) == 0 {
		return nil, errors.New("no inputs provided")
	}
	if len(params.Outputs) == 0 {
		return nil, errors.New("no outputs provided")
	}

	// 构建输入（按 txid:vout 排序以确保确定性）
	inputs := make([]TxInput, len(params.Inputs))
	for i, utxo := range params.Inputs {
		inputs[i] = TxInput{
			TxID:     utxo.TxID,
			Vout:     utxo.Vout,
			Amount:   utxo.Amount,
			Sequence: DefaultSequence,
		}
	}
	sortInputs(inputs)

	// 构建输出（保持 JobPlanner 传入的顺序，找零放最后）
	// 注意：Outputs 顺序必须由 JobPlanner 保证按 withdraw.seq 排序
	outputs := make([]TxOutput, 0, len(params.Outputs)+1)
	for _, out := range params.Outputs {
		outputs = append(outputs, TxOutput{
			Address: out.To,
			Amount:  out.Amount,
		})
	}
	if len(params.WithdrawIDs) != len(params.Outputs) {
		return nil, errors.New("withdraw_ids and outputs length mismatch")
	}
	for i, out := range params.Outputs {
		if out.WithdrawID != params.WithdrawIDs[i] {
			return nil, errors.New("withdraw output order mismatch")
		}
	}

	// 验证资金充足（Fee 和 ChangeAmount 由 JobPlanner 确定性计算）
	var totalIn, totalOut uint64
	for _, in := range params.Inputs {
		totalIn += in.Amount
	}
	for _, out := range params.Outputs {
		totalOut += out.Amount
	}

	expectedTotal := totalOut + params.Fee + params.ChangeAmount
	if totalIn != expectedTotal {
		return nil, errors.New("input/output mismatch: totalIn != totalOut + fee + change")
	}

	// 添加找零输出（如果有）
	if params.ChangeAmount > 0 && params.ChangeAddress != "" {
		outputs = append(outputs, TxOutput{
			Address: params.ChangeAddress,
			Amount:  params.ChangeAmount,
		})
	}

	// 保持 WithdrawIDs 原始顺序，顺序确定性由 JobPlanner 保证
	withdrawIDs := make([]string, len(params.WithdrawIDs))
	copy(withdrawIDs, params.WithdrawIDs)

	return &BTCTemplate{
		Version:     2,
		LockTime:    DefaultLockTime,
		Inputs:      inputs,
		Outputs:     outputs,
		VaultID:     params.VaultID,
		KeyEpoch:    params.KeyEpoch,
		WithdrawIDs: withdrawIDs,
	}, nil
}

// sortInputs 按 txid:vout 字典序排序（确定性）
func sortInputs(inputs []TxInput) {
	sort.Slice(inputs, func(i, j int) bool {
		if inputs[i].TxID != inputs[j].TxID {
			return inputs[i].TxID < inputs[j].TxID
		}
		return inputs[i].Vout < inputs[j].Vout
	})
}

// Serialize 规范化序列化（用于 template_hash 计算）
// 序列化顺序严格固定，确保相同逻辑输入产生相同字节序列
func (t *BTCTemplate) Serialize() []byte {
	var buf bytes.Buffer

	// 1. 版本号（4 bytes, little-endian）
	binary.Write(&buf, binary.LittleEndian, t.Version)

	// 2. 锁定时间（4 bytes, little-endian）
	binary.Write(&buf, binary.LittleEndian, t.LockTime)

	// 3. VaultID（4 bytes, little-endian）
	binary.Write(&buf, binary.LittleEndian, t.VaultID)

	// 4. KeyEpoch（8 bytes, little-endian）
	binary.Write(&buf, binary.LittleEndian, t.KeyEpoch)

	// 5. 输入数量（varint）
	writeVarInt(&buf, uint64(len(t.Inputs)))

	// 6. 输入列表（已排序）
	for _, in := range t.Inputs {
		// txid（32 bytes，反转为内部字节序）
		txidBytes, _ := hex.DecodeString(in.TxID)
		reverseBytes(txidBytes)
		buf.Write(txidBytes)
		// vout（4 bytes）
		binary.Write(&buf, binary.LittleEndian, in.Vout)
		// amount（8 bytes）
		binary.Write(&buf, binary.LittleEndian, in.Amount)
		// sequence（4 bytes）
		binary.Write(&buf, binary.LittleEndian, in.Sequence)
	}

	// 7. 输出数量（varint）
	writeVarInt(&buf, uint64(len(t.Outputs)))

	// 8. 输出列表
	for _, out := range t.Outputs {
		// amount（8 bytes）
		binary.Write(&buf, binary.LittleEndian, out.Amount)
		// address 长度（varint）+ address bytes
		addrBytes := []byte(out.Address)
		writeVarInt(&buf, uint64(len(addrBytes)))
		buf.Write(addrBytes)
	}

	// 9. WithdrawIDs 数量（varint）
	writeVarInt(&buf, uint64(len(t.WithdrawIDs)))

	// 10. WithdrawIDs 列表
	for _, wid := range t.WithdrawIDs {
		widBytes := []byte(wid)
		writeVarInt(&buf, uint64(len(widBytes)))
		buf.Write(widBytes)
	}

	return buf.Bytes()
}

// TemplateHash 计算模板哈希（SHA256 of 规范化序列化）
func (t *BTCTemplate) TemplateHash() []byte {
	serialized := t.Serialize()
	hash := sha256.Sum256(serialized)
	return hash[:]
}

// ToJSON 序列化为 JSON（用于存储/调试）
func (t *BTCTemplate) ToJSON() ([]byte, error) {
	return json.Marshal(t)
}

// FromJSON 从 JSON 反序列化
func FromJSON(data []byte) (*BTCTemplate, error) {
	var t BTCTemplate
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// writeVarInt 写入变长整数（Bitcoin varint 格式）
func writeVarInt(buf *bytes.Buffer, n uint64) {
	switch {
	case n < 0xfd:
		buf.WriteByte(byte(n))
	case n <= 0xffff:
		buf.WriteByte(0xfd)
		binary.Write(buf, binary.LittleEndian, uint16(n))
	case n <= 0xffffffff:
		buf.WriteByte(0xfe)
		binary.Write(buf, binary.LittleEndian, uint32(n))
	default:
		buf.WriteByte(0xff)
		binary.Write(buf, binary.LittleEndian, n)
	}
}

// reverseBytes 反转字节数组（用于 txid 字节序转换）
func reverseBytes(b []byte) {
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
}
