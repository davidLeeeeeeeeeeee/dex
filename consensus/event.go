package consensus

import "sync"

// ============================================
// 事件系统
// ============================================

type EventType string

const (
	EventBlockFinalized  EventType = "block.finalized"
	EventBlockReceived   EventType = "block.received"
	EventQueryComplete   EventType = "query.complete"
	EventSyncComplete    EventType = "sync.complete"
	EventNewBlock        EventType = "block.new"
	EventSnapshotCreated EventType = "snapshot.created" // 新增
	EventSnapshotLoaded  EventType = "snapshot.loaded"  // 新增
)

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
