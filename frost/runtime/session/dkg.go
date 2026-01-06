// frost/runtime/session/dkg.go
// DKG Session 状态机：纯状态 + 转换逻辑，不包含网络和密码学操作

package session

import (
	"errors"
	"sync"
	"time"
)

// ========== DKG 阶段定义 ==========

// DKGPhase DKG 会话阶段
type DKGPhase int

const (
	DKGPhaseInit       DKGPhase = iota
	DKGPhaseCommitting          // 收集承诺点
	DKGPhaseSharing             // 发送/收集加密 shares
	DKGPhaseResolving           // 处理投诉与 reveal
	DKGPhaseKeyReady            // 密钥生成完成
	DKGPhaseFailed              // 失败
)

func (p DKGPhase) String() string {
	switch p {
	case DKGPhaseInit:
		return "INIT"
	case DKGPhaseCommitting:
		return "COMMITTING"
	case DKGPhaseSharing:
		return "SHARING"
	case DKGPhaseResolving:
		return "RESOLVING"
	case DKGPhaseKeyReady:
		return "KEY_READY"
	case DKGPhaseFailed:
		return "FAILED"
	default:
		return "UNKNOWN"
	}
}

// ========== 错误定义 ==========

var (
	ErrDKGInvalidPhase      = errors.New("invalid DKG phase")
	ErrDKGSessionClosed     = errors.New("DKG session closed")
	ErrDKGDuplicateCommit   = errors.New("duplicate commitment")
	ErrDKGDuplicateShare    = errors.New("duplicate share")
	ErrDKGCommitmentMissing = errors.New("commitment missing for dealer")
)

// ========== DKG Session 参数 ==========

// DKGSessionParams 创建 DKG Session 的参数
type DKGSessionParams struct {
	SessionID string
	Chain     string
	VaultID   uint32
	EpochID   uint64
	SignAlgo  int32
	Committee []string // 委员会成员地址列表
	Threshold int      // 门限 t
	MyIndex   int      // 本节点在委员会中的索引 (1-based)
	MyAddress string   // 本节点地址
}

// ========== 数据结构 ==========

// DKGCommitmentData 承诺数据
type DKGCommitmentData struct {
	DealerIndex      int
	DealerAddress    string
	CommitmentPoints [][]byte // Feldman VSS 承诺点
	AI0              []byte   // 第一个承诺点 A_i0
	ReceivedAt       time.Time
}

// DKGShareData 加密 share 数据
type DKGShareData struct {
	DealerIndex   int
	DealerAddress string
	Ciphertext    []byte
	ReceivedAt    time.Time
}

// DKGComplaintData 投诉数据
type DKGComplaintData struct {
	DealerIndex   int
	ReceiverIndex int
	Bond          uint64
	CreatedAt     time.Time
	Status        string // PENDING | REVEALED | SLASHED
}

// ========== DKG Session ==========

// DKGSession DKG 会话状态机
type DKGSession struct {
	mu sync.RWMutex

	// 标识
	SessionID string
	Chain     string
	VaultID   uint32
	EpochID   uint64
	SignAlgo  int32

	// 委员会
	Committee []string
	Threshold int
	MyIndex   int // 1-based
	MyAddress string

	// 状态
	Phase       DKGPhase
	StartedAt   time.Time
	CompletedAt time.Time
	closed      bool

	// 收集的数据
	Commitments map[int]*DKGCommitmentData   // dealerIndex -> commitment
	Shares      map[int]*DKGShareData        // dealerIndex -> encrypted share for me
	Complaints  map[string]*DKGComplaintData // "dealer_receiver" -> complaint

	// 本地生成的数据（由 worker 设置）
	LocalCommitmentPoints [][]byte
	LocalAI0              []byte

	// 输出
	LocalShare  []byte // 本地密钥份额
	GroupPubkey []byte // 群公钥
}

// NewDKGSession 创建新的 DKG 会话
func NewDKGSession(params DKGSessionParams) *DKGSession {
	return &DKGSession{
		SessionID:   params.SessionID,
		Chain:       params.Chain,
		VaultID:     params.VaultID,
		EpochID:     params.EpochID,
		SignAlgo:    params.SignAlgo,
		Committee:   params.Committee,
		Threshold:   params.Threshold,
		MyIndex:     params.MyIndex,
		MyAddress:   params.MyAddress,
		Phase:       DKGPhaseInit,
		StartedAt:   time.Now(),
		Commitments: make(map[int]*DKGCommitmentData),
		Shares:      make(map[int]*DKGShareData),
		Complaints:  make(map[string]*DKGComplaintData),
	}
}

// ========== 状态转换方法 ==========

// Start 启动 DKG 会话，进入 COMMITTING 阶段
func (s *DKGSession) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrDKGSessionClosed
	}
	if s.Phase != DKGPhaseInit {
		return ErrDKGInvalidPhase
	}

	s.Phase = DKGPhaseCommitting
	return nil
}

// TransitionToSharing 检查是否可以进入 SHARING 阶段
func (s *DKGSession) TransitionToSharing() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrDKGSessionClosed
	}
	if s.Phase != DKGPhaseCommitting {
		return ErrDKGInvalidPhase
	}

	// 检查是否收集到足够的 commitments
	if !s.hasEnoughCommitmentsLocked() {
		return errors.New("not enough commitments")
	}

	s.Phase = DKGPhaseSharing
	return nil
}

// TransitionToResolving 进入投诉解决阶段
func (s *DKGSession) TransitionToResolving() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrDKGSessionClosed
	}
	if s.Phase != DKGPhaseSharing {
		return ErrDKGInvalidPhase
	}

	s.Phase = DKGPhaseResolving
	return nil
}

// TransitionToKeyReady 进入密钥就绪状态
func (s *DKGSession) TransitionToKeyReady(localShare, groupPubkey []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrDKGSessionClosed
	}
	if s.Phase != DKGPhaseSharing && s.Phase != DKGPhaseResolving {
		return ErrDKGInvalidPhase
	}

	s.LocalShare = localShare
	s.GroupPubkey = groupPubkey
	s.Phase = DKGPhaseKeyReady
	s.CompletedAt = time.Now()
	return nil
}

// MarkFailed 标记会话失败
func (s *DKGSession) MarkFailed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Phase = DKGPhaseFailed
	s.CompletedAt = time.Now()
}

// ========== 数据收集方法 ==========

// AddCommitment 添加承诺数据
func (s *DKGSession) AddCommitment(dealerIndex int, dealerAddr string, points [][]byte, ai0 []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrDKGSessionClosed
	}
	if s.Phase != DKGPhaseCommitting {
		return ErrDKGInvalidPhase
	}
	if _, exists := s.Commitments[dealerIndex]; exists {
		return ErrDKGDuplicateCommit
	}

	s.Commitments[dealerIndex] = &DKGCommitmentData{
		DealerIndex:      dealerIndex,
		DealerAddress:    dealerAddr,
		CommitmentPoints: points,
		AI0:              ai0,
		ReceivedAt:       time.Now(),
	}
	return nil
}

// AddShare 添加加密 share
func (s *DKGSession) AddShare(dealerIndex int, dealerAddr string, ciphertext []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrDKGSessionClosed
	}
	if s.Phase != DKGPhaseSharing {
		return ErrDKGInvalidPhase
	}
	if _, exists := s.Shares[dealerIndex]; exists {
		return ErrDKGDuplicateShare
	}

	// 检查 dealer 是否已提交 commitment
	if _, hasCommit := s.Commitments[dealerIndex]; !hasCommit {
		return ErrDKGCommitmentMissing
	}

	s.Shares[dealerIndex] = &DKGShareData{
		DealerIndex:   dealerIndex,
		DealerAddress: dealerAddr,
		Ciphertext:    ciphertext,
		ReceivedAt:    time.Now(),
	}
	return nil
}

// SetLocalCommitment 设置本地生成的承诺数据
func (s *DKGSession) SetLocalCommitment(points [][]byte, ai0 []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LocalCommitmentPoints = points
	s.LocalAI0 = ai0
}

// ========== 查询方法 ==========

// GetPhase 获取当前阶段
func (s *DKGSession) GetPhase() DKGPhase {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Phase
}

// HasEnoughCommitments 检查是否收集到足够的承诺
func (s *DKGSession) HasEnoughCommitments() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hasEnoughCommitmentsLocked()
}

func (s *DKGSession) hasEnoughCommitmentsLocked() bool {
	// 需要至少 threshold+1 个承诺
	return len(s.Commitments) >= s.Threshold+1
}

// HasEnoughShares 检查是否收集到足够的 shares
func (s *DKGSession) HasEnoughShares() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.Shares) >= s.Threshold+1
}

// GetCommitment 获取指定 dealer 的承诺
func (s *DKGSession) GetCommitment(dealerIndex int) (*DKGCommitmentData, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.Commitments[dealerIndex]
	return c, ok
}

// GetShare 获取指定 dealer 发给我的 share
func (s *DKGSession) GetShare(dealerIndex int) (*DKGShareData, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sh, ok := s.Shares[dealerIndex]
	return sh, ok
}

// GetAllCommitments 获取所有承诺的快照
func (s *DKGSession) GetAllCommitments() map[int]*DKGCommitmentData {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[int]*DKGCommitmentData, len(s.Commitments))
	for k, v := range s.Commitments {
		result[k] = v
	}
	return result
}

// GetAllShares 获取所有 shares 的快照
func (s *DKGSession) GetAllShares() map[int]*DKGShareData {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[int]*DKGShareData, len(s.Shares))
	for k, v := range s.Shares {
		result[k] = v
	}
	return result
}

// GetAllAI0s 获取所有 A_i0 承诺点（用于计算群公钥）
func (s *DKGSession) GetAllAI0s() [][]byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([][]byte, 0, len(s.Commitments))
	for i := 1; i <= len(s.Committee); i++ {
		if c, ok := s.Commitments[i]; ok {
			result = append(result, c.AI0)
		}
	}
	return result
}

// GetLocalShare 获取本地密钥份额
func (s *DKGSession) GetLocalShare() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.LocalShare == nil {
		return nil
	}
	result := make([]byte, len(s.LocalShare))
	copy(result, s.LocalShare)
	return result
}

// GetGroupPubkey 获取群公钥
func (s *DKGSession) GetGroupPubkey() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.GroupPubkey == nil {
		return nil
	}
	result := make([]byte, len(s.GroupPubkey))
	copy(result, s.GroupPubkey)
	return result
}

// Close 关闭会话
func (s *DKGSession) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}

// IsClosed 检查会话是否已关闭
func (s *DKGSession) IsClosed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closed
}
