---
name: git
description: Git craft beyond commit-and-push — branching, interactive rebase, squash/fixup/reorder, resolving merge or rebase conflicts, bisecting a regression, recovering lost commits via reflog, reset vs revert decisions, cherry-picking across branches, stashing, worktrees for parallel branches, log and blame archaeology. Triggers "rebase", "squash commits", "fix history", "recover deleted branch", "bisect", "cherry-pick", "resolve conflicts", "undo last commit", "worktree", "what changed this line". Distinct from git-pushing (only the commit-and-push happy path with conventional messages) and github (PR creation, review threads, CI, merge — the platform, not git itself).
version: 1.0.0
---

# Git craft

**Portability:** Works with any `git` install; no host-specific tooling. For push to a remote and conventional commits, use `git-pushing`. For PR lifecycle and CI, use `github`.

Per-operation reference lives in `topics/<name>.md` — read on demand when you actually need to run the commands.

## Core safety rules

Read these before running anything destructive. Violating them is how work gets lost.

1. **Never push to `main` / `master` / `trunk` / `release/*` directly unless the user has explicitly asked for it.** Push to a feature branch and open a PR (see `github` skill). If the user asks to "push", the default target is a branch named for the work, not the default branch.
2. **Never force-push without the user's explicit consent.** When consent is given, use `--force-with-lease` (or `--force-if-includes` on git ≥ 2.30), never bare `--force`.
3. **Never rewrite history that's been pushed to a shared branch.** Amend, rebase, squash, reset-then-push are all history rewrites. Before any of them, run the [published check](#is-this-commit-published) below.
4. **Never skip hooks** (`--no-verify`, `--no-gpg-sign`) unless the user has explicitly asked.
5. **Back up before risky ops.** Before rebase, reset --hard, filter-branch, or anything that mutates refs, create a backup: `git branch backup/<desc>-$(date +%s)`.
6. **Dry-run when available.** `git clean -n`, `git push --dry-run`, `git rm --dry-run`, `git rebase -i` (the editor is itself a preview).
7. **When uncertain, stop and ask.** Git's safety net (reflog, unreachable objects) is generous but not unlimited; recovery costs time. Asking is cheaper.

## Command safety classification

| Category | Examples | Behaviour |
|---|---|---|
| **Safe (read-only)** | `status`, `log`, `diff`, `show`, `branch`, `blame`, `reflog`, `stash list`, `cat-file` | Run freely |
| **Mostly safe (local, reversible)** | `add`, `commit` (on a branch), `stash push`, `branch <new>`, `switch`, `restore --staged`, `tag <name>` | Run; describe what you did |
| **Dangerous (mutates working tree or local refs)** | `reset`, `checkout -- <path>` / `restore <path>`, `stash pop`, `rebase`, `merge`, `cherry-pick`, `clean`, `branch -D` | Check working-tree state, back up if needed, confirm before running |
| **Very dangerous (rewrites / publishes history)** | `push --force*`, `rebase` of pushed commits, `commit --amend` of pushed commits, `reset --hard` of pushed commits, `filter-branch`, `reflog expire --expire=now --all` | Require explicit user authorization every time |

## Is this commit published?

This is the single decision that gates every history-rewrite operation.

```bash
# Does this commit live on any remote branch?
git branch -r --contains <sha>
```

- **No output** → commit is local-only; history rewrite is safe.
- **Lists remote branches** → commit is published. If any of them is a shared/integration branch (`main`, `develop`, `release/*`), treat as **very dangerous** — `git revert` instead, or stop and ask.
- **Lists only your own feature branch** → rewrite is usually fine, but the next push will need `--force-with-lease`. Tell the user before doing it.

---

## Topic reference

Load the matching file when you actually need the commands.

| Task | File |
|---|---|
| Rebase (interactive, autosquash, splitting commits) | [topics/rebase.md](topics/rebase.md) |
| Conflicts (rebase, merge, cherry-pick, stash-pop) | [topics/conflicts.md](topics/conflicts.md) |
| Recovery (reflog, lost commits, aborted rebase) | [topics/recovery.md](topics/recovery.md) |
| Bisect (finding the regression commit) | [topics/bisect.md](topics/bisect.md) |
| Worktrees (parallel branches without stashing) | [topics/worktrees.md](topics/worktrees.md) |
| History archaeology (blame, pickaxe, log --follow) | [topics/history-archaeology.md](topics/history-archaeology.md) |
| Dangerous ops (force-push, reset --hard, clean -fd, submodules) | [topics/dangerous-ops.md](topics/dangerous-ops.md) |

Each file follows the same shape: when to reach for it, the minimal command set, common variants, and the specific footguns.

---

## Interactive rebase (quick orientation)

The single most-requested operation. Details in [topics/rebase.md](topics/rebase.md).

```bash
# Rebase the last N commits onto HEAD
git rebase -i HEAD~N

# Rebase your feature branch onto the latest main (without pulling main down)
git fetch origin
git rebase -i origin/main
```

Actions inside the rebase todo:
- `pick` — keep as-is
- `reword` — keep commit, rewrite message
- `edit` — pause after applying so you can amend
- `squash` — fold into previous, combine messages
- `fixup` — fold into previous, discard this message (pairs with `--autosquash`)
- `drop` — remove the commit
- `exec` — run a shell command (e.g. `exec cargo test`)

**Safe rebase workflow:** `git branch backup/$(git branch --show-current)-$(date +%s)` → `git rebase -i …` → resolve conflicts ([topics/conflicts.md](topics/conflicts.md)) → `git rebase --continue`. If anything goes wrong: `git rebase --abort`.

**Do not rebase** commits that have been pushed to a shared branch — see [published check](#is-this-commit-published).

---

## Reset vs. revert (decision table)

The most common footgun-producing confusion. Pick based on two questions.

| Is the bad commit pushed? | Do you want to keep history? | Use |
|---|---|---|
| No | Doesn't matter | `git reset --hard <good-sha>` (back up first) |
| No | Yes, preserve a visible "undo" | `git revert <bad-sha>` |
| Yes (shared branch) | — | `git revert <bad-sha>` — **always**. `reset --hard` + force-push rewrites what others pulled. |
| Yes (your own feature branch only) | Doesn't matter, keep it clean | `git reset --hard <good-sha>` then `git push --force-with-lease` after telling the user |

```bash
# Soft reset — keep changes staged, move HEAD
git reset --soft <sha>

# Mixed reset (default) — keep changes in working tree, unstage them, move HEAD
git reset <sha>

# Hard reset — discard working tree and move HEAD (BACK UP FIRST)
git reset --hard <sha>

# Revert — create a new commit that undoes <sha>
git revert <sha>

# Revert a merge commit (requires --mainline)
git revert -m 1 <merge-sha>
```

**Recovery** from an unintentional `reset --hard` is in [topics/recovery.md](topics/recovery.md) — the reflog has your back if you act within ~90 days.

---

## Undo the last commit

| Situation | Command |
|---|---|
| Haven't pushed yet, want to edit the commit message | `git commit --amend` |
| Haven't pushed yet, want to add a file to the commit | stage it → `git commit --amend --no-edit` |
| Haven't pushed yet, want to un-commit but keep changes staged | `git reset --soft HEAD~1` |
| Haven't pushed yet, want to throw the commit away entirely | `git reset --hard HEAD~1` (back up first) |
| Already pushed | `git revert HEAD` → push the revert commit. Do NOT amend + force-push on a shared branch. |

---

## Cherry-pick

Apply a specific commit from elsewhere to the current branch. Useful for hotfixes and backports.

```bash
# Apply one commit
git cherry-pick <sha>

# Apply a range (exclusive start, inclusive end)
git cherry-pick <start-sha>..<end-sha>

# Apply without committing — lets you amend / squash first
git cherry-pick -n <sha>

# Apply preserving original author and committer info
git cherry-pick -x <sha>   # adds "(cherry picked from commit …)" to the message
```

Conflicts resolve the same way as rebase conflicts — see [topics/conflicts.md](topics/conflicts.md). Abort with `git cherry-pick --abort`.

**Common pitfall:** cherry-picking a merge commit requires `-m 1` to pick the mainline parent's diff.

---

## Stash

Park uncommitted work temporarily.

```bash
# Stash everything (tracked files only) with a message
git stash push -m "wip: refactoring auth"

# Include untracked files
git stash push -u -m "…"

# Include ignored files too (rare — think .env)
git stash push -a -m "…"

# Stash only some paths
git stash push -m "…" -- path/to/file

# List, inspect, reapply
git stash list
git stash show -p stash@{0}
git stash pop           # apply and drop
git stash apply         # apply and keep
git stash branch <new-branch> stash@{0}   # create a branch from the stash
```

**Footgun:** `git stash drop` and `git stash clear` remove stashes. They aren't protected by the reflog the same way commits are — once dropped, recovery is hard. Prefer `git stash apply` + `git stash drop` only after you've confirmed the changes are back.

**Conflict on `stash pop`:** the stash stays in the list until you explicitly drop it. Resolve conflicts, stage, then `git stash drop stash@{0}` — don't leave stale stashes.

---

## Force-push etiquette

Force-pushing rewrites the remote ref. On a shared branch it tells everyone else "my history replaces yours" — any of their work built on the old tip is orphaned.

**Rules:**

1. Never force-push without the user's explicit consent.
2. Never force-push to `main` / `master` / `release/*` — refuse and ask for another approach (usually `git revert`).
3. When force-pushing your own feature branch, use `--force-with-lease`:

```bash
# Safe: refuses if the remote has moved since your last fetch
git push --force-with-lease

# Even safer (git ≥ 2.30): refuses if any commit on the remote isn't in your local reflog
git push --force-with-lease --force-if-includes
```

4. Tell the user *before* force-pushing — collaborators on the branch need to `git fetch && git reset --hard origin/<branch>` to resync.

Further detail and recovery in [topics/dangerous-ops.md](topics/dangerous-ops.md) and [topics/recovery.md](topics/recovery.md).

---

## Branching (quick orientation)

```bash
# Create and switch in one step
git switch -c feature/foo

# Switch to an existing branch
git switch main

# Rename the current branch
git branch -m new-name

# Delete a merged branch (refuses if unmerged)
git branch -d feature/foo

# Force-delete (only after confirming commits live elsewhere)
git branch -D feature/foo

# List branches with their last commit
git branch -v

# What branches contain <sha>?
git branch --contains <sha>      # local
git branch -r --contains <sha>   # remote
```

**Footgun:** `git branch -D` on a branch whose commits aren't reachable elsewhere permanently loses them (well, reflog for ~90 days — but don't rely on that). Try `-d` first; if it refuses, verify the commits are on another branch before escalating to `-D`.

For parallel branches without stashing, see [topics/worktrees.md](topics/worktrees.md).

---

## `git pull` is two operations

`git pull` = `git fetch` + (`git merge` or `git rebase`, depending on config). The default varies between machines and can surprise.

**Prefer explicit:**

```bash
git fetch origin
git merge origin/<branch>    # integrate via merge commit
# or
git rebase origin/<branch>   # replay your local commits on top
```

Don't assume the user's `pull.rebase` setting. When in doubt, fetch + rebase for local feature branches, fetch + merge for integration branches.

---

## Bisect (quick orientation)

Find the commit that introduced a regression by binary search. Details in [topics/bisect.md](topics/bisect.md).

```bash
git bisect start
git bisect bad                # current commit is broken
git bisect good <sha>         # this older commit worked
# git checks out the midpoint — test it, then:
git bisect good   # or: git bisect bad
# ... repeat until bisect reports the first bad commit
git bisect reset              # return to where you started
```

**Automated:** write a test script that exits 0 (good) or non-zero (bad), then `git bisect run ./test.sh`. Exit 125 to tell bisect to skip an untestable commit.

---

## Conflicts (quick orientation)

Conflicts appear identically in rebase, merge, cherry-pick, and `stash pop`. Full handling in [topics/conflicts.md](topics/conflicts.md).

1. `git status` — lists the conflicted paths.
2. Open each file; resolve the `<<<<<<<` / `=======` / `>>>>>>>` markers by hand. Remove the markers.
3. `git add <path>` for each resolved file.
4. Continue the operation: `git rebase --continue` / `git merge --continue` / `git cherry-pick --continue`. For stash: `git stash drop` after verifying.
5. To abort instead: `git rebase --abort` / `git merge --abort` / `git cherry-pick --abort`. (No `stash --abort` — instead, revert files and don't drop the stash.)

---

## History archaeology (quick orientation)

Finding *when* or *why* something changed. Full details in [topics/history-archaeology.md](topics/history-archaeology.md).

```bash
# Who wrote this line, ignoring whitespace-only and moved-code changes?
git blame -w -M -C <file>

# Which commit introduced/removed a string?
git log -S"functionName"                # pickaxe: changed count of occurrences
git log -G"regex pattern"               # pickaxe: diff matches regex

# History of a file across renames
git log --follow -p <file>

# Commits touching a path
git log -- path/to/file

# What did commit <sha> change?
git show <sha>

# What's on origin/main that I haven't merged?
git log ..origin/main
```

---

## Worktrees (brief)

Parallel branches without stashing. One repo, many working directories. Full details in [topics/worktrees.md](topics/worktrees.md).

```bash
# Create a worktree for a new branch off main
git worktree add ../myrepo-fix-123 -b fix/issue-123 origin/main

# List worktrees
git worktree list

# Remove when done (branch stays)
git worktree remove ../myrepo-fix-123
```

---

## Quick reference — safe exploration

These are read-only; run them freely.

```bash
git status -s                                  # short working-tree state
git log --oneline --graph --decorate --all -20 # visual history
git log --oneline ..origin/HEAD                # unmerged-upstream commits
git diff                                       # unstaged changes
git diff --cached                              # staged changes
git diff <branch1>..<branch2>                  # compare two branches
git reflog                                     # HEAD move history (safety net)
git stash list                                 # parked work
git branch -vv                                 # local branches + their upstreams
git remote -v                                  # remote URLs
git config --get-regexp '^(user|core|pull|push)\.'  # relevant config
```
