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
  "budget_tool_calls":{"type":"integer","description":"Soft cap on actual tool calls. Default 25. When reached, the worker gets one final wrap-up turn (no tools available) to produce its <result>/<reasoning>, instead of erroring out."},
  "context":{"type":"string","description":"Free-form pre-computed context for one-off info that doesn't fit the structured fields below."},
  "use_skill":{"type":"string","description":"Pin a specific skill the sub-agent should apply (e.g. 'code-review', 'test-driven-development'). The sub-agent loads its body via read_skill and follows its procedures."},
  "repo_facts":{"type":"array","items":{"type":"string"},"description":"File-purpose lines you already know, e.g. 'internal/llm/anthropic.go: prompt-caching adapter'. Saves the worker a discovery round-trip."},
  "prior_findings":{"type":"array","items":{"type":"string"},"description":"Selected attributed results from earlier workers, e.g. 's3 found auth at internal/auth/jwt.go:42'. Use sparingly — only what THIS worker needs to know."},
  "do_not_revisit":{"type":"array","items":{"type":"string"},"description":"Paths/areas other parallel workers are handling. Anti-collision: tells THIS worker to stay clear so multiple parallel workers don't redo the same change."}
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
// subAgentOutputCap is the maximum number of characters of a sub-agent's
// final output returned by check_subagent. Large outputs bloat master context;
// the master can request a focused follow-up via send_to_subagent if needed.
const subAgentOutputCap = 4000

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
		if len(final) > subAgentOutputCap {
			truncated := len(final) - subAgentOutputCap
			final = final[:subAgentOutputCap] + fmt.Sprintf("\n\n[... %d chars truncated — use send_to_subagent to request a focused summary of specific sections]", truncated)
		}
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
	if !s.Send(p.Message) {
		return "", fmt.Errorf("could not deliver to %s (status=%s, inbox may be full)", p.ID, s.Status())
	}
	return fmt.Sprintf("queued for %s — will be processed at the next turn boundary", p.ID), nil
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
