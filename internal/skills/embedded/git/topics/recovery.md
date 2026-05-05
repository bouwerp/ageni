# Recovery

**Read when:** commits seem lost after a `reset --hard`, a bad rebase, an accidental branch delete, or a force-push.

The reflog is the safety net. HEAD moves — every commit, checkout, reset, rebase, pull — are recorded for ~90 days by default. Most "lost" commits are still reachable through it.

## Inspect what happened

```bash
# HEAD's movement history (most recent first)
git reflog                        # or: git reflog HEAD

# A specific branch's movement
git reflog show <branch>

# Reflog entries include an age column; limit to last N minutes:
git reflog --since='10 minutes ago'
```

Entries look like `abc1234 HEAD@{3}: reset: moving to HEAD~2`. Find the entry before the destructive op.

## Recover after `reset --hard`

```bash
# Find HEAD's position before the reset
git reflog
# e.g. "HEAD@{1}: commit: fix login redirect"

# Recover by moving HEAD back
git reset --hard HEAD@{1}

# ...or cherry-pick specific lost commits onto a safety branch
git switch -c recovered
git cherry-pick <sha1> <sha2>
```

## Recover after a bad rebase / merge / cherry-pick

Reflog records each rebase step as its own entry. Pre-rebase state appears as something like `HEAD@{7}: rebase (start): checkout …`.

```bash
git reflog                         # find the entry labelled "rebase (start)" or similar
git reset --hard HEAD@{<N>}        # where <N> is the entry just before the op
```

**Better:** make the `backup/<name>-<timestamp>` branch a habit before rebase. Recovery becomes `git reset --hard backup/<name>`.

## Recover a deleted branch

Deleted-branch tip is in `git reflog <branch>` *if* the branch still has a reflog. If not, it's in `HEAD`'s reflog (from when you were last on it).

```bash
# Find the last commit of the deleted branch
git reflog --all | grep <branch-name>

# Recreate the branch at that commit
git branch <branch-name> <sha>
```

Or search the loose-object pool (slower but thorough):

```bash
git fsck --no-reflogs --lost-found | grep commit
# Inspect each dangling commit:
git show <sha>
# Recover:
git branch recovered-<name> <sha>
```

## Recover after a force-push

**On the pushing machine**, the old tip is still in the local reflog. Recreate it and force-push (with user consent):

```bash
git reflog                        # find the pre-force-push sha
git branch recovered <sha>
git push origin recovered         # or force the branch back after agreement
```

**On a collaborator's machine**, their local branch still has the old history. They can push it back:

```bash
# On collaborator's clone, still holding the old tip as HEAD:
git push --force-with-lease origin HEAD:<branch>
```

Coordinate — if both sides try to "fix" it independently, you'll replay the force-push problem.

## Recover "unreachable" commits (fsck)

When the reflog has been expired or pruned, loose objects survive for `gc.pruneExpire` (default 2 weeks).

```bash
git fsck --lost-found
# Dangling commits go to .git/lost-found/commit/
ls .git/lost-found/commit/
# Inspect and recover:
git show $(cat .git/lost-found/commit/<sha>)
git branch rescued <sha>
```

## Recover stashes

Stashes are refs under `refs/stash`. A *dropped* stash has no reflog entry for that ref, but its blob may still be reachable via fsck.

```bash
# Find dangling commits that look like stashes (2 or 3 parents)
git fsck --unreachable | awk '/commit/ {print $3}' \
  | xargs -I{} git log -n 1 --pretty='%H %s' {} \
  | grep -i 'WIP\|stash'
# Recover one:
git stash apply <sha>
```

## Emergency footgun list

- **Do not** run `git reflog expire --expire=now --all` to "clean up." That nukes the safety net.
- **Do not** `git gc --prune=now --aggressive` immediately after a loss — it can reap the commits you're trying to recover. Wait, or raise `gc.pruneExpire`.
- **Do not** `rm -rf .git/` when things feel broken. The repo is almost always recoverable with reflog/fsck. Clone a fresh copy from the remote into a sibling dir and compare — don't destroy what's there.

## Prevention

- Make `git branch backup/<name>-$(date +%s)` before any rebase, reset --hard, filter-branch, or interactive history surgery.
- Set `gc.reflogExpire` and `gc.reflogExpireUnreachable` to larger values on long-running working copies: `git config gc.reflogExpireUnreachable 90.days` (default is 30 days for unreachable).
- Push work-in-progress to a `wip/<name>` remote branch if the work matters — even if you'll never merge it.
