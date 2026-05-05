---
name: github
description: This skill should be used when the user asks to "create a PR", "check PR status", "get review comments", "resolve PR threads", "check CI/CD", "fix PR feedback", "poll a PR to merge", "babysit a PR", or any GitHub workflow involving pull requests, reviews, and checks. Provides guidance on GitHub CLI (gh) usage for PR lifecycle management, including a reusable poll-and-merge loop that watches CI + reviewers, fixes failures/comments, and merges when green.
version: 1.4.0
---

# GitHub Pull Request Workflow

This skill provides guidance on working with GitHub pull requests via the `gh` CLI — creating PRs, handling reviews, resolving feedback, and monitoring checks.

## Overview

Use this skill when:
- Creating or updating pull requests
- Reading and addressing PR review comments
- Resolving review threads after fixing issues
- Checking CI/CD status and reading check logs
- Managing the full PR review cycle

## Prerequisites

### GitHub CLI

The `gh` CLI must be installed and authenticated:

```bash
# Check if authenticated
gh auth status

# Login if needed
gh auth login
```

### Repository Context

All commands assume you are inside a git repository. The `--repo OWNER/REPO` flag can be used to target a specific repository when outside one.

### Copilot Reviewer (Optional)

To request `@copilot` as a reviewer, ensure:

- GitHub plan includes Copilot code review
- **Copilot code review** is enabled in org/repo settings
- `gh` version is `2.88.0` or later (released 2026-03-11 — earlier versions have no `@copilot` alias)
- Auth uses a PAT or user-scoped token. The default `GITHUB_TOKEN` in GitHub Actions and some GitHub App installation tokens are not reliable for this call.

```bash
gh --version
```

**Bot identity behind `@copilot`:**

| Field | Value |
|---|---|
| Login | `copilot-pull-request-reviewer[bot]` |
| User ID | `175728472` |
| GraphQL `__typename` | `Bot` |

Do not confuse this with `copilot-swe-agent` / the `Copilot` coding agent — that's a *different* bot that authors PRs, not the reviewer.

---

## Creating Pull Requests

### Basic PR Creation

```bash
# Create PR against default branch
gh pr create --title "Short descriptive title" --body "Description of changes" --reviewer @copilot

# Create PR against a specific base branch
gh pr create --base develop --title "Title" --body "Description" --reviewer @copilot

# Create draft PR
gh pr create --draft --title "WIP: Title" --body "Description" --reviewer @copilot
```

### PR Body Best Practices

Use a structured body with a HEREDOC for correct formatting:

```bash
gh pr create --title "Add user authentication" --body "$(cat <<'EOF'
## Summary
- Add JWT-based authentication middleware
- Create login/logout endpoints
- Add session management

## Test plan
- [ ] Unit tests for auth middleware
- [ ] Integration tests for login flow
- [ ] Manual testing with expired tokens
EOF
)"
```

### Updating an Existing PR

```bash
# Update title
gh pr edit PR_NUMBER --title "New title"

# Update body
gh pr edit PR_NUMBER --body "New description"

# Add reviewers
gh pr edit PR_NUMBER --add-reviewer username1,username2

# Request Copilot reviewer
gh pr edit PR_NUMBER --add-reviewer @copilot

# Add labels
gh pr edit PR_NUMBER --add-label "bug,priority:high"
```

### Best-Effort Copilot Reviewer Request

Use this pattern so PR flows continue even when `@copilot` is unavailable:

```bash
gh pr edit PR_NUMBER --add-reviewer @copilot \
  && echo "Requested @copilot review" \
  || echo "@copilot reviewer unavailable; continuing with human reviewers"
```

**Fallback for older `gh` or environments where `@copilot` isn't resolved** — use the bot's actual login:

```bash
gh pr edit PR_NUMBER --add-reviewer "copilot-pull-request-reviewer[bot]"
```

**REST API alternative** (undocumented but works with a PAT):

```bash
gh api repos/OWNER/REPO/pulls/PR_NUMBER/requested_reviewers \
  -X POST \
  -f 'reviewers[]=copilot-pull-request-reviewer[bot]'
```

> Note: the GraphQL `requestReviews` mutation officially only accepts `userIds`/`teamIds`, so there is no supported GraphQL path for bots. The REST endpoint above is the only programmatic route.

### Known Copilot reviewer failure modes

1. **`'' not found`** — `gh pr edit --add-reviewer @copilot` called while Copilot is mid-review. The bot briefly appears in `requestedReviewers` with no login field; `gh` tries to round-trip the current reviewer list and trips on the empty string. **Fix:** retry after Copilot has submitted its review, or upgrade `gh` (fixed by [cli/cli#11689](https://github.com/cli/cli/pull/11689)).
2. **Silent no-op on re-request** — once Copilot has already reviewed, re-adding it as reviewer often does *not* trigger a fresh review. The Web UI's "re-request review" button is currently the only reliable trigger. There is no public API equivalent.
3. **422 with `reviewers[]=copilot`** — the REST endpoint rejects the short name. Always use the full bot login `copilot-pull-request-reviewer[bot]`.
4. **Empty-reviewer 422 from `GITHUB_TOKEN`** — the default Actions token and many App tokens can't add bot reviewers. Use a user-scoped PAT.

### Alternative: automatic Copilot code review

If you don't need per-PR control, enable **automatic Copilot code review** in repo/org settings — Copilot reviews every matching PR on each push without any `--add-reviewer` call, and the re-request failure mode (#2 above) disappears. Caveat: auto-review triggers on *new commits to existing PRs*, not on initial open, so the first review may lag until the next push.

Optional repository guidance for Copilot reviews:

```text
.github/copilot-code-review-instructions.md
```

---

## Checking PR Status and CI/CD

### View PR Status

```bash
# Overview of a PR
gh pr view PR_NUMBER

# JSON output for programmatic use
gh pr view PR_NUMBER --json state,reviewDecision,statusCheckRollup,mergeable
```

### Check CI/CD Status

```bash
# List all checks and their status
gh pr checks PR_NUMBER

# Wait for checks to complete (blocks until done)
gh pr checks PR_NUMBER --watch

# Get check details as JSON
gh pr view PR_NUMBER --json statusCheckRollup --jq '.statusCheckRollup[] | {name: .name, status: .status, conclusion: .conclusion}'
```

### Reading Check Logs

When a check fails, retrieve the logs to understand what went wrong:

```bash
# List workflow runs for the PR's branch
gh run list --branch BRANCH_NAME --limit 5

# View a specific run
gh run view RUN_ID

# Download failed run logs
gh run view RUN_ID --log-failed

# Download full logs
gh run view RUN_ID --log
```

### Re-running Failed Checks

```bash
# Re-run all failed jobs in a workflow run
gh run rerun RUN_ID --failed

# Re-run a specific job
gh run rerun RUN_ID --job JOB_ID
```

---

## Getting Review Comments

### List All Review Comments

```bash
# Get all review comments on a PR via the REST API
gh api repos/OWNER/REPO/pulls/PR_NUMBER/reviews

# Get inline review comments (code-level feedback)
gh api repos/OWNER/REPO/pulls/PR_NUMBER/comments
```

### Get Review Threads with Resolution Status

Use GraphQL to get full thread details including resolution status and thread IDs:

```bash
gh api graphql -f query='
query {
  repository(owner: "OWNER", name: "REPO") {
    pullRequest(number: PR_NUMBER) {
      reviewThreads(first: 100) {
        nodes {
          id
          isResolved
          isOutdated
          path
          line
          comments(first: 10) {
            nodes {
              author { login }
              body
              createdAt
            }
          }
        }
      }
    }
  }
}'
```

### Filter Unresolved Threads

Use `jq` to extract only unresolved threads:

```bash
gh api graphql -f query='
query {
  repository(owner: "OWNER", name: "REPO") {
    pullRequest(number: PR_NUMBER) {
      reviewThreads(first: 100) {
        nodes {
          id
          isResolved
          path
          line
          comments(first: 10) {
            nodes {
              author { login }
              body
            }
          }
        }
      }
    }
  }
}' --jq '.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved == false)'
```

---

## Resolving PR Review Feedback

Follow this workflow when addressing PR review comments.

### Step 1: Gather All Unresolved Feedback

First, retrieve all unresolved review threads to understand the full scope of required changes:

```bash
gh api graphql -f query='
query {
  repository(owner: "OWNER", name: "REPO") {
    pullRequest(number: PR_NUMBER) {
      reviewThreads(first: 100) {
        nodes {
          id
          isResolved
          path
          line
          comments(first: 10) {
            nodes {
              author { login }
              body
            }
          }
        }
      }
    }
  }
}' --jq '.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved == false)'
```

Note down:
- The **thread ID** (`id` field) for each unresolved thread
- The **file path** and **line number** where the comment applies
- The **comment body** describing what needs to change

### Step 2: Address Each Issue

For each unresolved review comment:

1. **Read the comment** carefully to understand the requested change
2. **Fix the code** — make the change in the relevant file at the indicated line
3. **Add or update tests** if the comment relates to correctness or coverage
4. **Verify everything passes** — run the test suite and any linters before committing

Commit fixes with clear messages referencing the feedback:

```bash
git add <changed-files>
git commit -m "Address review: <summary of fix>"
git push
```

### Step 3: Mark Threads as Resolved

After pushing fixes, resolve the corresponding review threads using the `resolveReviewThread` GraphQL mutation.

Resolve a single thread:

```bash
gh api graphql -f query='
mutation {
  resolveReviewThread(input: {threadId: "THREAD_ID_HERE"}) {
    thread {
      id
      isResolved
    }
  }
}'
```

Batch-resolve multiple threads in one request (up to 7-8 per batch):

```bash
gh api graphql -f query='
mutation {
  t1: resolveReviewThread(input: {threadId: "THREAD_ID_1"}) {
    thread { id isResolved }
  }
  t2: resolveReviewThread(input: {threadId: "THREAD_ID_2"}) {
    thread { id isResolved }
  }
  t3: resolveReviewThread(input: {threadId: "THREAD_ID_3"}) {
    thread { id isResolved }
  }
}'
```

### Step 4: Add a Summary Comment

Post a summary of all fixes to the PR so reviewers can see what was addressed at a glance:

```bash
gh pr comment PR_NUMBER --body "$(cat <<'EOF'
## Review feedback addressed

- **file.ts:42** — Fixed null check as suggested
- **utils.ts:18** — Renamed variable for clarity
- **test_auth.py** — Added missing edge case test

All threads resolved. Ready for re-review.
EOF
)"
```

### Step 5: Request Re-review

```bash
# Request re-review from the original reviewers
gh pr edit PR_NUMBER --add-reviewer reviewer-username

# Also request Copilot review when available
gh pr edit PR_NUMBER --add-reviewer @copilot \
  || echo "@copilot reviewer unavailable; continuing"
```

---

## Replying to Review Comments

### Reply to a Specific Review Comment

```bash
# Reply to an inline review comment by comment ID
gh api repos/OWNER/REPO/pulls/PR_NUMBER/comments/COMMENT_ID/replies \
  -f body="Fixed in the latest commit — changed X to Y as suggested."
```

### Add a General PR Comment

```bash
gh pr comment PR_NUMBER --body "Comment text here"
```

---

## Merging Pull Requests

### Merge When Ready

```bash
# Merge with default strategy
gh pr merge PR_NUMBER

# Squash merge (single commit)
gh pr merge PR_NUMBER --squash

# Rebase merge
gh pr merge PR_NUMBER --rebase

# Auto-merge when checks pass
gh pr merge PR_NUMBER --auto --squash
```

### Pre-merge Checklist

Before merging, verify:

```bash
# All checks passing
gh pr checks PR_NUMBER

# Review approved
gh pr view PR_NUMBER --json reviewDecision --jq '.reviewDecision'

# No merge conflicts
gh pr view PR_NUMBER --json mergeable --jq '.mergeable'
```

---

## Common Workflows

### Full Review Cycle

```bash
# 1. Create the PR
gh pr create --title "Feature: user auth" --body "Adds authentication" --reviewer @copilot

# 2. Wait for checks
gh pr checks PR_NUMBER --watch

# 3. Get review feedback
gh api graphql -f query='...' # (see "Get Review Threads" above)

# 4. Fix issues, commit, push
git add . && git commit -m "Address review feedback" && git push

# 5. Resolve threads
gh api graphql -f query='mutation { resolveReviewThread(...) }'

# 6. Post summary
gh pr comment PR_NUMBER --body "All feedback addressed."

# 7. Request re-review (human + Copilot if available)
gh pr edit PR_NUMBER --add-reviewer reviewer-username,@copilot \
  || echo "@copilot reviewer unavailable; requested human re-review only"

# 8. Merge
gh pr merge PR_NUMBER --squash
```

### PR Poll-and-Merge Loop

Reusable end-to-end procedure: open a PR, wait for CI + reviewers, fix failures/comments as they arrive, and merge when green. Run the **one-shot setup** once per PR, then enter the **poll loop** until the PR is merged or the user stops it.

#### Trigger

User asks to: open a PR and see it through to merge — wait for CI + reviewer(s), fix failures/comments, merge when green.

#### One-shot setup (once per PR)

**Prep** — branch must be non-`main` and committed + pushed:

```bash
git status -s && git branch --show-current
```

**Create PR** — HEREDOC so body formatting survives (see "Creating Pull Requests" above for the full pattern):

```bash
gh pr create --title "..." --body "$(cat <<'EOF'
## Summary
...
## Test plan
- [x] ...
EOF
)"
```

**Reviewer setup** — confirm per-repo convention first by scanning prior PRs:

```bash
gh pr list --state all --limit 10 --json number,title,reviewRequests
```

Some repos auto-attach Copilot as a reviewer (no manual action); others expect `gh pr edit N --add-reviewer @copilot`. Prior PRs show which convention applies. If `@copilot` fails, fall back to the bot's full login `copilot-pull-request-reviewer[bot]`. See **"Known Copilot reviewer failure modes"** above — especially the `'' not found` error when Copilot is mid-review. Do not confuse the reviewer with `copilot-swe-agent` (the coding agent that authors PRs, not a reviewer).

**Pick the merge strategy up front** — look at prior merge commits (one-parent = squash, two-parent = merge):

```bash
gh pr list --state merged --limit 5 --json mergeCommit,title
```

Most repos use `--squash --delete-branch`.

#### Poll loop (every 60–300 s — see cadence below)

Single diagnostic block that answers every question the loop needs:

```bash
PR=<number>
REPO=<owner>/<repo>

# Top-level state (checks + mergeability + requested reviewers)
gh pr view $PR --json reviews,statusCheckRollup,reviewRequests,reviewDecision,state,mergeable,mergeStateStatus

# Line-level comments (one per discussion thread)
gh api repos/$REPO/pulls/$PR/comments

# Review summaries (APPROVED / CHANGES_REQUESTED / COMMENTED / PENDING / DISMISSED)
gh api repos/$REPO/pulls/$PR/reviews

# Pending reviews — drafted comments someone hasn't submitted yet.
# A review with state=PENDING is in-progress — don't merge over it.
gh api repos/$REPO/pulls/$PR/reviews --jq '.[] | select(.state=="PENDING") | {id,user:.user.login}'

# Review threads via GraphQL (more reliable than the REST view above)
gh api graphql -f query='
  query($owner:String!,$name:String!,$pr:Int!){
    repository(owner:$owner,name:$name){
      pullRequest(number:$pr){
        reviewThreads(first:50){
          nodes{ id isResolved isOutdated comments(first:1){ nodes{ author{login} body path state } } }
        }
      }
    }
  }' -F owner=<owner> -F name=<name> -F pr=$PR
```

#### Decision gates

On each poll, classify the state and act:

1. **`statusCheckRollup` has a `FAILURE`** → fetch the failing run and fix:
   ```bash
   runId=$(gh run list --branch "$BRANCH" --workflow "CI" --limit 1 --json databaseId -q '.[0].databaseId')
   gh run view $runId --log-failed | grep -E " FAIL | AssertionError:| Error:" | head -20
   ```
   Fix → commit → push → restart the loop.

2. **Any `reviews[].state == "PENDING"`** → a reviewer is mid-draft. **Do NOT merge.** Surface it to the user ("pending review by X — waiting") and re-poll on a longer interval (draft reviews have no ETA).

3. **Any `reviewThreads.nodes[]` with `isResolved == false`** → unresolved comments. For each:
   - Read the diff context (file + line from the comment).
   - Implement the fix.
   - Commit + push.
   - Then mark the thread resolved (only **after** pushing the fix):
     ```bash
     gh api graphql -f query='
       mutation($id:ID!){ resolveReviewThread(input:{threadId:$id}){ thread{ isResolved } } }
     ' -f id=<threadId>
     ```

4. **Checks still `IN_PROGRESS` / `QUEUED`, no new comments** → re-poll. Log a one-liner ("Build & Test still running on `<sha>`; no new comments") so the user sees progress.

5. **`reviewDecision == "CHANGES_REQUESTED"`** → treat as #3: fix each unresolved thread, then push.

6. **Green path** — every check `SUCCESS` AND `mergeable != "false"` AND `mergeStateStatus != "BLOCKED"` AND no `PENDING` reviews AND no unresolved threads → merge:
   ```bash
   gh pr merge $PR --squash --delete-branch
   gh pr view $PR --json state,mergedAt,mergeCommit  # verify
   ```
   Stop the loop. `state: MERGED` + a nonzero `mergeCommit.oid` is the only success signal — a clean `gh pr merge` exit is necessary but not sufficient. If local branch cleanup fails ("worktree" error), delete the remote branch manually: `git push origin --delete <branch>`.

#### Scheduling cadence

Between polls, prefer scheduling a wakeup (if your agent supports it — e.g. Claude Code's `ScheduleWakeup`) rather than holding context open with `sleep`. Typical delays:

- CI in progress, no reviewer attached yet → **90 s**
- Just pushed a fix, waiting for CI restart → **120 s**
- `PENDING` review detected → **300 s** (reviewer has no SLA)
- Waiting for a reviewer who hasn't started → **300 s**

For agents without a scheduler, a bounded `while` loop with `sleep` is acceptable (e.g. poll every 120 s, cap at 90 min).

#### Invariants

- Never push to `main`.
- Never `--force`-push without explicit user permission.
- Never dismiss a review or skip hooks (`--no-verify`) to land faster.
- Always resolve threads **after** pushing the fix — stale resolution is worse than unresolved.
- `state: MERGED` + a nonzero `mergeCommit.oid` are the only success signals.

### Debugging a Failed Check

```bash
# 1. See which checks failed
gh pr checks PR_NUMBER

# 2. Find the run ID
gh run list --branch BRANCH_NAME --limit 5

# 3. Read the failed logs
gh run view RUN_ID --log-failed

# 4. Fix the issue, push
git add . && git commit -m "Fix CI: <what was wrong>" && git push

# 5. Watch checks pass
gh pr checks PR_NUMBER --watch
```

---

## Tips

- Always use `gh api graphql` for thread resolution — there is no REST API equivalent for `resolveReviewThread`
- Batch thread resolutions into groups of 7-8 to avoid request size limits
- Use `--jq` with `gh api` to filter JSON output directly
- Use `gh pr checks --watch` to block until CI completes instead of polling manually
- When fixing review feedback, commit each logical fix separately for reviewer clarity
- Always run tests locally before pushing fixes to avoid unnecessary CI churn
