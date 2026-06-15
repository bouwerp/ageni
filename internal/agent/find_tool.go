package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/ageni/internal/tools"
)

// FindInCodebase is a master-only tool that delegates code-search to a
// focused sub-agent. The sub-agent uses grep / glob / read_file to find
// what the master is asking about and returns a distilled summary.
//
// Why this exists: a raw grep on a non-trivial repo can return hundreds of
// lines that bloat the master's context. The Librarian sub-agent reads the
// grep output itself, picks out what's relevant, and hands the master a
// 200–500 token summary with paths + line numbers + how-it's-used context.
type FindInCodebase struct {
	M   *Manager
	Bus *Bus
}

func (FindInCodebase) Name() string { return "find_in_codebase" }

func (FindInCodebase) Description() string {
	return `Search the codebase via a worker sub-agent and get a distilled answer back.

The worker uses grep, glob, and read_file to locate what you're after, then returns a synthesised summary with file paths, line numbers, and brief context — instead of dumping raw grep output into your context window. Use this for "where is X defined?", "what files use Y?", "how is Z implemented?" — anything that would otherwise need multiple grep + read_file rounds.

The worker runs at the haiku tier with a 10-call budget, so this is cheap. Bounded to 10 minutes; cancels itself if you cancel the master.`
}

func (FindInCodebase) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "query":{"type":"string","description":"What to find. Be specific — the worker reads this verbatim. Examples: 'definition of the SubagentTask struct', 'all callers of bus.Publish', 'how skills are loaded at startup'."},
  "intent":{"type":"string","description":"Why you're looking — helps the worker pick relevant context. Optional."},
  "scope":{"type":"string","description":"Path or directory to limit the search to. Defaults to the whole repo. Optional."}
},
"required":["query"]
}`)
}

func (t FindInCodebase) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Query  string `json:"query"`
		Intent string `json:"intent"`
		Scope  string `json:"scope"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Query == "" {
		p.Query = tools.ResolveQuery(args)
	}
	if strings.TrimSpace(p.Query) == "" {
		return "", errors.New("query is required")
	}
	if p.Scope == "" {
		p.Scope = tools.ResolvePath(args)
	}

	objective := "Search the codebase for: " + p.Query
	if p.Intent != "" {
		objective += " (intent: " + p.Intent + ")"
	}

	taskContext := ""
	if p.Scope != "" {
		taskContext = "Limit your search to the path: " + p.Scope
	}

	task := SubagentTask{
		Objective:       objective,
		OutputFormat:    CanonicalWorkerOutputFormat,
		AllowedTools:    []string{"grep", "glob", "read_file", "list_dir"},
		BudgetToolCalls: 10,
		ModelTier:       "haiku",
		Context:         taskContext,
	}

	// Subscribe BEFORE spawning so we don't race the sub-agent's first
	// event. The buffered channel catches anything fired in the gap.
	sub := t.Bus.Subscribe(128)

	// 10-minute outer cap. The worker's own 10-call budget is the real
	// limiter; this exists only to free find_in_codebase if the worker
	// hangs (e.g. an LLM stream that never completes). 3 minutes was too
	// tight for non-trivial searches and surfaced as spurious cancellations.
	findCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	id, err := t.M.Spawn(findCtx, &task)
	if err != nil {
		return "", err
	}

	for {
		select {
		case ev := <-sub:
			if ev.SubagentID != id {
				continue
			}
			switch ev.Kind {
			case EvSubagentDone:
				return ev.Text, nil
			case EvSubagentError:
				_ = t.M.Kill(id)
				if ev.Err != nil {
					return "", fmt.Errorf("find_in_codebase: %w", ev.Err)
				}
				return "", errors.New("find_in_codebase: worker errored without detail")
			}
		case <-findCtx.Done():
			_ = t.M.Kill(id)
			return "", fmt.Errorf("find_in_codebase: %w", findCtx.Err())
		}
	}
}
