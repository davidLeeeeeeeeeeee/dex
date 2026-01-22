// frost/chain/solana/adapter.go
// Solana 链适配器：构建 Solana 指令模板、计算消息哈希、组装签名交易

package solana

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"

	"dex/frost/chain"
	"dex/pb"
)

// ========== 常量 ==========

const (
	// ChainSOL Solana 链标识
	ChainSOL = "sol"
	// Ed25519SigSize Ed25519 签名大小（64 字节）
	Ed25519SigSize = 64
	// Ed25519PubKeySize Ed25519 公钥大小（32 字节）
	Ed25519PubKeySize = 32
)

// ========== 错误定义 ==========

var (
	// ErrInvalidTemplateData 无效的模板数据
	ErrInvalidTemplateData = errors.New("invalid template data")
	// ErrInvalidSignature 无效的签名
	ErrInvalidSignature = errors.New("invalid signature")
	// ErrInvalidProgramID 无效的程序 ID
	ErrInvalidProgramID = errors.New("invalid program ID")
)

// ========== SolanaTemplate ==========

// SolanaTemplate Solana 交易模板
type SolanaTemplate struct {
	ProgramID       string         `json:"program_id"`       // 程序 ID（PDA）
	VaultID         uint32         `json:"vault_id"`         // Vault ID
	KeyEpoch        uint64         `json:"key_epoch"`        // 密钥版本
	WithdrawIDs     []string       `json:"withdraw_ids"`     // 提现 ID 列表
	Outputs         []SolanaOutput `json:"outputs"`          // 输出列表
	RecentBlockhash string         `json:"recent_blockhash"` // 最近区块哈希
}

// SolanaOutput Solana 输出
type SolanaOutput struct {
	To     string `json:"to"`     // 目标地址（base58）
	Amount uint64 `json:"amount"` // 金额（lamports）
	Token  string `json:"token"`  // SPL Token 地址（空表示 SOL）
}

// Serialize 规范化序列化
func (t *SolanaTemplate) Serialize() []byte {
	var buf []byte

	// 1. ProgramID（32 bytes）
	programIDBytes := base58Decode(t.ProgramID)
	buf = append(buf, padLeft(programIDBytes, 32)...)

	// 2. VaultID（4 bytes）
	vaultIDBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(vaultIDBytes, t.VaultID)
	buf = append(buf, vaultIDBytes...)

	// 3. KeyEpoch（8 bytes）
	keyEpochBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(keyEpochBytes, t.KeyEpoch)
	buf = append(buf, keyEpochBytes...)

	// 4. 输出数量（4 bytes）
	outputCountBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(outputCountBytes, uint32(len(t.Outputs)))
	buf = append(buf, outputCountBytes...)

	// 5. 输出列表
	for _, out := range t.Outputs {
		toBytes := base58Decode(out.To)
		buf = append(buf, padLeft(toBytes, 32)...)
		amountBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(amountBytes, out.Amount)
		buf = append(buf, amountBytes...)
		tokenBytes := base58Decode(out.Token)
		buf = append(buf, padLeft(tokenBytes, 32)...)
	}

	// 6. WithdrawIDs
	widCountBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(widCountBytes, uint32(len(t.WithdrawIDs)))
	buf = append(buf, widCountBytes...)
	for _, wid := range t.WithdrawIDs {
		widBytes := []byte(wid)
		widLenBytes := make([]byte, 4)
		binary.BigEndian.PutUint32(widLenBytes, uint32(len(widBytes)))
		buf = append(buf, widLenBytes...)
		buf = append(buf, widBytes...)
	}

	return buf
}

// TemplateHash 计算模板哈希
func (t *SolanaTemplate) TemplateHash() []byte {
	serialized := t.Serialize()
	hash := sha256.Sum256(serialized)
	return hash[:]
}

// ToJSON 序列化为 JSON
func (t *SolanaTemplate) ToJSON() ([]byte, error) {
	return json.Marshal(t)
}

// FromJSON 从 JSON 反序列化
func FromJSON(data []byte) (*SolanaTemplate, error) {
	var t SolanaTemplate
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// ========== SolanaAdapter ==========

// SolanaAdapter Solana 链适配器
type SolanaAdapter struct {
	programID string // Frost Vault 程序 ID
}

// NewSolanaAdapter 创建 Solana 适配器
func NewSolanaAdapter(programID string) *SolanaAdapter {
	if programID == "" {
		// 默认程序 ID（需要替换为实际部署的程序地址）
		programID = "FrostVau1t11111111111111111111111111111111"
	}
	return &SolanaAdapter{
		programID: programID,
	}
}

// Chain 返回链标识
func (a *SolanaAdapter) Chain() string {
	return ChainSOL
}

// SignAlgo 返回签名算法
func (a *SolanaAdapter) SignAlgo() pb.SignAlgo {
	return pb.SignAlgo_SIGN_ALGO_ED25519
}

// BuildWithdrawTemplate 构建提现交易模板
func (a *SolanaAdapter) BuildWithdrawTemplate(params chain.WithdrawTemplateParams) (*chain.TemplateResult, error) {
	if a.programID == "" {
		return nil, ErrInvalidProgramID
	}

	// 构建输出列表
	outputs := make([]SolanaOutput, len(params.Outputs))
	for i, out := range params.Outputs {
		outputs[i] = SolanaOutput{
			To:     out.To,
			Amount: out.Amount,
			Token:  "", // 默认为 SOL（原生币）
		}
	}

	template := &SolanaTemplate{
		ProgramID:       a.programID,
		VaultID:         params.VaultID,
		KeyEpoch:        params.KeyEpoch,
		WithdrawIDs:     params.WithdrawIDs,
		Outputs:         outputs,
		RecentBlockhash: "", // TODO: 从链上获取最近区块哈希
	}

	templateHash := template.TemplateHash()
	templateData, err := template.ToJSON()
	if err != nil {
		return nil, err
	}

	// Solana 只需要一个签名（Ed25519）
	return &chain.TemplateResult{
		TemplateHash: templateHash,
		TemplateData: templateData,
		SigHashes:    [][]byte{templateHash},
	}, nil
}

// TemplateHash 计算模板哈希
func (a *SolanaAdapter) TemplateHash(templateData []byte) ([]byte, error) {
	template, err := FromJSON(templateData)
	if err != nil {
		return nil, ErrInvalidTemplateData
	}
	return template.TemplateHash(), nil
}

// PackageSigned 组装签名后的交易包
func (a *SolanaAdapter) PackageSigned(templateData []byte, signatures [][]byte) (*chain.SignedPackage, error) {
	template, err := FromJSON(templateData)
	if err != nil {
		return nil, ErrInvalidTemplateData
	}

	if len(signatures) != 1 {
		return nil, errors.New("Solana requires exactly one signature")
	}

	if len(signatures[0]) != Ed25519SigSize {
		return nil, ErrInvalidSignature
	}

	// 构建 Solana 交易（简化版）
	rawTx := a.buildTransaction(template, signatures[0])

	return &chain.SignedPackage{
		TemplateHash: template.TemplateHash(),
		TemplateData: templateData,
		Signatures:   signatures,
		RawTx:        rawTx,
	}, nil
}

// buildTransaction 构建 Solana 交易
func (a *SolanaAdapter) buildTransaction(template *SolanaTemplate, signature []byte) []byte {
	// 简化实现：返回签名 + 模板数据
	// 实际应该构建完整的 Solana Transaction 格式
	var buf []byte
	buf = append(buf, signature...)
	buf = append(buf, template.Serialize()...)
	return buf
}

// VerifySignature 验证 Ed25519 签名
func (a *SolanaAdapter) VerifySignature(groupPubkey []byte, msg []byte, signature []byte) (bool, error) {
	if len(groupPubkey) != Ed25519PubKeySize {
		return false, errors.New("invalid Ed25519 public key length")
	}
	if len(signature) != Ed25519SigSize {
		return false, ErrInvalidSignature
	}

	// TODO: 实现 Ed25519 签名验证
	// 需要使用 golang.org/x/crypto/ed25519 或类似库
	// return ed25519.Verify(groupPubkey, msg, signature), nil

	// 暂时返回 true（需要实际实现）
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

// base58Decode 简化的 base58 解码（实际应使用标准库）
func base58Decode(s string) []byte {
	// TODO: 实现实际的 base58 解码
	// 这里返回占位符
	return []byte(s)
}
