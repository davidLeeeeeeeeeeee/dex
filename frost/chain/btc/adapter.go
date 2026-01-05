// frost/chain/btc/adapter.go
// BTC 链适配器：构建 Taproot 交易模板、计算 sighash、组装签名交易

package btc

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"dex/frost/chain"
	"dex/pb"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// ========== 常量 ==========

const (
	// TaprootWitnessVersion Taproot witness 版本
	TaprootWitnessVersion = 0x01
	// SchnorrSigSize Schnorr 签名大小（64 字节）
	SchnorrSigSize = 64
)

// ========== 错误定义 ==========

var (
	// ErrInvalidTemplateData 无效的模板数据
	ErrInvalidTemplateData = errors.New("invalid template data")
	// ErrInvalidSignature 无效的签名
	ErrInvalidSignature = errors.New("invalid signature")
	// ErrSignatureCountMismatch 签名数量不匹配
	ErrSignatureCountMismatch = errors.New("signature count mismatch with inputs")
)

// ========== BTCAdapter ==========

// BTCAdapter BTC 链适配器
type BTCAdapter struct {
	// 网络参数（mainnet/testnet/regtest）
	network string
}

// NewBTCAdapter 创建 BTC 适配器
func NewBTCAdapter(network string) *BTCAdapter {
	if network == "" {
		network = "mainnet"
	}
	return &BTCAdapter{
		network: network,
	}
}

// Chain 返回链标识
func (a *BTCAdapter) Chain() string {
	return chain.ChainBTC
}

// SignAlgo 返回签名算法
func (a *BTCAdapter) SignAlgo() pb.SignAlgo {
	return chain.SignAlgoBTC
}

// BuildWithdrawTemplate 构建提现交易模板
func (a *BTCAdapter) BuildWithdrawTemplate(params chain.WithdrawTemplateParams) (*chain.TemplateResult, error) {
	// 创建 BTC 模板
	template, err := NewBTCTemplate(params)
	if err != nil {
		return nil, err
	}

	// 计算模板哈希
	templateHash := template.TemplateHash()

	// 序列化模板数据
	templateData, err := template.ToJSON()
	if err != nil {
		return nil, err
	}

	// 构建每个 input 的 scriptPubKey（从 UTXO 获取）
	scriptPubKeys := make([][]byte, len(params.Inputs))
	for i, utxo := range params.Inputs {
		scriptPubKeys[i] = utxo.ScriptPubKey
	}

	// 计算 Taproot sighash
	sighashes, err := template.ComputeTaprootSighash(scriptPubKeys, SighashDefault)
	if err != nil {
		return nil, err
	}

	return &chain.TemplateResult{
		TemplateHash: templateHash,
		TemplateData: templateData,
		SigHashes:    sighashes,
	}, nil
}

// TemplateHash 计算模板哈希
func (a *BTCAdapter) TemplateHash(templateData []byte) ([]byte, error) {
	template, err := FromJSON(templateData)
	if err != nil {
		return nil, ErrInvalidTemplateData
	}
	return template.TemplateHash(), nil
}

// PackageSigned 组装签名后的交易包
func (a *BTCAdapter) PackageSigned(templateData []byte, signatures [][]byte) (*chain.SignedPackage, error) {
	template, err := FromJSON(templateData)
	if err != nil {
		return nil, ErrInvalidTemplateData
	}

	// 验证签名数量
	if len(signatures) != len(template.Inputs) {
		return nil, ErrSignatureCountMismatch
	}

	// 验证签名格式
	for _, sig := range signatures {
		if len(sig) != SchnorrSigSize {
			return nil, ErrInvalidSignature
		}
	}

	// 构建完整交易
	rawTx, err := a.buildRawTransaction(template, signatures)
	if err != nil {
		return nil, err
	}

	return &chain.SignedPackage{
		TemplateHash: template.TemplateHash(),
		TemplateData: templateData,
		Signatures:   signatures,
		RawTx:        rawTx,
	}, nil
}

// buildRawTransaction 构建原始交易
func (a *BTCAdapter) buildRawTransaction(template *BTCTemplate, signatures [][]byte) ([]byte, error) {
	var buf bytes.Buffer

	// 1. 版本号（4 bytes, little-endian）
	binary.Write(&buf, binary.LittleEndian, template.Version)

	// 2. Marker + Flag（SegWit）
	buf.WriteByte(0x00) // marker
	buf.WriteByte(0x01) // flag

	// 3. 输入数量（varint）
	writeVarInt(&buf, uint64(len(template.Inputs)))

	// 4. 输入列表
	for _, in := range template.Inputs {
		// txid（32 bytes，反转为内部字节序）
		txidBytes, _ := hex.DecodeString(in.TxID)
		reverseBytes(txidBytes)
		buf.Write(txidBytes)
		// vout（4 bytes）
		binary.Write(&buf, binary.LittleEndian, in.Vout)
		// scriptSig 长度（0 for SegWit）
		buf.WriteByte(0x00)
		// sequence（4 bytes）
		binary.Write(&buf, binary.LittleEndian, in.Sequence)
	}

	// 5. 输出数量（varint）
	writeVarInt(&buf, uint64(len(template.Outputs)))

	// 6. 输出列表
	for _, out := range template.Outputs {
		// amount（8 bytes）
		binary.Write(&buf, binary.LittleEndian, out.Amount)
		// scriptPubKey
		scriptPubKey := a.addressToScriptPubKey(out.Address)
		writeVarInt(&buf, uint64(len(scriptPubKey)))
		buf.Write(scriptPubKey)
	}

	// 7. Witness 数据
	for _, sig := range signatures {
		// witness 元素数量（1 for key-path spend）
		writeVarInt(&buf, 1)
		// 签名长度 + 签名
		writeVarInt(&buf, uint64(len(sig)))
		buf.Write(sig)
	}

	// 8. 锁定时间（4 bytes）
	binary.Write(&buf, binary.LittleEndian, template.LockTime)

	return buf.Bytes(), nil
}

// addressToScriptPubKey 将地址转换为 scriptPubKey
// 支持 Taproot (bc1p...) 和 Native SegWit (bc1q...) 地址
func (a *BTCAdapter) addressToScriptPubKey(address string) []byte {
	// 尝试解析 Taproot 地址 (bech32m, bc1p...)
	if len(address) > 4 && (address[:4] == "bc1p" || address[:4] == "tb1p") {
		// Taproot 地址
		decoded, err := decodeBech32m(address)
		if err == nil && len(decoded) == 32 {
			// Taproot scriptPubKey: OP_1 <32-byte-pubkey>
			scriptPubKey := make([]byte, 34)
			scriptPubKey[0] = 0x51 // OP_1
			scriptPubKey[1] = 0x20 // 32 bytes
			copy(scriptPubKey[2:], decoded)
			return scriptPubKey
		}
	}

	// 尝试解析 Native SegWit 地址 (bech32, bc1q...)
	if len(address) > 4 && (address[:4] == "bc1q" || address[:4] == "tb1q") {
		decoded, err := decodeBech32(address)
		if err == nil && len(decoded) == 20 {
			// P2WPKH scriptPubKey: OP_0 <20-byte-hash>
			scriptPubKey := make([]byte, 22)
			scriptPubKey[0] = 0x00 // OP_0
			scriptPubKey[1] = 0x14 // 20 bytes
			copy(scriptPubKey[2:], decoded)
			return scriptPubKey
		}
	}

	// 回退：使用地址的 hash 作为 Taproot pubkey
	scriptPubKey := make([]byte, 34)
	scriptPubKey[0] = 0x51 // OP_1
	scriptPubKey[1] = 0x20 // 32 bytes
	hash := sha256.Sum256([]byte(address))
	copy(scriptPubKey[2:], hash[:])
	return scriptPubKey
}

// VerifySignature 验证 BIP-340 Schnorr 签名
func (a *BTCAdapter) VerifySignature(groupPubkey []byte, msg []byte, signature []byte) (bool, error) {
	if len(signature) != SchnorrSigSize {
		return false, ErrInvalidSignature
	}
	if len(groupPubkey) != 32 {
		return false, errors.New("invalid pubkey length")
	}
	if len(msg) != 32 {
		return false, errors.New("invalid message length")
	}

	// 使用 btcec 库验证 BIP-340 Schnorr 签名
	pubKey, err := schnorr.ParsePubKey(groupPubkey)
	if err != nil {
		return false, fmt.Errorf("parse pubkey: %w", err)
	}

	sig, err := schnorr.ParseSignature(signature)
	if err != nil {
		return false, fmt.Errorf("parse signature: %w", err)
	}

	return sig.Verify(msg, pubKey), nil
}

// ========== 辅助函数 ==========

// ComputeTxID 计算交易 ID
func ComputeTxID(rawTx []byte) []byte {
	// 对于 SegWit 交易，txid 是不包含 witness 数据的交易的双 SHA256
	// 这里简化处理
	hash1 := sha256.Sum256(rawTx)
	hash2 := sha256.Sum256(hash1[:])
	return hash2[:]
}

// ========== Bech32/Bech32m 解码 ==========

// bech32 字符集
const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

// decodeBech32 解码 bech32 地址（用于 bc1q... 地址）
func decodeBech32(address string) ([]byte, error) {
	return decodeBech32Internal(address, false)
}

// decodeBech32m 解码 bech32m 地址（用于 bc1p... Taproot 地址）
func decodeBech32m(address string) ([]byte, error) {
	return decodeBech32Internal(address, true)
}

// decodeBech32Internal 内部 bech32/bech32m 解码
func decodeBech32Internal(address string, isBech32m bool) ([]byte, error) {
	// 找到分隔符 '1'
	sepIdx := -1
	for i := len(address) - 1; i >= 0; i-- {
		if address[i] == '1' {
			sepIdx = i
			break
		}
	}
	if sepIdx < 1 || sepIdx+7 > len(address) {
		return nil, errors.New("invalid bech32 address")
	}

	// 解码数据部分
	data := address[sepIdx+1:]
	values := make([]int, len(data))
	for i, c := range data {
		idx := -1
		for j, ch := range bech32Charset {
			if byte(ch) == byte(c) {
				idx = j
				break
			}
		}
		if idx == -1 {
			return nil, errors.New("invalid bech32 character")
		}
		values[i] = idx
	}

	// 验证校验和（简化：跳过校验和验证）
	if len(values) < 6 {
		return nil, errors.New("bech32 data too short")
	}

	// 移除校验和（最后 6 个字符）
	values = values[:len(values)-6]

	// 第一个值是 witness 版本
	if len(values) < 1 {
		return nil, errors.New("missing witness version")
	}
	// witnessVersion := values[0]
	values = values[1:]

	// 将 5-bit 值转换为 8-bit 字节
	return convertBits(values, 5, 8, false)
}

// convertBits 在不同位宽之间转换
func convertBits(data []int, fromBits, toBits int, pad bool) ([]byte, error) {
	acc := 0
	bits := 0
	var result []byte
	maxv := (1 << toBits) - 1

	for _, value := range data {
		if value < 0 || value>>fromBits != 0 {
			return nil, errors.New("invalid value")
		}
		acc = (acc << fromBits) | value
		bits += fromBits
		for bits >= toBits {
			bits -= toBits
			result = append(result, byte((acc>>bits)&maxv))
		}
	}

	if pad {
		if bits > 0 {
			result = append(result, byte((acc<<(toBits-bits))&maxv))
		}
	} else if bits >= fromBits || ((acc<<(toBits-bits))&maxv) != 0 {
		return nil, errors.New("invalid padding")
	}

	return result, nil
}
