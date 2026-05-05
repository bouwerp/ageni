---
name: git-pushing
description: Stage, commit, and push git changes with conventional commit messages. Use when user wants to commit and push changes, mentions pushing to remote, or asks to save and push their work. Also activates when user says "push changes", "commit and push", "push this", "push to github", or similar git workflow requests.
---
# Git Push Workflow

**Portability:** Same git workflow for OpenCode, Claude Code, Gemini, Cursor, Pi, or any agent with shell access. **Paths:** run commands from the **repository root**. If this skill was installed under a user config directory, use the full path to `smart_commit.sh` there instead of `skills/git-pushing/...`.

Stage all changes, create a conventional commit, and push to the remote branch.

## When to Use

Automatically activate when the user:
- Explicitly asks to push changes ("push this", "commit and push")
- Mentions saving work to remote ("save to github", "push to remote")
- Completes a feature and wants to share it
- Says phrases like "let's push this up" or "commit these changes"

## Workflow

**Preferred:** use the bundled script (staging, commit, push with upstream):

```bash
bash skills/git-pushing/scripts/smart_commit.sh
```

With custom message:

```bash
bash skills/git-pushing/scripts/smart_commit.sh "feat: add feature"
```

**If the script is unavailable or the user wants granular control:** stage selectively with `git add`, `git commit` with a [Conventional Commits](https://www.conventionalcommits.org/) message, then `git push -u origin "$(git rev-parse --abbrev-ref HEAD)"`. Never force-push or rewrite shared history unless the user explicitly requests it.

**Scope:** This skill targets everyday commit-and-push flows ([claude-skills-marketplace git-pushing](https://github.com/mhattingpete/claude-skills-marketplace/tree/main/engineering-workflow-plugin/skills/git-pushing)). Complex rebases or release branching are out of scope unless the user asks.