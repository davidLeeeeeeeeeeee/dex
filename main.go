package main

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ============================================
// 核心类型定义和接口
// ============================================

type NodeID int

// 消息类型
type MessageType int

const (
	MsgPullQuery MessageType = iota
	MsgPushQuery
	MsgChits
	MsgGet
	MsgPut
	MsgGossip
	MsgSyncRequest
	MsgSyncResponse
)

// 基础消息结构
type Message struct {
	Type      MessageType
	From      NodeID
	RequestID uint32
	BlockID   string
	Block     *Block
	Height    uint64
	// For Chits
	PreferredID       string
	PreferredIDHeight uint64
	AcceptedID        string
	AcceptedHeight    uint64
	// For Sync
	FromHeight uint64
	ToHeight   uint64
	Blocks     []*Block
	SyncID     uint32
}

// 区块定义
type Block struct {
	ID       string
	Height   uint64
	ParentID string
	Data     string
	Proposer int
	Round    int
}

func (b *Block) String() string {
	return fmt.Sprintf("Block{ID:%s, Height:%d, Parent:%s, Proposer:%d, Round:%d}",
		b.ID, b.Height, b.ParentID, b.Proposer, b.Round)
}

// ============================================
// 配置管理
// ============================================

type Config struct {
	Network   NetworkConfig
	Consensus ConsensusConfig
	Node      NodeConfig
	Sync      SyncConfig
	Gossip    GossipConfig
}

type NetworkConfig struct {
	NumNodes          int
	NumByzantineNodes int
	NetworkLatency    time.Duration
}

type ConsensusConfig struct {
	K                    int
	Alpha                int
	Beta                 int
	QueryTimeout         time.Duration
	MaxConcurrentQueries int
	NumHeights           int
	BlocksPerHeight      int
}

type NodeConfig struct {
	ProposalInterval time.Duration
}

type SyncConfig struct {
	CheckInterval   time.Duration
	BehindThreshold uint64
	BatchSize       uint64
	Timeout         time.Duration
}

type GossipConfig struct {
	Fanout   int
	Interval time.Duration
}

func DefaultConfig() *Config {
	return &Config{
		Network: NetworkConfig{
			NumNodes:          100,
			NumByzantineNodes: 10,
			NetworkLatency:    100 * time.Millisecond,
		},
		Consensus: ConsensusConfig{
			K:                    20,
			Alpha:                15,
			Beta:                 15,
			QueryTimeout:         3 * time.Second,
			MaxConcurrentQueries: 20,
			NumHeights:           10,
			BlocksPerHeight:      5,
		},
		Node: NodeConfig{
			ProposalInterval: 1200 * time.Millisecond,
		},
		Sync: SyncConfig{
			CheckInterval:   2 * time.Second,
			BehindThreshold: 2,
			BatchSize:       10,
			Timeout:         5 * time.Second,
		},
		Gossip: GossipConfig{
			Fanout:   15,
			Interval: 50 * time.Millisecond,
		},
	}
}

// ============================================
// 事件系统
// ============================================

type EventType string

const (
	EventBlockFinalized EventType = "block.finalized"
	EventBlockReceived  EventType = "block.received"
	EventQueryComplete  EventType = "query.complete"
	EventSyncComplete   EventType = "sync.complete"
	EventNewBlock       EventType = "block.new"
)

type Event interface {
	Type() EventType
	Data() interface{}
}

type BaseEvent struct {
	eventType EventType
	data      interface{}
}

func (e BaseEvent) Type() EventType   { return e.eventType }
func (e BaseEvent) Data() interface{} { return e.data }

type EventHandler func(Event)

type EventBus interface {
	Subscribe(topic EventType, handler EventHandler)
	Publish(event Event)
	PublishAsync(event Event)
}

type SimpleEventBus struct {
	mu       sync.RWMutex
	handlers map[EventType][]EventHandler
}

func NewEventBus() EventBus {
	return &SimpleEventBus{
		handlers: make(map[EventType][]EventHandler),
	}
}

func (eb *SimpleEventBus) Subscribe(topic EventType, handler EventHandler) {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	eb.handlers[topic] = append(eb.handlers[topic], handler)
}

func (eb *SimpleEventBus) Publish(event Event) {
	eb.mu.RLock()
	handlers := eb.handlers[event.Type()]
	eb.mu.RUnlock()

	for _, handler := range handlers {
		handler(event)
	}
}

func (eb *SimpleEventBus) PublishAsync(event Event) {
	go eb.Publish(event)
}

// ============================================
// 网络传输层接口
// ============================================

type Transport interface {
	Send(to NodeID, msg Message) error
	Receive() <-chan Message
	Broadcast(msg Message, peers []NodeID)
	SamplePeers(exclude NodeID, count int) []NodeID
}

type SimulatedTransport struct {
	nodeID         NodeID
	inbox          chan Message
	network        *NetworkManager
	ctx            context.Context
	networkLatency time.Duration
}

func NewSimulatedTransport(nodeID NodeID, network *NetworkManager, ctx context.Context, latency time.Duration) Transport {
	return &SimulatedTransport{
		nodeID:         nodeID,
		inbox:          make(chan Message, 1000000),
		network:        network,
		ctx:            ctx,
		networkLatency: latency,
	}
}

func (t *SimulatedTransport) Send(to NodeID, msg Message) error {
	go func() {
		delay := t.networkLatency + time.Duration(rand.Intn(int(t.networkLatency/2)))
		time.Sleep(delay)

		select {
		case t.network.GetTransport(to).(*SimulatedTransport).inbox <- msg:
		case <-time.After(100 * time.Millisecond):
		case <-t.ctx.Done():
		}
	}()
	return nil
}

func (t *SimulatedTransport) Receive() <-chan Message {
	return t.inbox
}

func (t *SimulatedTransport) Broadcast(msg Message, peers []NodeID) {
	for _, peer := range peers {
		t.Send(peer, msg)
	}
}

func (t *SimulatedTransport) SamplePeers(exclude NodeID, count int) []NodeID {
	return t.network.SamplePeers(exclude, count)
}

// ============================================
// 区块存储接口
// ============================================

type BlockStore interface {
	Add(block *Block) (bool, error)
	Get(id string) (*Block, bool)
	GetByHeight(height uint64) []*Block
	GetLastAccepted() (string, uint64)
	GetFinalizedAtHeight(height uint64) (*Block, bool)
	GetFinalizedFromHeight(from, to uint64) []*Block
}

type MemoryBlockStore struct {
	mu                 sync.RWMutex
	blocks             map[string]*Block
	heightIndex        map[uint64][]*Block
	lastAccepted       *Block
	lastAcceptedHeight uint64
	finalizedBlocks    map[uint64]*Block
}

func NewMemoryBlockStore() BlockStore {
	store := &MemoryBlockStore{
		blocks:          make(map[string]*Block),
		heightIndex:     make(map[uint64][]*Block),
		finalizedBlocks: make(map[uint64]*Block),
	}

	// 创世区块
	genesis := &Block{
		ID:       "genesis",
		Height:   0,
		ParentID: "",
		Proposer: -1,
	}
	store.blocks[genesis.ID] = genesis
	store.heightIndex[0] = []*Block{genesis}
	store.lastAccepted = genesis
	store.lastAcceptedHeight = 0
	store.finalizedBlocks[0] = genesis

	return store
}

func (s *MemoryBlockStore) Add(block *Block) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.blocks[block.ID]; exists {
		return false, nil
	}

	if err := s.validateBlock(block); err != nil {
		return false, err
	}

	s.blocks[block.ID] = block
	s.heightIndex[block.Height] = append(s.heightIndex[block.Height], block)

	return true, nil
}

func (s *MemoryBlockStore) validateBlock(block *Block) error {
	if block == nil || block.ID == "" {
		return fmt.Errorf("invalid block")
	}
	if block.Height == 0 && block.ID != "genesis" {
		return fmt.Errorf("invalid genesis block")
	}
	if block.Height > 0 && block.ParentID == "" {
		return fmt.Errorf("non-genesis block must have parent")
	}
	return nil
}

func (s *MemoryBlockStore) Get(id string) (*Block, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	block, exists := s.blocks[id]
	return block, exists
}

func (s *MemoryBlockStore) GetByHeight(height uint64) []*Block {
	s.mu.RLock()
	defer s.mu.RUnlock()
	blocks := s.heightIndex[height]
	result := make([]*Block, len(blocks))
	copy(result, blocks)
	return result
}

func (s *MemoryBlockStore) GetLastAccepted() (string, uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastAccepted.ID, s.lastAcceptedHeight
}

func (s *MemoryBlockStore) GetFinalizedAtHeight(height uint64) (*Block, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	block, exists := s.finalizedBlocks[height]
	return block, exists
}

func (s *MemoryBlockStore) GetFinalizedFromHeight(from, to uint64) []*Block {
	s.mu.RLock()
	defer s.mu.RUnlock()

	blocks := make([]*Block, 0)
	for h := from; h <= to && h <= s.lastAcceptedHeight; h++ {
		if block, exists := s.finalizedBlocks[h]; exists {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func (s *MemoryBlockStore) SetFinalized(height uint64, blockID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if block, exists := s.blocks[blockID]; exists {
		s.finalizedBlocks[height] = block
		s.lastAccepted = block
		s.lastAcceptedHeight = height

		// 清理同高度其他区块
		newBlocks := make([]*Block, 0, 1)
		for _, b := range s.heightIndex[height] {
			if b.ID == blockID {
				newBlocks = append(newBlocks, b)
			} else {
				delete(s.blocks, b.ID)
			}
		}
		s.heightIndex[height] = newBlocks
	}
}

// ============================================
// Snowball 共识算法核心
// ============================================

type Snowball struct {
	mu         sync.RWMutex
	blockID    string
	preference string
	confidence int
	finalized  bool
	lastVotes  map[string]int
}

func NewSnowball(blockID string) *Snowball {
	return &Snowball{
		blockID:   blockID,
		lastVotes: make(map[string]int),
	}
}

func (sb *Snowball) RecordVote(candidates []string, votes map[string]int, alpha int) {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	sb.lastVotes = votes

	var winner string
	maxVotes := 0
	for cid, v := range votes {
		if v > maxVotes {
			maxVotes = v
			winner = cid
		}
	}

	if maxVotes >= alpha {
		if winner != sb.preference {
			sb.preference = winner
			sb.confidence = 1
		} else {
			sb.confidence++
		}
	} else {
		if len(candidates) > 0 {
			sort.Strings(candidates)
			largestBlock := candidates[len(candidates)-1]
			if largestBlock != sb.preference {
				sb.preference = largestBlock
				sb.confidence = 0
			}
		}
	}
}

func (sb *Snowball) GetPreference() string {
	sb.mu.RLock()
	defer sb.mu.RUnlock()
	return sb.preference
}

func (sb *Snowball) GetConfidence() int {
	sb.mu.RLock()
	defer sb.mu.RUnlock()
	return sb.confidence
}

func (sb *Snowball) CanFinalize(beta int) bool {
	sb.mu.RLock()
	defer sb.mu.RUnlock()
	return sb.confidence >= beta && !sb.finalized
}

func (sb *Snowball) Finalize() {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.finalized = true
}

func (sb *Snowball) IsFinalized() bool {
	sb.mu.RLock()
	defer sb.mu.RUnlock()
	return sb.finalized
}

// ============================================
// 共识引擎
// ============================================

type ConsensusEngine interface {
	Start(ctx context.Context) error
	RegisterQuery(nodeID NodeID, requestID uint32, blockID string, height uint64) string
	SubmitChit(nodeID NodeID, queryKey string, preferredID string)
	GetActiveQueryCount() int
	GetPreference(height uint64) string
}

type SnowmanEngine struct {
	mu            sync.RWMutex
	nodeID        NodeID
	store         BlockStore
	config        *ConsensusConfig
	events        EventBus
	snowballs     map[uint64]*Snowball
	activeQueries map[string]*QueryContext
	preferences   map[uint64]string
}

type QueryContext struct {
	queryKey  string
	blockID   string
	votes     map[string]int
	voters    map[NodeID]bool
	responded int
	startTime time.Time
	height    uint64
}

func NewSnowmanEngine(nodeID NodeID, store BlockStore, config *ConsensusConfig, events EventBus) ConsensusEngine {
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

func (e *SnowmanEngine) RegisterQuery(nodeID NodeID, requestID uint32, blockID string, height uint64) string {
	e.mu.Lock()
	defer e.mu.Unlock()

	queryKey := fmt.Sprintf("%d-%d", nodeID, requestID)
	e.activeQueries[queryKey] = &QueryContext{
		queryKey:  queryKey,
		blockID:   blockID,
		votes:     make(map[string]int),
		voters:    make(map[NodeID]bool),
		responded: 0,
		startTime: time.Now(),
		height:    height,
	}

	return queryKey
}

func (e *SnowmanEngine) SubmitChit(nodeID NodeID, queryKey string, preferredID string) {
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
		e.events.PublishAsync(BaseEvent{
			eventType: EventQueryComplete,
			data:      ctx,
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
	if store, ok := e.store.(*MemoryBlockStore); ok {
		store.SetFinalized(height, blockID)
	}

	sb := e.snowballs[height]
	if sb != nil {
		sb.Finalize()
	}

	if block, exists := e.store.Get(blockID); exists {
		Logf("[Engine] 🎉 Finalized block %s at height %d\n", blockID, height)
		e.events.PublishAsync(BaseEvent{
			eventType: EventBlockFinalized,
			data:      block,
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
		e.events.PublishAsync(BaseEvent{
			eventType: EventQueryComplete,
			data:      nil,
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

// ============================================
// 查询管理器
// ============================================

type QueryManager struct {
	nodeID      NodeID
	transport   Transport
	store       BlockStore
	engine      ConsensusEngine
	config      *ConsensusConfig
	events      EventBus
	activePolls sync.Map
	nextReqID   uint32
	mu          sync.Mutex
}

type Poll struct {
	requestID uint32
	blockID   string
	queryKey  string
	startTime time.Time
	height    uint64
}

func NewQueryManager(nodeID NodeID, transport Transport, store BlockStore, engine ConsensusEngine, config *ConsensusConfig, events EventBus) *QueryManager {
	qm := &QueryManager{
		nodeID:    nodeID,
		transport: transport,
		store:     store,
		engine:    engine,
		config:    config,
		events:    events,
	}

	// 监听查询完成事件
	events.Subscribe(EventQueryComplete, func(e Event) {
		qm.tryIssueQuery()
	})

	// 监听区块最终化事件
	events.Subscribe(EventBlockFinalized, func(e Event) {
		qm.tryIssueQuery()
	})

	return qm
}

func (qm *QueryManager) tryIssueQuery() {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	_, currentHeight := qm.store.GetLastAccepted()
	nextHeight := currentHeight + 1

	blocks := qm.store.GetByHeight(nextHeight)
	if len(blocks) == 0 {
		return
	}

	if qm.engine.GetActiveQueryCount() >= qm.config.MaxConcurrentQueries {
		return
	}

	qm.issueQuery()
}

func (qm *QueryManager) issueQuery() {
	_, currentHeight := qm.store.GetLastAccepted()
	nextHeight := currentHeight + 1

	blockID := qm.engine.GetPreference(nextHeight)
	if blockID == "" {
		blocks := qm.store.GetByHeight(nextHeight)
		if len(blocks) == 0 {
			return
		}

		candidates := make([]string, 0, len(blocks))
		for _, b := range blocks {
			candidates = append(candidates, b.ID)
		}
		sort.Strings(candidates)
		blockID = candidates[len(candidates)-1]
	}

	block, exists := qm.store.Get(blockID)
	if !exists {
		return
	}

	requestID := atomic.AddUint32(&qm.nextReqID, 1)
	queryKey := qm.engine.RegisterQuery(qm.nodeID, requestID, blockID, block.Height)

	poll := &Poll{
		requestID: requestID,
		blockID:   blockID,
		queryKey:  queryKey,
		startTime: time.Now(),
		height:    block.Height,
	}
	qm.activePolls.Store(requestID, poll)

	peers := qm.transport.SamplePeers(qm.nodeID, qm.config.K)
	msg := Message{
		Type:      MsgPushQuery,
		From:      qm.nodeID,
		RequestID: requestID,
		BlockID:   blockID,
		Block:     block,
		Height:    block.Height,
	}

	qm.transport.Broadcast(msg, peers)
}

func (qm *QueryManager) HandleChit(msg Message) {
	if poll, ok := qm.activePolls.Load(msg.RequestID); ok {
		p := poll.(*Poll)
		qm.engine.SubmitChit(NodeID(msg.From), p.queryKey, msg.PreferredID)
	}
}

func (qm *QueryManager) Start(ctx context.Context) {
	// 初始查询
	go func() {
		time.Sleep(100 * time.Millisecond)
		for i := 0; i < qm.config.MaxConcurrentQueries; i++ {
			qm.tryIssueQuery()
		}
	}()

	// 定期触发查询
	go func() {
		ticker := time.NewTicker(107 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				qm.tryIssueQuery()
			case <-ctx.Done():
				return
			}
		}
	}()
}

// ============================================
// Gossip管理器
// ============================================

type GossipManager struct {
	nodeID     NodeID
	transport  Transport
	store      BlockStore
	config     *GossipConfig
	events     EventBus
	seenBlocks map[string]bool
	mu         sync.RWMutex
}

func NewGossipManager(nodeID NodeID, transport Transport, store BlockStore, config *GossipConfig, events EventBus) *GossipManager {
	gm := &GossipManager{
		nodeID:     nodeID,
		transport:  transport,
		store:      store,
		config:     config,
		events:     events,
		seenBlocks: make(map[string]bool),
	}

	// 监听新区块事件
	events.Subscribe(EventNewBlock, func(e Event) {
		if block, ok := e.Data().(*Block); ok {
			gm.gossipBlock(block)
		}
	})

	return gm
}

func (gm *GossipManager) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(gm.config.Interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				gm.gossipNewBlocks()
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (gm *GossipManager) gossipNewBlocks() {
	_, currentHeight := gm.store.GetLastAccepted()

	blocks := make([]*Block, 0)
	blocks = append(blocks, gm.store.GetByHeight(currentHeight+1)...)
	blocks = append(blocks, gm.store.GetByHeight(currentHeight+2)...)

	for _, block := range blocks {
		if block.ID == "genesis" {
			continue
		}

		gm.mu.RLock()
		alreadyGossiped := gm.seenBlocks[block.ID]
		gm.mu.RUnlock()

		if !alreadyGossiped {
			gm.gossipBlock(block)
		}
	}
}

func (gm *GossipManager) gossipBlock(block *Block) {
	gm.mu.Lock()
	gm.seenBlocks[block.ID] = true
	gm.mu.Unlock()

	peers := gm.transport.SamplePeers(gm.nodeID, gm.config.Fanout)
	msg := Message{
		Type:    MsgGossip,
		From:    gm.nodeID,
		Block:   block,
		BlockID: block.ID,
		Height:  block.Height,
	}

	gm.transport.Broadcast(msg, peers)
}

func (gm *GossipManager) HandleGossip(msg Message) {
	if msg.Block == nil {
		return
	}

	gm.mu.RLock()
	alreadySeen := gm.seenBlocks[msg.Block.ID]
	gm.mu.RUnlock()

	if alreadySeen {
		return
	}

	gm.mu.Lock()
	gm.seenBlocks[msg.Block.ID] = true
	gm.mu.Unlock()

	isNew, err := gm.store.Add(msg.Block)
	if err != nil {
		return
	}

	if isNew {
		Logf("[Node %d] Received new block %s via gossip from Node %d\n",
			gm.nodeID, msg.Block.ID, msg.From)

		gm.events.Publish(BaseEvent{
			eventType: EventBlockReceived,
			data:      msg.Block,
		})

		// 继续传播
		go func() {
			time.Sleep(time.Duration(50+rand.Intn(100)) * time.Millisecond)
			gm.gossipBlock(msg.Block)
		}()
	}
}

// ============================================
// 同步管理器
// ============================================

type SyncManager struct {
	nodeID       NodeID
	transport    Transport
	store        BlockStore
	config       *SyncConfig
	events       EventBus
	syncRequests map[uint32]time.Time
	nextSyncID   uint32
	syncing      bool
	mu           sync.RWMutex
}

func NewSyncManager(nodeID NodeID, transport Transport, store BlockStore, config *SyncConfig, events EventBus) *SyncManager {
	return &SyncManager{
		nodeID:       nodeID,
		transport:    transport,
		store:        store,
		config:       config,
		events:       events,
		syncRequests: make(map[uint32]time.Time),
	}
}

func (sm *SyncManager) Start(ctx context.Context) {
	// 定期检查同步需求
	go func() {
		ticker := time.NewTicker(sm.config.CheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				sm.checkAndSync()
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (sm *SyncManager) checkAndSync() {
	sm.mu.Lock()
	if sm.syncing {
		sm.mu.Unlock()
		return
	}
	sm.mu.Unlock()

	_, localHeight := sm.store.GetLastAccepted()

	// 这里简化了peer高度的检查
	// 实际应该通过查询其他节点来获取
	maxPeerHeight := localHeight + sm.config.BehindThreshold + 1

	if maxPeerHeight > localHeight+sm.config.BehindThreshold {
		sm.requestSync(localHeight+1, minUint64(localHeight+sm.config.BatchSize, maxPeerHeight))
	}
}

func (sm *SyncManager) requestSync(fromHeight, toHeight uint64) {
	sm.mu.Lock()
	if sm.syncing {
		sm.mu.Unlock()
		return
	}
	sm.syncing = true
	syncID := atomic.AddUint32(&sm.nextSyncID, 1)
	sm.syncRequests[syncID] = time.Now()
	sm.mu.Unlock()

	peers := sm.transport.SamplePeers(sm.nodeID, 5)
	if len(peers) > 0 {
		msg := Message{
			Type:       MsgSyncRequest,
			From:       sm.nodeID,
			SyncID:     syncID,
			FromHeight: fromHeight,
			ToHeight:   toHeight,
		}
		sm.transport.Send(peers[0], msg)
	}
}

func (sm *SyncManager) HandleSyncRequest(msg Message) {
	blocks := sm.store.GetFinalizedFromHeight(msg.FromHeight, msg.ToHeight)

	if len(blocks) == 0 {
		return
	}

	response := Message{
		Type:       MsgSyncResponse,
		From:       sm.nodeID,
		SyncID:     msg.SyncID,
		Blocks:     blocks,
		FromHeight: msg.FromHeight,
		ToHeight:   msg.ToHeight,
	}

	sm.transport.Send(NodeID(msg.From), response)
}

func (sm *SyncManager) HandleSyncResponse(msg Message) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if _, ok := sm.syncRequests[msg.SyncID]; !ok {
		return
	}

	delete(sm.syncRequests, msg.SyncID)
	sm.syncing = false

	for _, block := range msg.Blocks {
		sm.store.Add(block)
	}

	sm.events.PublishAsync(BaseEvent{
		eventType: EventSyncComplete,
		data:      len(msg.Blocks),
	})
}

// ============================================
// 提案管理器
// ============================================

type ProposalManager struct {
	nodeID         NodeID
	transport      Transport
	store          BlockStore
	config         *NodeConfig
	events         EventBus
	proposedBlocks map[string]bool
	proposalRound  int
	mu             sync.Mutex
}

func NewProposalManager(nodeID NodeID, transport Transport, store BlockStore, config *NodeConfig, events EventBus) *ProposalManager {
	return &ProposalManager{
		nodeID:         nodeID,
		transport:      transport,
		store:          store,
		config:         config,
		events:         events,
		proposedBlocks: make(map[string]bool),
	}
}

func (pm *ProposalManager) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(pm.config.ProposalInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				pm.proposeBlock()
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (pm *ProposalManager) proposeBlock() {
	pm.mu.Lock()
	pm.proposalRound++
	currentRound := pm.proposalRound
	pm.mu.Unlock()

	lastAcceptedID, lastHeight := pm.store.GetLastAccepted()
	targetHeight := lastHeight + 1

	// 简化的提案逻辑
	if rand.Float32() > 0.03 { // 3%概率提案
		return
	}

	blockID := fmt.Sprintf("block-%d-%d-r%d", targetHeight, pm.nodeID, currentRound)

	pm.mu.Lock()
	if pm.proposedBlocks[blockID] {
		pm.mu.Unlock()
		return
	}
	pm.proposedBlocks[blockID] = true
	pm.mu.Unlock()

	block := &Block{
		ID:       blockID,
		Height:   targetHeight,
		ParentID: lastAcceptedID,
		Data:     fmt.Sprintf("Height %d, Proposer %d, Round %d", targetHeight, pm.nodeID, currentRound),
		Proposer: int(pm.nodeID),
		Round:    currentRound,
	}

	isNew, err := pm.store.Add(block)
	if err != nil || !isNew {
		return
	}

	Logf("[Node %d] Proposing %s on parent %s\n", pm.nodeID, block, lastAcceptedID)

	pm.events.Publish(BaseEvent{
		eventType: EventNewBlock,
		data:      block,
	})
}

// ============================================
// 消息处理器
// ============================================

type MessageHandler struct {
	nodeID        NodeID
	isByzantine   bool
	transport     Transport
	store         BlockStore
	engine        ConsensusEngine
	queryManager  *QueryManager
	gossipManager *GossipManager
	syncManager   *SyncManager
	events        EventBus
	config        *ConsensusConfig
}

func NewMessageHandler(nodeID NodeID, isByzantine bool, transport Transport, store BlockStore, engine ConsensusEngine, events EventBus, config *ConsensusConfig) *MessageHandler {
	return &MessageHandler{
		nodeID:      nodeID,
		isByzantine: isByzantine,
		transport:   transport,
		store:       store,
		engine:      engine,
		events:      events,
		config:      config,
	}
}

func (h *MessageHandler) SetManagers(qm *QueryManager, gm *GossipManager, sm *SyncManager) {
	h.queryManager = qm
	h.gossipManager = gm
	h.syncManager = sm
}

func (h *MessageHandler) Handle(msg Message) {
	if h.isByzantine && (msg.Type == MsgPullQuery || msg.Type == MsgPushQuery) {
		return
	}

	switch msg.Type {
	case MsgPullQuery:
		h.handlePullQuery(msg)
	case MsgPushQuery:
		h.handlePushQuery(msg)
	case MsgChits:
		h.queryManager.HandleChit(msg)
	case MsgGet:
		h.handleGet(msg)
	case MsgPut:
		h.handlePut(msg)
	case MsgGossip:
		h.gossipManager.HandleGossip(msg)
	case MsgSyncRequest:
		h.syncManager.HandleSyncRequest(msg)
	case MsgSyncResponse:
		h.syncManager.HandleSyncResponse(msg)
	}
}

func (h *MessageHandler) handlePullQuery(msg Message) {
	block, exists := h.store.Get(msg.BlockID)
	if !exists {
		h.transport.Send(NodeID(msg.From), Message{
			Type:      MsgGet,
			From:      h.nodeID,
			RequestID: msg.RequestID,
			BlockID:   msg.BlockID,
		})
		return
	}

	h.sendChits(NodeID(msg.From), msg.RequestID, block.Height)
}

func (h *MessageHandler) handlePushQuery(msg Message) {
	if msg.Block != nil {
		isNew, err := h.store.Add(msg.Block)
		if err != nil {
			return
		}

		if isNew {
			Logf("[Node %d] Received new block %s via PushQuery\n", h.nodeID, msg.Block.ID)
			h.events.Publish(BaseEvent{
				eventType: EventNewBlock,
				data:      msg.Block,
			})
		}

		h.sendChits(NodeID(msg.From), msg.RequestID, msg.Block.Height)
	}
}

// main.go: MessageHandler.sendChits
func (h *MessageHandler) sendChits(to NodeID, requestID uint32, queryHeight uint64) {
	// 1) 先从存储层读取偏好（需要有一个“存储层偏好”的来源，见修复 B 的另一步）
	preferred := "" // try store-level preference first
	// 尝试读取“本高度已最终化的块”
	if preferred == "" {
		if block, ok := h.store.GetFinalizedAtHeight(queryHeight); ok {
			preferred = block.ID
		}
	}
	// 如果仍为空：从本地已知候选中，选一个确定性最大值以避免空偏好
	if preferred == "" {
		blocks := h.store.GetByHeight(queryHeight)
		if len(blocks) > 0 {
			ids := make([]string, 0, len(blocks))
			for _, b := range blocks {
				ids = append(ids, b.ID)
			}
			sort.Strings(ids)
			preferred = ids[len(ids)-1]
		}
	}

	accepted, acceptedHeight := h.store.GetLastAccepted()
	h.transport.Send(to, Message{
		Type: MsgChits, From: h.nodeID, RequestID: requestID,
		PreferredID: preferred, PreferredIDHeight: queryHeight,
		AcceptedID: accepted, AcceptedHeight: acceptedHeight,
	})
}

func (h *MessageHandler) handleGet(msg Message) {
	if block, exists := h.store.Get(msg.BlockID); exists {
		h.transport.Send(NodeID(msg.From), Message{
			Type:      MsgPut,
			From:      h.nodeID,
			RequestID: msg.RequestID,
			Block:     block,
			Height:    block.Height,
		})
	}
}

func (h *MessageHandler) handlePut(msg Message) {
	if msg.Block != nil {
		isNew, err := h.store.Add(msg.Block)
		if err != nil {
			return
		}

		if isNew {
			Logf("[Node %d] Received new block %s via Put from Node %d, gossiping it\n",
				h.nodeID, msg.Block.ID, msg.From)
			h.events.Publish(BaseEvent{
				eventType: EventBlockReceived,
				data:      msg.Block,
			})
		}
	}
}

// ============================================
// 节点实现
// ============================================

type Node struct {
	ID              NodeID
	IsByzantine     bool
	transport       Transport
	store           BlockStore
	engine          ConsensusEngine
	events          EventBus
	messageHandler  *MessageHandler
	queryManager    *QueryManager
	gossipManager   *GossipManager
	syncManager     *SyncManager
	proposalManager *ProposalManager
	ctx             context.Context
	cancel          context.CancelFunc
	config          *Config
}

func NewNode(id NodeID, transport Transport, byzantine bool, config *Config) *Node {
	ctx, cancel := context.WithCancel(context.Background())

	store := NewMemoryBlockStore()
	events := NewEventBus()
	engine := NewSnowmanEngine(id, store, &config.Consensus, events)

	messageHandler := NewMessageHandler(id, byzantine, transport, store, engine, events, &config.Consensus)
	queryManager := NewQueryManager(id, transport, store, engine, &config.Consensus, events)
	gossipManager := NewGossipManager(id, transport, store, &config.Gossip, events)
	syncManager := NewSyncManager(id, transport, store, &config.Sync, events)
	proposalManager := NewProposalManager(id, transport, store, &config.Node, events)

	messageHandler.SetManagers(queryManager, gossipManager, syncManager)

	return &Node{
		ID:              id,
		IsByzantine:     byzantine,
		transport:       transport,
		store:           store,
		engine:          engine,
		events:          events,
		messageHandler:  messageHandler,
		queryManager:    queryManager,
		gossipManager:   gossipManager,
		syncManager:     syncManager,
		proposalManager: proposalManager,
		ctx:             ctx,
		cancel:          cancel,
		config:          config,
	}
}

func (n *Node) Start() {
	// 启动消息处理
	go func() {
		for {
			select {
			case msg := <-n.transport.Receive():
				n.messageHandler.Handle(msg)
			case <-n.ctx.Done():
				return
			}
		}
	}()

	// 启动各组件
	n.engine.Start(n.ctx)
	n.queryManager.Start(n.ctx)
	n.gossipManager.Start(n.ctx)
	n.syncManager.Start(n.ctx)

	if !n.IsByzantine {
		n.proposalManager.Start(n.ctx)
	}
}

func (n *Node) Stop() {
	n.cancel()
}

// ============================================
// 网络管理器
// ============================================

type NetworkManager struct {
	nodes      map[NodeID]*Node
	transports map[NodeID]Transport
	config     *Config
	startTime  time.Time
	mu         sync.RWMutex
}

func NewNetworkManager(config *Config) *NetworkManager {
	return &NetworkManager{
		nodes:      make(map[NodeID]*Node),
		transports: make(map[NodeID]Transport),
		config:     config,
	}
}

func (nm *NetworkManager) CreateNodes() {
	// 随机选择拜占庭节点
	byzantineMap := make(map[NodeID]bool)
	indices := rand.Perm(nm.config.Network.NumNodes)
	for i := 0; i < nm.config.Network.NumByzantineNodes; i++ {
		byzantineMap[NodeID(indices[i])] = true
	}

	// 创建传输层
	ctx := context.Background()
	for i := 0; i < nm.config.Network.NumNodes; i++ {
		nodeID := NodeID(i)
		transport := NewSimulatedTransport(nodeID, nm, ctx, nm.config.Network.NetworkLatency)
		nm.transports[nodeID] = transport
	}

	// 创建节点
	for i := 0; i < nm.config.Network.NumNodes; i++ {
		nodeID := NodeID(i)
		node := NewNode(nodeID, nm.transports[nodeID], byzantineMap[nodeID], nm.config)
		nm.nodes[nodeID] = node
	}
}

func (nm *NetworkManager) GetTransport(nodeID NodeID) Transport {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	return nm.transports[nodeID]
}

func (nm *NetworkManager) SamplePeers(exclude NodeID, count int) []NodeID {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	peers := make([]NodeID, 0, len(nm.nodes)-1)
	for id := range nm.nodes {
		if id != exclude {
			peers = append(peers, id)
		}
	}

	rand.Shuffle(len(peers), func(i, j int) {
		peers[i], peers[j] = peers[j], peers[i]
	})

	if count > len(peers) {
		count = len(peers)
	}

	return peers[:count]
}

func (nm *NetworkManager) Start() {
	nm.startTime = time.Now()
	for _, node := range nm.nodes {
		node.Start()
	}
}

func (nm *NetworkManager) CheckProgress() (minHeight uint64, allDone bool) {
	minHeight = ^uint64(0)
	honestCount := 0

	for _, node := range nm.nodes {
		if !node.IsByzantine {
			honestCount++
			_, height := node.store.GetLastAccepted()
			if height < minHeight {
				minHeight = height
			}
		}
	}

	allDone = minHeight >= uint64(nm.config.Consensus.NumHeights)
	return minHeight, allDone
}

func (nm *NetworkManager) PrintStatus() {
	fmt.Println("\n===== Network Status =====")
	consensusMap := make(map[uint64]map[string]int)

	for id, node := range nm.nodes {
		lastAccepted, lastHeight := node.store.GetLastAccepted()

		nodeType := "Honest"
		if node.IsByzantine {
			nodeType = "Byzantine"
		}

		Logf("Node %d (%s): Height=%d, LastAccepted=%s\n",
			id, nodeType, lastHeight, lastAccepted)

		if lastHeight > 0 {
			if consensusMap[lastHeight] == nil {
				consensusMap[lastHeight] = make(map[string]int)
			}
			consensusMap[lastHeight][lastAccepted]++
		}
	}

	fmt.Println("\n--- Consensus by Height ---")
	for height := uint64(1); height <= uint64(nm.config.Consensus.NumHeights); height++ {
		if blocks, exists := consensusMap[height]; exists {
			Logf("Height %d: ", height)
			for blockID, count := range blocks {
				fmt.Printf("%s(%d nodes) ", blockID, count)
			}
			fmt.Println()
		}
	}
}

func (nm *NetworkManager) PrintFinalResults() {
	chains := make(map[NodeID][]string)

	for id, node := range nm.nodes {
		chain := make([]string, 0, nm.config.Consensus.NumHeights)
		for h := uint64(1); h <= uint64(nm.config.Consensus.NumHeights); h++ {
			if b, ok := node.store.GetFinalizedAtHeight(h); ok {
				chain = append(chain, b.ID)
			} else {
				chain = append(chain, "<none>")
			}
		}
		chains[id] = chain
	}

	// 检查一致性
	allEqual := true
	var refChain []string
	for _, chain := range chains {
		if refChain == nil {
			refChain = chain
		} else {
			for i := range chain {
				if chain[i] != refChain[i] {
					allEqual = false
					break
				}
			}
		}
		if !allEqual {
			break
		}
	}

	fmt.Println("\n--- Global Agreement Check ---")
	if allEqual {
		Logf("All nodes have identical finalized chains: ✅ YES\n")
		fmt.Println("Consensus chain:")
		fmt.Println(strings.Join(refChain, " -> "))
	} else {
		Logf("All nodes have identical finalized chains: ❌ NO\n")
		for id, chain := range chains {
			nodeType := "Honest"
			if nm.nodes[id].IsByzantine {
				nodeType = "Byzantine"
			}
			Logf("Node %3d (%s): %s\n", id, nodeType, strings.Join(chain, " -> "))
		}
	}
}

// ============================================
// 工具函数
// ============================================

func Logf(format string, args ...interface{}) {
	now := time.Now()
	timestamp := now.Format("15:04:05.999")
	fmt.Printf("[%s] %s", timestamp, fmt.Sprintf(format, args...))
}

func minUint64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

// ============================================
// 主函数
// ============================================

func main() {
	rand.Seed(time.Now().UnixNano())

	config := DefaultConfig()

	fmt.Println("Starting Decoupled Snowman Consensus...")
	Logf("Network: %d nodes (%d honest, %d byzantine)\n",
		config.Network.NumNodes,
		config.Network.NumNodes-config.Network.NumByzantineNodes,
		config.Network.NumByzantineNodes)
	Logf("Heights: %d, Blocks per height: %d\n",
		config.Consensus.NumHeights,
		config.Consensus.BlocksPerHeight)

	network := NewNetworkManager(config)
	network.CreateNodes()

	programStart := time.Now()
	network.Start()

	// 监控进度
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	lastHeight := uint64(0)
	for {
		select {
		case <-ticker.C:
			minHeight, allDone := network.CheckProgress()
			if minHeight > lastHeight {
				Logf("\n✅ All honest nodes reached consensus on height %d\n", minHeight)
				lastHeight = minHeight
			}

			if allDone {
				totalTime := time.Since(programStart)
				Logf("\n🎉 All heights completed! Total time: %v\n", totalTime)

				time.Sleep(1 * time.Second)

				fmt.Println("\n\n===== FINAL RESULTS =====")
				network.PrintStatus()
				network.PrintFinalResults()

				fmt.Println("\n--- Time Statistics ---")
				Logf("Total Time: %v\n", totalTime)
				Logf("Average/Height: %v\n", totalTime/time.Duration(config.Consensus.NumHeights))

				return
			}
		}
	}
}
