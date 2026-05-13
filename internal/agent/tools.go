package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bouwerp/ageni/internal/llm"
)

// SpawnTool is the master-only tool for delegating work to a sub-agent. The
// schema enforces the task contract — a vague spawn is rejected by the
// registry validator (objective/output_format are required).
type SpawnTool struct{ M *Manager }

func (SpawnTool) Name() string { return "spawn_subagent" }
func (SpawnTool) Description() string {
	return `Delegate a focused task to a sub-agent that runs in parallel. The sub-agent has its own conversation and tool access. Returns a sub-agent ID immediately; the sub-agent runs in the background. Use check_subagent to inspect progress, send_to_subagent to course-correct, kill_subagent to cancel.

Routing rules:
- Trivial lookup (file search, grep, listing): model_tier=haiku, budget_tool_calls=15
- Standard task (multi-file edit, ordinary debug, code review): model_tier=sonnet, budget_tool_calls=40
- Complex/ambiguous: decompose into multiple parallel sub-agents, budget_tool_calls=60; reserve opus for synthesis only

Every spawn must specify a clear objective AND output_format. Vague objectives cause duplicated work.`
}
func (SpawnTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "objective":{"type":"string","description":"Single-sentence imperative goal. Be specific."},
  "output_format":{"type":"string","description":"Exactly what the sub-agent must return — schema, structure, or example."},
  "model_tier":{"type":"string","enum":["haiku","sonnet","opus"],"description":"Cost tier. Default sonnet."},
  "allowed_tools":{"type":"array","items":{"type":"string"},"description":"Optional whitelist of tool names. Omit entirely for full tool access (recommended for editing tasks). Only set this when you need to deliberately restrict access (e.g. read-only research: ['read_file','grep','glob','list_dir']). Never provide a partial list that omits tools the worker will need — a missing tool causes an error that wastes the worker's budget."},
  "task_boundaries":{"type":"string","description":"What the sub-agent must NOT touch or decide."},
  "budget_tool_calls":{"type":"integer","description":"Soft cap on actual tool calls. Default 40. When reached, the worker gets one final wrap-up turn (no tools available) to produce its <result>/<reasoning>, instead of erroring out."},
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
		return "", errors.New("spawn_subagent failed — no sub-agent was created: objective is required")
	}
	if strings.TrimSpace(task.OutputFormat) == "" {
		return "", errors.New("spawn_subagent failed — no sub-agent was created: output_format is required")
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

// criticSystemPrompt is the fixed system prompt for the soundboard critic.
// The critic has NO tools — it only reasons about the plan and returns critique.
const criticSystemPrompt = `You are a senior adversarial reviewer. Your job is to stress-test the plan you receive before it is executed.

Structure your critique under these headings (be concise — 3-6 bullet points total):

**Delegation Audit** — Is the master about to perform any work it should delegate to a sub-agent? Every concrete step (file read, grep, edit, shell command, analysis) must be assigned to a worker. Flag any step where the master appears to be acting as executor rather than planner.
**Risks & Gaps** — What could go wrong? What assumptions are not validated?
**Edge Cases Missed** — Unusual inputs, concurrent conditions, or error paths not addressed.
**Better Alternatives** — Is there a simpler, safer, or more robust approach?
**What's Correct** — Briefly acknowledge what the plan gets right.

Be direct and specific. Cite file paths or line numbers when relevant. Do not repeat the plan back.`

// SoundboardTool calls the critic adapter synchronously and returns its critique.
// If no critic is configured it returns a notice rather than failing.
type SoundboardTool struct{ M *Master }

func (SoundboardTool) Name() string { return "soundboard" }
func (SoundboardTool) Description() string {
return `Submit your decomposed plan to an independent critic LLM for adversarial review BEFORE spawning any workers. This is mandatory for every plan — trivial lookups, single-file edits, and complex multi-step changes alike. The critic audits whether you are properly delegating (not doing work yourself), surfaces risks, flags missing edge cases, and suggests alternatives. Returns a structured critique; you MUST incorporate the feedback before proceeding. Do not call spawn_subagent or find_in_codebase without first calling soundboard.`
}
func (SoundboardTool) Schema() json.RawMessage {
return json.RawMessage(`{
"type":"object",
"properties":{
  "plan":{"type":"string","description":"1-5 sentences describing what you are about to do and why. Be specific about files, tools, and expected outcomes."}
},
"required":["plan"]
}`)
}
func (t SoundboardTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
var p struct{ Plan string }
if err := json.Unmarshal(args, &p); err != nil {
return "", err
}
if strings.TrimSpace(p.Plan) == "" {
return "", errors.New("soundboard requires a non-empty plan")
}

adapter, model := t.M.CriticAdapter()
if adapter == nil {
return "(soundboard: critic not configured — skipping review)", nil
}

req := llm.Request{
Model:  model,
System: criticSystemPrompt,
Messages: []llm.Message{
{Role: llm.RoleUser, Text: "Plan to review:\n\n" + p.Plan},
},
}

stream, err := adapter.Stream(ctx, req)
if err != nil {
return "", fmt.Errorf("soundboard: critic call failed: %w", err)
}

var sb strings.Builder
for ev := range stream {
switch ev.Type {
case llm.StreamEventText:
sb.WriteString(ev.TextDelta)
case llm.StreamEventError:
return "", fmt.Errorf("soundboard: critic streaming error: %w", ev.Err)
}
}
return sb.String(), nil
}
