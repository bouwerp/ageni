package agent

import "sync"

// CollaborationMode defines the active configuration for multi-LLM teamwork.
type CollaborationMode string

const (
	// CollabOff runs the standard single-agent orchestration loop.
	CollabOff CollaborationMode = "off"
	// CollabCascade escalates tasks sequentially from tiny/fast models to flagship models.
	CollabCascade CollaborationMode = "cascade"
	// CollabDebate runs Developer/Critic structured debate loop prior to code integration.
	CollabDebate CollaborationMode = "debate"
	// CollabSelfMoA runs parallel flagship model completions and aggregates their traces.
	CollabSelfMoA CollaborationMode = "self_moa"
)

// SetCollaborationMode updates the active collaboration mode on the master.
func (m *Master) SetCollaborationMode(mode CollaborationMode) {
	m.mu.Lock()
	m.collabMode = mode
	m.mu.Unlock()
}

// CollaborationMode returns the current active collaboration mode on the master.
func (m *Master) CollaborationMode() CollaborationMode {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.collabMode == "" {
		return CollabOff
	}
	return m.collabMode
}

type collaborationRegistry struct {
	mu   sync.RWMutex
	mode CollaborationMode
}
