---
name: context-compactor
description: This skill should be used when an agent's context window is growing large, a long-running task risks losing information to auto-compaction, or the user asks about context management. Provides techniques for proactive context compaction, filesystem offloading, pre-compaction checkpointing, and post-compaction recovery. Use proactively during multi-step tasks to prevent context degradation.
version: 1.0.0
---

# Context Compactor

**Portability:** Context limits and compaction behaviour vary by product; the **patterns** (offload to disk, checkpoint, recover) apply to any long-running coding agent.

Techniques for managing context window capacity during complex tasks. The goal is to keep the agent effective across long sessions by offloading information to the filesystem, compacting proactively before auto-compaction fires, and recovering cleanly when compaction occurs.

## Core Principle

> **Context window = RAM. Filesystem = Disk. Git = Archive.**
>
> Anything the agent might need later should be on disk, not solely in the conversation.

Most agent failures in long sessions are **context failures**, not model failures. Better context management beats better prompting.

---

## When to Apply This Skill

- Task will take **more than 20 tool calls**
- Conversation has been running for a while and you notice **repeated file reads** or **lost details**
- You are about to start a **distinct new phase** of work within the same session
- Auto-compaction has fired and you need to **recover state**
- The user asks to compact, clear, or manage context

---

## The Three-Tier Memory Model

| Tier | Where | Access Speed | Survives Compaction | Capacity |
|------|-------|-------------|--------------------|---------|
| **Hot** | Context window | Instant | No | Limited (100-200K tokens) |
| **Warm** | Filesystem (`.plan/`, `NOTES.md`, `AGENTS.md` / `CLAUDE.md`) | 1 tool call | Yes | Unlimited |
| **Cold** | Git history, archived logs | Search required | Yes | Unlimited |

### What Belongs in Each Tier

**Hot (keep in context):**
- Current task objective and constraints
- Active reasoning chain
- Last 5-7 conversation turns
- File paths currently being modified
- Unresolved errors being debugged

**Warm (write to files, read on demand):**
- Completed research findings
- Architectural decisions and rationale
- Progress logs and completed steps
- Full tool outputs from earlier operations
- Plan files and checklists

**Cold (commit to git, search when needed):**
- Previous session logs
- Completed task artifacts
- Historical file states
- Earlier conversation context

---

## Proactive Compaction Techniques

### 1. Offload Large Tool Output

When a tool returns a large result, save it to a file and keep only a summary in context.

**Pattern:**
```
1. Run a command or search that returns extensive output
2. Write the full output to a scratch file (e.g., .plan/search_results.txt)
3. Keep only a summary in your response:
   "Found 47 matches across 12 files. Full results in .plan/search_results.txt.
    Top hits: src/auth.ts:42, src/middleware.ts:18, src/api/routes.ts:93"
4. Read specific sections from the file when needed later
```

**Threshold rule:** If a tool result exceeds ~100 lines, offload it.

### 2. Write Findings Immediately

Don't hold research results in context hoping to use them later. Write them to `findings.md` or `NOTES.md` immediately after discovery.

**The 2-action rule:** After every 2 file reads, searches, or web lookups, write what you learned to a findings file. This prevents knowledge loss if compaction occurs.

### 3. Summarize Completed Work

After completing a phase or significant step, write a summary to `progress.md` and let the detailed reasoning fall out of context naturally.

**Pattern:**
```
After completing Phase 1:
1. Write to progress.md: what was done, files modified, decisions made
2. Mark phase complete in task_plan.md
3. The detailed reasoning from Phase 1 can now safely be compacted
```

### 4. Compact Before Switching Tasks

Before starting a different type of work within the same session, manually save state:

```
1. Write current status to progress.md
2. Commit any pending changes
3. Run your product’s compact/summarize command or clear unnecessary context
4. Read progress.md to reorient for the new task
```

### 5. Avoid Re-reading Unchanged Files

If you've already read a file and it hasn't been modified, don't read it again. Instead, reference what you learned from it. If you need specific details, read only the relevant line range.

### 6. Use Targeted Reads

```
BAD:  Read the entire 500-line file to find one function
GOOD: Grep for the function name, then read only lines 45-80
```

---

## Pre-Compaction Checkpoint

Before compaction occurs (or before running a manual compact/summarize), write a checkpoint file that captures everything needed to resume:

### Checkpoint Template

Write to `.plan/checkpoint.md` or `NOTES.md`:

```markdown
# Context Checkpoint — [Timestamp]

## Current Task
[What you're doing and why]

## Completed Steps
- [x] Step 1: [description] → modified `path/to/file.ts`
- [x] Step 2: [description] → modified `path/to/other.ts`

## In Progress
- [ ] Step 3: [description] — currently working on [specific aspect]

## Key Decisions Made
- Chose approach A over B because [reason]
- Using [library/pattern] for [purpose]

## Active Errors / Blockers
- [Error description and current debugging state]

## Files Modified This Session
- `src/auth.ts` — added JWT validation middleware
- `src/routes/api.ts` — integrated auth middleware
- `tests/auth.test.ts` — added test cases

## Important Context
- [Any domain knowledge, user preferences, or constraints that aren't in persistent files]

## Next Steps
1. [Immediate next action]
2. [Following action]
```

---

## Post-Compaction Recovery

When resuming after compaction (auto or manual), follow this recovery sequence:

```
1. Read checkpoint file (.plan/checkpoint.md or NOTES.md)
2. Read task plan (.plan/task_plan.md) for overall progress
3. Read recent git log (git log --oneline -10) for what was committed
4. Read any findings file (.plan/findings.md) for decisions and context
5. Continue from "Next Steps" in the checkpoint
```

### What Gets Lost in Compaction

| Lost | Impact | Mitigation |
|------|--------|-----------|
| Detailed reasoning chains | Agent may re-explore settled decisions | Write decisions to findings.md |
| Specific tool outputs | Agent re-reads files unnecessarily | Offload to scratch files |
| Exact error messages | Debugging state reset | Write errors to progress.md |
| Nuanced user preferences | Agent reverts to defaults | Capture in project instruction files or checkpoint |
| Loaded skill content | Skills need re-evaluation | Auto-restore via hooks if available |

### What Survives Compaction

- Files on disk (everything in `.plan/`, `NOTES.md`, code files)
- Project instruction files such as `AGENTS.md` or `CLAUDE.md` (when the product re-injects them)
- Git state (commits, branches, working tree)
- The compacted summary itself (a condensed version of the conversation)

---

## Context-Efficient Patterns

### Writing

- **Lead with the answer**, not the reasoning. Skip preamble and transitions.
- **Don't repeat the user's question** back to them in your response.
- **Don't add trailing summaries** of what you just did — the tool output and code speak for themselves.
- **Use structured formats** (bullet points, tables) over prose — models parse them more efficiently.

### Reading Code

- **Read line ranges**, not entire files, when you know where to look.
- **Search the codebase first** (e.g. ripgrep, IDE search, or your agent’s search) to locate what you need, then read the specific section.
- **Read function signatures and types** before reading implementations.
- **Never read the same unchanged file twice** in a session.

### Tool Usage

- **Use `--query` / `--jq` filters** on CLI tools to limit output at the source.
- **Pipe to `head`** or use `--max-items` for commands that return large lists.
- **Prefer incremental edits over whole-file rewrites** — smaller diffs cost less context.
- **Don't run commands you don't need** — every tool call adds to context.

### Planning

- **Write plans to files**, not to the conversation.
- **Use the planning-with-files skill** for tasks over 3 steps — it provides filesystem-backed state that survives compaction.
- **Reference file paths** in your reasoning rather than quoting file contents.

---

## Anti-Patterns That Waste Context

| Anti-Pattern | Token Cost | Fix |
|-------------|-----------|-----|
| Reading entire files to find one function | 2,000-4,000 tokens | Search first, then read the range |
| Verbose explanations after simple edits | 200-500 tokens/occurrence | State what you did in one line |
| Re-reading unchanged files | 2,000-4,000 tokens each | Reference what you already know |
| Dumping raw API/command output into context | 1,000-50,000 tokens | Offload to file, keep summary |
| Repeating the task description in every response | 100-300 tokens/occurrence | Say it once, reference it after |
| Running unnecessary intermediate commands | 500-2,000 tokens each | Only run what you need |
| Exploring broadly before focusing | Variable, often 10,000+ | Plan first, explore targeted paths |
| Indexing entire trees (no ignore rules) | 10,000+ tokens from irrelevant files | Use ignore/exclude patterns (e.g. `.gitignore`, product-specific ignore files) for build output, `node_modules`, lock files |

---

## Compaction Strategies by Situation

### Routine Long Task
```
Proactive: Write findings to files every 2 research actions
           Checkpoint before each new phase
           trigger context reduction between distinct phases (e.g. your product’s “compact” or “summarize” command)
```

### Debugging Session
```
Proactive: Log each hypothesis and result to NOTES.md
           When a hypothesis is disproven, note it and move on
           Don't keep failed approaches in context
Recovery:  Read NOTES.md to see what was already tried
```

### Multi-File Refactoring
```
Proactive: Write the refactoring plan to a file
           Work one file at a time, commit after each
           Drop completed files from mental context
Recovery:  git log shows completed files
           Plan file shows remaining files
```

### Research Then Implement
```
Proactive: Write ALL research to findings.md immediately
           compact or summarize after research, before implementation
           Read findings.md to start implementation with clean context
```

---

## Platform-Specific Notes

Names and shortcuts differ by product; map the **ideas** (manual summarize, auto-trim, rules files, plan directories) to your assistant.

### Claude Code
- Slash commands such as **`/compact`** / **`/clear`** trigger summarization or full reset; behaviour and thresholds are product-specific.
- Auto-compaction near context limits may fire without manual action — checkpoint to disk first.
- Hooks and settings files can re-inject critical context after compaction.
- **`CLAUDE.md`** is often re-loaded after compaction; keep durable rules there or in **`AGENTS.md`**.

### Cursor
- Large tool outputs may be summarized or truncated automatically depending on settings.
- MCP tool descriptions may load on demand.
- Project rules under **`.cursor/rules/`** persist across sessions alongside repo docs.
- Shorter files are easier for agents to work with.

### OpenCode
- Context condensation may replace older events with summaries while preserving early task context.
- **`.opencode/plans/`** can hold durable plan state.

### Pi
- Use **`AGENTS.md`** for persistent project instructions.
- Write session state to files; skills load from disk across resets.

### Gemini
- Put durable instructions in system/developer prompts (e.g. “read `NOTES.md` before proceeding”).
- Context limits vary by model — monitor usage.

### Other assistants
- Prefer **filesystem-backed** notes and plans over long chat-only state; use whatever “summarize session” or “new chat” workflow your tool provides.

---

## Quick Reference

```
PROACTIVE (during work):
  Every 2 research actions → write to findings file
  After each phase         → write to progress file, commit
  Before task switch       → write checkpoint, then compact/summarize if available
  Large tool output        → offload to file, keep summary
  File already read        → don't re-read, reference what you know

PRE-COMPACTION (before manual compact/summarize or when context is ~70% full):
  Write checkpoint.md with: task, progress, decisions, errors, next steps
  Commit pending work
  Run your product’s compact/summarize command (with a topic/focus if supported)

POST-COMPACTION (after compaction fires):
  Read checkpoint.md → task_plan.md → progress.md → findings.md
  Check git log for recent commits
  Continue from checkpoint's "Next Steps"

BETWEEN TASKS:
  Full session reset (e.g. /clear or new chat) — start fresh with a clean brief
  Better than compacting across unrelated work
```
