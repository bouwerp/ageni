package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// RememberTool creates or updates a persistent memory snippet.
type RememberTool struct{ Reg *Registry }

func (RememberTool) Name() string { return "remember" }
func (RememberTool) Description() string {
	return `Store a persistent memory snippet that will be available in all future sessions and to all agents. Use this when:
- The user explicitly asks you to remember something ("remember that...", "keep this in mind...", "save this...")
- You discover an important project-specific fact (build commands, conventions, credentials layout, service URLs, key contacts)
- You want to avoid re-discovering the same information across sessions

The memory is saved to .ageni/memories/<key>.md and injected into every future system prompt automatically.
Choose a short, stable key (kebab-case slug, e.g. "build-commands", "db-host", "user-pref-lang").
If a memory with the same key already exists it is overwritten.`
}
func (RememberTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "key":{"type":"string","description":"Short kebab-case identifier, e.g. 'build-commands', 'db-host'. Stable across updates."},
  "description":{"type":"string","description":"One-line summary shown in the catalog (≤ 100 chars)."},
  "content":{"type":"string","description":"The fact or snippet to store. Keep it concise — a few sentences or a short code block."}
},
"required":["key","description","content"]
}`)
}
func (t RememberTool) Call(_ context.Context, args json.RawMessage) (string, error) {
	if t.Reg == nil {
		return "", errors.New("memory registry not initialised")
	}
	var p struct {
		Key         string `json:"key"`
		Description string `json:"description"`
		Content     string `json:"content"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Key) == "" {
		return "", errors.New("key is required")
	}
	if strings.TrimSpace(p.Description) == "" {
		return "", errors.New("description is required")
	}
	if strings.TrimSpace(p.Content) == "" {
		return "", errors.New("content is required")
	}
	if err := t.Reg.Set(p.Key, p.Description, p.Content); err != nil {
		return "", err
	}
	return fmt.Sprintf("memory %q saved (%d bytes). It will appear in all future system prompts.", p.Key, len(p.Content)), nil
}

// RecallTool loads a memory by key. Useful when the inline content in the
// system prompt was truncated or when a sub-agent needs the raw text.
type RecallTool struct{ Reg *Registry }

func (RecallTool) Name() string { return "recall" }
func (RecallTool) Description() string {
	return "Load the full content of a stored memory by key. Memories are already injected into the system prompt; use this only when you need to confirm the exact stored value or if a memory is very long."
}
func (RecallTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "key":{"type":"string","description":"Memory key to retrieve."}
},
"required":["key"]
}`)
}
func (t RecallTool) Call(_ context.Context, args json.RawMessage) (string, error) {
	if t.Reg == nil {
		return "", errors.New("memory registry not initialised")
	}
	var p struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Key == "" {
		return "", errors.New("key is required")
	}
	m := t.Reg.Get(p.Key)
	if m == nil {
		names := t.Reg.Names()
		return "", fmt.Errorf("no memory with key %q. Available: %s", p.Key, strings.Join(names, ", "))
	}
	return fmt.Sprintf("# Memory: %s\n%s\n\n---\n%s", m.Key, m.Description, m.Content), nil
}

// ForgetTool deletes a memory by key.
type ForgetTool struct{ Reg *Registry }

func (ForgetTool) Name() string { return "forget" }
func (ForgetTool) Description() string {
	return "Delete a stored memory by key. Use when the user asks you to forget something, or when a stored fact is no longer accurate."
}
func (ForgetTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "key":{"type":"string","description":"Memory key to delete."}
},
"required":["key"]
}`)
}
func (t ForgetTool) Call(_ context.Context, args json.RawMessage) (string, error) {
	if t.Reg == nil {
		return "", errors.New("memory registry not initialised")
	}
	var p struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Key == "" {
		return "", errors.New("key is required")
	}
	if err := t.Reg.Delete(p.Key); err != nil {
		return "", err
	}
	return fmt.Sprintf("memory %q deleted.", p.Key), nil
}
