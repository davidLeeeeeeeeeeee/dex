// frost/runtime/signer.go
// 签名服务：协调 ROAST/FROST 签名流程

package runtime

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"sync"
	"time"

	"dex/frost/core/curve"
	"dex/frost/core/dkg"
	"dex/frost/core/roast"
	"dex/logs"
	"dex/pb"
)

// ========== 错误定义 ==========

var (
	// ErrSigningTimeout 签名超时
	ErrSigningTimeout = errors.New("signing timeout")
	// ErrSigningFailed 签名失败
	ErrSigningFailed = errors.New("signing failed")
	// ErrNoShare 没有密钥份额
	ErrNoShare = errors.New("no share available")
)

// ========== SignerService ==========

// SignerService 签名服务
type SignerService struct {
	mu sync.RWMutex

	nodeID      NodeID
	coordinator *Coordinator
	participant *Participant

	// 密钥份额存储
	shares map[string]*big.Int // "chain_vaultID_epoch" -> share

	// 公钥份额存储
	pubShares map[string]curve.Point // "chain_vaultID_signerID_epoch" -> pubKey share

	// 群公钥存储
	groupPubKeys map[string]curve.Point // "chain_vaultID_epoch" -> groupPubKey

	// 配置
	signTimeout time.Duration
	group       curve.Group
}

// NewSignerService 创建签名服务
func NewSignerService(nodeID NodeID, coordinator *Coordinator, participant *Participant) *SignerService {
	return &SignerService{
		nodeID:       nodeID,
		coordinator:  coordinator,
		participant:  participant,
		shares:       make(map[string]*big.Int),
		pubShares:    make(map[string]curve.Point),
		groupPubKeys: make(map[string]curve.Point),
		signTimeout:  30 * time.Second,
		group:        curve.NewSecp256k1Group(),
	}
}

// SetShare 设置本地密钥份额
func (s *SignerService) SetShare(chain string, vaultID uint32, epoch uint64, share *big.Int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := shareKeyStr(chain, vaultID, epoch)
	s.shares[key] = share

	// 同步到 participant
	if s.participant != nil {
		s.participant.SetShare(chain, vaultID, epoch, share.Bytes())
	}
}

// SetPublicKeyShare 设置公钥份额
func (s *SignerService) SetPublicKeyShare(chain string, vaultID uint32, signerID int, epoch uint64, pubShare curve.Point) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := pubShareKeyStr(chain, vaultID, signerID, epoch)
	s.pubShares[key] = pubShare
}

// SetGroupPublicKey 设置群公钥
func (s *SignerService) SetGroupPublicKey(chain string, vaultID uint32, epoch uint64, groupPub curve.Point) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := shareKeyStr(chain, vaultID, epoch)
	s.groupPubKeys[key] = groupPub
}

// GetShare 获取本地密钥份额
func (s *SignerService) GetShare(chain string, vaultID uint32, epoch uint64) *big.Int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := shareKeyStr(chain, vaultID, epoch)
	return s.shares[key]
}

// shareKeyStr 生成 share key
func shareKeyStr(chain string, vaultID uint32, epoch uint64) string {
	return chain + "_" + itoa(int(vaultID)) + "_" + itoa(int(epoch))
}

// pubShareKeyStr 生成 pubShare key
func pubShareKeyStr(chain string, vaultID uint32, signerID int, epoch uint64) string {
	return chain + "_" + itoa(int(vaultID)) + "_" + itoa(signerID) + "_" + itoa(int(epoch))
}

func itoa(i int) string {
	return string(rune('0' + i%10))
}

// SignParams 签名参数
type SignParams struct {
	JobID     string
	Chain     string
	VaultID   uint32
	KeyEpoch  uint64
	SignAlgo  pb.SignAlgo
	Messages  [][]byte // 待签名消息（每个 32 字节）
	Threshold int
}

// SignResult 签名结果
type SignResult struct {
	JobID      string
	Signatures [][]byte // 每个消息的签名（64 字节 BIP-340 格式）
	Success    bool
	Error      error
}

// Sign 执行签名（同步等待结果）
func (s *SignerService) Sign(ctx context.Context, params *SignParams) (*SignResult, error) {
	// 获取本地密钥份额
	share := s.GetShare(params.Chain, params.VaultID, params.KeyEpoch)
	if share == nil {
		return nil, ErrNoShare
	}

	// 启动协调者会话
	startParams := &StartSessionParams{
		JobID:     params.JobID,
		VaultID:   params.VaultID,
		Chain:     params.Chain,
		KeyEpoch:  params.KeyEpoch,
		SignAlgo:  params.SignAlgo,
		Messages:  params.Messages,
		Threshold: params.Threshold,
	}

	if err := s.coordinator.StartSession(ctx, startParams); err != nil {
		return nil, err
	}

	// 等待签名完成
	result := s.waitForSignature(ctx, params.JobID)
	return result, result.Error
}

// waitForSignature 等待签名完成
func (s *SignerService) waitForSignature(ctx context.Context, jobID string) *SignResult {
	deadline := time.After(s.signTimeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return &SignResult{JobID: jobID, Success: false, Error: ctx.Err()}
		case <-deadline:
			return &SignResult{JobID: jobID, Success: false, Error: ErrSigningTimeout}
		case <-ticker.C:
			sess := s.coordinator.GetSession(jobID)
			if sess == nil {
				continue
			}
			sess.mu.RLock()
			state := sess.State
			sigs := sess.FinalSignatures
			sess.mu.RUnlock()

			if state == CoordStateComplete {
				return &SignResult{JobID: jobID, Signatures: sigs, Success: true}
			}
			if state == CoordStateFailed {
				return &SignResult{JobID: jobID, Success: false, Error: ErrSigningFailed}
			}
		}
	}
}

// GenerateNonce 生成 nonce 对
func (s *SignerService) GenerateNonce() (*roast.SignerNonce, error) {
	// 生成随机 nonce
	hidingBytes := make([]byte, 32)
	bindingBytes := make([]byte, 32)
	if _, err := rand.Read(hidingBytes); err != nil {
		return nil, err
	}
	if _, err := rand.Read(bindingBytes); err != nil {
		return nil, err
	}

	hiding := new(big.Int).SetBytes(hidingBytes)
	hiding.Mod(hiding, s.group.Order())

	binding := new(big.Int).SetBytes(bindingBytes)
	binding.Mod(binding, s.group.Order())

	// 计算承诺点
	hidingPoint := s.group.ScalarBaseMult(hiding)
	bindingPoint := s.group.ScalarBaseMult(binding)

	return &roast.SignerNonce{
		HidingNonce:  hiding,
		BindingNonce: binding,
		HidingPoint:  hidingPoint,
		BindingPoint: bindingPoint,
	}, nil
}

// ComputePartialSig 计算部分签名
func (s *SignerService) ComputePartialSig(
	signerID int,
	nonce *roast.SignerNonce,
	allNonces []roast.SignerNonce,
	msg []byte,
	share *big.Int,
	groupPubX *big.Int,
	signerIDs []int,
) (*big.Int, error) {
	// 计算群承诺 R
	R, err := roast.ComputeGroupCommitment(allNonces, msg, s.group)
	if err != nil {
		return nil, err
	}

	// 计算绑定系数 ρ_i
	rho := roast.ComputeBindingCoefficient(signerID, msg, allNonces, s.group)

	// 计算挑战值 e
	e := roast.ComputeChallenge(R, groupPubX, msg, s.group)

	// 计算拉格朗日系数
	lambdas := roast.ComputeLagrangeCoefficientsForSet(signerIDs, s.group.Order())
	lambda := lambdas[signerID]

	// 计算部分签名
	z := roast.ComputePartialSignature(
		signerID,
		nonce.HidingNonce,
		nonce.BindingNonce,
		share,
		rho,
		lambda,
		e,
		s.group,
	)

	return z, nil
}

// VerifyPartialSig 验证部分签名
func (s *SignerService) VerifyPartialSig(
	signerID int,
	share *big.Int,
	nonce roast.SignerNonce,
	allNonces []roast.SignerNonce,
	msg []byte,
	pubKeyShare curve.Point,
	groupPubX *big.Int,
	signerIDs []int,
) bool {
	// 计算绑定系数
	rho := roast.ComputeBindingCoefficient(signerID, msg, allNonces, s.group)

	// 计算群承诺 R
	R, err := roast.ComputeGroupCommitment(allNonces, msg, s.group)
	if err != nil {
		return false
	}

	// 计算挑战值
	e := roast.ComputeChallenge(R, groupPubX, msg, s.group)

	// 计算拉格朗日系数
	lambdas := roast.ComputeLagrangeCoefficientsForSet(signerIDs, s.group.Order())
	lambda := lambdas[signerID]

	return roast.VerifyPartialSignature(signerID, share, nonce, rho, lambda, e, pubKeyShare, s.group)
}

// AggregateSignatures 聚合签名
func (s *SignerService) AggregateSignatures(
	allNonces []roast.SignerNonce,
	shares []roast.SignerShare,
	msg []byte,
) ([]byte, error) {
	// 计算群承诺 R
	R, err := roast.ComputeGroupCommitment(allNonces, msg, s.group)
	if err != nil {
		return nil, err
	}

	// 聚合签名
	return roast.AggregateSignatures(R, shares, s.group)
}

// ========== 辅助函数 ==========

// ComputeLagrangeCoefficient 计算拉格朗日系数
func ComputeLagrangeCoefficient(signerID int, signerIDs []int, order *big.Int) *big.Int {
	ids := make([]*big.Int, len(signerIDs))
	for i, id := range signerIDs {
		ids[i] = big.NewInt(int64(id))
	}

	lambdas := dkg.ComputeLagrangeCoefficients(ids, order)

	for i, id := range signerIDs {
		if id == signerID {
			return lambdas[i]
		}
	}
	return big.NewInt(0)
}

// init 用于避免未使用导入
func init() {
	_ = logs.Debug
}
