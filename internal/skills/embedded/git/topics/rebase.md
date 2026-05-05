# Rebase

**Read when:** rewording/squashing/reordering/dropping commits, replaying your branch on top of a fresher base, splitting a commit, or fixing up a specific commit with `--autosquash`.

**Never rebase** commits that have been pushed to a shared branch. Check first: `git branch -r --contains <sha>`.

## Setup — always back up first

```bash
git branch backup/$(git branch --show-current)-$(date +%s)
```

If anything goes sideways, `git reset --hard backup/<name>` restores you. Delete the backup branch once the rebase is done and pushed.

## Interactive rebase

```bash
# Edit the last N commits
git rebase -i HEAD~N

# Rebase onto a different base (e.g. latest main without merging it)
git fetch origin
git rebase -i origin/main
```

The todo list opens in your editor. Edit it top-down (oldest → newest). Commands:

| Command | Shortcut | What it does |
|---|---|---|
| `pick` | `p` | Keep the commit as-is |
| `reword` | `r` | Keep the commit, open editor for a new message |
| `edit` | `e` | Stop after applying; run `git commit --amend` or further edits, then `git rebase --continue` |
| `squash` | `s` | Fold into previous commit; open editor to combine messages |
| `fixup` | `f` | Fold into previous commit; **discard** this commit's message |
| `drop` | `d` | Remove the commit entirely |
| `exec` | `x` | Run a shell command after applying (e.g. `exec cargo test`) |
| `break` | `b` | Pause the rebase here (useful mid-sequence) |

Reordering lines in the todo reorders the commits. Deleting a line is equivalent to `drop`.

## Autosquash — the tidy workflow

When you spot a fix for an earlier commit, make a fixup commit against it:

```bash
# Make changes, then:
git commit --fixup <sha-to-fix>
# Or a squash (lets you edit the combined message):
git commit --squash <sha-to-fix>
```

Later, rebase with `--autosquash` and git pre-orders the todo for you:

```bash
git rebase -i --autosquash HEAD~10
```

Set `git config --global rebase.autosquash true` to make it the default.

## Splitting a commit

Use `edit` to pause on the target commit, then undo it while keeping changes:

```bash
git rebase -i HEAD~N     # mark the commit to split as 'edit'
# rebase pauses after applying that commit; HEAD is at it
git reset HEAD^           # move HEAD back one, keep changes unstaged
git add -p                # stage piece-by-piece
git commit -m "first piece"
git add -p && git commit -m "second piece"
git rebase --continue
```

## Rebasing onto a different base (e.g. switching parents)

```bash
# Replay commits that are on feat but not on old-base, onto new-base
git rebase --onto new-base old-base feat
```

Useful when you branched from the wrong point.

## Conflicts during rebase

Each commit is replayed one at a time. If one conflicts:

1. `git status` — see conflicted paths.
2. Resolve (see [conflicts.md](conflicts.md)).
3. `git add <paths>`.
4. `git rebase --continue` — if the diff was empty after resolution, git may prompt to skip or commit anyway.
5. `git rebase --skip` — if this commit's changes are now redundant.
6. `git rebase --abort` — return to the state before `rebase` started.

## Post-rebase push

If the rebased branch was previously pushed:

```bash
git push --force-with-lease
```

Never bare `--force`. Inform the user so collaborators on the branch can resync (`git fetch && git reset --hard origin/<branch>`).

## Gotchas

- **`--autosquash` respects only `fixup!` / `squash!` prefixes** — which `git commit --fixup` / `--squash` create automatically. Manual messages don't trigger it.
- **Rebase preserves authorship, not committer date.** `git log --format=fuller` shows both. If you rely on commit date for anything, it'll change.
- **Empty commits after resolution:** when a resolved conflict yields no diff, git asks whether to keep the commit. Usually `--skip` is correct.
- **`git pull --rebase` mixes fetch + rebase.** Prefer explicit `git fetch && git rebase origin/<branch>` so the base is visible.

## When NOT to rebase

- The commits are already on a shared branch. Use `git revert` instead.
- You're the only one who ever rebases and your collaborators merge — the workflow gets noisy. Match the team's convention.
- The commit graph is being used as a release record (some orgs treat every merge as a checkpoint). Squashing erases that.
