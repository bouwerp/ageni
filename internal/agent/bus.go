package agent

import (
	"sync"
	"time"

	"github.com/bouwerp/ageni/internal/llm"
)

// EventKind identifies the variant of an Event.
type EventKind string

const (
	EvUserMessage     EventKind = "user_message"
	EvMasterText      EventKind = "master_text"
	EvMasterToolCall  EventKind = "master_tool_call"
	EvMasterToolDone  EventKind = "master_tool_done"
	EvMasterTurnDone  EventKind = "master_turn_done"
	EvMasterUsage     EventKind = "master_usage"
	EvSubagentSpawn   EventKind = "subagent_spawn"
	EvSubagentText    EventKind = "subagent_text"
	EvSubagentToolCall EventKind = "subagent_tool_call"
	EvSubagentToolDone EventKind = "subagent_tool_done"
	EvSubagentDone    EventKind = "subagent_done"
	EvSubagentError   EventKind = "subagent_error"
	EvSubagentUsage   EventKind = "subagent_usage"
	EvError           EventKind = "error"
)

// Event is a single message on the bus.
type Event struct {
	Kind EventKind
	At   time.Time

	// Identifiers
	SubagentID string

	// Content (one of these is populated based on Kind)
	Text       string
	ToolCall   *llm.ToolCall
	ToolResult *llm.ToolResult
	Usage      *llm.Usage

	// Subagent metadata for spawn/done events
	SubagentTask  string
	SubagentModel string

	Err error
}

// Bus is a many-to-many event channel. Publishers call Publish; subscribers
// call Subscribe to get a buffered channel they own.
type Bus struct {
	mu   sync.RWMutex
	subs []chan Event
}

func NewBus() *Bus { return &Bus{} }

// Subscribe returns a buffered channel that receives events. Slow subscribers
// drop events rather than blocking publishers.
func (b *Bus) Subscribe(buffer int) <-chan Event {
	if buffer <= 0 {
		buffer = 64
	}
	ch := make(chan Event, buffer)
	b.mu.Lock()
	b.subs = append(b.subs, ch)
	b.mu.Unlock()
	return ch
}

// Publish broadcasts an event. Stamps At if zero.
func (b *Bus) Publish(e Event) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	b.mu.RLock()
	subs := b.subs
	b.mu.RUnlock()
	for _, ch := range subs {
		select {
		case ch <- e:
		default:
			// drop
		}
	}
}
