package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/bouwerp/ageni/internal/llm"
	"github.com/bouwerp/ageni/internal/tools"
)

// SubagentSpec lets the manager construct a Subagent from a high-level tier
// without callers needing to know which Adapter or model to use.
type SubagentSpec struct {
	Task SubagentTask
}

// AdapterFactory returns the adapter + model for a given tier. The master
// owns this so it can swap providers at runtime.
type AdapterFactory func(tier string) (adapter llm.Adapter, model string)

// Manager owns active sub-agents and provides spawn/check/send/kill.
type Manager struct {
	mu            sync.Mutex
	subs          map[string]*Subagent
	bus           *Bus
	tools         *tools.Registry
	tracker       *llm.Tracker
	factory       AdapterFactory
	maxConcurrent int
	nextID        int
}

func NewManager(bus *Bus, registry *tools.Registry, tracker *llm.Tracker, factory AdapterFactory, maxConcurrent int) *Manager {
	return &Manager{
		subs:          make(map[string]*Subagent),
		bus:           bus,
		tools:         registry,
		tracker:       tracker,
		factory:       factory,
		maxConcurrent: maxConcurrent,
	}
}

// Spawn creates and starts a sub-agent. Returns its ID.
func (m *Manager) Spawn(ctx context.Context, task SubagentTask) (string, error) {
	if task.Objective == "" {
		return "", fmt.Errorf("objective is required")
	}
	if task.OutputFormat == "" {
		return "", fmt.Errorf("output_format is required")
	}
	if task.ModelTier == "" {
		task.ModelTier = "sonnet"
	}

	m.mu.Lock()
	running := 0
	for _, s := range m.subs {
		if s.Status() == StatusRunning {
			running++
		}
	}
	if running >= m.maxConcurrent {
		m.mu.Unlock()
		return "", fmt.Errorf("max concurrent sub-agents (%d) reached", m.maxConcurrent)
	}
	m.nextID++
	id := fmt.Sprintf("s%d", m.nextID)
	adapter, model := m.factory(task.ModelTier)
	if adapter == nil {
		m.mu.Unlock()
		return "", fmt.Errorf("no adapter configured for tier=%s", task.ModelTier)
	}
	sub := NewSubagent(id, task, adapter, model, m.tools, m.bus, m.tracker)
	m.subs[id] = sub
	m.mu.Unlock()

	go sub.Run(ctx)
	return id, nil
}

func (m *Manager) Get(id string) (*Subagent, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.subs[id]
	return s, ok
}

func (m *Manager) List() []*Subagent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Subagent, 0, len(m.subs))
	for _, s := range m.subs {
		out = append(out, s)
	}
	return out
}

func (m *Manager) Kill(id string) error {
	s, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("no such sub-agent: %s", id)
	}
	s.Cancel()
	s.setStatus(StatusCancelled)
	return nil
}
