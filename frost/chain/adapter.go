package chain

import (
	"errors"
	"strings"

	"dex/pb"
)

// ========== 链名常量（统一使用小写）==========

const (
	ChainBTC = "btc" // Bitcoin
	ChainETH = "eth" // Ethereum
	ChainBNB = "bnb" // BNB Smart Chain
	ChainTRX = "trx" // Tron
	ChainSOL = "sol" // Solana
)

// SupportedChains 支持的链列表
var SupportedChains = []string{ChainBTC, ChainETH, ChainBNB, ChainTRX, ChainSOL}

// ErrUnsupportedChain 不支持的链
var ErrUnsupportedChain = errors.New("unsupported chain")

// NormalizeChain 规范化链名（统一转小写）
// 所有入口处应调用此函数确保链名一致性
func NormalizeChain(chain string) string {
	return strings.ToLower(strings.TrimSpace(chain))
}

// IsValidChain 检查是否为有效的链名
func IsValidChain(chain string) bool {
	normalized := NormalizeChain(chain)
	for _, c := range SupportedChains {
		if c == normalized {
			return true
		}
	}
	return false
}

// 直接使用 pb.SignAlgo，避免多层映射
// 常用值的别名（方便使用）
const (
	SignAlgoBTC = pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340 // BTC
	SignAlgoETH = pb.SignAlgo_SIGN_ALGO_SCHNORR_ALT_BN128        // ETH/BNB
	SignAlgoSOL = pb.SignAlgo_SIGN_ALGO_ED25519                  // SOL
	SignAlgoTRX = pb.SignAlgo_SIGN_ALGO_ECDSA_SECP256K1          // TRX (GG20/CGGMP)
)

// WithdrawOutput 提现输出
type WithdrawOutput struct {
	WithdrawID string // 提现请求 ID
	To         string // 目标地址
	Amount     uint64 // 金额（最小单位）
}

// UTXO 表示一个未花费输出
type UTXO struct {
	TxID          string // 交易 ID（hex）
	Vout          uint32 // 输出索引
	Amount        uint64 // 金额（satoshi）
	ScriptPubKey  []byte // 锁定脚本
	ConfirmHeight uint64 // 确认高度（用于 FIFO 排序）
}

// WithdrawTemplateParams 提现模板参数
// 注意：所有字段都必须由 JobPlanner 确定性计算后传入，模板构建不做任何推算
type WithdrawTemplateParams struct {
	Chain       string           // 链标识
	Asset       string           // 资产标识
	VaultID     uint32           // Vault ID
	KeyEpoch    uint64           // 密钥版本
	WithdrawIDs []string         // 提现请求 ID 列表（必须已按 seq 排序）
	Outputs     []WithdrawOutput // 输出列表（必须已按 withdraw seq 排序）
	// BTC 专用
	Inputs        []UTXO // 输入 UTXO 列表（模板内部会按 txid:vout 排序）
	ChangeAddress string // 找零地址
	Fee           uint64 // 手续费（satoshi，由 JobPlanner 根据配置确定性计算）
	ChangeAmount  uint64 // 找零金额（由 JobPlanner 确定性计算，0 表示无找零）
	// 合约链专用
	ContractAddr string // 合约地址
	MethodID     []byte // 方法签名
}

// TemplateResult 模板构建结果
type TemplateResult struct {
	TemplateHash []byte // 模板哈希（用于签名绑定）
	TemplateData []byte // 模板数据（可序列化存储）
	// BTC 专用
	SigHashes [][]byte // 每个 input 的签名哈希
}

// SignedPackage 签名包
type SignedPackage struct {
	TemplateHash []byte   // 模板哈希
	TemplateData []byte   // 模板数据
	Signatures   [][]byte // 签名列表（BTC: 每个 input 一个签名）
	RawTx        []byte   // 可广播的完整交易（可选）
}

// ChainAdapter 链适配器接口
// 负责：构建模板 / 计算 template_hash / 组装签名交易
type ChainAdapter interface {
	// Chain 返回链标识
	Chain() string

	// SignAlgo 返回该链使用的签名算法（直接使用 pb.SignAlgo）
	SignAlgo() pb.SignAlgo

	// BuildWithdrawTemplate 构建提现交易模板
	// 返回模板哈希、模板数据、每个签名任务的消息哈希
	BuildWithdrawTemplate(params WithdrawTemplateParams) (*TemplateResult, error)

	// TemplateHash 计算模板哈希（规范化序列化后 sha256）
	// 用于已有模板数据时重新计算哈希
	TemplateHash(templateData []byte) ([]byte, error)

	// PackageSigned 组装签名后的交易包
	// 将模板数据和签名组装成可广播的交易
	PackageSigned(templateData []byte, signatures [][]byte) (*SignedPackage, error)

	// VerifySignature 验证聚合签名（VM 用于验证 SignedPackage）
	VerifySignature(groupPubkey []byte, msg []byte, signature []byte) (bool, error)
}

// ChainAdapterFactory 链适配器工厂
type ChainAdapterFactory interface {
	// Adapter 获取指定链的适配器
	Adapter(chain string) (ChainAdapter, error)

	// RegisterAdapter 注册链适配器
	RegisterAdapter(adapter ChainAdapter)
}

// DefaultAdapterFactory 默认适配器工厂实现
type DefaultAdapterFactory struct {
	adapters map[string]ChainAdapter
}

// NewDefaultAdapterFactory 创建默认适配器工厂
func NewDefaultAdapterFactory() *DefaultAdapterFactory {
	return &DefaultAdapterFactory{
		adapters: make(map[string]ChainAdapter),
	}
}

// Adapter 获取指定链的适配器
func (f *DefaultAdapterFactory) Adapter(chain string) (ChainAdapter, error) {
	// 规范化链名
	normalized := NormalizeChain(chain)
	adapter, ok := f.adapters[normalized]
	if !ok {
		return nil, ErrUnsupportedChain
	}
	return adapter, nil
}

// RegisterAdapter 注册链适配器
func (f *DefaultAdapterFactory) RegisterAdapter(adapter ChainAdapter) {
	f.adapters[adapter.Chain()] = adapter
}
