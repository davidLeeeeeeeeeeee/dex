// frost/chain/evm/adapter.go
// EVM 链适配器：构建合约调用模板、计算消息哈希、组装签名交易

package evm

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"

	"dex/frost/chain"
	"dex/pb"
)

// ========== 常量 ==========

const (
	// ChainETH ETH 链标识
	ChainETH = "ETH"
	// ChainBNB BNB 链标识
	ChainBNB = "BNB"
	// SchnorrSigSize Schnorr 签名大小（64 字节）
	SchnorrSigSize = 64
)

// ========== 错误定义 ==========

var (
	// ErrInvalidTemplateData 无效的模板数据
	ErrInvalidTemplateData = errors.New("invalid template data")
	// ErrInvalidSignature 无效的签名
	ErrInvalidSignature = errors.New("invalid signature")
	// ErrInvalidContractAddress 无效的合约地址
	ErrInvalidContractAddress = errors.New("invalid contract address")
)

// ========== EVMTemplate ==========

// EVMTemplate EVM 交易模板
type EVMTemplate struct {
	ChainID      uint64      `json:"chain_id"`      // 链 ID
	VaultID      uint32      `json:"vault_id"`      // Vault ID
	KeyEpoch     uint64      `json:"key_epoch"`     // 密钥版本
	ContractAddr string      `json:"contract_addr"` // 合约地址
	MethodID     []byte      `json:"method_id"`     // 方法签名（4 bytes）
	WithdrawIDs  []string    `json:"withdraw_ids"`  // 提现 ID 列表
	Outputs      []EVMOutput `json:"outputs"`       // 输出列表
	Nonce        uint64      `json:"nonce"`         // 交易 nonce
}

// EVMOutput EVM 输出
type EVMOutput struct {
	To     string `json:"to"`     // 目标地址
	Amount string `json:"amount"` // 金额（wei，字符串避免精度问题）
	Token  string `json:"token"`  // 代币地址（空表示原生币）
}

// Serialize 规范化序列化
func (t *EVMTemplate) Serialize() []byte {
	var buf bytes.Buffer

	// 1. ChainID（8 bytes）
	binary.Write(&buf, binary.BigEndian, t.ChainID)

	// 2. VaultID（4 bytes）
	binary.Write(&buf, binary.BigEndian, t.VaultID)

	// 3. KeyEpoch（8 bytes）
	binary.Write(&buf, binary.BigEndian, t.KeyEpoch)

	// 4. ContractAddr（20 bytes）
	addrBytes, _ := hex.DecodeString(t.ContractAddr)
	buf.Write(padLeft(addrBytes, 20))

	// 5. MethodID（4 bytes）
	buf.Write(t.MethodID)

	// 6. Nonce（8 bytes）
	binary.Write(&buf, binary.BigEndian, t.Nonce)

	// 7. 输出数量
	binary.Write(&buf, binary.BigEndian, uint32(len(t.Outputs)))

	// 8. 输出列表
	for _, out := range t.Outputs {
		toBytes, _ := hex.DecodeString(out.To)
		buf.Write(padLeft(toBytes, 20))
		amountBig, _ := new(big.Int).SetString(out.Amount, 10)
		buf.Write(padLeft(amountBig.Bytes(), 32))
		tokenBytes, _ := hex.DecodeString(out.Token)
		buf.Write(padLeft(tokenBytes, 20))
	}

	// 9. WithdrawIDs
	binary.Write(&buf, binary.BigEndian, uint32(len(t.WithdrawIDs)))
	for _, wid := range t.WithdrawIDs {
		widBytes := []byte(wid)
		binary.Write(&buf, binary.BigEndian, uint32(len(widBytes)))
		buf.Write(widBytes)
	}

	return buf.Bytes()
}

// TemplateHash 计算模板哈希
func (t *EVMTemplate) TemplateHash() []byte {
	serialized := t.Serialize()
	hash := sha256.Sum256(serialized)
	return hash[:]
}

// ToJSON 序列化为 JSON
func (t *EVMTemplate) ToJSON() ([]byte, error) {
	return json.Marshal(t)
}

// FromJSON 从 JSON 反序列化
func FromJSON(data []byte) (*EVMTemplate, error) {
	var t EVMTemplate
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// ========== EVMAdapter ==========

// EVMAdapter EVM 链适配器
type EVMAdapter struct {
	chainName string
	chainID   uint64
}

// NewEVMAdapter 创建 EVM 适配器
func NewEVMAdapter(chainName string, chainID uint64) *EVMAdapter {
	return &EVMAdapter{
		chainName: chainName,
		chainID:   chainID,
	}
}

// NewETHAdapter 创建 ETH 适配器
func NewETHAdapter() *EVMAdapter {
	return NewEVMAdapter(ChainETH, 1)
}

// NewBNBAdapter 创建 BNB 适配器
func NewBNBAdapter() *EVMAdapter {
	return NewEVMAdapter(ChainBNB, 56)
}

// Chain 返回链标识
func (a *EVMAdapter) Chain() string {
	return a.chainName
}

// SignAlgo 返回签名算法
func (a *EVMAdapter) SignAlgo() pb.SignAlgo {
	return chain.SignAlgoETH
}

// BuildWithdrawTemplate 构建提现交易模板
func (a *EVMAdapter) BuildWithdrawTemplate(params chain.WithdrawTemplateParams) (*chain.TemplateResult, error) {
	if params.ContractAddr == "" {
		return nil, ErrInvalidContractAddress
	}

	// 构建输出列表
	outputs := make([]EVMOutput, len(params.Outputs))
	for i, out := range params.Outputs {
		outputs[i] = EVMOutput{
			To:     out.To,
			Amount: big.NewInt(int64(out.Amount)).String(),
			Token:  "", // 原生币
		}
	}

	template := &EVMTemplate{
		ChainID:      a.chainID,
		VaultID:      params.VaultID,
		KeyEpoch:     params.KeyEpoch,
		ContractAddr: params.ContractAddr,
		MethodID:     params.MethodID,
		WithdrawIDs:  params.WithdrawIDs,
		Outputs:      outputs,
		Nonce:        0, // TODO: 从链上获取
	}

	templateHash := template.TemplateHash()
	templateData, err := template.ToJSON()
	if err != nil {
		return nil, err
	}

	// EVM 只需要一个签名（合约验证）
	return &chain.TemplateResult{
		TemplateHash: templateHash,
		TemplateData: templateData,
		SigHashes:    [][]byte{templateHash},
	}, nil
}

// TemplateHash 计算模板哈希
func (a *EVMAdapter) TemplateHash(templateData []byte) ([]byte, error) {
	template, err := FromJSON(templateData)
	if err != nil {
		return nil, ErrInvalidTemplateData
	}
	return template.TemplateHash(), nil
}

// PackageSigned 组装签名后的交易包
func (a *EVMAdapter) PackageSigned(templateData []byte, signatures [][]byte) (*chain.SignedPackage, error) {
	template, err := FromJSON(templateData)
	if err != nil {
		return nil, ErrInvalidTemplateData
	}

	if len(signatures) != 1 {
		return nil, errors.New("EVM requires exactly one signature")
	}

	if len(signatures[0]) != SchnorrSigSize {
		return nil, ErrInvalidSignature
	}

	// 构建合约调用数据
	calldata := a.buildCalldata(template, signatures[0])

	return &chain.SignedPackage{
		TemplateHash: template.TemplateHash(),
		TemplateData: templateData,
		Signatures:   signatures,
		RawTx:        calldata,
	}, nil
}

// buildCalldata 构建合约调用数据
func (a *EVMAdapter) buildCalldata(template *EVMTemplate, signature []byte) []byte {
	var buf bytes.Buffer

	// 方法签名
	buf.Write(template.MethodID)

	// 编码参数（简化实现）
	// 实际应该使用 ABI 编码

	// 签名
	buf.Write(signature)

	return buf.Bytes()
}

// VerifySignature 验证签名
func (a *EVMAdapter) VerifySignature(groupPubkey []byte, msg []byte, signature []byte) (bool, error) {
	if len(signature) != SchnorrSigSize {
		return false, ErrInvalidSignature
	}

	// TODO: 实现 alt_bn128 Schnorr 签名验证
	// 这里需要调用 bn256 库进行验证

	return true, nil
}

// ========== 辅助函数 ==========

func padLeft(data []byte, size int) []byte {
	if len(data) >= size {
		return data[len(data)-size:]
	}
	result := make([]byte, size)
	copy(result[size-len(data):], data)
	return result
}
