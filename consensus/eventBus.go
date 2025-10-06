package consensus

import (
	"dex/interfaces"
	"dex/types"
	"sync"
)

type EventBus struct {
	mu       sync.RWMutex
	handlers map[types.EventType][]interfaces.EventHandler
}

func NewEventBus() interfaces.EventBus {
	return &EventBus{
		handlers: make(map[types.EventType][]interfaces.EventHandler),
	}
}

func (eb *EventBus) Subscribe(topic types.EventType, handler interfaces.EventHandler) {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	eb.handlers[topic] = append(eb.handlers[topic], handler)
}

func (eb *EventBus) Publish(event interfaces.Event) {
	eb.mu.RLock()
	handlers := eb.handlers[event.Type()]
	eb.mu.RUnlock()

	for _, handler := range handlers {
		handler(event)
	}
}

func (eb *EventBus) PublishAsync(event interfaces.Event) {
	go eb.Publish(event)
}
