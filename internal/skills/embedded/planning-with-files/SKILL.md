---
name: planning-with-files
description: This skill should be used when an agent needs to plan, track, and execute multi-step tasks using persistent markdown files. Use when the task requires more than 3 steps, spans multiple files, or could benefit from structured planning before implementation. Provides the file-based planning methodology for creating task plans, tracking findings, and logging progress.
version: 1.0.0
---

# Planning with Files

**Portability:** File-based plans work the same for OpenCode, Claude Code, Gemini, Cursor, Pi, or any agent that can read and write project files. Product-specific shortcuts (todos, plan UI) are optional add-ons.

A structured methodology for planning and executing complex tasks using persistent markdown files. The filesystem is your extended memory — anything important gets written to disk rather than relying on volatile context.

## Core Principle

> **Context window = RAM (volatile, limited). Filesystem = Disk (persistent, unlimited).**

Write plans, findings, and progress to files so work survives context resets, session boundaries, and long-running tasks.

## When to Use This Skill

- Task requires **3 or more distinct steps**
- Work spans **multiple files or components**
- Task involves **research before implementation**
- Work may **span multiple sessions** or context resets
- Task is complex enough to benefit from **human review before execution**
- You need to **resume interrupted work**

## When NOT to Use This Skill

- Single-file, obvious changes (rename a variable, fix a typo)
- Tasks completable in 1-2 steps
- Pure Q&A or explanation requests

---

## The Three-File System

All planning files live in a `plans/` or `.plans/` directory at the project root. Check for `plans/` first, then `.plans/` if `plans/` doesn't exist. Create the directory at the start of every planned task.

When on a git worktree branch, use the branch name as the plan filename (without the `.md` extension) for all three files.

Example directory structures:
```
# Standard setup:
plans/                # or .plans/ if plans/ doesn't exist
├── task_plan.md
├── findings.md
└── progress.md

# Worktree branch setup (e.g., branch "feature/auth"):
plans/                # or .plans/ if plans/ doesn't exist
├── feature-auth_task_plan.md
├── feature-auth_findings.md
└── feature-auth_progress.md
```

### Why Three Files

| File | Purpose | Analogy |
|------|---------|---------|
| `task_plan.md` | Roadmap and checklist | The blueprint |
| `findings.md` | Research and decisions | The lab notebook |
| `progress.md` | Execution log | The build journal |

Keeping them separate prevents any single file from becoming unwieldy and lets each serve a focused purpose.

---

## File 1: task_plan.md

The plan is the primary artifact. It defines what needs to happen, in what order, and tracks completion.

### Template

```markdown
# Task Plan: [Short Title]

## Objective
[1-2 sentences: what does success look like?]

## Constraints
- [Time, scope, technology, or style constraints]
- [Files/areas that must NOT be modified]

## Phases

### Phase 1: [Name]
- [ ] Task 1.1: [Description] → `path/to/file.ts`
- [ ] Task 1.2: [Description] → `path/to/other.ts`

### Phase 2: [Name]
- [ ] Task 2.1: [Description]
  - [ ] 2.1a: [Subtask]
  - [ ] 2.1b: [Subtask]
- [ ] Task 2.2: [Description]

### Phase 3: Verification
- [ ] Run test suite
- [ ] Verify against acceptance criteria
- [ ] Clean up .plan/ directory

## Dependencies
- Task 2.1 depends on Task 1.2
- Phase 3 cannot start until Phase 2 is complete

## Acceptance Criteria
- [ ] [Criterion 1]
- [ ] [Criterion 2]
- [ ] All tests pass
```

### Rules for task_plan.md

1. **Create before writing any code.** The plan is the first file you create.
2. **One task at a time.** Only one task should be in progress. Complete it or note it as blocked before moving on.
3. **Mark tasks immediately.** Check off each task (`[x]`) as soon as it is done — never batch.
4. **Include file paths.** Every task that modifies a file should reference the target path.
5. **Keep tasks atomic.** Each task should be completable in a single focused action. If a task is too large, break it into subtasks.
6. **Dependency order.** Tasks within a phase should be listed in the order they must be executed. Cross-phase dependencies go in the Dependencies section.
7. **Re-read before acting.** Before starting any task, re-read `task_plan.md` from disk to ensure you have the current state.

### Status Symbols

Use standard markdown checkboxes for tool compatibility:

```
- [ ]  Not started
- [x]  Completed
- [-]  Blocked (add reason in findings.md)
```

For phases, use a header prefix:

```
### Phase 1: Setup [DONE]
### Phase 2: Implementation [IN PROGRESS]
### Phase 3: Verification [PENDING]
```

---

## File 2: findings.md

A chronological log of everything learned during research and implementation. This is your persistent memory for decisions, discoveries, and error patterns.

### Template

```markdown
# Findings

## [Date/Timestamp] — [Topic]

**Context:** [Why you looked into this]

**Finding:** [What you discovered]

**Decision:** [What you decided to do and why]

**Impact:** [How this affects the plan]

---

## [Date/Timestamp] — [Topic]

...
```

### Rules for findings.md

1. **Write after every 2 research actions.** After every 2 file reads, searches, or web lookups, record what you found. This prevents knowledge loss if context resets.
2. **Record decisions with rationale.** Don't just record what you decided — record why. Future sessions (or other agents) need the reasoning.
3. **Log errors and their resolutions.** When something fails, document the error, the root cause, and the fix. This prevents repeating the same mistakes.
4. **Note rejected alternatives.** If you considered approach A but chose approach B, document why A was rejected. This prevents future re-evaluation of dead ends.
5. **Include code snippets.** When you discover a pattern, API signature, or configuration format, paste the relevant snippet directly into findings.

### Example Entry

```markdown
## 2026-03-27 — Auth Middleware Architecture

**Context:** Evaluating how to add JWT validation to the API routes.

**Finding:** The existing middleware chain in `src/middleware/index.ts` uses
a compose pattern. Auth must run before rate-limiting because rate limits
are per-user.

**Decision:** Insert auth middleware at position 0 in the chain. Use the
existing `verifyToken()` from `src/lib/jwt.ts` rather than a new library.

**Impact:** No new dependencies. Phase 1 Task 1.2 can reuse existing code.
Updating task estimate from 30min to 15min.
```

---

## File 3: progress.md

A session-oriented execution log. Records what was done, what succeeded, and what failed. This is the file that enables session recovery.

### Template

```markdown
# Progress

## Session: [Date/Time]

### Actions
1. [Action taken] → [Result]
2. [Action taken] → [Result]

### Files Modified
- `path/to/file.ts` — [What changed]
- `path/to/test.ts` — [What changed]

### Test Results
- [Test suite]: [PASS/FAIL] — [Details if failed]

### Errors Encountered
- [Error description] → [Resolution or workaround]

### Status at End of Session
- Phase 1: DONE
- Phase 2: IN PROGRESS (Task 2.3 next)
- Phase 3: PENDING

---

## Session: [Earlier Date/Time]

...
```

### Rules for progress.md

1. **Log every significant action.** File edits, test runs, git commits, commands executed.
2. **Record test results explicitly.** Include pass/fail status after every test run.
3. **Track files modified.** List every file changed in the session with a brief description.
4. **End-of-session status.** Always write a status summary at the end of each session showing phase completion and the next task to pick up.
5. **Errors get full context.** Include the error message, the file/line if applicable, and how it was resolved.

---

## Workflow

### Starting a New Task

```
1. Check for `plans/` directory, if not exists check for `.plans/`, if not exists create `plans/`
2. Determine the plan prefix: if on a git worktree branch, use the branch name (with slashes replaced by hyphens) followed by an underscore, otherwise use empty string
3. Write `{prefix}task_plan.md` with phases and tasks
4. Write `{prefix}findings.md` header
5. Write `{prefix}progress.md` header
6. STOP — Present the plan for review before proceeding
```

**Critical:** Do not write any code until the plan exists and has been reviewed. Planning before acting prevents assumption-driven bugs and wasted effort.

### Executing the Plan

```
For each task in task_plan.md:
  1. Re-read task_plan.md from disk
  2. Identify the next unchecked task
  3. Execute the task (edit code, run command, etc.)
  4. Mark the task as [x] in task_plan.md
  5. Log the action in progress.md
  6. If you learned something new → update findings.md
  7. If the plan needs to change → update task_plan.md, note the change in findings.md
```

### Handling Plan Changes (Replanning)

Plans are living documents. When new information requires changes:

1. **Don't silently change the plan.** Document what changed and why in `findings.md`.
2. **Add tasks, don't remove them.** If a task becomes unnecessary, mark it `[x] SKIPPED — [reason]` rather than deleting it. This preserves the decision trail.
3. **Note scope changes.** If the task is growing significantly, flag it in `progress.md` and consider splitting into a child plan.

### Session Recovery

When resuming work after a context reset or new session:

```
1. Check for `plans/` directory, if not exists check for `.plans/`
2. Determine the plan prefix: if on a git worktree branch, use the branch name (with slashes replaced by hyphens) followed by an underscore, otherwise use empty string
3. Read `{prefix}task_plan.md` — understand the overall plan and what's done
4. Read `{prefix}progress.md` — understand the last session's state and next task
5. Read `{prefix}findings.md` — recover decisions and context
6. Continue from the next unchecked task
```

This is the primary value of file-based planning: the agent can fully recover its working state from disk.

### Completing the Task

```
1. All tasks in task_plan.md are checked
2. All acceptance criteria are met
3. Final entry written to progress.md with completion summary
4. Present results to the user
5. Ask whether to keep or remove .plan/ directory
```

---

## Scaling: Hierarchical Plans

For large tasks (more than ~20 tasks or 3 phases), use child plans:

```
.plan/
├── task_plan.md              # Parent plan — high-level phases only
├── findings.md
├── progress.md
├── auth-plan.md              # Child plan for Phase 1
└── dashboard-plan.md         # Child plan for Phase 2
```

The parent plan references child plans:

```markdown
### Phase 1: Authentication
See detailed plan: [auth-plan.md](auth-plan.md)
- [ ] All auth tasks complete (see child plan)
```

Each child plan follows the same template as `task_plan.md`.

---

## Git Integration

Use git commits as checkpoints throughout execution:

1. **Commit after each phase** — not after each task. This keeps the history meaningful.
2. **Reference the plan in commit messages** — e.g., "Phase 1 complete: auth middleware (see .plan/task_plan.md)"
3. **Commit the plan files themselves** if the project tracks them, or add `.plan/` to `.gitignore` if they're ephemeral working state.

---

## Platform-Specific Notes

The **file-based planning workflow** below is universal: any agent can read and update `plans/` or `.plans/` on disk. The bullets are optional integrations with each supported product — use what exists in your environment.

### Claude Code
- Built-in task/todo tools work for in-session tracking; use `plans/` or `.plans/` for **durable** state across compaction.
- Hooks can re-read the task plan before edits if you configure them in product settings.

### Cursor
- Plan mode and workspace plans complement on-disk `plans/` — persist multi-step work to the task plan file when sessions may reset.

### OpenCode
- Plan output may also live under `.opencode/plans/`. Use `plans/` or `.plans/` for task-specific execution tracking alongside any global plan files.

### Pi
- Reference `plans/` or `.plans/` from `AGENTS.md` so in-progress work survives context changes; extensions can automate plan I/O if available.

### Gemini
- Give explicit instructions to read/update the task plan file before code changes when using Gemini CLI or API workflows.

---

## Anti-Patterns to Avoid

| Anti-Pattern | Why It Fails | Do This Instead |
|-------------|-------------|-----------------|
| Planning in context only | Lost on context reset | Write to `.plan/` files |
| One giant plan file | Becomes unwieldy, hard to parse | Use the three-file system |
| Updating the whole plan at once | Obscures what changed | Update one task at a time |
| Skipping the plan for "quick" tasks | Scope creep, missed steps | If 3+ steps, plan first |
| Never replanning | Plan becomes fiction | Update the plan as you learn |
| Deleting completed tasks | Loses decision trail | Mark `[x]`, never delete |
| Planning without file paths | Vague, unactionable tasks | Every code task names its file |
| Starting code before plan review | Wasted work if plan is wrong | Present plan, wait for approval |

---

## Quick Reference

```
START:
  1. Check for `plans/` directory, if not exists check for `.plans/`, if not exists create `plans/`
  2. Determine the plan prefix: if on a git worktree branch, use the branch name (with slashes replaced by hyphens) followed by an underscore, otherwise use empty string
  3. Write `{prefix}task_plan.md` with phases and tasks
  4. Write `{prefix}findings.md` header
  5. Write `{prefix}progress.md` header
  6. STOP — Present the plan for review before proceeding

EXECUTE:
  1. Check for `plans/` directory, if not exists check for `.plans/`
  2. Determine the plan prefix: if on a git worktree branch, use the branch name (with slashes replaced by hyphens) followed by an underscore, otherwise use empty string
  3. Re-read `{prefix}task_plan.md` from disk
  4. Identify the next unchecked task
  5. Execute the task (edit code, run command, etc.)
  6. Mark the task as [x] in `{prefix}task_plan.md`
  7. Log the action in `{prefix}progress.md`
  8. If you learned something new → update `{prefix}findings.md`
  9. Repeat

REPLAN:
  1. Document what changed and why in `{prefix}findings.md`
  2. Update `{prefix}task_plan.md`
  3. Continue

RECOVER:
  1. Check for `plans/` directory, if not exists check for `.plans/`
  2. Determine the plan prefix: if on a git worktree branch, use the branch name (with slashes replaced by hyphens) followed by an underscore, otherwise use empty string
  3. Read `{prefix}task_plan.md` — understand the overall plan and what's done
  4. Read `{prefix}progress.md` — understand the last session's state and next task
  5. Read `{prefix}findings.md` — recover decisions and context
  6. Continue from the next unchecked task

FINISH:
  1. All tasks in `{prefix}task_plan.md` are checked
  2. All acceptance criteria are met
  3. Final entry written to `{prefix}progress.md` with completion summary
  4. Present results to the user
  5. Ask whether to keep or remove the plans directory
```
