package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/bouwerp/ageni/internal/llm"
	"github.com/bouwerp/ageni/internal/memory"
	"github.com/bouwerp/ageni/internal/models"
	"github.com/bouwerp/ageni/internal/tools"
)

// SubagentSpec lets the manager construct a Subagent from a high-level tier
// without callers needing to know which Adapter or model to use.
type SubagentSpec struct {
	Task SubagentTask
}

// AdapterFactory returns the adapter + model for a given tier and optional
// required capabilities. The master owns this so it can swap providers at
// runtime. requiredCaps may be nil or empty (meaning any model for the tier).
type AdapterFactory func(tier string, requiredCaps []string) (adapter llm.Adapter, model string)

// Manager owns active sub-agents and provides spawn/check/send/kill.
type Manager struct {
	mu            sync.Mutex
	subs          map[string]*Subagent
	bus           *Bus
	tools         *tools.Registry
	tracker       *llm.Tracker
	factory       AdapterFactory
	skillCatalog  string
	roleCatalog   string
	memReg        *memory.Registry
	maxConcurrent int
	defaultBudget int
	nextID        int
	scrubber      func(string) string // propagated to newly-spawned sub-agents

	// rootCtx is the long-lived context sub-agent goroutines inherit. Must
	// outlive any individual master-turn ctx — otherwise a sub-agent gets
	// cancelled the moment the master's spawning turn returns.
	rootCtx context.Context
}

func NewManager(rootCtx context.Context, bus *Bus, registry *tools.Registry, tracker *llm.Tracker, factory AdapterFactory, maxConcurrent int) *Manager {
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	return &Manager{
		rootCtx:       rootCtx,
		subs:          make(map[string]*Subagent),
		bus:           bus,
		tools:         registry,
		tracker:       tracker,
		factory:       factory,
		maxConcurrent: maxConcurrent,
	}
}

// SetSkillCatalog updates the catalog passed to newly-spawned sub-agents.
// Existing sub-agents keep the catalog they were spawned with.
func (m *Manager) SetSkillCatalog(catalog string) {
	m.mu.Lock()
	m.skillCatalog = catalog
	m.mu.Unlock()
}

// SetRoleCatalog updates the role catalog passed to newly-spawned sub-agents.
// Existing sub-agents keep the catalog they were spawned with.
func (m *Manager) SetRoleCatalog(catalog string) {
	m.mu.Lock()
	m.roleCatalog = catalog
	m.mu.Unlock()
}

// SetMemoryRegistry wires a live memory registry into the manager so newly
// spawned sub-agents receive the current memory block in their system prompt.
func (m *Manager) SetMemoryRegistry(reg *memory.Registry) {
	m.mu.Lock()
	m.memReg = reg
	m.mu.Unlock()
}

// SetScrubber sets the scrubber function that will be applied to all
// newly-spawned sub-agents. Existing sub-agents are unaffected.
func (m *Manager) SetScrubber(f func(string) string) {
	m.mu.Lock()
	m.scrubber = f
	m.mu.Unlock()
}

// SetDefaultBudget updates the default tool-call budget applied to every
// spawn that doesn't override it. Existing sub-agents are unaffected.
func (m *Manager) SetDefaultBudget(n int) {
	m.mu.Lock()
	m.defaultBudget = n
	m.mu.Unlock()
}

// SetNextSubagentID nudges the spawn counter so the next worker created
// gets ID s<n+1>. Used on session resume to skip past IDs the master
// remembers from before the restart — keeps fresh workers' IDs distinct
// from references in the replayed history.
func (m *Manager) SetNextSubagentID(n int) {
	m.mu.Lock()
	if n > m.nextID {
		m.nextID = n
	}
	m.mu.Unlock()
}

// Spawn creates and starts a sub-agent. Returns its ID.
func (m *Manager) Spawn(ctx context.Context, task SubagentTask) (string, error) {
	if task.Objective == "" {
		return "", fmt.Errorf("spawn_subagent failed — no sub-agent was created: objective is required")
	}
	if task.OutputFormat == "" {
		return "", fmt.Errorf("spawn_subagent failed — no sub-agent was created: output_format is required")
	}
	if task.ModelTier == "" {
		task.ModelTier = "sonnet"
	}

	m.mu.Lock()
	if task.BudgetToolCalls <= 0 && m.defaultBudget > 0 {
		task.BudgetToolCalls = m.defaultBudget
	}
	running := 0
	for _, s := range m.subs {
		switch s.Status() {
		case StatusRunning, StatusPaused:
			running++
		}
	}
	if running >= m.maxConcurrent {
		m.mu.Unlock()
		return "", fmt.Errorf("max concurrent sub-agents (%d) reached", m.maxConcurrent)
	}
	m.nextID++
	id := fmt.Sprintf("s%d", m.nextID)
	correlationID := CorrelationIDFromContext(ctx)
	if correlationID == "" {
		correlationID = NewCorrelationID("spawn")
	}
	correlationID = correlationID + "/" + id
	adapter, model := m.factory(task.ModelTier, task.RequiredCaps)
	if adapter == nil {
		m.mu.Unlock()
		return "", fmt.Errorf("no adapter configured for tier=%s", task.ModelTier)
	}
	caps := models.Global.CapabilitiesForModel(model)
	memBlock := ""
	if m.memReg != nil {
		memBlock = m.memReg.InlineBlockForQuery(memoryQueryForTask(task), 6)
	}
	sub := NewSubagent(id, task, adapter, model, m.tools, m.bus, m.tracker, m.skillCatalog, m.roleCatalog, memBlock, caps, correlationID)
	if m.scrubber != nil {
		sub.SetScrubber(m.scrubber)
	}
	m.subs[id] = sub
	rootCtx := m.rootCtx
	m.mu.Unlock()

	// Use the manager's long-lived root ctx, NOT the caller's. The caller is
	// usually the master's per-turn ctx, which is cancelled the instant the
	// master's turn returns — taking every freshly-spawned sub-agent down
	// with it. Sub-agents must outlive the spawning turn.
	go sub.Run(rootCtx)
	return id, nil
}

func memoryQueryForTask(task SubagentTask) string {
	parts := []string{task.Objective, task.Context, task.TaskBoundaries, task.UseSkill}
	parts = append(parts, task.RepoFacts...)
	parts = append(parts, task.PriorFindings...)
	return strings.TrimSpace(strings.Join(parts, " "))
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

func (m *Manager) Pause(id string) error {
	s, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("no such sub-agent: %s", id)
	}
	if !s.Pause() {
		return fmt.Errorf("sub-agent %s cannot be paused (status=%s)", id, s.Status())
	}
	return nil
}

func (m *Manager) Resume(id string) error {
	s, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("no such sub-agent: %s", id)
	}
	if !s.Resume() {
		return fmt.Errorf("sub-agent %s is not paused", id)
	}
	return nil
}

// UpdateFactory swaps the adapter factory used by future Spawn calls. Existing
// sub-agents keep running with their original adapter.
func (m *Manager) UpdateFactory(factory AdapterFactory) {
	m.mu.Lock()
	m.factory = factory
	m.mu.Unlock()
}

// CancelAll cancels every running sub-agent. Done/error sub-agents are
// untouched.
func (m *Manager) CancelAll() int {
	m.mu.Lock()
	subs := make([]*Subagent, 0, len(m.subs))
	for _, s := range m.subs {
		if s.Status() == StatusRunning {
			subs = append(subs, s)
		}
	}
	m.mu.Unlock()

	for _, s := range subs {
		s.Cancel()
		s.setStatus(StatusCancelled)
	}
	return len(subs)
}
