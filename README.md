# ageni

A custom agentic coding harness. One master LLM you chat with directly; the master plans work, spawns sub-agents to execute it, and monitors them mid-flight to keep them on track.

Works against any OpenAI- or Anthropic-compatible hosted endpoint.

## Goals

- **One master, many sub-agents.** The user only talks to the master. The master decomposes work, spawns sub-agents with focused tasks, and intervenes when they go off-track.
- **Live monitoring, not polling.** Sub-agents push events as they run. The master sees those events in its own context and can inspect, message, or kill any sub-agent at any time.
- **Provider-agnostic.** Master and sub-agents each pick from a catalog of providers (Anthropic, OpenAI, OpenRouter, Groq, HuggingFace, Cerebras, Mistral, DeepSeek, Gemini, z.AI, local Ollama / llama.cpp / vLLM, Ollama Cloud, or any custom OpenAI-compatible endpoint). Mix and match — e.g. Anthropic Opus master + free Groq Llama sub-agents.
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

Tools are unified internally as Go structs implementing a `Tool` interface, then translated per-provider when sent to the LLM. Adding a tool = one struct + one registration call. MCP servers configured at `~/.ageni/mcp.json` are auto-loaded and their tools become available alongside the built-ins.

**Built-in toolset (master + sub-agents both have access):**

| Category | Tools |
|---|---|
| **Files** | `read_file` (with line ranges), `write_file`, `edit_file`, `apply_diff` (search/replace blocks + miss diagnostics), `multi_edit` (atomic batch), `list_dir`, `glob` (`**` patterns) |
| **Search** | `grep` (ripgrep --json), `web_fetch` (HTML→markdown), `web_search` (Tavily) |
| **Shell** | `run_bash` (streamed), `run_tests` (typed Go/npm/pytest/cargo) |
| **Git** | `git_status` (porcelain v2), `git_diff`, `git_log`, `compute_diff` (in-memory unified diff) |
| **Plan** | `todo_write` (session todo list at `.ageni/todo.json`) |
| **GitHub** | `github` (PRs, issues, code search via `gh` CLI) |
| **Registries** | `pkg_info` (npm / PyPI / Go modules / crates.io) |
| **MCP** | Any tools exported by servers in `~/.ageni/mcp.json` (prefixed `<server>__<tool>`) |
| **Skills** | `read_skill(name, topic?)` loads on-demand instructions; ~21 skills bundled from [realfi-co/agent-skills](https://github.com/realfi-co/agent-skills) (MIT) + anything in `~/.ageni/skills/` or `./.ageni/skills/` |

After every successful file mutation, `lintAfterEdit` appends a language-appropriate lint summary to the tool result (Go: `gofmt -d`; Python: `flake8` summary). The model sees lint feedback in the same turn without a second round-trip.

**Master-only tools:**
- `spawn_subagent(task, context)` → `subagent_id`
- `check_subagent(id)` → recent transcript
- `send_to_subagent(id, msg)`
- `kill_subagent(id)`

### Command queue

Submitting a message while the master is processing queues it rather than
dropping it. The queued message appears in the chat immediately with a
`[queued — N pending]` marker. When the current turn finishes the next
message is dispatched automatically; `masterBusy` stays true until the
queue drains. The status bar shows `N queued` while messages are waiting.

**Esc** cancels the in-flight turn, kills any running sub-agents, *and*
clears the queue — the flash message reports how many queued messages were
dropped alongside how many sub-agents were cancelled.

### Selecting and copying text

Bubble Tea captures mouse events for wheel-scrolling the chat pane, which blocks the terminal's native click-drag selection. Two ways around it:

- **Shift+drag** — works in most modern terminals (iTerm2, kitty, Alacritty, GNOME Terminal, Windows Terminal). The terminal bypasses application mouse capture while Shift is held.
- **F2** — toggles mouse capture off entirely. Drag-select normally, copy with your platform shortcut, then F2 again to re-enable wheel-scroll. Status bar shows `mouse(ON|OFF)`.

### External CLI dependencies

`grep` shells out to `rg`, `git_*` to `git`, `github` to `gh`. Run `ageni doctor` to check what's installed and `ageni doctor --install` to install missing ones via your platform package manager (brew / apt / dnf / yum / pacman / apk). The `install.sh` one-liner runs this automatically; pass `--install-deps` for non-interactive auto-install or `--skip-deps` to bypass.

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
| `github.com/charmbracelet/huh` | Forms (init wizard, settings) |
| `github.com/charmbracelet/glamour` | Markdown rendering |
| `github.com/JohannesKaufmann/html-to-markdown/v2` | `web_fetch` HTML→markdown |
| `github.com/bmatcuk/doublestar/v4` | `glob` `**` support |
| `github.com/hexops/gotextdiff` | `compute_diff` unified diffs |
| `github.com/modelcontextprotocol/go-sdk` | MCP client |
| stdlib `os/exec` | Shell + CLI tool wrappers |
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
    agent/
      master.go              # master loop, lead/worker routing, system prompt
      subagent.go            # sub-agent runtime + budget wrap-up
      manager.go             # spawn / track / kill sub-agents
      bus.go                 # EventBus
      tools.go               # spawn/check/send/kill + record_correction (master-only)
      find_tool.go           # find_in_codebase Librarian tool
    llm/
      llm.go                 # Adapter interface + Message / ToolCall / Request types
      anthropic.go           # AnthropicAdapter with prompt-cache breakpoints
      openai.go              # OpenAIAdapter (covers all OpenAI-compatible endpoints)
      fallback.go            # FallbackAdapter — ordered chain, per-provider rotation
      sanitize.go            # SanitizeText + sanitizeArgs
      providers.go           # provider registry (base URL, API key env, models)
      fetch_models.go        # live /v1/models autocomplete
      pricing.go             # paid + indicative pricing tables
    tools/
      registry.go            # Tool interface + JSON-schema + sanitizeOutput
      files.go               # read_file / write_file / edit_file / list_dir
      applydiff.go           # apply_diff (search_replace + whole + miss diagnostics)
      multiedit.go           # multi_edit (atomic batch)
      shell.go               # run_bash
      lint.go                # lintAfterEdit (gofmt, flake8)
      git.go                 # git_status / git_diff / git_log / compute_diff
      changes.go             # ChangeTracker (snapshots + checkpoints)
      glob.go / grep.go      # glob + ripgrep wrapper
      web.go                 # web_fetch / web_search
    tui/
      app.go                 # Bubble Tea root model
      settings.go            # Ctrl+, settings page
      diffrender.go          # Claude Code-style diff rendering
      atfile.go / atcomplete.go  # @-mention expansion + autocomplete popup
      history.go             # command history
      styles.go              # lipgloss styles + colour palette
    session/
      session.go             # per-instance state
      log.go / replay.go     # JSONL logger + session resume
      picker.go              # interactive session browser
    config/
      config.go              # env-based config loader
    mcp/
      mcp.go                 # MCP client (subprocess transport)
    skills/
      catalog.go             # lazy skill catalog + read_skill tool
  go.mod
  go.sum
  .env.example
  AGENTS.md                  # per-project master instructions (optional)
  README.md
  BUILDING.md
```

## Token efficiency

Token cost is a hard constraint. Multi-agent systems are powerful but [Anthropic's published data](https://www.anthropic.com/engineering/multi-agent-research-system) shows they consume ~15× the tokens of single-turn chat and ~4× a single agent. The harness is designed around the principle that *the master directs, workers execute* — and the worker is the cheaper model.

### Lead / worker model routing

The master runs in two adapter slots per turn:

- **Lead adapter** (iteration 0 of each `takeTurns` call) — used for planning, decomposition, and synthesis. Set to the most capable model you can afford (Opus / GPT-4o class).
- **Worker adapter** (iterations 1+) — used for execution loops. Set to a cheaper model (Sonnet / Haiku class).

Configure both in the settings page (`Ctrl+,`) under "Master lead model" and "Master worker model". When the lead and worker are the same, the routing is transparent.

### Provider fallback chains

Each provider slot (master, sub-agent) is backed by a `FallbackAdapter` — an ordered chain of adapters. On a retryable error (rate limit, 5xx, connection failure, context-length exceeded), the chain tries the next entry. Fallback only triggers before any response content has been emitted; once text or tool calls are streaming the chain commits.

Within each chain entry, `FallbackModels` lists alternative models to try on the same provider before crossing to the next provider. A `LiveModelFetcher` callback, called at most once when all static candidates are exhausted, fetches the provider's live model list and appends any IDs not yet tried — preventing stale hardcoded model names from blocking progress.

The TUI status bar flashes a `fallback: <from> → <to> (reason)` message on each transition.

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

The recommended way to configure ageni is the interactive wizard:

```sh
ageni init
```

It walks you through picking a master provider + model, a sub-agent provider + model, and writes `~/.ageni/.env`. On first launch with no config, ageni drops straight into the wizard.

### Provider catalog

Built-in presets — pick any of these in the wizard, or set `MASTER_PROVIDER` / `SUBAGENT_PROVIDER` directly:

| Provider | Free tier | Cache | Notes |
|---|:-:|:-:|---|
| `anthropic` | trial | ✓ | Claude Opus / Sonnet / Haiku. Best tool-use, full prompt caching. |
| `openai` |  | ✓ | GPT-4o / o3 / mini. Automatic prompt caching. |
| `openrouter` | ✓ |  | Aggregator: 100+ models, many `:free` (Llama, Qwen, DeepSeek). |
| `groq` | ✓ |  | Very fast Llama / Qwen / DeepSeek. Free RPM-limited (~30/min). |
| `huggingface` | ✓ |  | HF Inference Providers router. Small monthly free credit. |
| `cerebras` | ✓ |  | World's fastest Llama. Generous free tier (~30 RPM, 1M tok/day). |
| `mistral` | ✓ |  | Mistral La Plateforme: Codestral, Large, Nemo. Free tier with limits. |
| `deepseek` | trial |  | DeepSeek V3 / R1. Cheap pay-as-you-go. |
| `gemini` | ✓ |  | Gemini 2.5 Pro / Flash via OpenAI-compat. Generous free quotas. |
| `togetherai` | trial |  | Together.ai inference: Llama 3.3 70B, Qwen, DeepSeek. Competitive pricing. |
| `opencode-zen` |  |  | OpenCode's hosted Zen model. OpenAI-compatible; strong on coding tasks. |
| `ollama` | local |  | Local: `ollama serve` on :11434. Wizard auto-detects + lists models. |
| `llamacpp` | local |  | Local: `llama-server` on :8080. |
| `vllm` | local |  | Self-hosted vLLM on :8000. |
| `ollama-cloud` | trial |  | Hosted Ollama Turbo. |
| `zai` |  |  | z.AI PaaS coding models. |
| `custom` |  |  | Arbitrary OpenAI-compatible endpoints. |

The wizard shows free-tier markers, suggests free defaults, and reduces the concurrent-sub-agent cap when you pick a strict free tier (Groq → 2, HF → 2).

### Free-tier caveats

- **Rate limits.** Groq, HF, and many OpenRouter free models cap at 30-60 requests/min. The harness retries on 429s with exponential backoff (4 attempts), but parallel sub-agent fan-out still hits ceilings. Lower `AGENI_MAX_SUBAGENTS` accordingly.
- **No prompt caching.** Only Anthropic and OpenAI proper offer real prompt caching. On other providers the master's stable system prompt is re-charged every turn.
- **Tool-use compliance varies.** Claude Sonnet 4.6+, Llama 3.3 70B, Qwen 2.5 Coder, DeepSeek V3 are all reliable. Smaller / older models can hallucinate tool args.

### Config file load order

1. `~/.ageni/.env` — global default written by `ageni init`
2. `./.env` — per-project override
3. Real environment — always wins

### Manual config

If you'd rather skip the wizard, see [`.env.example`](.env.example) for the full env-var surface.

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

### v1 (current — v0.38.40)

- Master/sub-agent loop with EventBus
- Anthropic + OpenAI-compatible adapters with streaming + tool use
- Provider fallback chains with per-provider model rotation and live model fetch
- Lead/worker adapter routing for master turns
- TUI: chat pane, sub-agent sidebar, sub-agent detail pane, settings (Ctrl+,)
- Inline diff rendering after file mutations
- File + bash tools, `apply_diff` with miss diagnostics, `multi_edit`
- Auto-lint feedback appended to file edit results
- Per-tool-call checkpoints + rewind
- Spawn/check/send/kill sub-agent tools with AllowedTools whitelisting
- AGENTS.md per-project instruction loader
- @-mention file expansion + fuzzy autocomplete popup
- Session logging, resume, interactive session browser
- MCP server support
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
- Sandboxed sub-agent working directories

### Roadmap (post-research, see `research.md`)

- **Sandbox tiers via Landlock (Linux) + Seatbelt (macOS).** `read-only` / `workspace-write` / `danger-full-access` modes with `writable_roots` and `network_access` config keys. Reuse Codex's vocabulary for muscle memory. Real safety, no Docker, fits the single-binary constraint.
- **Recipes — parameterised, executable agent workflows in YAML.** `ageni run recipe.yaml --param target=foo`. Skills are read-only docs; recipes are repeatable team knowledge. Distro story for ageni-in-a-team.
- **Polyglot benchmark harness.** Fork Aider's 225-exercise polyglot suite, add `make bench`, publish a leaderboard for ageni against its supported providers. Run on every release tag.
- **Mode-enforced Plan / Act / Auto split** gated at the tool layer. In Plan, `write_file` / `edit_file` / `run_bash` return refusals.
- **`ageni exec "<prompt>"` headless mode + GitHub Action** that wraps it. Structured JSON output for CI / PR-bot use.
- **Reverse MCP** — `ageni mcp-server` so other agents can call into ageni.
- **Context condenser** — explicit memory compression strategy when the master fills, beyond Anthropic's auto-compaction.

### Follow-up TODOs from technical + functional review

#### Runtime architecture / orchestration

- [x] Enforce hard master-vs-worker tool boundaries at the registry/tool layer instead of relying on prompt instructions alone.
- [x] Make live monitoring real: forward mid-flight worker events to the master, add periodic tick/self-check events, and support interruptible supervision.
- [x] Add durable, append-only event journaling with correlation IDs so replay/debugging does not depend on lossy bus subscribers.
- [x] Replace string-matched error recovery with typed/provider-aware error classes and clearer retry / escalation policy.
- [ ] Add substantially more race/integration coverage around the master loop, resume flow, shells, logging, and safety boundaries.

#### Safety / permissions

- [ ] Add approval modes for risky/destructive operations instead of v1 auto-approval.
- [ ] Implement sandbox tiers (`read-only` / `workspace-write` / `danger-full-access`) with explicit writable roots and network policy.
- [ ] Add risky-command detection and stricter permission scopes for bash, file deletion, and other high-blast-radius actions.
- [ ] Default sub-agents to narrower capabilities/allowed-tools by role instead of broad full-access unless explicitly widened.
- [x] Make session/event logs private-by-default and scrub tool args/results before persistence.

#### Durability / long-running work

- [ ] Make session continuity stronger than chat replay: persist shell output/state, support resumable jobs, and allow worker/shell reattachment after restart.
- [ ] Add pause/interrupt primitives for long model/tool turns instead of only coarse stop/kill flows.
- [ ] Persist shell logs for long-running services/tasks and surface buffer-wrap / output-loss warnings.

#### Code intelligence / editing ergonomics

- [x] Add semantic code intelligence (LSP / AST-backed symbol search, find references, rename, move, etc.) instead of relying mostly on text search and string replacement.
- [ ] Add transactional multi-file edit support so coordinated refactors can validate first and apply atomically.
- [ ] Improve repo mapping/navigation beyond the current simple optional ctags-based map and ranking.
- [ ] Expand post-edit validation beyond Go/Python and add stronger ecosystem-aware verification for TS/JS, Rust, Java, etc.

#### UX / control surface

- [ ] Add staged diff review with approve/reject checkpoints before applying high-impact edits.
- [ ] Add pause/resume/inspect controls for the master and workers mid-flight, not just stop/kill.
- [ ] Improve worker visibility in the TUI with richer status, progress, and transcript inspection during long runs.
