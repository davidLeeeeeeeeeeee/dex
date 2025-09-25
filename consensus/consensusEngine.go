package consensus

import (
	"context"
	"dex/interfaces"
	"dex/types"
	"fmt"
	"sync"
	"time"
)

// ============================================
// 共识引擎
// ============================================

type SnowmanEngine struct {
	mu            sync.RWMutex
	nodeID        types.NodeID
	store         interfaces.BlockStore
	config        *ConsensusConfig
	events        interfaces.EventBus
	snowballs     map[uint64]*Snowball
	activeQueries map[string]*QueryContext
	preferences   map[uint64]string
}

type QueryContext struct {
	queryKey  string
	blockID   string
	votes     map[string]int
	voters    map[types.NodeID]bool
	responded int
	startTime time.Time
	height    uint64
}

func NewSnowmanEngine(nodeID types.NodeID, store interfaces.BlockStore, config *ConsensusConfig, events interfaces.EventBus) interfaces.ConsensusEngine {
	return &SnowmanEngine{
		nodeID:        nodeID,
		store:         store,
		config:        config,
		events:        events,
		snowballs:     make(map[uint64]*Snowball),
		activeQueries: make(map[string]*QueryContext),
		preferences:   make(map[uint64]string),
	}
}

func (e *SnowmanEngine) Start(ctx context.Context) error {
	// 初始化创世区块的Snowball
	e.mu.Lock()
	genesisSB := NewSnowball("genesis")
	genesisSB.Finalize()
	e.snowballs[0] = genesisSB
	e.preferences[0] = "genesis"
	e.mu.Unlock()

	// 定期检查超时
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				e.checkTimeouts()
			case <-ctx.Done():
				return
			}
		}
	}()

	return nil
}

func (e *SnowmanEngine) RegisterQuery(nodeID types.NodeID, requestID uint32, blockID string, height uint64) string {
	e.mu.Lock()
	defer e.mu.Unlock()

	queryKey := fmt.Sprintf("%s-%d", nodeID, requestID)
	e.activeQueries[queryKey] = &QueryContext{
		queryKey:  queryKey,
		blockID:   blockID,
		votes:     make(map[string]int),
		voters:    make(map[types.NodeID]bool),
		responded: 0,
		startTime: time.Now(),
		height:    height,
	}

	return queryKey
}

func (e *SnowmanEngine) SubmitChit(nodeID types.NodeID, queryKey string, preferredID string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	ctx, exists := e.activeQueries[queryKey]
	if !exists || ctx.voters[nodeID] {
		return
	}

	ctx.voters[nodeID] = true
	ctx.votes[preferredID]++
	ctx.responded++

	if ctx.responded >= e.config.Alpha {
		e.processVotes(ctx)
		delete(e.activeQueries, queryKey)
		e.events.PublishAsync(types.BaseEvent{
			EventType: types.EventQueryComplete,
			EventData: ctx,
		})
	}
}

func (e *SnowmanEngine) processVotes(ctx *QueryContext) {
	sb, exists := e.snowballs[ctx.height]
	if !exists {
		sb = NewSnowball("")
		e.snowballs[ctx.height] = sb
	}

	candidates := make([]string, 0)
	blocks := e.store.GetByHeight(ctx.height)
	for _, block := range blocks {
		candidates = append(candidates, block.ID)
	}

	sb.RecordVote(candidates, ctx.votes, e.config.Alpha)

	newPreference := sb.GetPreference()
	if newPreference != "" {
		e.preferences[ctx.height] = newPreference
	}

	if sb.CanFinalize(e.config.Beta) && newPreference != "" {
		e.finalizeBlock(ctx.height, newPreference)
	}
}

func (e *SnowmanEngine) finalizeBlock(height uint64, blockID string) {
	e.store.SetFinalized(height, blockID)

	sb := e.snowballs[height]
	if sb != nil {
		sb.Finalize()
	}

	if block, exists := e.store.Get(blockID); exists {
		Logf("[Engine] 🎉 Finalized block %s at height %d\n", blockID, height)
		e.events.PublishAsync(types.BaseEvent{
			EventType: types.EventBlockFinalized,
			EventData: block,
		})
	}
}

func (e *SnowmanEngine) checkTimeouts() {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now()
	toDelete := make([]string, 0)

	for queryKey, ctx := range e.activeQueries {
		if now.Sub(ctx.startTime) > e.config.QueryTimeout {
			toDelete = append(toDelete, queryKey)
		}
	}

	for _, queryKey := range toDelete {
		delete(e.activeQueries, queryKey)
	}

	if len(toDelete) > 0 {
		e.events.PublishAsync(types.BaseEvent{
			EventType: types.EventQueryComplete,
			EventData: nil,
		})
	}
}

func (e *SnowmanEngine) GetActiveQueryCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.activeQueries)
}

func (e *SnowmanEngine) GetPreference(height uint64) string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if pref, exists := e.preferences[height]; exists {
		return pref
	}

	if sb, exists := e.snowballs[height]; exists {
		return sb.GetPreference()
	}

	return ""
}
