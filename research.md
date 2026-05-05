# Gap Analysis: ageni vs the OSS Coding-Agent Field (May 2026)

This report compares ageni against the prominent open-source coding agents
in active use as of May 2026, identifies concrete gaps, and recommends a
pivot strategy. Authored as a single research pass; sources cited inline.

---

## 1. claw-code: the named comparator

**What it is.** A Rust agent harness explicitly described as a "clean-room
rewrite of Claude Code architecture." Single binary, REPL-first, 9-crate
workspace (`api`, `commands`, `compat-harness`, `mock-anthropic-service`,
`plugins`, `runtime`, `rusty-claude-cli`, `telemetry`, `tools`). Tools
mirror Claude Code's set verbatim: Bash, ReadFile, WriteFile, EditFile,
GlobSearch, GrepSearch, WebSearch, WebFetch, Agent, TodoWrite,
NotebookEdit, Skill, ToolSearch.
([repo](https://github.com/ultraworkers/claw-code),
[deepwiki](https://deepwiki.com/ultraworkers/claw-code))

**What ageni already matches or beats.**

- Multi-provider, cross-platform single binary — *parity*.
- MCP, sub-agents, sessions, skills, find_in_codebase Librarian —
  claw-code's own roadmap admits it captures "20–25% of Claude Code's
  functional surface" and explicitly **lacks subagents, MCP, IDE bridges,
  the full prompt pipeline.** ageni is ahead on agent-loop sophistication.
- Repo map, prompt caching, structured worker output schema — claw-code
  has none of this.

**What claw-code does that ageni does not.**

1. **Compat-harness crate** — a tool that automatically extracts
   tool/prompt manifests from upstream Claude Code TypeScript source, so
   they can do parity diffing as Anthropic ships changes. This is a
   *process* asset, not a feature. Equivalent for ageni would be a
   `compat/` test fixture set sourced from real Claude Code traces.
2. **Mock-anthropic-service** — a deterministic local mock for testing
   the agent loop without burning tokens or hitting rate limits. This is
   a real engineering hole in ageni: every test today presumably hits a
   real provider.
3. **Plugin marketplace surface** (`/plugin`, `/marketplace`) — an
   explicit install/enable/disable lifecycle for community plugins,
   separate from MCP.
4. **NotebookEdit tool** — first-class Jupyter notebook editing
   (cell-aware), which `edit_file` cannot do correctly because notebooks
   are JSON, not text.
5. **Lane Events / typed event streams** — "deterministic state machines
   and machine-readable event streams" for external automation. ageni
   has log.jsonl but no documented schema.
6. **Slash commands as a category** — `/cost`, `/usage`, `/stats`,
   `/diff`, `/commit`, `/pr`, `/issue`, `/release-notes`,
   `/security-review`, `/review`, `/cron`, `/team`, `/hooks`. ageni has
   CLI subcommands; claw-code surfaces these inside the REPL.
   Friction-of-use difference.
7. **Default `danger-full-access` mode that's explicit** — every
   operation has a sandbox stance documented. ageni's default permission
   posture is implicit.

**Note on credibility.** claw-code was a viral project (172k stars in
~days) but the actual code is 20K LOC, the core differentiator (ACP/Zed
daemon) is unshipped, and core features (subagents, MCP) are missing.
**Don't pivot to mimic it strategically — its surface is impressive, its
substance lags ageni.**

---

## 2. The serious competitors

### Aider

[paul-gauthier/aider](https://github.com/paul-gauthier/aider) ·
[edit-formats](https://aider.chat/docs/more/edit-formats.html) ·
[leaderboard](https://aider.chat/docs/leaderboards/)

**Where Aider is genuinely best-in-class:**

- **Edit-format research is 2+ years deep.** Five formats — `whole`,
  `diff` (search/replace with git-conflict markers), `diff-fenced` (path
  inside fence; Gemini-tuned), `udiff` (designed for GPT-4 Turbo to
  suppress lazy "// rest unchanged" output), `editor-diff` /
  `editor-whole` for architect mode. Each format is per-model-tuned and
  the polyglot leaderboard (225 Exercism exercises across
  C++/Go/Java/JS/Python/Rust) is the de facto industry benchmark. GPT-5
  high tops at 88.0%.
- **Architect mode** — strong reasoning model plans, cheap editor model
  produces syntactically valid edits. Two-model loop, separated prompts.
- **Auto-lint and auto-test post-edit**, with the agent fixing detected
  errors in the same turn.
- **Voice-to-code** and **watch mode** (write `# ai do X` as a comment
  in your IDE; Aider acts).
- **Git-native commits** with generated messages per change.

**ageni gap:** ageni has `edit_file` + `multi_edit` but no documented
edit format, no per-model edit-format selection, no benchmark numbers.
This is the single biggest correctness gap.

### Cline

[cline/cline](https://github.com/cline/cline) ·
[plan-and-act](https://docs.cline.bot/features/plan-and-act) ·
[checkpoints](https://docs.cline.bot/features/checkpoints)

- **Plan mode vs Act mode.** Plan mode is read/search-only, no file
  writes, no command execution; Act mode unlocks them. Conversation
  context carries forward. The mode flag is enforced at the tool layer.
- **Checkpoint shadow git repo** — separate from project's git,
  snapshots after every tool use, three restore options (Files only /
  Task only / Both). Survives across the task; doesn't pollute user git
  history.
- **Computer Use / browser automation** with screenshot + console-log
  feedback.
- **MCP-driven dynamic tool creation** — user says "add a Jira tool,"
  Cline writes an MCP server.

**ageni gap:** No mode-locked planning phase. ageni's file change
tracker is similar in spirit to checkpoints but is single-snapshot per
file (first touch only); Cline snapshots **per tool use**, enabling
fine-grained rewind to any step.

### OpenHands

[All-Hands-AI/OpenHands](https://github.com/All-Hands-AI/OpenHands) ·
[runtime architecture](https://docs.openhands.dev/usage/architecture/runtime)

- **Docker-sandboxed action executor** with a clean Action/Observation
  loop. Bash, Browser, Jupyter, VS Code plugins all run inside the
  container.
- **CodeActAgent** — single-action-per-turn architecture proven on
  SWE-bench.
- **GitHub Action for autonomous PR fixing.**
- **Microagents** — small specialised prompts triggered by file path /
  repo content.
- **Condenser** — explicit memory compression strategy when context fills.

**ageni gap:** No sandbox at all (the spec says "no Docker" so this is a
deliberate concession). No condenser — when ageni's master fills,
behaviour is undefined.

### Plandex

[plandex-ai/plandex](https://github.com/plandex-ai/plandex)

- **Plan branches** — version-controlled forks of an agent plan; you can
  try GPT-5 and Claude on the same task in parallel branches and merge
  the winner.
- **Cumulative diff sandbox** — every edit stages into a holding area
  separate from project files; you review and apply en bloc, with rollback.
- **Configurable autonomy levels** — none / basic / plus / semi / full.
- **2M effective context** via tree-sitter chunking and smart loading.

**ageni gap:** No staging buffer (edits go straight to disk; F4 dump and
`sessions diff` are forensic, not interactive). No autonomy-level dial.

### Goose

[block/goose](https://github.com/block/goose)

- **Recipes** — YAML-described reusable agent workflows, parameterised,
  shareable. Now under Linux Foundation governance.
- **Lead-worker model split** — `GOOSE_LEAD_MODEL` for planning turns,
  smaller worker model for execution turns. Cost optimisation that's
  orthogonal to ageni's master-vs-subagent split.
- **Distro builder** — teams can ship branded `goose` with preconfigured
  providers/extensions/system prompts.

**ageni gap:** No recipe format. ageni has skills but skills are
read-only documentation; recipes are *executable parameterised plans*.

### OpenAI Codex CLI

[openai/codex](https://github.com/openai/codex)

- **Approval modes** in config: `untrusted` / `on-failure` /
  `on-request` / `never`.
- **Sandbox modes**: `read-only` / `workspace-write` /
  `danger-full-access` with `writable_roots` and `network_access` knobs.
- **Platform sandboxes**: macOS Seatbelt, Linux Landlock + seccomp,
  Windows policy. `codex sandbox <platform> [COMMAND]` lets users wrap
  any command in the same policy used internally.
- **AGENTS.md** — the emerging cross-vendor standard for project
  instructions ([agents.md](https://agents.md/)). 60k+ projects use it.
  Codex, Cursor, Amp, Jules, Factory all read it. Multi-level: nearest
  AGENTS.md to the file being edited wins.
- **`codex exec`** — non-interactive mode for piping prompts and CI use.
- **`codex mcp-server`** — Codex itself as an MCP server, so other
  agents can call into it.

**ageni gap:** No AGENTS.md support. No native sandbox (spec says no
Docker, but Landlock and Seatbelt require zero runtime — they're
syscalls). No headless `ageni exec "prompt"` mode. No reverse-MCP
(ageni-as-server).

### SWE-agent

[princeton-nlp/SWE-agent](https://github.com/princeton-nlp/SWE-agent)

- **Agent-Computer Interface (ACI)** philosophy: file viewer with line
  ranges, focused tools, single bash interface. The benchmark winner
  among open-source.
- **YAML-configurable tool stack** — every tool, every prompt, every
  loop parameter.
- **Trajectory recording** for retraining and replay.

**ageni gap:** Tool config is hardcoded in Go. No YAML override surface.

---

## 3. Capability gaps (concrete)

| Feature | Who has it | ageni has? | Severity |
|---|---|---|---|
| Search/replace edit format with model-specific tuning | Aider | No (raw `edit_file`) | **Critical** |
| Polyglot benchmark CI | Aider | No | **Critical** |
| Plan/Act mode separation (tool-layer enforced) | Cline, Plandex | No | High |
| Per-step checkpoints with file/task/both restore | Cline | Partial (first-touch only) | High |
| AGENTS.md project instruction loader | Codex, Cursor, Amp, Factory, Jules | No | **Critical** |
| Sandbox via Landlock/Seatbelt (no Docker needed) | Codex | No | High |
| `ageni exec` headless mode for CI | Codex, Aider, Goose | No | High |
| Architect/editor two-model split per edit | Aider | No (master/subagent is different) | Medium |
| Recipe format (parameterised reusable plans) | Goose | No (skills are read-only) | Medium |
| Edit staging / cumulative diff with apply gate | Plandex | No | Medium |
| Context condenser when master fills | OpenHands | No | High |
| Reverse MCP (ageni-as-server) | Codex, Goose | No | Medium |
| NotebookEdit tool | Codex, Claude Code, claw-code | No | Low |
| Plan branches (multi-model fork/merge) | Plandex | No | Low |
| Auto-lint + auto-fix loop after every edit | Aider | No (run_tests is manual) | High |
| Deterministic local mock provider for tests | claw-code | No | Medium |

---

## 4. Behavioural gaps (same feature, others do it better)

- **Edit reliability.** ageni's `edit_file` likely uses Anthropic-style
  str_replace. Aider's `diff` format with merge-conflict markers +
  retry-on-mismatch + per-model selection is better-tested across more
  models. The udiff insight (suppresses lazy "// rest unchanged") is the
  kind of finding ageni won't replicate without its own benchmark loop.
- **Sub-agent cost.** ageni spawns subagents in goroutines, master-driven.
  Goose's lead-worker is *automatic per turn* (planning → expensive
  model, execution → cheap model). ageni's tier-by-role telemetry exists
  but the routing decision is master-LLM judgement, not deterministic.
  Goose probably saves more tokens.
- **Repo map.** ageni uses ctags + PageRank ~2000 tokens. Aider does the
  same (and invented the technique). Plandex uses tree-sitter chunking
  for 2M effective context — different trade-off (no PageRank but full
  structural awareness when loaded). Worth comparing on real repos.
- **Project instructions.** ageni's `init` writes its own format.
  Codex/Cursor/Amp/Factory all converge on `AGENTS.md`. ageni is in the
  wrong format.

---

## 5. Strategic gaps

- **No public benchmark.** Aider has the polyglot leaderboard. SWE-agent
  has SWE-bench. OpenHands has its eval harness. ageni has no number.
  This is the single biggest credibility gap.
- **No IDE bridge.** Aider has watch mode, Cline/Continue are
  extensions, Codex has VS Code/Cursor/Windsurf integration, claw-code
  has roadmap'd ACP/Zed. ageni is TUI-only.
- **No CI mode.** No headless `ageni exec`, no GitHub Action. Limits use
  to interactive sessions.
- **Anthropic-centric prompt caching.** Other providers (OpenAI, Gemini,
  DeepSeek) have their own caching APIs ageni doesn't exploit yet.

---

## 6. Anti-patterns to avoid

- **Don't ship Computer Use / browser automation** unless you have a
  sandbox. Cline's browser tool runs in an Electron process; ageni
  without Docker would be running real Chrome on the user's machine with
  LLM-driven clicks. Correctness-vs-blast-radius is bad.
- **Don't copy claw-code's `danger-full-access` default.** It's why
  claw-code's "run any tool" looks impressive in demos but fails real
  workflows. Codex's tiered `read-only` / `workspace-write` /
  `danger-full-access` is the right model.
- **Don't build a plan-branch UI** like Plandex unless you commit to the
  staging-buffer model. Half-implemented branches confuse users.
- **Don't add 30 slash commands** like claw-code did. Most are unused;
  they bloat the help text and the prompt.
- **Don't replace your edit_file with udiff blindly.** Aider's data
  shows udiff helps GPT-4 Turbo but hurts Claude. Per-model selection is
  the *real* lesson.

---

## 7. Recommended next 5–10 features (ranked by impact)

1. **Adopt AGENTS.md as the project-instruction loader** (replace
   `ageni init`'s output). *Why:* Free interop with Codex, Cursor, Amp,
   Factory, Jules, GitHub Copilot. 60k+ repos already have one.
   Multi-level (nearest-AGENTS.md-wins) means ageni reads what you've
   already written for other tools. One-week feature, large compounding
   benefit.

2. **Build a per-model edit-format selector with at least `whole`,
   `diff` (search/replace with conflict markers), and `udiff`.** Tag
   each format per provider+model in a config map. Add a self-check:
   when a search/replace miss happens, retry once with a fuzzy-match
   prompt. *Why:* This is the correctness lever Aider has refined for
   two years. Closing this gap is non-negotiable for being taken
   seriously on edits.

3. **Add `ageni exec "<prompt>"` headless mode and a GitHub Action that
   wraps it.** Output structured JSON with edits + cost + exit code.
   *Why:* Unblocks CI use, PR-bot use, scripting. Codex and Aider both
   have it; ageni is locked to interactive only.

4. **Implement Landlock (Linux) + Seatbelt (macOS) sandbox tiers
   `read-only` / `workspace-write` / `danger-full-access`** with
   `writable_roots` and `network_access` config keys. Reuse Codex's
   vocabulary verbatim for muscle memory. *Why:* Real safety, no Docker,
   fits the single-binary constraint. Without this, "let it run" is
   irresponsible.

5. **Polyglot benchmark harness.** Fork Aider's 225-exercise polyglot
   suite, add `make bench`, publish a leaderboard for ageni against its
   supported providers. Run on every release tag. *Why:* You ship
   per-tag (memory note); add eval-per-tag and you have a credibility
   moat nobody else of your size has. Doubles as regression CI for your
   edit-format work.

6. **Lead/worker auto-routing with deterministic rules.** Tag each turn
   type (planning vs execution vs file-edit vs diagnosis) and route to a
   configured model per tag. `master_planning_model`,
   `master_execution_model`, `subagent_model`. *Why:* Goose proves this
   saves ~50% on tokens for equivalent quality. ageni's multi-provider
   story makes this a natural fit — it's a config feature, not a
   redesign.

7. **Mode-enforced Plan / Act / Auto split, gated at the tool layer.**
   In Plan, `write_file` / `edit_file` / `run_bash` return refusals.
   Master can flip with explicit user confirmation. *Why:* Cline's data
   shows users plan better when forced. Cheap to add, big behaviour win.

8. **Per-tool-call checkpoints (extend the existing change tracker).**
   Snapshot after every write/edit, not just first-touch.
   `ageni sessions checkpoint <id> <step>` to rewind workspace +
   conversation tail. *Why:* Cline's killer feature for risk-free
   experimentation. ageni already has the snapshot scaffolding; this is
   generalisation, not a rewrite.

9. **Recipes: parameterised, executable agent workflows in YAML.**
   `ageni run recipe.yaml --param target=foo`. Goose's recipe format is
   a fine starting point. *Why:* Skills are read-only docs; recipes are
   repeatable team knowledge. Distro story for ageni-in-a-team.

10. **Auto-lint + auto-fix-loop after every edit, scoped to the touched
    files.** Detect language, run linter (gofmt/eslint/ruff/clippy), if
    errors feed them back to the same model with a budget of 2 retries.
    *Why:* Aider proves this is the highest-yield correctness
    improvement after edit-format choice. Cheap to wire because
    `run_bash` + `run_tests` already exist.

---

## 8. Where ageni should concede

- **IDE integration.** Don't build an ACP/Zed daemon, don't build a VS
  Code extension. Continue/Cline/Codex own this; the surface area is
  huge. Stay TUI-first and let MCP be the bridge.
- **Browser/Computer Use.** Without a sandbox you cannot do this safely;
  with one, the engineering cost is huge. Punt.
- **Edit-format depth.** You will not catch up to two years of Aider
  research on the long tail of formats (diff-fenced for Gemini, udiff
  for Turbo, editor-* for architect mode). Ship the top three formats
  and call it done.
- **Cloud/team product.** OpenHands and Goose are going Linux Foundation
  / Slack/Jira/Linear integrations. That's a different business. Stay
  single-binary local.
- **Star-count games.** claw-code's 172k stars are a marketing artifact,
  not engineering signal. Compete on the benchmark and the per-tag
  release cadence.

---

## Sources

- [ultraworkers/claw-code](https://github.com/ultraworkers/claw-code) ·
  [PHILOSOPHY.md](https://github.com/ultraworkers/claw-code/blob/main/PHILOSOPHY.md) ·
  [rust/README.md](https://github.com/ultraworkers/claw-code/blob/main/rust/README.md) ·
  [deepwiki](https://deepwiki.com/ultraworkers/claw-code)
- [paul-gauthier/aider](https://github.com/paul-gauthier/aider) ·
  [edit-formats](https://aider.chat/docs/more/edit-formats.html) ·
  [leaderboards](https://aider.chat/docs/leaderboards/)
- [cline/cline](https://github.com/cline/cline) ·
  [plan-and-act](https://docs.cline.bot/features/plan-and-act) ·
  [checkpoints](https://docs.cline.bot/features/checkpoints)
- [All-Hands-AI/OpenHands](https://github.com/All-Hands-AI/OpenHands) ·
  [runtime architecture](https://docs.openhands.dev/usage/architecture/runtime)
- [plandex-ai/plandex](https://github.com/plandex-ai/plandex)
- [block/goose](https://github.com/block/goose)
- [openai/codex](https://github.com/openai/codex)
- [princeton-nlp/SWE-agent](https://github.com/princeton-nlp/SWE-agent)
- [agents.md](https://agents.md/) ·
  [agentsmd/agents.md](https://github.com/agentsmd/agents.md) ·
  [GitHub blog: how to write a great agents.md](https://github.blog/ai-and-ml/github-copilot/how-to-write-a-great-agents-md-lessons-from-over-2500-repositories/)
