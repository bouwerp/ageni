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
	ID      int        `json:"id"`
	Content string     `json:"content"`
	Status  TodoStatus `json:"status"`
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
- clear: empty the list

Mark an item 'in_progress' when you start it, 'completed' when done. Keep items short and outcome-oriented.`
}
func (*TodoWrite) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "action":{"type":"string","enum":["list","add","update","replace","clear"]},
  "content":{"type":"string","description":"For action=add: a single todo's content."},
  "id":{"type":"integer","description":"For action=update."},
  "status":{"type":"string","enum":["pending","in_progress","completed"],"description":"For action=update."},
  "items":{
    "type":"array",
    "description":"For action=add or action=replace: full list. Each item: {content, status?}.",
    "items":{
      "type":"object",
      "properties":{
        "content":{"type":"string"},
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
		Action  string     `json:"action"`
		Content string     `json:"content"`
		ID      int        `json:"id"`
		Status  TodoStatus `json:"status"`
		Items   []struct {
			Content string     `json:"content"`
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
				Status  TodoStatus `json:"status"`
			}{Content: p.Content})
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
			t.items = append(t.items, TodoItem{ID: t.nextID, Content: it.Content, Status: st})
		}
	case "update":
		if p.ID == 0 {
			return "", errors.New("update requires 'id'")
		}
		if p.Status == "" {
			return "", errors.New("update requires 'status'")
		}
		found := false
		for i := range t.items {
			if t.items[i].ID == p.ID {
				t.items[i].Status = p.Status
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("no todo with id=%d", p.ID)
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
			t.items = append(t.items, TodoItem{ID: t.nextID, Content: it.Content, Status: st})
		}
	case "clear":
		t.items = nil
		t.nextID = 0
	default:
		return "", fmt.Errorf("unknown action: %s", p.Action)
	}

	if err := t.save(); err != nil {
		return "", fmt.Errorf("persist: %w", err)
	}
	return t.render(), nil
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
		sb.WriteString(fmt.Sprintf("%s #%d %s\n", mark, it.ID, it.Content))
	}
	return strings.TrimRight(sb.String(), "\n")
}
