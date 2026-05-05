# History archaeology

**Read when:** finding *who*, *when*, or *why* a line, file, or behaviour changed. The "digging through history" toolkit.

## Blame — who last touched this line

```bash
# Basic
git blame <file>

# Ignore whitespace-only changes (huge noise reducer)
git blame -w <file>

# Detect moved lines within the same file
git blame -w -M <file>

# Detect lines copied from other files
git blame -w -M -C <file>

# Even more aggressive — look across entire history for copies
git blame -w -M -C -C -C <file>

# Limit to a range of lines
git blame -L 100,150 <file>

# Blame at a specific revision (not current HEAD)
git blame <sha> -- <file>
```

**Follow through "meaningless" commits:** pipe blame through `git log --reverse` on the shown sha to see the *real* introducing commit when a file was moved:

```bash
# If blame points at "rename commit", trace back:
git log --follow --diff-filter=A -- <file>
```

**Ignore a commit in blame** (e.g. a "format everything" commit polluting every line):

```bash
# One-off
git blame --ignore-rev <sha> <file>

# Permanently (repo-wide):
git config blame.ignoreRevsFile .git-blame-ignore-revs
# then list the commits in that file, one sha per line.
```

Both GitHub and `git blame` honour `.git-blame-ignore-revs`.

## Pickaxe — find commits that changed a string

```bash
# "S": commits where the count of occurrences of <string> changed
git log -S"functionName"

# Limit to a file
git log -S"functionName" -- path/to/file

# "G": commits where the diff matches a regex (broader — matches any hunk containing the pattern)
git log -G"regex .* pattern"

# Show the diff along with the commit
git log -S"functionName" -p
```

**When to use which:** `-S` for "when did this token first appear / last disappear?", `-G` for "where did any line mentioning this pattern change?". `-S` is the more precise tool.

## Log patterns

```bash
# File history across renames
git log --follow -p <file>

# Commits touching a path
git log -- path/to/file

# Only merge commits (integration history)
git log --merges --first-parent

# Only non-merge commits (actual work)
git log --no-merges

# Commits in A but not in B
git log B..A                     # ancestor exclusive
git log B...A                    # symmetric difference (both sides)

# Commits by author
git log --author='pieter' --since='2 weeks ago'

# Commits whose subject matches
git log --grep='auth' -i

# Show the graph
git log --oneline --graph --decorate --all

# Branches that contain a commit
git branch --contains <sha>
git branch -r --contains <sha>  # remote
```

## Show — inspect a single commit

```bash
git show <sha>                   # message + full diff
git show <sha> --stat            # summary of files changed
git show <sha>:path/to/file      # file at that revision
git show <sha>^                  # parent commit
git show <sha>^^                 # grandparent
git show HEAD@{yesterday}        # HEAD as of yesterday (from reflog)
```

## Diff across time

```bash
git diff <sha1> <sha2>                          # two revisions
git diff <sha1>..<sha2>                         # same thing, range syntax
git diff <sha1>...<sha2>                        # merge-base to sha2
git diff HEAD~5 -- path/to/file                 # scoped to a path
git diff --stat HEAD origin/main                # what's different from upstream
```

## Find when a file was introduced or removed

```bash
# First commit touching a path
git log --reverse --diff-filter=A -- <path>

# Commit that deleted a path
git log --diff-filter=D -- <path>

# All renames for a path
git log --follow --name-status -- <path>
```

## Tag archaeology

```bash
git tag --contains <sha>               # which tags contain this commit?
git describe <sha>                     # nearest tag + offset
git describe --tags --abbrev=0 <sha>   # just the nearest tag name
```

## Blame a stash

```bash
git stash list                         # get the stash ref
git log --all stash@{0}                # treat the stash as a ref
git show stash@{0}                     # full diff of the stash
```

## Gotchas

- **`git log -p <file>`** without `--follow` stops at the point the file was renamed. Always `--follow` for historical digs across renames.
- **`git blame`** blames the last commit that *touched* the line — formatting-only commits poison the output. `-w -M -C` plus `.git-blame-ignore-revs` are non-optional for a real archaeology task.
- **`git show <sha>` on a merge commit** shows the "combined diff", which is often empty when each parent had the change independently. Use `git show -m <sha>` to see per-parent diffs or `git log -p -1 --cc <sha>` for the combined form.
- **Pickaxe operates on diff content, not blame.** `-S"foo"` won't find commits where `foo` was moved unchanged — add `-C` for that scenario.
- **`git log B..A`** excludes commits in B. `git log B...A` (three dots) includes commits from both sides that aren't in the merge base. The distinction matters when comparing divergent branches.
