# Building ageni — a detailed account

This document records the design and construction of **ageni**, a custom
agentic coding harness shipped as a single Go binary. It explains *what
was built*, *why each piece exists*, and *the order things came in* so
future contributors can ramp without re-deriving the design from the
commit log.

The repo is at `github.com/bouwerp/ageni`. Releases follow the rule
"every push that ships a user-visible change gets a new tag" — minor
bumps for features, patch bumps for fixes. At the time of writing the
project is on **v0.37.5** and 62 commits.

---

## Table of contents

1. [Goals and constraints](#goals-and-constraints)
2. [Architectural decisions](#architectural-decisions)
3. [Build phases, in order](#build-phases-in-order)
4. [Package layout](#package-layout)
5. [Key design patterns](#key-design-patterns)
6. [Release pipeline](#release-pipeline)
7. [Per-version changelog](#per-version-changelog)
8. [Lessons learned](#lessons-learned)

---

## Goals and constraints

The original brief, paraphrased:

- A custom agentic coding harness — one **master LLM** the user chats
  with, which delegates parallel work to **sub-agents** and integrates
  the results.
- **Single-binary distribution.** No language runtime, no `pip install`,
  no Docker. Curl-pipe-bash works.
- **Multi-provider.** Anthropic (paid), OpenAI-compatible (paid + free
  tiers), local LLMs (Ollama, vLLM, llama.cpp) — a user shouldn't have
  to commit to one vendor.
- **Beautiful TUI.** Real markdown rendering, syntax highlighting,
  styled tool calls + results — not ANSI-escape soup.
- **Token-efficient.** Prompt caching, compaction, smart context
  preloading, low-cost workers for grunt work, opus-class models only
  when needed.
- **Per-session state.** Multiple instances of ageni in the same repo
  must not collide. Sessions resumable.
- **Continuous release pipeline.** Every change ships a new GitHub
  release with platform-specific binaries; users get fixes via
  `ageni update`.

Everything that follows is downstream of those constraints.

---

## Architectural decisions

### Go

Picked early. Single static binary, robust concurrency primitives
(goroutines + channels for sub-agent orchestration), tooling that the
average dev has installed. The Anthropic and OpenAI Go SDKs were already
mature enough at start.

### Bubble Tea + Bubbles + Lipgloss + Glamour + Huh

The Charm stack. Bubble Tea is the event-loop framework; Bubbles
provides the textarea + viewport + history widgets; Lipgloss does
styling; Glamour renders Markdown to terminal; Huh runs the
configuration wizard and the in-app settings page. All five integrate
cleanly; using them avoids reinventing terminal UI primitives.

### Master / sub-agent split

Two *kinds* of agent, not just two instances:

- The **master** is the only thing the user talks to. It owns the
  conversation, plans the work, and integrates results. It runs on the
  best model the user can afford (typically Opus or GPT-4-class).
- **Sub-agents** are spawned with a tightly-scoped task and a
  cost-aware model tier (haiku / sonnet / opus). They run in their own
  goroutines, in parallel.

This separation is what makes the orchestration work. The master's
tokens are expensive; the workers' tokens are cheap. Anything that
takes more than a couple of tool calls becomes a sub-agent task.

### Event bus, not direct method calls

`internal/agent.Bus` is a many-to-many in-memory pub/sub. The master
publishes; sub-agents publish; the TUI subscribes; the session logger
subscribes; the find_in_codebase tool subscribes briefly to wait for
its worker. Using a bus instead of direct callbacks decouples the TUI
from agent internals — anything that can subscribe gets a feed of
everything happening.

Slow subscribers drop events rather than blocking publishers. That's
intentional: the TUI is fastest, the logger is next, and back-pressure
on the master would degrade UX.

### Tool registry

`internal/tools.Registry` holds named tools and produces deterministic
`ToolDef` lists for the LLM (sorted by name, for prompt-cache
stability). Master and sub-agents use *different* registries — the
master gets the orchestration tools (`spawn_subagent`, `check_subagent`,
`find_in_codebase`, etc.), workers don't. The `Subset` method scopes a
sub-agent's tool access by name when its `AllowedTools` whitelist is
non-empty.

### Sessions

Per-instance state lives under `~/.ageni/sessions/<id>/`:

```
meta.json           ID, repo path, started/last-used, model snapshot
log.jsonl           every Bus event ever published (append-only)
todo.json           shared todo list across master + workers
corrections.jsonl   user corrections injected into active_context
changes.jsonl       file mutations recorded by tools
snapshots/<sha>     pre-mutation file content for diffs
```

Multiple ageni instances in the same directory get distinct session
dirs and don't collide. Resumed sessions read everything back.

---

## Build phases, in order

Listed as the actual order things were built. Each phase corresponds
roughly to a cluster of commits. Bracketed tags show the version where
each piece landed.

### Phase 0 — skeleton (v0.1.0)

Bootstrap a Go module. Stub `cmd/ageni/main.go`. Wire one Anthropic
adapter and one OpenAI adapter behind a common `llm.Adapter` interface
that streams text + tool calls. Pick the message + tool-call types in
`internal/llm/llm.go`: `Message`, `ToolCall`, `ToolResult`, `ToolDef`,
`Request`, `StreamEvent`. Everything else builds on this layer.

### Phase 1 — master + workers (v0.2.0)

Implement the master loop in `internal/agent/master.go`: read user
messages, call the LLM, dispatch tool calls, repeat. Implement the
sub-agent loop in `internal/agent/subagent.go` with the same shape
plus a per-task tool-call budget. Add `Manager` to spawn / track /
kill sub-agents and `Bus` for events.

Land the orchestration tools (`spawn_subagent`, `check_subagent`,
`send_to_subagent`, `kill_subagent`) in `internal/agent/tools.go`. The
master sees them; workers don't.

### Phase 2 — file ops + config wizard (v0.3.x → v0.5.0)

Build the basic tool catalog in `internal/tools/`: `read_file`,
`write_file`, `edit_file`, `multi_edit`, `list_dir`, `glob`, `grep`,
`make_dir`, `move_file`, `delete_file`, `run_bash`, `run_tests`,
`web_fetch`, `web_search`, `git_*`, `github`, `pkg_info`. Each tool
has a stable JSON schema (cache-friendly).

Add `cmd/ageni/init.go`: a Huh-driven first-run wizard that detects
which API keys are set, asks for missing ones, and writes
`~/.ageni/.env`.

Ship the install script + GitHub release pipeline (`release.yml`)
matrix-building macOS / Linux / Windows binaries on every tag.

### Phase 3 — multi-provider (v0.6.0 → v0.8.0)

Add OpenRouter, Groq, HuggingFace, OpenCode adapters. They're all
OpenAI-compatible at the wire level, so they share the OpenAI
adapter with different base URLs and model lists. Add Ollama, vLLM,
llama.cpp for local inference.

Build `internal/llm/providers.go` as the registry of provider
metadata (name, kind, base URL, env-var name for the API key).
`internal/llm/fetch_models.go` queries each provider's `/v1/models`
endpoint at startup so the autocomplete in the wizard / settings page
shows live model lists, not stale hard-coded ones.

Add `internal/llm/pricing.go` with price tables for paid providers.
For free / local / unknown models we record a price of zero but also
an *indicative* paid-equivalent price (pretend it ran on a comparable
paid model) so users can see what their session would cost on paid
rates.

### Phase 4 — TUI polish (v0.8.1 → v0.9.7)

Real Markdown rendering with Glamour. Force `termenv.TrueColor`
explicitly — Glamour's auto-detection inside Bubble Tea's alt-screen
can fall back to no-tty profile, producing raw markdown text.

Bubbles textarea for input, viewport for chat, custom history
component (`history.go`) with up/down arrow recall persisted to
`~/.ageni/history.txt`.

Styled tool-call rendering — distinct background, header with tool
name, syntax-highlighted args. Tool results similarly framed.

Mouse capture toggle (F2) so users can drag-select text in their
terminal — when capture is on the wheel scrolls the chat viewport;
when off, the terminal handles selection. Documented in app text and
acknowledged as a fundamental terminal limitation.

### Phase 5 — token economics (v0.9.x → v0.13.x)

Prompt caching: the Anthropic adapter inserts `cache_control`
breakpoints at the system prompt and the conversation prefix. The
tool definition list is sorted by name so the cache key is stable.
Cache-creation and cache-read tokens are tracked separately from
input tokens.

Compaction: Anthropic auto-compacts at 120k input tokens. Beyond a
threshold we summarise older turns ourselves so we don't blow the
context window.

`<active_context>` block: the master's prior pattern was to inject
fresh "system reminders" each turn — they accumulated and bloated the
context. Replaced with a single self-replacing tail block that holds:
recent corrections, current sub-agent state, pending events. Stripped
and rewritten on every turn instead of accumulating.

Aider-style **repo map**: `internal/repomap/repomap.go` shells out to
universal-ctags to enumerate symbols in the repo, then uses a PageRank
heuristic to pick the most-referenced files. The result is a
~2000-token system-prompt block listing each important file and the
symbols it defines. Cached at `~/.ageni/cache/repomap-<hash>`.

`find_in_codebase` Librarian tool: a master-only tool that delegates
code search to a haiku-tier sub-agent with a 10-call budget. The
Librarian uses grep / glob / read_file to find what's asked, then
returns a 200-500 token summary with paths + line numbers. This
prevents raw grep output from bloating the master's context.

Sub-agent budget: counts actual tool calls, not turns. When exhausted,
the worker gets one "wrap-up turn" (no tools available) to produce its
final result, instead of erroring out. Default 40 calls; configurable
via `AGENI_SUBAGENT_BUDGET` and the in-TUI settings page.

Per-role telemetry: master and sub-agent token usage tracked
separately. Status bar shows `M:tokens c=hit% S:tokens c=hit%` plus
session cost.

### Phase 6 — orchestration sharpening (v0.14.x → v0.16.0)

Master prompt rewritten with aggressive parallel-delegation rules.
Anti-patterns explicitly called out: "about to grep more than twice
→ use find_in_codebase", "about to read more than 3 files → spawn a
sub-agent", "spawning sequentially when work is independent → fan
out".

Inbox event coalescing: when multiple sub-agents finish in a burst,
the master's inbox is drained before the next turn. One integration
turn handles all of them instead of N separate turns.

Structured worker return schema (`CanonicalWorkerOutputFormat`):
every worker returns `<result><findings>HIGH|path:line|claim ...
</findings></result><reasoning>...</reasoning>`. Confidence-marked
findings let the master decide what to trust without re-reading
files itself.

Richer spawn context: `spawn_subagent` schema gained `repo_facts`,
`prior_findings`, `do_not_revisit` fields so the master pre-curates
each worker's context. This was the alternative to a shared-memory
pattern (which research recommended *against*: workers writing into a
shared scratch corrupted each other's runs in published experiments).

### Phase 7 — sessions (v0.17.0)

Introduced `internal/session.Session`. ID format
`YYYYMMDD-HHMMSS-XXXX` is sortable by time and short enough to type.
Each session gets a directory under `~/.ageni/sessions/<id>/`.

`ageni sessions list/show/resume/rm` CLI for managing them. The
`--session <id>` flag resumes (and accepts a unique prefix). Multiple
ageni instances in the same repo no longer collide on `todo.json`.

### Phase 8 — work claiming + corrections + dump (v0.18.0)

`TodoItem.ClaimedBy` field plus `claim` / `release` actions. When
fanning out parallel workers, the master claims each item up front so
two workers don't race on the same todo.

`record_correction` master-only tool. Writes `{at, was, now, why}` to
`corrections.jsonl`. The active_context block surfaces the most-recent
N corrections so the master honours them over older history.

`ageni sessions dump <id>` CLI + F3 keybind: dump the session log
as a human-readable transcript for hand-off / debugging.

### Phase 9 — visible activity (v0.19.0 → v0.20.0)

Sub-agent cancellation handling: `context.Canceled` is a benign
terminal, not an error. Sets status=Cancelled and emits `EvSubagentDone`
with empty text. Stops spurious "[s2 error] context canceled" lines.

`find_in_codebase` outer timeout bumped 3min → 10min. The worker's
own 10-call budget is the real limiter; 3 minutes was killing
legitimate searches.

`find_in_codebase` registered in the sub-agent base registry too —
the master prompt promotes it heavily and that vocabulary leaks into
spawn_subagent contexts; workers were hallucinating the call. The
Librarian itself excludes find_in_codebase from its `AllowedTools`,
so no infinite recursion.

Activity indicators: 120 ms `tea.Tick` drives a braille spinner.
- Status bar: `⠋ master thinking…`, `⠋ master:tool_name…`,
  `⠋ master waiting on s1,s2`, or `master idle`.
- Side pane: per-sub-agent marker animates while running; activity
  label shows `thinking` / `tool:NAME` / `spawning`.
- Inline: `⠋ master · thinking…` line at the bottom of the chat
  pane while the master is generating but hasn't emitted text yet.
  Once tokens stream, the live text replaces it.

New events `EvMasterTurnStart` and `EvSubagentTurnStart` emitted
right before each `Stream()` call so the indicator follows the
*actual* LLM call — not the user's Enter press. Generation triggered
by sub-agent completion now lights up too.

### Phase 10 — master ownership (v0.21.0)

Master prompt gained `<ownership_rules>`:
1. Master owns every sub-agent it spawns. Never asks the user about
   worker state.
2. Verify each `<result>` before integrating.
3. Drive goals to completion: user-message → plan → workers →
   integration → deliverable.
4. Pause only for genuine blockers (missing info, auth, irreversible
   actions, real ambiguity narrowed to ≤3 options).
5. End turns cleanly.

Plus tightened `<output_discipline>`: don't narrate orchestration
("I'll spawn s1 now…") — the side pane already shows it.

### Phase 11 — change tracking + diffs (v0.22.0)

`internal/tools/changes.go`: `ChangeTracker` records every file
mutation with a pre-mutation snapshot under `<session>/snapshots/`.
The snapshot is taken the *first* time a path is touched in the
session so we always have a known baseline to diff against —
independent of git, working-tree state, or partial commits.

Wired into `WriteFile`, `EditFile`, `MultiEdit`, `MakeDir`,
`MoveFile`, `DeleteFile`. Each calls `Tracker.Snapshot(absPath)`
before mutating and `Tracker.Record(...)` after success.

CLI:
- `ageni sessions changes <id>` — one-line-per-path summary
- `ageni sessions diff <id> [path] [-o file]` — unified diff (uses
  platform `diff -u`)

TUI:
- Side-pane "changes (N)" section with per-file kind markers
  (`+` created, `~` edited, `-` deleted, `→` moved)
- F4 dumps a full diff to `/tmp/ageni-diff-<id>.diff`

### Phase 12 — full session resume (v0.23.0)

`session.LoadHistory(s)` walks `log.jsonl` and reconstructs the
master's prior message buffer: every user message, assistant turn
(text + tool calls), and tool result. Tool-call IDs are minted at
replay time (`replay-N`) and applied consistently to call+result
pairs so the LLM API accepts the seeded history.

`Master.LoadHistory` seeds `Master.messages` before `Run` starts.
TUI's `App.LoadHistory` renders the replayed conversation into the
chat buffer with a "─── resumed: N prior message(s) ───" header.

`session.NewLogger` switched from `os.Create` (which truncates) to
`OpenFile(O_CREATE|O_APPEND)` so resumed sessions keep building
their log on top of prior turns — future replays see everything.

Sub-agent transcripts don't survive a process restart (workers were
per-process). The master's *memory* of what they returned is preserved
through tool-result messages on its replayed history.

### Phase 13 — session browser + @-mentions (v0.24.0 → v0.25.0)

Bare `ageni` now shows an interactive picker (Huh `Select`) listing
every saved session ordered by last-used. Each entry shows last-used
age + repo + master model + (todo count, change count) so users pick
by context, not by ID. Esc/Ctrl+C aborts → starts fresh; `--new`
skips the picker; `--session <id>` jumps directly.

`@<path>` file references in user input. The TUI's Enter handler
calls `expandFileMentions(text)` which scans for `@<path>` tokens
(regex requires whitespace or start-of-input before the `@` so
emails don't match), reads each as a file (capped 200 KB), and
appends `<attached_file path="X">CONTENT</attached_file>` blocks to
the message the master receives. The chat pane shows what the user
typed (raw); the master sees the expanded form.

### Phase 14 — resume hardening (v0.25.1)


The master's replayed history contained tool results like "spawned
sub-agent s1" — so on resume it treated `s1` as live and would call
`check_subagent("s1")` against a fresh empty Manager. Fix:

1. `session.PriorSubagentIDs(messages)` scrapes `spawned sub-agent
   sN` from tool results, returns the IDs and the highest numeric
   suffix.
2. `Manager.SetNextSubagentID(maxN)` bumps the spawn counter so
   fresh workers don't collide with remembered IDs.
3. `session.ResumeReminder(...)` builds an explicit
   `<system-reminder>` warning the master that the listed IDs are
   TERMINATED; appended to the replayed message buffer before
   `master.Run` starts.

### Phase 15 — AGENTS.md project instructions (v0.26.0)

`ageni` now reads an `AGENTS.md` file from the project root on startup
(falling back to `~/.ageni/AGENTS.md` for global defaults). Its
contents are appended to the master's system prompt under an
`<agents_instructions>` block. This is the equivalent of Claude Code's
`CLAUDE.md`: a place for per-project conventions ("always use `errors.As`
not string matching", "the test suite is `go test ./...`") that are
injected once into the cache region so every master turn sees them
without incurring user-message tokens.

The loader is lazy — `AGENTS.md` is read once at start and its digest
cached so a stale stat on every turn doesn't add I/O.

### Phase 16 — apply_diff with miss diagnostics (v0.27.0)

`edit_file` performs a single literal search-and-replace. For multi-block
changes on actively-modified files LLMs mis-predict the current content and
miss. Added `apply_diff` in `internal/tools/applydiff.go`:

- **`search_replace` format** (default, matches Aider's format): one or
  more `<<<<<<< SEARCH … ======= … >>>>>>> REPLACE` blocks applied
  in order. Each SEARCH must match exactly once unless `replace_all`
  is set.
- **`whole` format**: replaces the entire file. Equivalent to
  `write_file` but included so callers use one tool.

The key addition is **miss diagnostics**: when a SEARCH block doesn't
match, the tool computes an edit-distance score against every window of
the same size in the file and returns the top-3 closest candidate
regions with line numbers. The model sees "here's the closest thing I
found, starting at line 47" and can retry without re-reading the file.

### Phase 17 — lead / worker adapter routing (v0.28.0)

Added two fields to `Master`: `leadAdapter` and `leadModel`. When set,
iteration 0 of every master `takeTurns` call uses the lead adapter;
iteration 1+ use the worker adapter.

```go
func (m *Master) adapterForIter(iter int) (llm.Adapter, string) {
    if iter == 0 && m.leadAdapter != nil {
        return m.leadAdapter, m.leadModel
    }
    return m.adapter, m.model
}
```

The pattern mirrors Goose's `GOOSE_LEAD_MODEL` concept: run planning and
decomposition on the expensive model (Opus / GPT-4o), then switch to the
cheaper worker model (Sonnet / Haiku) for the execution loop iterations.
The TUI settings page exposes "Master lead model" and "Master worker
model" as separate fields. Passing nil lead reverts to uniform routing.

### Phase 18 — per-tool-call checkpoints + rewind (v0.29.0)

The session log already recorded every event but there was no way to
undo a bad edit short of `git checkout`. Added checkpointing:

- `ChangeTracker` already snapshots each file on first touch. Extended
  it with an explicit `Checkpoint(label)` call that records a named
  marker to `changes.jsonl`.
- The `rewind_to_checkpoint` master-only tool restores every file
  touched since the named checkpoint to its snapshotted state.
- Called automatically after each tool call that mutates files — the
  master gets the checkpoint ID back in the tool result and can call
  `rewind_to_checkpoint(id)` if the subsequent test/lint step fails.

This gives the master a lightweight undo without requiring a git
working tree.

### Phase 19 — auto-lint after edits (v0.30.0)

`internal/tools/lint.go` adds `lintAfterEdit(absPath)`, called by
`WriteFile`, `EditFile`, `MultiEdit`, and `ApplyDiff` after every
successful mutation. It runs a language-appropriate linter capped at
8 seconds:

- **Go**: `gofmt -l` — if the output is non-empty, re-runs `gofmt -d`
  and appends the diff to the tool's return string.
- **Python**: `flake8 --select E,W --max-line-length 120` summary.

The lint output is appended to the tool result the model already sees —
no extra turn needed. The model reads "gofmt: file needs reformat" and
can decide whether to fix it. We deliberately don't auto-fix: silent
side-effect mutations would surprise users and produce diffs they
didn't request.

### Phase 20 — Together.ai and OpenCode Zen (v0.31.0)

Added two first-class providers to `internal/llm/providers.go`:

- **`togetherai`** — Together.ai's inference API. OpenAI-compatible;
  hosts Llama 3.3 70B, Qwen 2.5 Coder, DeepSeek V3, and many others.
  Competitive pricing; tends to be faster than OpenRouter for the same
  model.
- **`opencode-zen`** — OpenCode's hosted Zen model. OpenAI-compatible;
  good for coding tasks; distinct from the `opencode` local proxy entry.

Both appear in the init wizard, the settings provider picker, and the
model autocomplete list. Their `RecommendedModels` lists are pre-seeded
with the models that reliably support tool use.

### Phase 21 — provider fallback chains (v0.32.0)

`internal/llm/fallback.go` introduces `FallbackAdapter` and
`FallbackEntry`. A chain is a prioritised list of adapters; `Stream`
tries each in turn, falling through on retryable errors (429, 402, 5xx,
connection failures, timeouts, context-length) but only if the current
adapter hasn't emitted any text or tool calls yet — once partial content
has streamed, fallback stops.

```go
type FallbackEntry struct {
    Adapter          Adapter
    Model            string
    Label            string
    FallbackModels   []string
    LiveModelFetcher func() []string
}
```

`FallbackModels` lists alternative models to try on the same provider
before crossing to the next entry. `LiveModelFetcher`, called at most
once per run, fetches the provider's current model list and appends any
IDs not yet tried — so a deprecated hardcoded model doesn't block
progress.

The `OnFallback` callback fires on each fall-through with `from`, `to`,
and `reason` strings. The TUI uses this to flash a status-bar message.

A special **OpenRouter 402 retry** is built in: when the error says
"can only afford N tokens", the chain retries the same model once with
`MaxTokens` capped to N before trying alternatives, preserving the
preferred model over a transient budget cap.

### Phase 22 — settings overhaul: static page + model ranking + price display (v0.33.0–v0.34.0)

**v0.33.0** replaced the previous multi-screen Huh form with a single
scrollable static settings page (`internal/tui/settings.go`). All
fields are visible at once; the form only triggers Huh when a field
needs a text input pop-up. This fixed a class of viewport-sizing bugs
where field titles were clipped on narrow terminals.

**v0.34.0** added per-model price display and quality ranking to the
model picker:

- `internal/llm/pricing.go` extended with `RankedModel` type and a
  `ModelsByQuality` sort key. Models are ordered by tier:
  `opus/gpt-4` class → `sonnet/gpt-4o` class → `haiku/flash` class →
  free / local.
- The settings model autocomplete shows `[provider] model-name  $X.XX/Mtok` so
  users can see cost before committing.
- Section names embedded in each field's title so the user knows which
  section a field belongs to without needing to scroll to find the header
  (fixed a disorientation issue from v0.33.0).

### Phase 23 — @ path autocomplete (v0.35.x)

The v0.25.0 `@<path>` expansion was expand-on-submit only. Added a
VSCode-style popup in `internal/tui/atcomplete.go`:

- Typing `@` in the input bar opens a fuzzy-searchable file picker
  listing every file in the working tree (excluding `.git`, `vendor`,
  `node_modules`).
- Arrow keys + Enter select. Tab completes the common prefix. Esc
  closes without inserting.
- The popup is rendered as a floating overlay anchored to the cursor
  position in the input bar using Lipgloss border + background styles.

### Phase 24 — fallback chain bug fixes (v0.36.x)

Several correctness fixes to the fallback chain and TUI after v0.32.0:

- **413 and context-length errors** added to `isFallbackable` — Groq
  rejects prompts that exceed its context window with a 413; other
  providers use `context_length_exceeded` strings. Both now trigger
  chain advance.
- **OpenRouter 402** wording variations (`insufficient credits`,
  `payment required`) added to `isFallbackable`.
- **Stale terminal cells**: rapid pane-switch (Tab / Shift+Tab) left
  cells from the previous pane. Fixed by forcing a full repaint on
  pane-switch events.
- **Layout overflow**: side pane overflowed the terminal width on
  narrow terminals when sub-agent names were long. Fixed with
  `lipgloss.MaxWidth` on the pane container.
- **Stderr bleed**: `run_bash` was mixing stdout + stderr into a single
  buffer. Separated them; stderr now appears with a distinct style in
  the chat pane.
- **Malformed tool names**: some provider responses emit tool names
  with trailing whitespace or null bytes. Trimmed at deserialization.

### Phase 25 — per-provider model rotation + lazy live fetch (v0.37.0)

Extended `FallbackEntry` with two fields:

```go
FallbackModels   []string       // tried in order before advancing
LiveModelFetcher func() []string // called at most once when all static exhausted
```

`tryFrom` now cycles through `FallbackModels` (same provider, different
model) before advancing to the next `FallbackEntry` (different
provider). `LiveModelFetcher`, when non-nil, is called once after all
static candidates are exhausted: it fetches the provider's live model
list and appends any IDs not yet tried. This means a deprecated
hardcoded `FallbackModels` entry doesn't block progress — the live
list fills in.

### Phase 26 — plan→delegate enforcement (v0.37.1)

The master prompt gained an explicit `<rule>` block enforcing the
plan→delegate cycle. Prior to this change the master would occasionally
call file/shell tools directly on trivial requests rather than spawning
a sub-agent. The new rule reads:

> DELEGATE — Spawn sub-agents for every task. You NEVER call grep,
> glob, read_file, write_file, edit_file, run_bash, or any file/shell
> tool yourself. Those are worker tools. If you find yourself about to
> call one — STOP and spawn a worker instead.

Enforcing this at the prompt level (rather than the tool layer) avoids
hard refusals for edge cases while still steering the model away from
the anti-pattern in the common case.

### Phase 27 — Claude Code-style diff rendering (v0.37.2)

`internal/tui/diffrender.go` renders unified diffs inline in the chat
pane after every file mutation. The rendering matches Claude Code's
style:

- Green (`+`) lines for additions, red (`-`) lines for deletions.
- Faint cyan `@@` hunk headers.
- Bold accent file path header.
- Capped at `diffMaxLines` (50) with a "N more lines" truncation notice.

`styledLines` applies the lipgloss style to each non-empty line
independently so the ANSI reset lands on the same line as its content —
this prevents colour bleed when the viewport clips or scrolls past a
styled line.

The diff is appended to the write/edit tool's return string so it flows
naturally into the chat pane alongside the tool result box.

### Phase 28 — control character sanitization (v0.37.3–v0.37.5)

Three related fixes landed in quick succession after providers started
rejecting requests with "invalid control character" 400 errors.

**Root cause.** Terminal tools (`run_bash`, `grep`, etc.) emit ANSI
escape sequences (`\x1b[32m`, etc.) and bare control characters
(`\x00`–`\x1F`) as part of their output. The session log stores these
bytes verbatim in JSONL. When the session is later replayed into a new
API request, `json.RawMessage(e.ToolArgs)` re-introduces the raw bytes
into the request body. Strict providers (Together.ai, Fireworks via
OpenRouter, Cerebras) reject even properly-escaped `` sequences
in JSON string values.

**v0.37.3: tool output layer.** Added `internal/tools/sanitize.go` with
`sanitizeOutput(s)`. Applied in `Registry.Execute` to every tool result
and error string before they enter the message history:

```go
func sanitizeOutput(s string) string {
    s = ansiRE.ReplaceAllString(s, "")
    // strip bare control chars (0x00–0x1F) except \n, \r, \t
    ...
}
```

Also removed the non-existent model `gpt-oss-120b` from Cerebras's
`RecommendedModels` (Cerebras only has `llama-3.3-70b` and
`llama3.1-8b`).

**v0.37.4: message serialization layer.** The tool-output fix wasn't
enough for history replay — tool call *arguments* in replayed messages
can also contain control bytes. Added `SanitizeText` (exported) to
`internal/llm/sanitize.go` and applied it at the provider boundary:

- `messageToOpenAI()` — all `Text`, `Content`, `ToolResult.Content`
  fields sanitized; `sanitizeArgs()` re-applied to replayed tool-call
  argument bytes.
- `messageToAnthropic()` — same, plus `sanitizeArgs()` applied *before*
  `json.Unmarshal` on replayed arguments (the Anthropic adapter
  unmarshals to `any`, so invalid JSON would panic rather than produce
  a bad string).

**v0.37.5: 404 as model-unsupported.** Cerebras returns HTTP 404 when
a model ID doesn't exist (rather than the more specific 422 other
providers use). The fallback chain's `isFallbackable` guard didn't
include 404, so `isModelUnsupported` was never reached and the error
propagated to the user unchanged. Fixed by adding `"404"` to both
`isFallbackable` and `isModelUnsupported`. Now a Cerebras 404 triggers
same-provider model rotation (tries `llama-3.3-70b`); if all static
candidates are exhausted, the chain advances to the next provider.

---

## Package layout

```
cmd/ageni/
  main.go         entry point: parse args, wire up adapters/manager/master/TUI, run
  init.go         first-run config wizard (huh)
  doctor.go       `ageni doctor` — environment / dep diagnostic
  update.go       `ageni update` — self-update from GitHub releases
  sessions.go     `ageni sessions list/show/resume/rm/dump/changes/diff`
  skills.go       `ageni skills install/list`

internal/agent/
  bus.go          Event types + many-to-many channel bus
  master.go       master loop, system prompt, active_context block
  subagent.go     worker loop, retries, budget wrap-up, classifyErr
  manager.go      sub-agent manager: spawn/get/list/cancel/setNextID
  tools.go        spawn/check/send/kill master-only orchestration tools
  find_tool.go    find_in_codebase Librarian tool

internal/config/
  config.go       parses ~/.ageni/.env, resolves provider+model

internal/llm/
  llm.go          Adapter interface + Message/ToolCall/Request types
  anthropic.go    Anthropic adapter with prompt-cache breakpoints
  openai.go       OpenAI-compatible adapter (used by OpenRouter/Groq/etc.)
  fallback.go     FallbackAdapter — ordered chain with per-provider model rotation
  sanitize.go     SanitizeText + sanitizeArgs — strip control chars before API send
  providers.go    provider registry + base URL/api-key env mapping
  fetch_models.go dynamic /v1/models autocomplete fetch
  pricing.go      paid + indicative pricing tables, savings tracking
  tracker.go      per-role token usage tracker

internal/mcp/
  mcp.go          Model Context Protocol client (subprocess transport)

internal/repomap/
  repomap.go      Aider-style repo map via universal-ctags + PageRank

internal/session/
  session.go      per-instance state container (Session struct)
  log.go          JSONL Bus event logger
  dump.go         human-readable transcript renderer
  changes_view.go `sessions changes` + `sessions diff` formatters
  replay.go       LoadHistory + PriorSubagentIDs + ResumeReminder
  picker.go       interactive session browser (huh select)

internal/skills/
  catalog.go      lazy skill catalog loader, read_skill tool

internal/tools/
  files.go        read_file / write_file / edit_file / list_dir
  fileops.go      make_dir / move_file / delete_file
  multiedit.go    multi_edit (atomic batch replacements)
  applydiff.go    apply_diff (search_replace + whole formats + miss diagnostics)
  glob.go         glob (recursive ** patterns)
  grep.go         grep via ripgrep --json
  shell.go        run_bash
  tests.go        run_tests
  lint.go         lintAfterEdit (gofmt, flake8 — appended to edit results)
  git.go          git_status / git_diff / git_log + ComputeDiff
  github.go       github (gh CLI passthrough)
  web.go          web_fetch / web_search
  cli.go          generic CLI tool helpers
  pkginfo.go      pkg_info (Go module info)
  todo.go         TodoWrite with claim/release
  corrections.go  RecordCorrection
  changes.go      ChangeTracker (snapshots + JSONL + checkpoints)
  sanitize.go     sanitizeOutput — strips ANSI/control chars from tool output
  registry.go     Tool / Registry / Subset / unknownToolMessage

internal/tui/
  app.go          Bubble Tea root model — chat, side pane, status bar
  settings.go     Ctrl+, settings form (huh)
  history.go      command history (~/.ageni/history.txt)
  atfile.go       @<path> mention expansion
  atcomplete.go   @<path> fuzzy autocomplete popup
  diffrender.go   Claude Code-style diff rendering for chat pane
  toolcall.go     tool-call + tool-result rendering helpers
  styles.go       lipgloss styles + color palette
```

---

## Key design patterns

### The Bus

Single producer / multi-consumer of `agent.Event`. Buffered channels
per subscriber; slow subscribers drop events. Used by:

- TUI (chat + side-pane rendering)
- Session logger (JSONL append)
- find_in_codebase (waits for its worker's terminal event)
- Master (receives sub-agent completions to trigger integration turns)

### Master loop pattern

```go
for turn := 0; turn < maxTurns; turn++ {
    refreshActiveContext()           // strip + rewrite tail block
    publish(EvMasterTurnStart)
    stream := adapter.Stream(req)
    for ev := range stream {         // text deltas + tool calls
        handle(ev)
    }
    if len(toolCalls) == 0 {
        publish(EvMasterTurnDone)
        return
    }
    for _, tc := range toolCalls {
        result := tools.Execute(ctx, tc)
        publish(EvMasterToolDone)
        appendToolResult(result)
    }
}
```

Sub-agent loop is similar but with a per-call budget and a "wrap-up
turn" mode (Tools=nil) when budget exhausted.

### Active context

Single self-replacing user-role tail block. Stripped and rewritten on
every turn so reminders don't accumulate. Contains:

- Recent corrections (most recent first)
- Current sub-agent statuses
- New events since the last turn (spawned, finished, errored)

### Tool registry split

Master and sub-agents have separate `Registry` instances. Sub-agents
get a base catalog (file ops, grep, glob, find_in_codebase, etc.).
Master adds the orchestration tools (spawn/check/send/kill,
record_correction). Whitelisting via `Subset(names)` further scopes
what a particular spawn can call.

### Session paths

Every persistent file lives under `~/.ageni/sessions/<id>/`. The
`Session.Path(name)` helper resolves names relative to that root.
Works for both fresh and resumed sessions; makes per-session
isolation trivial.

### Change tracking

`Tracker.Snapshot(abs)` is the first thing a mutation tool calls.
Idempotent — only snapshots once per path per session. The
`seen` map is restored from `changes.jsonl` on session open, so
resumed sessions keep their original baselines instead of
overwriting them with the post-edit content.

---

## Release pipeline

`.github/workflows/release.yml` runs on every `v*.*.*` tag push. The
matrix builds for:

- `darwin-amd64`, `darwin-arm64`
- `linux-amd64`, `linux-arm64`
- `windows-amd64`

Each runner does `go build -ldflags "-X main.version=$TAG"` and
uploads the binary as a release asset. The install script (`install.sh`,
served from the repo) detects the user's OS+arch and curls the right
binary.

`ageni update` reads the latest release tag from the GitHub API and
downloads the appropriate binary into `~/.ageni/bin/`, replacing the
running executable on next launch.

CI (`ci.yml`) runs on every push: `go vet`, `golangci-lint v2.12.1`
(see `.golangci.yml`), `go test ./...`. Lint config disables a handful
of false-positive rules (e.g. `G306` for the file perms tools
deliberately set, `G304` for paths derived from session dirs).

---

## Per-version changelog

Each entry is the *what* and *why*; the *how* is in the corresponding
phase above.

| Tag | Highlights |
|---|---|
| v0.1.0 | First release: skeleton + Anthropic adapter |
| v0.2.0 | Master / sub-agent loop + bus + manager |
| v0.3.x | OpenAI-compat adapters (OpenRouter, Groq, HF, OpenCode) |
| v0.4.x | Local provider support (Ollama, vLLM, llama.cpp) |
| v0.5.0 | First-run wizard + token-efficiency briefing |
| v0.6.0 | Per-provider model autocomplete (`/v1/models`) |
| v0.7.x | Cost estimator (paid pricing tables) |
| v0.8.x | OpenRouter dynamic price registration |
| v0.9.0 | Indicative paid-equivalent for free models |
| v0.9.2 | Markdown rendering fixed (force TrueColor profile) |
| v0.9.3 | Sub-agent context-cancellation fix (use rootCtx) |
| v0.9.4–7 | TUI polish: tool-call rendering, mouse toggle, scroll |
| v0.10.0 | Dynamic OpenRouter model fetching |
| v0.11.0 | Cache-savings tally in status bar |
| v0.12.0 | Sub-agent error classification + master prompt updates |
| v0.13.0 | Soft budget wrap-up turn (no more hard error on cap) |
| v0.13.1 | Default budget 25 → 40, AGENI_SUBAGENT_BUDGET env |
| v0.14.0 | Phase A: per-role telemetry + active_context block |
| v0.14.1–3 | Phase A polish |
| v0.15.0 | Aggressive parallel-delegation prompt + burst coalescing |
| v0.16.0 | Structured worker return + richer spawn Context fields |
| v0.17.0 | Session abstraction (multi-instance + resume foundation) |
| v0.18.0 | Work claiming + corrections log + sessions CLI |
| v0.19.0 | Silent cancel + find timeout + session dump + activity indicators + todos in TUI |
| v0.19.1 | Register find_in_codebase in sub-agent registry too |
| v0.20.0 | Live LLM-call indicators (inline + side pane + status bar) |
| v0.21.0 | Master prompt: own sub-agents, drive to completion |
| v0.22.0 | Track modified files + diff viewing |
| v0.23.0 | Full session resume — replay master history |
| v0.24.0 | Interactive session browser at startup |
| v0.25.0 | @path file references in user input |
| v0.25.1 | Warn master about dead sub-agents on resume |
| v0.26.0 | AGENTS.md project-instruction loader |
| v0.27.0 | apply_diff tool with search/replace blocks + miss diagnostics |
| v0.28.0 | Lead/worker adapter routing for master turns |
| v0.29.0 | Per-tool-call checkpoints + rewind_to_checkpoint tool |
| v0.30.0 | Auto-lint after file edits (gofmt, flake8) |
| v0.31.0 | Together.ai and OpenCode Zen as first-class providers |
| v0.32.0 | Provider fallback chains (FallbackAdapter + per-provider model rotation) |
| v0.33.0 | Settings redesign: single static scrollable page |
| v0.34.0 | Model ranking by quality tier + per-model price display in settings |
| v0.35.x | @-mention autocomplete: VSCode-style fuzzy file picker popup |
| v0.36.x | Fallback bug fixes: 413/context-length, 402 variants, stale cells, layout overflow, stderr bleed, malformed tool names |
| v0.37.0 | Per-provider model rotation + lazy live-model fetch via LiveModelFetcher |
| v0.37.1 | Plan→delegate enforcement in master system prompt |
| v0.37.2 | Claude Code-style diff rendering in chat pane after file mutations |
| v0.37.3 | Tool output ANSI/control char sanitization; remove non-existent Cerebras model |
| v0.37.4 | Message-level control char sanitization (SanitizeText) at provider boundary |
| v0.37.5 | 404 treated as model-unsupported in fallback chain; enables Cerebras rotation |

---

## Lessons learned

### What worked

- **Bus + structured events.** Every subscriber renders or persists
  the same canonical event stream. The TUI side pane animations, the
  session log, and `find_in_codebase`'s wait-for-completion all work
  the same way: subscribe, filter, react.
- **Separate master / sub-agent registries.** Refusing to give workers
  the orchestration tools eliminated whole classes of misbehaviour
  (workers spawning their own workers in loops).
- **Snapshot-on-first-touch for diffs.** Independent of git, robust
  to partial commits, gives perfect diffs even in scratch directories.
- **Per-change releases.** The user gets fixes immediately via
  `ageni update`. CI caught regressions early because every change
  went through the matrix build.

### What was rebuilt

- **Active context.** The original "accumulating reminders" pattern
  (each turn appended a fresh reminder to the conversation) bloated
  context fast. Replaced by a single self-replacing tail block.
- **Sub-agent budget on hard error.** Initially the worker errored
  out at the cap. Felt like a useless dead-end. Replaced with a
  wrap-up turn (Tools=nil) so the worker always produces a proper
  `<result>` block, even at the limit.
- **Sub-agent context lifetime.** Originally workers used the master's
  per-turn context. The instant the master's turn returned, every
  freshly-spawned worker died. Fixed by routing all spawns through
  `Manager.rootCtx`, which outlives any individual turn.
- **Master prompt orchestration rules.** Multiple rewrites. The current
  `<ownership_rules>` + `<orchestration_rules>` pair is the result of
  iterating on observed misbehaviour: serial spawning, asking the user
  about worker status, narrating orchestration in user-facing turns.
- **Sub-agent cancellation as error.** Cancellation is a *decision*,
  not a failure. The fix was to detect `context.Canceled` in
  `subagent.fail()` and emit an `EvSubagentDone` with empty text
  instead of an error event.

### Gotchas

- **Glamour + Bubble Tea alt-screen + termenv.** Auto-detect can fall
  back to no-tty profile *inside the alt-screen*, producing unstyled
  raw markdown. Force TrueColor explicitly.
- **Mouse capture vs text selection.** ANSI offers no split mode;
  it's all or nothing. Document Shift+drag (most modern terminals
  support it) and lean on PgUp/PgDn for keyboard scroll.
- **Session log append vs truncate.** `os.Create` truncates. On
  session resume, that destroys the prior log and breaks any future
  replay. Use `OpenFile(O_CREATE|O_APPEND)`.
- **Sub-agent ID collisions across resume.** A fresh manager starts
  the spawn counter at 0; the master's history has `s1`, `s2`, `s3`
  from before. Bump the counter past the max seen, plus emit a
  reminder so the master doesn't try to interact with dead IDs.
- **Bundling fix + feature in one release.** Don't. The patch tag
  silently disappears into the feature release. Commit + tag + push
  the fix chunk *before* starting the feature chunk, even within the
  same conversation.
- **Tool definition ordering.** Sort tool defs by name in
  `Registry.Definitions()`. The LLM's prompt-cache key includes the
  full prompt; an unstable ordering invalidates the cache on every
  turn.
- **find_in_codebase vocabulary leakage.** The master prompt promotes
  it heavily; that vocabulary leaks into spawn_subagent contexts;
  workers hallucinated calls to it. Fix: register it in the sub-agent
  base registry too. The Librarian itself excludes it from
  `AllowedTools`, so no recursion.
- **Control characters require a three-layer defence.** Sanitizing at
  the tool-output layer (v0.37.3) wasn't sufficient: the session log
  round-trips through JSON decode/encode on replay, re-introducing raw
  bytes into replayed tool-call arguments. The fix required sanitization
  at three independent layers: tool execution output, message-text
  serialization, and tool-argument bytes. The Anthropic adapter additionally
  needs `sanitizeArgs` applied *before* `json.Unmarshal` because it
  unmarshals arguments to `any` (unlike the OpenAI adapter which passes
  them as raw strings).
- **Provider error codes don't match expectations.** Cerebras returns
  404 for "model ID not found" rather than a 422 or provider-specific
  string. isFallbackable was exhaustively enumerated but 404 was
  missing. The lesson: any HTTP status code that a strict provider
  *ought* to use for a config error should be assumed reachable by
  *some* provider and added proactively, even if it sounds like it
  shouldn't mean "model not found".
- **Fallback gate order matters.** The model-rotation path in
  `tryFrom` sits inside `if isFallbackable(ev.Err)`, which is a
  *superset* check of `isModelUnsupported`. The inner
  `isModelUnsupported` check was correct; the outer `isFallbackable`
  guard was what blocked it. When debugging "why didn't the chain
  rotate?", start at the outermost guard, not the innermost logic.

---

## What's next

Stuff that's been brought up but not yet built:

- **Interactive diff view in the TUI.** Inline rendering in the chat
  pane (v0.37.2) shows diffs after each edit. A full-session viewer
  (file list left, unified diff right, j/k navigation) is the natural
  next step.
- **Copy-mode (tmux-style).** A keyboard-driven select+yank inside
  the alt-screen, so users don't have to wrestle with mouse capture
  vs terminal selection.
- **Stronger refusal of stale sub-agent IDs.** If the master still
  drifts to calling `check_subagent("s1")` on a dead ID after resume,
  the next lever is structural: have `check_subagent` return a much
  louder "WORKER WAS TERMINATED IN A PRIOR PROCESS" instead of the
  current `"no such sub-agent: s1"`.
- **Sandbox tiers.** Landlock (Linux) + Seatbelt (macOS) for `read-only`
  / `workspace-write` / `danger-full-access` modes on sub-agent tool
  calls.
- **Headless mode.** `ageni exec "<prompt>"` with structured JSON
  output for CI/PR-bot use.

If any of those become important, the build process above is the
template — small focused PR, semver bump, one tag per change.
