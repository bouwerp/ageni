# Worktrees

**Read when:** you need to work on a second branch without disturbing the current working tree (no stash, no commit, no switch). Lets one repo serve multiple checkouts simultaneously.

## Create

```bash
# New branch off a base, in a sibling directory
git worktree add ../myrepo-fix-123 -b fix/issue-123 origin/main

# Existing branch, in a sibling directory
git worktree add ../myrepo-review review-branch

# Detached HEAD at a specific commit (for read-only inspection)
git worktree add --detach ../myrepo-at-abc1234 abc1234
```

**Where to put them:** a sibling directory (`../myrepo-<branch>`) is safest. Putting them inside the main repo works but risks them being tracked or matched by globs. If the project has a `.worktrees/` convention, follow it.

## List / inspect

```bash
git worktree list
git worktree list --porcelain     # machine-parseable
```

Each worktree shares the same `.git/` object store — only the index and working tree are separate. Branches across worktrees share their reflog.

## Clean up

```bash
# Remove when done (deletes the directory; the branch stays)
git worktree remove ../myrepo-fix-123

# If the directory was deleted manually, prune stale entries
git worktree prune
```

**A worktree cannot check out a branch that's already checked out in another worktree.** Git enforces this to prevent the same branch being advanced from two places.

## When worktrees beat stashing

- You need to run tests on the main branch while holding an in-progress change on a feature branch.
- You're reviewing a PR (fetch + worktree the PR branch) without touching your own work.
- You want to bisect without losing your current state.
- You need two branches compiled simultaneously (different build artefacts).

## When stashing is simpler

- Single, short-lived context switch.
- The other branch doesn't need its own build output.
- Disk space is tight.

## Gotchas

- **Submodules** require `git submodule update --init --recursive` inside each worktree.
- **Hooks are shared** (they live in `.git/hooks`), so hook authors should not assume a single working directory.
- **`.git/` is a file**, not a directory, inside a secondary worktree — it's a `gitdir: /path/to/main/.git/worktrees/<name>` pointer. Tools that stat `.git/` need to cope.
- **Don't commit the worktree directory into the main repo.** If the worktree path is *inside* the main checkout, add it to `.gitignore`.
- **Force-delete only when the branch has been merged or backed up.** `git worktree remove --force` can leave uncommitted work orphaned.

## Alternative reference

`obra/superpowers` ships a dedicated [`using-git-worktrees`](https://github.com/obra/superpowers/blob/main/skills/using-git-worktrees/SKILL.md) skill with a stricter decision flow (directory-selection ritual, safety check via `git check-ignore`) that's worth reading if worktrees become a habit.
