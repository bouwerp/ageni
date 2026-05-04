package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// SpawnTool is the master-only tool for delegating work to a sub-agent. The
// schema enforces the task contract — a vague spawn is rejected by the
// registry validator (objective/output_format are required).
type SpawnTool struct{ M *Manager }

func (SpawnTool) Name() string { return "spawn_subagent" }
func (SpawnTool) Description() string {
	return `Delegate a focused task to a sub-agent that runs in parallel. The sub-agent has its own conversation and tool access. Returns a sub-agent ID immediately; the sub-agent runs in the background. Use check_subagent to inspect progress, send_to_subagent to course-correct, kill_subagent to cancel.

Routing rules:
- Trivial lookup (file search, grep, listing): model_tier=haiku, budget_tool_calls<=5
- Standard task (multi-file edit, ordinary debug): model_tier=sonnet, budget_tool_calls<=15
- Complex/ambiguous: decompose into multiple parallel sub-agents; reserve opus for synthesis only

Every spawn must specify a clear objective AND output_format. Vague objectives cause duplicated work.`
}
func (SpawnTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "objective":{"type":"string","description":"Single-sentence imperative goal. Be specific."},
  "output_format":{"type":"string","description":"Exactly what the sub-agent must return — schema, structure, or example."},
  "model_tier":{"type":"string","enum":["haiku","sonnet","opus"],"description":"Cost tier. Default sonnet."},
  "allowed_tools":{"type":"array","items":{"type":"string"},"description":"Whitelist of tool names. Omit for all-tools-allowed."},
  "task_boundaries":{"type":"string","description":"What the sub-agent must NOT touch or decide."},
  "budget_tool_calls":{"type":"integer","description":"Hard cap on tool calls. Default 10."},
  "context":{"type":"string","description":"Pre-computed context the sub-agent needs (file paths, prior decisions). Pre-computing here avoids re-discovery cost."}
},
"required":["objective","output_format"]
}`)
}
func (t SpawnTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var task SubagentTask
	if err := json.Unmarshal(args, &task); err != nil {
		return "", err
	}
	if strings.TrimSpace(task.Objective) == "" {
		return "", errors.New("objective is required")
	}
	if strings.TrimSpace(task.OutputFormat) == "" {
		return "", errors.New("output_format is required")
	}
	id, err := t.M.Spawn(ctx, task)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("spawned sub-agent %s (tier=%s, budget=%d)", id, task.ModelTier, task.BudgetToolCalls), nil
}

// CheckTool returns the sub-agent's current status and recent transcript.
type CheckTool struct{ M *Manager }

func (CheckTool) Name() string { return "check_subagent" }
func (CheckTool) Description() string {
	return "Inspect a sub-agent's current status and recent activity (last ~50 events). Returns status (running/idle/done/error/cancelled) and a compact log."
}
func (CheckTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`)
}
func (t CheckTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct{ ID string }
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	s, ok := t.M.Get(p.ID)
	if !ok {
		return "", fmt.Errorf("no such sub-agent: %s", p.ID)
	}
	tr := s.Transcript()
	tail := tr
	if len(tail) > 50 {
		tail = tail[len(tail)-50:]
	}
	out := fmt.Sprintf("status: %s\nobjective: %s\n", s.Status(), s.Task.Objective)
	if final := s.FinalText(); final != "" {
		out += "final_output:\n" + final + "\n"
	}
	out += "recent_activity:\n" + strings.Join(tail, "\n")
	return out, nil
}

// SendTool injects a user-role message into a running sub-agent's conversation.
// (For v1, only effective if the sub-agent is still running and accepts the
// next turn — otherwise reports the status.)
type SendTool struct{ M *Manager }

func (SendTool) Name() string { return "send_to_subagent" }
func (SendTool) Description() string {
	return "Send a course-correction or follow-up instruction to a running sub-agent. The message is injected at the next turn boundary. If the sub-agent has already finished, returns its status."
}
func (SendTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"message":{"type":"string"}},"required":["id","message"]}`)
}
func (t SendTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		ID      string
		Message string
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	s, ok := t.M.Get(p.ID)
	if !ok {
		return "", fmt.Errorf("no such sub-agent: %s", p.ID)
	}
	// v1: send is best-effort. We append to the transcript so the master sees it
	// reflected on next check; full inbox-injection is v2.
	s.appendTranscript("master_message: " + p.Message)
	return fmt.Sprintf("delivered to %s (status=%s); full inbox injection lands in v2", p.ID, s.Status()), nil
}

// KillTool cancels a sub-agent.
type KillTool struct{ M *Manager }

func (KillTool) Name() string { return "kill_subagent" }
func (KillTool) Description() string {
	return "Cancel a sub-agent immediately. Use when the sub-agent has gone off-track and is not recoverable via send_to_subagent."
}
func (KillTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`)
}
func (t KillTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct{ ID string }
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if err := t.M.Kill(p.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("killed sub-agent %s", p.ID), nil
}
