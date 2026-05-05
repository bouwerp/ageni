# Bisect

**Read when:** a regression exists and you need to find the exact commit that introduced it. Binary search across history.

## Manual bisect

```bash
git bisect start
git bisect bad                    # current commit exhibits the bug
git bisect good <known-good-sha>  # this older commit was fine
# git checks out the midpoint
# Test the working tree. Then tell bisect which half:
git bisect good   # midpoint is fine → bug is newer
# or
git bisect bad    # midpoint is buggy → bug is this or older
# Repeat until bisect reports:
#   <sha> is the first bad commit
git bisect reset  # return to where you started
```

## Automated bisect (`bisect run`)

If you have a test that exits 0 (good) or non-zero (bad), git does the whole loop for you:

```bash
git bisect start
git bisect bad
git bisect good <sha>
git bisect run ./test.sh
# ... git tests each midpoint automatically, reports the first bad commit
git bisect reset
```

The test script must:
- Exit **0** if the commit is good.
- Exit **non-zero (1–124 or 126+)** if the commit is bad.
- Exit **125** to tell bisect this commit is **untestable** (e.g. build failed for unrelated reasons) — bisect will pick an adjacent commit instead.

### Writing a good bisect test script

```bash
#!/bin/bash
set -e

# 1. Build — if build fails, signal untestable
make build >/dev/null 2>&1 || exit 125

# 2. Run the regression-specific test
./run-repro-case.sh
# exits 0 if the bug is absent, 1 if present
```

Keep it **fast** and **deterministic**. A 30-second bisect test across 20 commits = 10 minutes. A flaky test will send bisect down the wrong path.

## Skipping a commit

If you reach a commit you can't test (dependency broken, unrelated bug):

```bash
git bisect skip
```

Multiple skips are fine — bisect will find the first bad commit in the remaining reachable range and tell you the exact ambiguity.

## Visualize progress

```bash
git bisect log           # show the commands run so far
git bisect visualize     # open gitk / tig / log with the candidate range
```

## Resume after interruption

```bash
git bisect log > bisect.log
# ... do other work, then:
git bisect reset
git bisect replay bisect.log
```

## Gotchas

- **`bisect good`/`bad` are semantic**, not a judgement of the code. If you're hunting a regression, the commit with the bug is "bad" even if the code looks cleaner.
- **`bisect start <bad> <good>`** is a shortcut for the three-command version.
- **`bisect start --term-old=works --term-new=broken`** lets you rename the terms when "good/bad" feels wrong (e.g. finding when a feature was *added*, where "broken" is actually "feature-absent").
- **Merge commits** are fine — bisect handles them. The "first bad commit" reported may be a merge; inspect its diff (`git show -m <sha>`) to see which parent introduced the change.
- **Rebased history invalidates previous bisect logs.** A replay only makes sense on the same history.
- **`git bisect run` with a test file inside the repo**: the test script is checked out at each midpoint along with everything else. If an old midpoint lacks your script, it won't run. Keep the script outside the repo or use `GIT_INDEX_FILE` / a worktree.

## After finding the bad commit

```bash
# See what changed
git show <first-bad-sha>

# If the fix is to revert:
git bisect reset
git revert <first-bad-sha>

# If the fix is to change something:
git bisect reset
# ... make your fix on a new branch
```
