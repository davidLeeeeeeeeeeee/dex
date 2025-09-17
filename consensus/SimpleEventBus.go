package consensus

import (
	"dex/interfaces"
	"dex/types"
	"sync"
)

type SimpleEventBus struct {
	mu       sync.RWMutex
	handlers map[types.EventType][]interfaces.EventHandler
}

func NewEventBus() interfaces.EventBus {
	return &SimpleEventBus{
		handlers: make(map[types.EventType][]interfaces.EventHandler),
	}
}

func (eb *SimpleEventBus) Subscribe(topic types.EventType, handler interfaces.EventHandler) {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	eb.handlers[topic] = append(eb.handlers[topic], handler)
}

func (eb *SimpleEventBus) Publish(event interfaces.Event) {
	eb.mu.RLock()
	handlers := eb.handlers[event.Type()]
	eb.mu.RUnlock()

	for _, handler := range handlers {
		handler(event)
	}
}

func (eb *SimpleEventBus) PublishAsync(event interfaces.Event) {
	go eb.Publish(event)
}
