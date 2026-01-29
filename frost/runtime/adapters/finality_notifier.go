// frost/runtime/adapters/finality_notifier.go
// FinalityNotifier 适配器实现（基于 EventBus）
package adapters

import (
	"dex/frost/runtime"
	"dex/interfaces"
	"dex/types"
)

// EventBus 事件总线接口（由外部实现）
type EventBus interface {
	Subscribe(eventType types.EventType, handler interfaces.EventHandler)
}

// EventBusFinalityNotifier 基于 EventBus 的 FinalityNotifier 实现
type EventBusFinalityNotifier struct {
	events EventBus
}

// NewEventBusFinalityNotifier 创建新的 EventBusFinalityNotifier
func NewEventBusFinalityNotifier(events EventBus) *EventBusFinalityNotifier {
	return &EventBusFinalityNotifier{
		events: events,
	}
}

// SubscribeBlockFinalized 订阅区块最终化事件
func (n *EventBusFinalityNotifier) SubscribeBlockFinalized(fn func(height uint64)) {
	if n.events == nil {
		return
	}

	// 创建事件处理器（EventHandler 是函数类型）
	handler := func(event interfaces.Event) {
		if event.Type() != types.EventBlockFinalized {
			return
		}

		// 从事件数据中提取区块高度
		if block, ok := event.Data().(*types.Block); ok && block != nil {
			fn(block.Header.Height)
		}
	}

	n.events.Subscribe(types.EventBlockFinalized, handler)
}

// Ensure EventBusFinalityNotifier implements runtime.FinalityNotifier
var _ runtime.FinalityNotifier = (*EventBusFinalityNotifier)(nil)
