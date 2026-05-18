package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// TodoStatus is the lifecycle state of a todo item.
type TodoStatus string

const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoCompleted  TodoStatus = "completed"
)

// TodoItem is a single planned task.
type TodoItem struct {
	ID        int        `json:"id"`
	Content   string     `json:"content"`
	Notes     string     `json:"notes,omitempty"`      // extended description / context; not shown in sidebar summary
	Status    TodoStatus `json:"status"`
	ClaimedBy string     `json:"claimed_by,omitempty"` // sub-agent ID, "master", or empty
}

// TodoWrite is a session-scoped task list. Used by the master to plan
// non-trivial work; the list is shown to the model on subsequent turns so it
// can track progress without re-deriving the plan.
type TodoWrite struct {
	mu     sync.Mutex
	path   string
	items  []TodoItem
	loaded bool
	nextID int
}

// NewTodoWrite returns a tool that persists its list to the given absolute
// path. Pass an empty string to fall back to <cwd>/.ageni/todo.json (the
// pre-session-abstraction behaviour).
func NewTodoWrite(path string) *TodoWrite {
	t := &TodoWrite{path: path}
	if t.path == "" {
		if cwd, err := os.Getwd(); err == nil {
			t.path = filepath.Join(cwd, ".ageni", "todo.json")
		}
	}
	return t
}

func (*TodoWrite) Name() string { return "todo_write" }
func (*TodoWrite) Description() string {
	return `Manage the session todo list. The list is persisted to .ageni/todo.json and surfaced back to the model on subsequent turns. Use this to plan non-trivial work and track progress.

Actions:
- list: return the current todos
- add: append one or more items (provide 'content' or 'items')
- update: change a single item's status (provide 'id' and 'status')
- replace: clear and rewrite the list (provide 'items')
- remove: delete specific items by ID, or (if no 'ids' given) remove all completed items. Use for pruning stale/irrelevant todos.
- clear: empty the list
- claim: assign one or more items to a worker (provide 'ids' and 'claimed_by'). Use this when you fan out parallel sub-agents — claim the items each will own so workers don't collide.
- release: clear the claim on items (provide 'ids'). Used when a worker errors or you reassign.

Mark an item 'in_progress' when you start it, 'completed' when done. Keep items short and outcome-oriented.`
}
func (*TodoWrite) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "action":{"type":"string","enum":["list","add","update","replace","remove","clear","claim","release"]},
  "content":{"type":"string","description":"For action=add: a single todo's content."},
  "notes":{"type":"string","description":"For action=add or action=update: extended description, context, or acceptance criteria for this item. Not shown in the sidebar summary — visible on demand."},
  "id":{"type":"integer","description":"For action=update."},
  "ids":{"type":"array","items":{"type":"integer"},"description":"For action=claim or action=release: the item IDs to operate on."},
  "claimed_by":{"type":"string","description":"For action=claim: who's claiming the items (sub-agent ID like 's3' or 'master')."},
  "status":{"type":"string","enum":["pending","in_progress","completed"],"description":"For action=update."},
  "items":{
    "type":"array",
    "description":"For action=add or action=replace: full list. Each item: {content, notes?, status?}.",
    "items":{
      "type":"object",
      "properties":{
        "content":{"type":"string"},
        "notes":{"type":"string","description":"Extended description / context for this item."},
        "status":{"type":"string","enum":["pending","in_progress","completed"]}
      },
      "required":["content"]
    }
  }
},
"required":["action"]
}`)
}

func (t *TodoWrite) Call(ctx context.Context, args json.RawMessage) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.loaded {
		t.load()
		t.loaded = true
	}

	var p struct {
		Action    string     `json:"action"`
		Content   string     `json:"content"`
		Notes     string     `json:"notes"`
		ID        int        `json:"id"`
		IDs       []int      `json:"ids"`
		ClaimedBy string     `json:"claimed_by"`
		Status    TodoStatus `json:"status"`
		Items     []struct {
			Content string     `json:"content"`
			Notes   string     `json:"notes"`
			Status  TodoStatus `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}

	switch p.Action {
	case "list":
		// fall through
	case "add":
		toAdd := p.Items
		if p.Content != "" {
			toAdd = append(toAdd, struct {
				Content string     `json:"content"`
				Notes   string     `json:"notes"`
				Status  TodoStatus `json:"status"`
			}{Content: p.Content, Notes: p.Notes})
		}
		if len(toAdd) == 0 {
			return "", errors.New("add requires 'content' or 'items'")
		}
		for _, it := range toAdd {
			t.nextID++
			st := it.Status
			if st == "" {
				st = TodoPending
			}
			t.items = append(t.items, TodoItem{ID: t.nextID, Content: it.Content, Notes: it.Notes, Status: st})
		}
	case "update":
		if p.ID == 0 {
			return "", errors.New("update requires 'id'")
		}
		if p.Status == "" && p.Notes == "" {
			return "", errors.New("update requires 'status' or 'notes'")
		}
		found := false
		for i := range t.items {
			if t.items[i].ID == p.ID {
				if p.Status != "" {
					t.items[i].Status = p.Status
				}
				if p.Notes != "" {
					t.items[i].Notes = p.Notes
				}
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("no todo with id=%d", p.ID)
		}
	case "remove":
		if len(p.IDs) > 0 {
			// remove specific items by ID
			keep := t.items[:0]
			for _, it := range t.items {
				found := false
				for _, id := range p.IDs {
					if it.ID == id {
						found = true
						break
					}
				}
				if !found {
					keep = append(keep, it)
				}
			}
			t.items = keep
		} else {
			// no IDs supplied: prune all completed items
			keep := t.items[:0]
			for _, it := range t.items {
				if it.Status != TodoCompleted {
					keep = append(keep, it)
				}
			}
			t.items = keep
		}
	case "replace":
		t.items = nil
		t.nextID = 0
		for _, it := range p.Items {
			t.nextID++
			st := it.Status
			if st == "" {
				st = TodoPending
			}
			t.items = append(t.items, TodoItem{ID: t.nextID, Content: it.Content, Notes: it.Notes, Status: st})
		}
	case "clear":
		t.items = nil
		t.nextID = 0
	case "claim":
		if len(p.IDs) == 0 {
			return "", errors.New("claim requires 'ids'")
		}
		if p.ClaimedBy == "" {
			return "", errors.New("claim requires 'claimed_by'")
		}
		claimed := 0
		for _, id := range p.IDs {
			for i := range t.items {
				if t.items[i].ID == id {
					if t.items[i].ClaimedBy != "" && t.items[i].ClaimedBy != p.ClaimedBy {
						return "", fmt.Errorf("todo #%d already claimed by %q", id, t.items[i].ClaimedBy)
					}
					t.items[i].ClaimedBy = p.ClaimedBy
					t.items[i].Status = TodoInProgress
					claimed++
				}
			}
		}
		if claimed == 0 {
			return "", fmt.Errorf("none of the supplied ids %v matched existing todos", p.IDs)
		}
	case "release":
		if len(p.IDs) == 0 {
			return "", errors.New("release requires 'ids'")
		}
		for _, id := range p.IDs {
			for i := range t.items {
				if t.items[i].ID == id {
					t.items[i].ClaimedBy = ""
					if t.items[i].Status == TodoInProgress {
						t.items[i].Status = TodoPending
					}
				}
			}
		}
	default:
		return "", fmt.Errorf("unknown action: %s", p.Action)
	}

	if err := t.save(); err != nil {
		return "", fmt.Errorf("persist: %w", err)
	}
	return t.render(), nil
}

// AutoRelease resets any in_progress items claimed by workerID back to
// pending. Called automatically when a worker exits (done, error, cancelled)
// so that todos don't stay "in_progress" forever if the master forgets to
// update them.
func (t *TodoWrite) AutoRelease(workerID string) {
	if workerID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.loaded {
		t.load()
		t.loaded = true
	}
	changed := false
	for i := range t.items {
		if t.items[i].ClaimedBy == workerID && t.items[i].Status == TodoInProgress {
			t.items[i].ClaimedBy = ""
			t.items[i].Status = TodoPending
			changed = true
		}
	}
	if changed {
		_ = t.save()
	}
}

// ReleaseAllInProgress resets every in_progress item back to pending and
// clears all claims. Used on session resume when all prior workers are dead.
func (t *TodoWrite) ReleaseAllInProgress() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.loaded {
		t.load()
		t.loaded = true
	}
	changed := false
	for i := range t.items {
		if t.items[i].Status == TodoInProgress {
			t.items[i].ClaimedBy = ""
			t.items[i].Status = TodoPending
			changed = true
		}
	}
	if changed {
		_ = t.save()
	}
}

// Items returns a snapshot of the current todo list. Lazy-loads from disk on
// first call. Safe for concurrent use; callers receive a copy they can mutate.
func (t *TodoWrite) Items() []TodoItem {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.loaded {
		t.load()
		t.loaded = true
	}
	out := make([]TodoItem, len(t.items))
	copy(out, t.items)
	return out
}

// Clear removes all todo items and persists the empty list. Used by the
// kill switch to wipe the session task list without touching context.
func (t *TodoWrite) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.loaded {
		t.load()
		t.loaded = true
	}
	t.items = nil
	t.nextID = 0
	t.save()
}

func (t *TodoWrite) load() {
	if t.path == "" {
		return
	}
	b, err := os.ReadFile(t.path) //nolint:gosec
	if err != nil {
		return
	}
	var stored struct {
		Items  []TodoItem `json:"items"`
		NextID int        `json:"next_id"`
	}
	if err := json.Unmarshal(b, &stored); err != nil {
		return
	}
	t.items = stored.Items
	t.nextID = stored.NextID
}

func (t *TodoWrite) save() error {
	if t.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(struct {
		Items  []TodoItem `json:"items"`
		NextID int        `json:"next_id"`
	}{Items: t.items, NextID: t.nextID}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(t.path, b, 0o644) //nolint:gosec
}

func (t *TodoWrite) render() string {
	if len(t.items) == 0 {
		return "(no todos)"
	}
	// Render in original order (creation order).
	items := make([]TodoItem, len(t.items))
	copy(items, t.items)
	sort.SliceStable(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	var sb strings.Builder
	for _, it := range items {
		mark := "[ ]"
		switch it.Status {
		case TodoInProgress:
			mark = "[~]"
		case TodoCompleted:
			mark = "[x]"
		}
		owner := ""
		if it.ClaimedBy != "" {
			owner = " (→ " + it.ClaimedBy + ")"
		}
		sb.WriteString(fmt.Sprintf("%s #%d %s%s\n", mark, it.ID, it.Content, owner))
		if it.Notes != "" {
			sb.WriteString(fmt.Sprintf("       %s\n", it.Notes))
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}
