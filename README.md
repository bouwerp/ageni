# ageni

A custom agentic coding harness. One master LLM you chat with directly; the master plans work, spawns sub-agents to execute it, and monitors them mid-flight to keep them on track.

Works against any OpenAI- or Anthropic-compatible hosted endpoint.

## Goals

- **One master, many sub-agents.** The user only talks to the master. The master decomposes work, spawns sub-agents with focused tasks, and intervenes when they go off-track.
- **Live monitoring, not polling.** Sub-agents push events as they run. The master sees those events in its own context and can inspect, message, or kill any sub-agent at any time.
- **Provider-agnostic.** Master and sub-agents can each use Anthropic, OpenAI, or any OpenAI-compatible endpoint. Mix and match.
- **Distributable.** Single static binary, cross-compiled for Linux/macOS/Windows. No runtime dependencies.
- **Day-to-day usable.** Real TUI, streaming output, persistent session log, sensible defaults.

## Architecture

### Master / sub-agent model

```
┌─────────────────────────────────────────────────────┐
│                       USER                          │
└──────────────────────┬──────────────────────────────┘
                       │ chat
                       ▼
┌─────────────────────────────────────────────────────┐
│                  MASTER AGENT                       │
│  - reads from EventBus (user msgs, subagent events) │
│  - tools: file/bash + spawn/check/send/kill         │
│  - decides each turn: respond, act, or stay silent  │
└──────┬──────────────────────────────────────────────┘
       │ spawns
       ▼
┌──────────────┐  ┌──────────────┐  ┌──────────────┐
│  SubAgent 1  │  │  SubAgent 2  │  │  SubAgent N  │
│  (goroutine) │  │  (goroutine) │  │  (goroutine) │
│  pushes      │  │  pushes      │  │  pushes      │
│  events ───▶ │  │  events ───▶ │  │  events ───▶ │
└──────────────┘  └──────────────┘  └──────────────┘
                       │
                       ▼
                  EventBus
                  (channel)
```

### Event-driven master loop

The master is **not** a turn-based REPL. It runs continuously, reading from an event bus that carries:

- `user_message` — user typed into the TUI
- `subagent_event` — sub-agent emitted a tool call, model text, completion, or error
- `tick` — periodic timer (lets the master self-check long-running sub-agents)

After each event the master gets called with the new context and decides whether to: respond to the user, take a tool action, course-correct a sub-agent, or stay silent. This is what makes "monitoring + correcting" work without polling hacks.

### Sub-agent runtime

Each sub-agent is its own goroutine with its own message history. It runs to completion or until cancelled, streaming compact events to the bus:

- `tool_call_started` / `tool_call_finished`
- `model_text` (deltas during streaming)
- `done` / `error`

The master sees these events as system messages in its own context. For deeper inspection it calls `check_subagent(id)`, which returns the full transcript. To intervene it calls `send_to_subagent(id, msg)` or `kill_subagent(id)`.

### Shared working directory

Sub-agents share `cwd` with the master. No sandboxing in v1 — file/bash tools operate directly. (See "v2" below for sandbox plans.)

### Tool system

Tools are unified internally as Go structs implementing a `Tool` interface, then translated per-provider when sent to the LLM. Adding a tool = one struct + one registration call.

**v1 toolset (master + sub-agents both have access):**
- `read_file(path)` / `write_file(path, content)` / `edit_file(path, old, new)` / `list_dir(path)`
- `run_bash(cmd, timeout)` — streamed stdout/stderr

**v1 master-only tools:**
- `spawn_subagent(task, context)` → `subagent_id`
- `check_subagent(id)` → recent transcript
- `send_to_subagent(id, msg)`
- `kill_subagent(id)`

## Stack

Go was chosen over Python and Rust after evaluating:

- **LLM SDKs.** Anthropic ships an official Go SDK; `openai-go` is official. Both stream + support tool use.
- **TUI.** Bubble Tea + Lipgloss + Bubbles + Glamour (Charm stack) is mature and proven in production by OpenCode (a directly analogous AI coding TUI).
- **Concurrency.** Goroutines + channels are a textbook fit for "master + N concurrent sub-agents + event bus" — arguably cleaner than Python asyncio.
- **Distribution.** Native single static binary, ~15 MB, instant startup. `GOOS=… GOARCH=… go build` cross-compiles to all major platforms from one machine. No interpreter, no runtime, no antivirus false positives.
- **Iteration speed.** Faster compile cycles than Rust, simpler language.

| Library | Purpose |
|---|---|
| `github.com/anthropics/anthropic-sdk-go` | Anthropic API client |
| `github.com/openai/openai-go` | OpenAI + compatible-endpoint client |
| `github.com/charmbracelet/bubbletea` | TUI framework |
| `github.com/charmbracelet/lipgloss` | Layout & styling |
| `github.com/charmbracelet/bubbles` | Pre-built widgets (textinput, viewport, list, spinner) |
| `github.com/charmbracelet/glamour` | Markdown rendering |
| stdlib `os/exec` | Shell tool |
| stdlib `context` | Cancellation |

### Why not Python?

Distribution. PyInstaller produces 80-150 MB binaries with 1-3s startup and antivirus issues. `uv tool install` is the modern Python answer but requires `uv` on the target. Textual is a slightly nicer TUI library than Bubble Tea, but not enough to outweigh real single-binary distribution.

### Why not Rust?

Slower iteration loop, less mature LLM SDKs (Anthropic's Rust SDK is newer; OpenAI uses community `async-openai`). Ratatui is immediate-mode and more boilerplate than Bubble Tea. Worth it for raw perf or <10 MB binaries; overkill here.

## Repo layout

```
ageni/
  cmd/ageni/main.go          # entrypoint
  internal/
    app/                     # Bubble Tea App, top-level model
    tui/
      chat.go                # master conversation pane
      subagent_list.go       # sidebar: live status of each sub-agent
      subagent_detail.go     # transcript of selected sub-agent
      input.go               # multiline input bar
    agent/
      master.go              # master loop, event handling
      subagent.go            # sub-agent runtime
      bus.go                 # EventBus
    llm/
      llm.go                 # unified Message / ToolCall / stream interface
      anthropic.go           # AnthropicAdapter
      openai.go              # OpenAIAdapter (covers OpenAI-compatible endpoints)
    tools/
      registry.go            # Tool interface + JSON-schema generation
      files.go               # read/write/edit/list
      shell.go               # run_bash
      spawn.go               # spawn/check/send/kill (master-only)
    config/
      config.go              # env-based config loader
    session/
      log.go                 # JSONL session persistence
  go.mod
  go.sum
  .env.example
  README.md
```

## Token efficiency

Token cost is a hard constraint. Multi-agent systems are powerful but [Anthropic's published data](https://www.anthropic.com/engineering/multi-agent-research-system) shows they consume ~15× the tokens of single-turn chat and ~4× a single agent. The harness is designed around the principle that *the master directs, workers execute* — and the worker is the cheaper model.

### Routing rules (baked into master system prompt)

- **Trivial lookup** (file search, grep, listing) → 1 worker, ≤5 tool calls, **Haiku**.
- **Standard task** (multi-file edit, ordinary debug, code review) → 1 worker, ≤15 tool calls, **Sonnet**.
- **Complex / ambiguous** → master decomposes into 3-5 parallel workers; **Opus** is reserved for the planner and final synthesis.

Workers auto-escalate one tier on a second validation failure (build/test/schema mismatch). Per-task-class retry rates are logged; if a class exceeds 20% retry rate, it gets re-tiered permanently.

### Prompt caching (highest-leverage lever)

Production hit rates of 80%+ are achievable. The harness enforces:

- **Stable prefix order** on every request: `tools → static system → CLAUDE-context → session context → messages`.
- **Deterministic tool ordering** (alphabetic by name).
- **Zero volatile content** (timestamps, session IDs, per-request metadata) inside the cached prefix.
- **`cache_control` breakpoints** at end-of-tools, end-of-system, last-user-turn.
- **`<system-reminder>` injections** for volatile state instead of editing the cached prefix. (Current file, git status, todo list go in per-turn messages.)
- **`defer_loading` stubs** rather than mid-session tool removal (Anthropic).

Cache hit rate is surfaced in the TUI status bar. A drop >5pp triggers a session-log warning.

### Context management

- **Compaction triggers at 60% utilization**, not 90% — by 90% the conversation is already operating on degraded summaries.
- **Plan state persists to disk** between worker calls (`~/.ageni/sessions/<id>/plan.md`). The master reads diffs, not full transcripts.
- **Decisions and invariants** live in the cached system region, never in conversation history.

### Worker output discipline

- Workers must reply with a `<result>` block containing JSON-schema-validated payload + a `<reasoning>` block. The master parses with Go structs — no second LLM call to extract.
- Workers have **extended thinking disabled by default**. Master uses adaptive thinking (Claude 4.6+) on plan/synthesis turns only.

### The subagent task contract (enforced)

The `spawn_subagent` tool requires every dispatch to specify:

- `objective` — single-sentence goal in imperative form
- `output_format` — exactly what the worker must return
- `allowed_tools` — explicit tool whitelist (defaults to read-only if unspecified)
- `task_boundaries` — what the worker must NOT touch / decide
- `budget_tool_calls` — hard cap, default 10
- `model_tier` — `haiku` | `sonnet` | `opus` (advisory; harness may auto-escalate)

Vague spawns are refused. (Anthropic's empirical finding: vague directives cause duplicated work and misalignment.)

### Per-model prompt rendering

The harness keeps an internal canonical `Prompt` type with named sections. At the provider boundary it renders to:

- **Claude** (Opus 4.7 / Sonnet 4.6 / Haiku 4.5) — XML tags (`<task>`, `<context>`, `<constraints>`, `<output_format>`, `<example>`).
- **GPT family** — markdown headings + structured-output mode (JSON schema).

No prompt text is shared verbatim across providers.

## Configuration

Config via env vars (or `.env` file in cwd):

```
MASTER_PROVIDER=anthropic              # anthropic | openai
MASTER_MODEL=claude-opus-4-7
SUBAGENT_PROVIDER=anthropic
SUBAGENT_MODEL=claude-sonnet-4-6

ANTHROPIC_API_KEY=sk-ant-...
OPENAI_API_KEY=sk-...

# Optional: point at OpenAI-compatible endpoints
OPENAI_BASE_URL=https://api.openai.com/v1

# Optional: cap on concurrent sub-agents (default 8)
AGENI_MAX_SUBAGENTS=8
```

Master and sub-agents are configured independently — run master on Anthropic and sub-agents on OpenAI, or vice versa.

## Sessions

Each run writes a JSONL session log to `~/.ageni/sessions/<timestamp>.jsonl` containing every event (user messages, master turns, sub-agent events, tool calls). Useful for replay, debugging, and post-hoc analysis.

## Install

### One-line install (recommended)

```sh
curl -sSL https://raw.githubusercontent.com/bouwerp/ageni/main/install.sh | bash
```

The installer detects your platform, downloads the latest pre-built binary from GitHub Releases, verifies its `.sha256` checksum, and installs it to `~/.local/bin` (configurable with `--prefix DIR` or `--system` for `/usr/local/bin`). It fails fast if no pre-built binary exists for your platform — use the source build below in that case.

### From source

```sh
git clone https://github.com/bouwerp/ageni
cd ageni
make install              # installs to $GOPATH/bin
```

### Manual download

Grab the appropriate archive from the [Releases page](https://github.com/bouwerp/ageni/releases), extract, and place on your `PATH`.

## Update

```sh
ageni update              # in-binary self-update (recommended)
```

Or via script:

```sh
curl -sSL https://raw.githubusercontent.com/bouwerp/ageni/main/scripts/update.sh | bash
```

The in-binary update fetches the latest release, downloads the platform-specific archive, verifies its checksum, and atomically replaces the running binary. The script form additionally creates a timestamped backup beside the installed binary, verifies the checksum before installing, and keeps the last 5 backups for rollback. Both fail fast if no pre-built binary exists for your platform.

## Build & run

```sh
make build                # build with version + build-time ldflags → ./ageni
make run                  # build and run
make test                 # tests with coverage
make ci                   # fmt + vet + test + lint

# Cross-compile (one-off)
GOOS=darwin  GOARCH=arm64 go build -o dist/ageni-darwin-arm64  ./cmd/ageni
GOOS=linux   GOARCH=amd64 go build -o dist/ageni-linux-amd64   ./cmd/ageni
GOOS=windows GOARCH=amd64 go build -o dist/ageni-windows-amd64.exe ./cmd/ageni
```

## Release pipeline

Tag pushes (`v*.*.*`) trigger `.github/workflows/release.yml`, which:

1. Builds the binary on a matrix of `darwin-{amd64,arm64}` / `linux-{amd64,arm64}` / `windows-amd64`, embedding the tag and build time via ldflags.
2. Packages each as `tar.gz` (Unix) or `zip` (Windows) with a `.sha256` checksum.
3. Creates a GitHub Release with auto-generated notes and uploads all artifacts.

CI (`.github/workflows/ci.yml`) runs on every PR and push to `main`: tests with `-race`, `golangci-lint`, and a build check.

## Scope

### v1 (this build)

- Master/sub-agent loop with EventBus
- Anthropic + OpenAI adapters with streaming + tool use
- TUI: chat pane, sub-agent sidebar, sub-agent detail pane, input bar
- File + bash tools
- Spawn/check/send/kill sub-agent tools
- Session logging
- Cross-platform single-binary distribution

### Research informing this design

The token-efficiency and prompt-strategy decisions above are sourced from:

- [How we built our multi-agent research system](https://www.anthropic.com/engineering/multi-agent-research-system) — Anthropic
- [Lessons from building Claude Code: Prompt caching is everything](https://claude.com/blog/lessons-from-building-claude-code-prompt-caching-is-everything)
- [Prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching), [Compaction](https://platform.claude.com/docs/en/build-with-claude/compaction), [Extended thinking](https://platform.claude.com/docs/en/build-with-claude/extended-thinking) — Claude API docs
- [Use XML tags to structure your prompts](https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/use-xml-tags) — Claude API docs
- [Piebald-AI/claude-code-system-prompts](https://github.com/Piebald-AI/claude-code-system-prompts) — leaked Claude Code prompt catalog
- [OpenAI Agents SDK — Handoffs](https://openai.github.io/openai-agents-python/handoffs/) and [Orchestrating Agents](https://cookbook.openai.com/examples/orchestrating_agents)
- [OpenHands SDK — Sub-Agent Delegation](https://docs.openhands.dev/sdk/guides/agent-delegation)
- [AI Model Routing Guide for Coding Agents](https://www.augmentcode.com/guides/ai-model-routing-guide) — Augment
- [ReAct](https://arxiv.org/abs/2210.03629), Reflexion, Plan-and-Solve — academic papers on agent reasoning patterns

### Deferred to v2

- Approval prompts for risky tool calls (v1 auto-approves; cwd is shared)
- Cost / token-usage dashboard
- Resume from previous session
- MCP server support
- Sandboxed sub-agent working directories
- Sub-agent tool-call permissioning (e.g., read-only sub-agents)
