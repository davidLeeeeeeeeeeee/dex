package types

// ============================================
// 事件系统
// ============================================

type EventType string

const (
	EventPreferenceChanged EventType = "snowball.preference"
	EventBlockFinalized    EventType = "block.finalized"
	EventBlockReceived     EventType = "block.received"
	EventQueryComplete     EventType = "query.complete"
	EventSyncComplete      EventType = "sync.complete"
	EventNewBlock          EventType = "block.new"
	EventSnapshotCreated   EventType = "snapshot.created" // 新增
	EventSnapshotLoaded    EventType = "snapshot.loaded"  // 新增
)

type BaseEvent struct {
	EventType EventType
	EventData interface{}
}

func (e BaseEvent) Type() EventType   { return e.EventType }
func (e BaseEvent) Data() interface{} { return e.EventData }
