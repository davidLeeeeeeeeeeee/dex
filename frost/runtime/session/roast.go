// frost/runtime/session/roast.go
// ROAST session state machine: state + data only, no crypto or transport.

package session

import (
	"errors"
	"sync"
	"time"
)

// ========== Errors ==========

var (
	ErrInvalidState  = errors.New("invalid state")
	ErrSessionClosed = errors.New("session closed")
)

// SessionParams defines inputs for creating a new ROAST session.
type SessionParams struct {
	JobID       string
	VaultID     uint32
	Chain       string
	KeyEpoch    uint64
	SignAlgo    int32
	Messages    [][]byte
	Committee   []Participant
	Threshold   int
	MyIndex     int
	StartHeight uint64
	StartedAt   time.Time
}

// NonceData contains nonce commitments from a participant.
type NonceData struct {
	ParticipantIndex int
	HidingNonces     [][]byte
	BindingNonces    [][]byte
	ReceivedAt       time.Time
}

// ShareData contains signature shares from a participant.
type ShareData struct {
	ParticipantIndex int
	Shares           [][]byte
	ReceivedAt       time.Time
}

// Session tracks ROAST coordinator state and collected data.
type Session struct {
	mu sync.RWMutex

	JobID    string
	VaultID  uint32
	Chain    string
	KeyEpoch uint64
	SignAlgo int32

	Messages  [][]byte
	Committee []Participant
	Threshold int
	MyIndex   int

	SelectedSet []int
	Nonces      map[int]*NonceData
	Shares      map[int]*ShareData

	State       SignSessionState
	RetryCount  int
	StartHeight uint64
	StartedAt   time.Time
	CompletedAt time.Time

	FinalSignatures [][]byte

	closed bool
}

// NewSession creates a new ROAST session with initialized maps.
func NewSession(params SessionParams) *Session {
	startedAt := params.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}

	return &Session{
		JobID:       params.JobID,
		VaultID:     params.VaultID,
		Chain:       params.Chain,
		KeyEpoch:    params.KeyEpoch,
		SignAlgo:    params.SignAlgo,
		Messages:    params.Messages,
		Committee:   params.Committee,
		Threshold:   params.Threshold,
		MyIndex:     params.MyIndex,
		Nonces:      make(map[int]*NonceData),
		Shares:      make(map[int]*ShareData),
		State:       SignSessionStateInit,
		StartHeight: params.StartHeight,
		StartedAt:   startedAt,
	}
}

// Start initializes the session to collect nonces.
func (s *Session) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrSessionClosed
	}
	if s.State != SignSessionStateInit {
		return ErrInvalidState
	}

	s.selectInitialSetLocked()
	s.State = SignSessionStateCollectingNonces
	return nil
}

// SelectInitialSet chooses the first threshold+1 participants.
func (s *Session) SelectInitialSet() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.selectInitialSetLocked()
}

func (s *Session) selectInitialSetLocked() {
	minSigners := s.minSigners()
	s.SelectedSet = make([]int, 0, minSigners)
	for i := 0; i < len(s.Committee) && len(s.SelectedSet) < minSigners; i++ {
		s.SelectedSet = append(s.SelectedSet, i)
	}
}

// ResetForRetry rotates the selected set and clears collected data.
func (s *Session) ResetForRetry(maxRetries int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return false
	}

	s.RetryCount++
	if maxRetries > 0 && s.RetryCount > maxRetries {
		return false
	}

	s.selectAlternativeSetLocked()
	s.Nonces = make(map[int]*NonceData)
	s.Shares = make(map[int]*ShareData)
	s.State = SignSessionStateCollectingNonces
	return true
}

func (s *Session) selectAlternativeSetLocked() {
	if len(s.Committee) == 0 {
		s.SelectedSet = nil
		return
	}

	minSigners := s.minSigners()
	offset := s.RetryCount % len(s.Committee)
	s.SelectedSet = make([]int, 0, minSigners)
	for i := 0; i < len(s.Committee) && len(s.SelectedSet) < minSigners; i++ {
		idx := (i + offset) % len(s.Committee)
		s.SelectedSet = append(s.SelectedSet, idx)
	}
}

// AddNonce records nonce commitments.
func (s *Session) AddNonce(participantIndex int, hidingNonces, bindingNonces [][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrSessionClosed
	}
	if s.State != SignSessionStateCollectingNonces {
		return ErrInvalidState
	}

	s.Nonces[participantIndex] = &NonceData{
		ParticipantIndex: participantIndex,
		HidingNonces:     hidingNonces,
		BindingNonces:    bindingNonces,
		ReceivedAt:       time.Now(),
	}
	return nil
}

// AddShare records signature shares.
func (s *Session) AddShare(participantIndex int, shares [][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrSessionClosed
	}
	if s.State != SignSessionStateCollectingShares {
		return ErrInvalidState
	}

	s.Shares[participantIndex] = &ShareData{
		ParticipantIndex: participantIndex,
		Shares:           shares,
		ReceivedAt:       time.Now(),
	}
	return nil
}

// HasEnoughNonces checks if enough nonces are collected.
func (s *Session) HasEnoughNonces() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	minSigners := s.minSigners()
	count := 0
	for _, idx := range s.SelectedSet {
		if _, ok := s.Nonces[idx]; ok {
			count++
		}
	}
	return count >= minSigners
}

// HasEnoughShares checks if enough shares are collected.
func (s *Session) HasEnoughShares() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	minSigners := s.minSigners()
	count := 0
	for _, idx := range s.SelectedSet {
		if _, ok := s.Shares[idx]; ok {
			count++
		}
	}
	return count >= minSigners
}

// GetState returns the current session state.
func (s *Session) GetState() SignSessionState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.State
}

// SetState updates the session state.
func (s *Session) SetState(state SignSessionState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.State = state
}

// SelectedSetSnapshot returns a copy of the selected set.
func (s *Session) SelectedSetSnapshot() []int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]int, len(s.SelectedSet))
	copy(result, s.SelectedSet)
	return result
}

// GetNonce returns the nonce data for a participant index.
func (s *Session) GetNonce(index int) (*NonceData, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	nonce, ok := s.Nonces[index]
	return nonce, ok
}

// GetShare returns the share data for a participant index.
func (s *Session) GetShare(index int) (*ShareData, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	share, ok := s.Shares[index]
	return share, ok
}

// GetFinalSignatures returns a copy of final signatures.
func (s *Session) GetFinalSignatures() [][]byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([][]byte, len(s.FinalSignatures))
	copy(result, s.FinalSignatures)
	return result
}

// MarkCompleted stores final signatures and marks the session complete.
func (s *Session) MarkCompleted(signatures [][]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.FinalSignatures = signatures
	s.State = SignSessionStateComplete
	s.CompletedAt = time.Now()
}

// Close marks the session as closed to reject further mutations.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}

func (s *Session) minSigners() int {
	minSigners := s.Threshold + 1
	if minSigners <= 0 {
		minSigners = 1
	}
	return minSigners
}
