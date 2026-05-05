---
name: jira
description: This skill should be used when the user asks to "read JIRA issue", "view JIRA ticket description", "update JIRA issue", "add JIRA comment", "modify JIRA description", or works with JIRA. Prefers the official Atlassian CLI (`acli`) for read/write operations, with a REST + Atlassian Document Format (ADF) fallback for environments without `acli`.
version: 2.0.0
---

# JIRA Issue Interaction

This skill provides guidance on interacting with JIRA issues. **Prefer `acli` (Atlassian CLI) for everything** — it handles auth, pagination, and ADF plumbing for you. Fall back to the REST API + raw ADF only when `acli` isn't installed or available (e.g. some CI runners).

## Overview

Use this skill when:

- **Reading** JIRA issues (summary, description, comments, status) — primary use case
- Updating JIRA issue descriptions or summaries
- Adding comments to JIRA issues
- Searching issues via JQL
- Working with raw ADF (only when forced to bypass `acli`)

## Decision order

Before running any command, check what's available on the machine:

```bash
if command -v acli >/dev/null 2>&1 && acli jira auth status >/dev/null 2>&1; then
  echo "acli path"
elif [ -n "$ATLASSIAN_API_TOKEN" ] && [ -n "$ATLASSIAN_EMAIL" ] && [ -n "$ATLASSIAN_SITE" ]; then
  echo "REST + env token path"
else
  echo "Needs human setup — see 'Install & auth acli' below"
fi
```

---

## Install & auth `acli` (one-time, requires human intervention)

`acli` is the official Atlassian CLI. [Install guide](https://developer.atlassian.com/cloud/acli/guides/install-acli/).

### Install

**macOS (Homebrew):**

```bash
brew tap atlassian/acli
brew install acli
```

**Linux / Windows:** see the official install guide linked above.

**Verify:**

```bash
acli --version
# If this prints an "outdated version" hint, upgrade with: brew upgrade acli
```

### Authenticate — **ask the user to run this themselves**

Two paths. Both require a real human at the terminal the first time; after that `acli` is non-interactive.

**Option A — OAuth via browser (recommended for workstations):**

```bash
acli jira auth login --web
```

This opens a browser, the user approves, and `acli` caches OAuth tokens in `~/.config/acli/`. **Claude cannot complete this step** — the user must run it themselves. If you're running as an agent and `acli jira auth status` reports "not authenticated", stop and ask the user:

> `acli` needs to be authenticated. Please run `acli jira auth login --web` in your terminal and let me know when it's done.

**Option B — API token (required for headless / CI environments):**

```bash
echo "$ATLASSIAN_API_TOKEN" | acli jira auth login \
  --site "$ATLASSIAN_SITE" \
  --email "$ATLASSIAN_EMAIL" \
  --token
```

The user generates the token at <https://id.atlassian.com/manage-profile/security/api-tokens>. In a shared terminal, prefer piping from stdin over passing the token as a flag so it doesn't land in shell history.

### Verify auth

```bash
acli jira auth status
# Expected:
#   ✓ Authenticated
#     Site: <your-site>.atlassian.net
#     Email: <your-email>
#     Authentication Type: oauth_global | api_token
```

If `acli jira auth status` fails, **do not try to repair tokens on disk** — ask the user to re-run `acli jira auth login --web` (or Option B for CI).

---

## Reading JIRA with `acli`

### Read a single issue (summary + description)

```bash
acli jira workitem view KEY-123 --fields summary,description --json
```

This returns the issue as JSON, with the description in ADF format. Without `--json`, `acli` prints a terminal-friendly view but **paragraph and heading boundaries collapse** — use `--json` for any machine processing.

**Flatten ADF → Markdown** (Python one-liner — paste as-is):

```bash
acli jira workitem view KEY-123 --fields summary,description --json | python3 -c '
import json, sys
def adf_to_md(node):
    if isinstance(node, list):
        return "".join(adf_to_md(n) for n in node)
    if not isinstance(node, dict):
        return ""
    t = node.get("type")
    content = node.get("content", [])
    if t == "text":
        txt = node.get("text", "")
        for m in node.get("marks", []) or []:
            mt = m.get("type")
            if mt == "strong": txt = f"**{txt}**"
            elif mt == "em": txt = f"*{txt}*"
            elif mt == "code": txt = f"`{txt}`"
            elif mt == "link": txt = f"[{txt}]({m.get('attrs',{}).get('href','')})"
        return txt
    if t == "paragraph": return adf_to_md(content) + "\n\n"
    if t == "heading":
        lvl = node.get("attrs", {}).get("level", 1)
        return "\n" + "#" * lvl + " " + adf_to_md(content).strip() + "\n\n"
    if t == "bulletList":
        return "".join("- " + adf_to_md(li.get("content", [])).strip() + "\n" for li in content) + "\n"
    if t == "orderedList":
        return "".join(f"{i+1}. " + adf_to_md(li.get("content", [])).strip() + "\n" for i, li in enumerate(content)) + "\n"
    if t == "listItem": return adf_to_md(content)
    if t == "codeBlock":
        lang = node.get("attrs", {}).get("language", "")
        return f"\n```{lang}\n{adf_to_md(content)}\n```\n\n"
    if t == "hardBreak": return "\n"
    if t in ("media", "mediaInline"):
        return f"[media:{node.get('attrs',{}).get('id','')}]"
    return adf_to_md(content)

d = json.load(sys.stdin)
print("#", d["fields"]["summary"])
print()
print(adf_to_md(d["fields"]["description"]).rstrip())
'
```

**Useful `--fields` selectors** (comma-separated):

- `summary,description` — minimal read
- `summary,description,status,assignee,reporter,priority,labels`
- `*all` — everything (large)
- `*navigable,-comment` — all navigable fields except comments

### Search issues (JQL)

```bash
acli jira workitem search --jql "assignee = currentUser() ORDER BY updated DESC" --limit 10 --json
acli jira workitem search --jql "project = PROJ AND status = 'In Progress'" --paginate --json
```

### List comments

```bash
acli jira workitem comment list --key KEY-123 --json
acli jira workitem comment list --key KEY-123 --paginate --json
```

Each comment's `body` is ADF — run it through the `adf_to_md` snippet above to flatten.

---

## Writing JIRA with `acli`

`acli` accepts **plain text or ADF** for description and comment bodies, so you almost never need to hand-craft ADF yourself.

### Edit description (plain text — simplest)

```bash
acli jira workitem edit --key KEY-123 \
  --description "Updated description with **no** ADF plumbing needed." \
  --yes
```

### Edit description from a file (ADF or markdown)

```bash
acli jira workitem edit --key KEY-123 --description-file desc.json --yes
```

### Add a comment

```bash
acli jira workitem comment create --key KEY-123 --body "Fixed in abc123." --json
# or from a file:
acli jira workitem comment create --key KEY-123 --body-file comment.md
```

### Generate an edit JSON template

When editing many fields at once, scaffold a template:

```bash
acli jira workitem edit --generate-json > edit.json
# edit the file, then:
acli jira workitem edit --from-json edit.json --yes
```

### Transition status

```bash
acli jira workitem transition --key KEY-123 --status "In Review"
```

---

## REST API fallback (no `acli` available)

Use this only when `acli` is not installable — otherwise prefer the `acli` paths above.

### Credentials (env vars)

```bash
: "${ATLASSIAN_SITE:?set me, e.g. yourco.atlassian.net}"
: "${ATLASSIAN_EMAIL:?set me}"
: "${ATLASSIAN_API_TOKEN:?generate at id.atlassian.com/manage-profile/security/api-tokens}"
```

### Read issue

```bash
curl -sS -u "$ATLASSIAN_EMAIL:$ATLASSIAN_API_TOKEN" \
  "https://$ATLASSIAN_SITE/rest/api/3/issue/KEY-123?fields=summary,description" \
  -H "Accept: application/json" | jq .
```

Run the response through the `adf_to_md` snippet above to render the description.

### Update description (raw ADF required — this is why `acli` is preferred)

```bash
curl -sS -u "$ATLASSIAN_EMAIL:$ATLASSIAN_API_TOKEN" \
  -X PUT "https://$ATLASSIAN_SITE/rest/api/3/issue/KEY-123" \
  -H "Content-Type: application/json" \
  -d '{
    "fields": {
      "description": {
        "version": 1,
        "type": "doc",
        "content": [
          {"type": "paragraph", "content": [{"type": "text", "text": "Updated description"}]}
        ]
      }
    }
  }'
```

### Add comment

```bash
curl -sS -u "$ATLASSIAN_EMAIL:$ATLASSIAN_API_TOKEN" \
  -X POST "https://$ATLASSIAN_SITE/rest/api/3/issue/KEY-123/comment" \
  -H "Content-Type: application/json" \
  -d '{"body":{"version":1,"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Comment text"}]}]}}'
```

---

## Atlassian Document Format (ADF) reference

Only needed on the REST path or when hand-editing a `--description-file`. `acli` with plain-text `--description` / `--body` avoids this entirely.

### Minimal envelope

```json
{ "version": 1, "type": "doc", "content": [ /* block nodes */ ] }
```

### Common nodes

```json
// Paragraph with bold + italic
{"type":"paragraph","content":[
  {"type":"text","text":"Bold","marks":[{"type":"strong"}]},
  {"type":"text","text":" and "},
  {"type":"text","text":"italic","marks":[{"type":"em"}]}
]}

// Heading level 2
{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Section"}]}

// Code block
{"type":"codeBlock","attrs":{"language":"python"},
 "content":[{"type":"text","text":"def hello():\n    print('hi')"}]}

// Bullet list
{"type":"bulletList","content":[
  {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"Item 1"}]}]}
]}

// Link
{"type":"text","text":"docs","marks":[{"type":"link","attrs":{"href":"https://example.com"}}]}
```

See the [ADF playground](https://developer.atlassian.com/cloud/jira/platform/apis/document/playground/) for interactive exploration.

---

## Common errors

### `acli: not authenticated` / `acli jira auth status` fails

**Cause:** No cached OAuth session or the token expired.

**Fix:** Ask the user to re-run `acli jira auth login --web`. Do **not** try to edit `~/.config/acli/` by hand.

### REST `401 Unauthorized`

**Cause:** Stale or wrong API token, or wrong email / site.

**Fix:** Ask the user for a fresh token from <https://id.atlassian.com/manage-profile/security/api-tokens>. Don't retry with the same credentials.

### REST `403 Forbidden`

**Cause:** User lacks edit permission on the issue, or the token lacks the required scopes.

**Fix:** Check project-level permissions in JIRA. If the token is scoped, regenerate with broader scope or switch to `acli` OAuth (which uses the user's full JIRA permissions).

### REST `400 Bad Request` with `description` field

**Cause:** Malformed ADF — almost always missing `version: 1`, wrong root type (`doc`), or a block node missing its `type`.

**Fix:** Validate the JSON (`jq .`). If this keeps biting, switch to `acli workitem edit --description "..."` which accepts plain text.

### REST `404 Not Found`

**Cause:** Wrong issue key or wrong site.

**Fix:** Verify `ATLASSIAN_SITE` and that the key exists (open it in the browser).

### REST `415 Unsupported Media Type`

**Cause:** Missing `Content-Type: application/json`.

**Fix:** Add the header.

---

## Best practices

1. **Default to `acli`.** Only drop to REST when there's no way to install/auth `acli`.
2. **`acli` auth is a human step.** Never try to complete `acli jira auth login --web` as an agent — always ask the user.
3. **Use `--json` for all reads** you intend to parse. The human-readable `acli` output loses paragraph and heading structure.
4. **Prefer plain-text `--description` / `--body` over hand-built ADF** — `acli` converts it correctly; your hand-built ADF may not.
5. **Don't commit tokens.** `ATLASSIAN_API_TOKEN` lives in the shell / secrets store, not in the repo.
6. **On 401/403, ask — don't retry.** Stale credentials won't fix themselves.
7. **Narrow `--fields`** on reads when you only need a subset — large issues with many comments and attachments can be hundreds of KB.

## References

- [Atlassian CLI (`acli`) install guide](https://developer.atlassian.com/cloud/acli/guides/install-acli/)
- [`acli` command reference](https://developer.atlassian.com/cloud/acli/reference/commands/)
- [JIRA REST API v3](https://developer.atlassian.com/cloud/jira/platform/rest/v3/intro/)
- [Atlassian Document Format](https://developer.atlassian.com/cloud/jira/platform/apis/document/structure/)
- [ADF playground](https://developer.atlassian.com/cloud/jira/platform/apis/document/playground/)
