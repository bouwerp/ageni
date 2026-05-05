# Dangerous ops

**Read when:** about to run something that could lose work or rewrite shared history. Reference for the common footguns and their safer alternatives.

## Force-push

```bash
git push --force                         # NEVER without explicit consent
git push --force-with-lease              # SAFER: refuses if remote has moved
git push --force-with-lease --force-if-includes   # git ≥ 2.30: also refuses if remote has commits not in your reflog
```

**Rules:**

1. **Never push to `main` / `master` / `release/*` with force.** Refuse and recommend `git revert` instead.
2. Never force-push without the user explicitly asking.
3. When you do, use `--force-with-lease`. Tell the user so collaborators on the branch can resync (`git fetch && git reset --hard origin/<branch>`).
4. If the local reflog is fresh (you just fetched), add `--force-if-includes` for an additional safety check.

Recovery from an accidental force-push: [recovery.md](recovery.md).

## `git reset --hard`

```bash
git reset --hard <sha>
```

Discards the working tree and moves HEAD. Uncommitted work **not in a stash or commit** is gone. Committed work is still in the reflog for ~90 days.

**Before running:**

```bash
git status                          # confirm working tree state
git branch backup/$(git branch --show-current)-$(date +%s)
```

**If the commits you're about to leave behind are pushed:** don't. Use `git revert` on a shared branch; use reset only on your own feature branch, and plan the follow-up `--force-with-lease`.

## `git clean`

Removes untracked files. The default flags matter:

```bash
git clean                       # refuses without -f / -i
git clean -n                    # DRY RUN — lists what would be deleted
git clean -f                    # delete untracked files
git clean -fd                   # also delete untracked directories
git clean -fx                   # ALSO delete ignored files (e.g. .env, node_modules)
git clean -fX                   # delete ONLY ignored files
git clean -fi                   # interactive
```

**Footgun:** `-x` (lowercase) eats ignored files. On a typical project that includes `.env` files with secrets and `node_modules/` (often 100+MB to rebuild). **Never use `-x` without the user's explicit consent**, and always dry-run first:

```bash
git clean -ndx          # preview
```

## `git checkout -- <path>` / `git restore <path>`

```bash
git checkout -- <path>          # discard unstaged changes in <path>
git restore <path>              # same thing (modern form)
git checkout -- .               # DISCARD ALL UNSTAGED CHANGES
git restore .                   # same
```

Both silently clobber unstaged work. There's no reflog for working-tree state.

**Before running `checkout .` or `restore .`:** stash instead.

```bash
git stash push -m "safety before restore"
# if you really wanted to discard, drop the stash afterwards
```

## `git branch -D`

```bash
git branch -d <branch>          # SAFE: refuses if unmerged
git branch -D <branch>          # FORCE: deletes even if commits aren't on another branch
```

`-D` on a branch whose commits aren't reachable from any other ref orphans them. They're in the reflog for ~90 days — but don't rely on that.

**Before escalating to `-D`:**

```bash
# Is the branch's tip reachable from anywhere else?
git branch --contains $(git rev-parse <branch>)
git branch -r --contains $(git rev-parse <branch>)
```

If neither command lists another branch, **stop** — either merge/rebase those commits somewhere first, or push the branch to a remote archive: `git push origin <branch>:wip/<branch>-archive`.

## `git commit --amend`

```bash
git commit --amend              # edit the most recent commit's message/content
```

Safe if the commit hasn't been pushed. Unsafe on a shared branch — the next push becomes a force-push, and the old commit's sha vanishes from everyone's local clone.

**Check before amending:** `git branch -r --contains HEAD`. If any shared branch appears, don't amend — make a fixup commit and squash it locally if you must, or just add a new commit.

## `git rebase` on pushed history

Same as amend, scaled up. Rebasing commits that exist on a shared branch rewrites them; any collaborator with those commits local will have their next `git pull` do confusing things (or outright fail).

**Check:** `git branch -r --contains <oldest-sha-you-want-to-rebase>`. If it's on a shared branch, use `git revert` instead.

## `git filter-branch` / `git filter-repo`

Whole-history rewrites (remove a file from every commit, change all emails, etc.). Effectively a force-push on everything.

Anything more than a toy repo: stop. Announce the plan, get explicit authorization, clone a backup, schedule the rewrite, notify all collaborators that they'll need to re-clone. `git filter-repo` (external tool) is strictly preferred over the built-in `filter-branch`, which is deprecated.

## Submodule footguns

```bash
# Update submodules to what the superproject records
git submodule update --init --recursive

# Update submodules to their own upstream HEAD (CHANGES THE SUPERPROJECT)
git submodule update --remote
# ^ this moves the submodule pointer; you must commit the superproject:
git add <submodule-path>
git commit -m "bump <submodule> to <sha>"
```

**Common foot-shot:** developer runs `submodule update --remote`, tests locally, commits the superproject — but forgets to push the *submodule's* new commits. Collaborators pull the superproject, the recorded sha doesn't exist in the submodule's remote, everything breaks.

Always push the submodule first, then the superproject.

## `git reflog expire` / `git gc --prune=now`

```bash
git reflog expire --expire=now --all         # DELETES the safety net
git gc --prune=now --aggressive              # reaps unreachable objects immediately
```

**Do not** run either to "clean up" unless you have a specific disk-space reason and the repo is newly in a known-good state. After a lost-commit incident, these are the commands that make recovery impossible.

## Detached HEAD

```bash
# You checked out a commit, not a branch
git status
# → "HEAD detached at <sha>"
```

Commits made in detached HEAD aren't attached to any branch. If you `git switch main` without first creating a branch, those commits are only reachable via the reflog.

**Fix:** before committing in detached HEAD, create a branch:

```bash
git switch -c work/<description>
```

If you already made commits and switched away:

```bash
git reflog                # find the last detached-HEAD commit
git branch recovered <sha>
```

## Summary

| Operation | Required check before running |
|---|---|
| `push --force*` to shared branch | Don't. Use `git revert`. |
| `push --force-with-lease` | User consent + inform collaborators |
| `reset --hard` | `git status` clean OR backup branch created |
| `clean -f[d]` | `git clean -n[d]` dry run first |
| `clean -fx` | Explicit user consent (eats `.env`, `node_modules`) |
| `checkout -- .` / `restore .` | Stash first |
| `branch -D` | `--contains` check confirms commits live elsewhere |
| `commit --amend` on HEAD | `branch -r --contains HEAD` is empty |
| `rebase <base>` | `branch -r --contains <oldest-target-sha>` is empty |
| `filter-repo` | User authorization + backup clone + collaborator notice |
| `submodule update --remote` + commit | Submodule commits pushed first |
