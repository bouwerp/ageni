---
name: secrets-management
description: Guardrails and tools for working with secrets (API keys, passwords, tokens) without loading them into the agent's context. The ageni runtime enforces this at the tool layer — values never enter LLM context. Use this skill for guidance on the available secure tools and workflows.
version: 2.0.0
---

# Secrets Management

**Core invariant:** Secret values must **never** appear in LLM context. If a value appears in your chat history, it has been leaked.

ageni enforces this at the infrastructure level — you do not need scripts or manual workarounds. Use the built-in secure tools below.

## Built-in Secure Tools

### `list_secrets`
Returns the **names** (aliases) of all stored credentials. Values are never returned.

```json
{}
```

Example response:
```
Available secrets (names only):
ANTHROPIC_API_KEY
GITHUB_TOKEN
STRIPE_SECRET_KEY
```

### `run_with_secret`
Runs a shell command with one or more secrets injected as environment variables **at process fork time**. The values are never in context — only the command output is returned (and that output is also scrubbed for accidental leakage).

```json
{
  "command": "npm test",
  "inject_secrets": ["STRIPE_SECRET_KEY", "ANTHROPIC_API_KEY"],
  "timeout_seconds": 120
}
```

### `http_with_auth`
Makes an authenticated HTTP request. The credential is injected into the request header at call time and is never returned.

```json
{
  "url": "https://api.example.com/data",
  "method": "GET",
  "secret_alias": "MY_API_KEY",
  "auth_type": "bearer"
}
```

Supported `auth_type` values: `bearer`, `basic`, `header` (X-Api-Key).

### `request_secret_store`
Asks the user (via a secure TUI prompt) to store a new secret. The agent receives only `"stored"` confirmation — never the value.

```json
{
  "alias": "STRIPE_SECRET_KEY",
  "description": "Stripe secret key for running integration tests"
}
```

## Core Guardrails (enforced by runtime)

1. **Path blocking:** `read_file` and `grep` refuse to read sensitive paths (`.env`, private keys, keyring files, vault files). If a path is blocked, use `list_secrets` or `run_with_secret` instead.
2. **Output scrubbing:** All tool output is scanned against known secret values and replaced with `[REDACTED:<alias>]` before entering context.
3. **No plain values in config:** API keys are stored in the OS keychain, not in config files. `ageni init` stores them there automatically.

## Workflow: Running a Command That Needs Credentials

```
1. list_secrets                          → confirm alias name
2. run_with_secret(command, [alias])     → get result
```

If the secret is missing:
```
3. request_secret_store(alias)           → user enters value securely via TUI
4. run_with_secret(command, [alias])     → try again
```

## Workflow: Authenticated API Call

```
1. list_secrets                          → confirm alias
2. http_with_auth(url, alias, auth_type) → get response
```

## What to Do If a Secret Leaks

1. Acknowledge the leak immediately.
2. Recommend the user rotate the credential.
3. Do not proceed until the user confirms rotation.

The runtime redactor is a backstop, not a guarantee — schema-level discipline (never asking for or echoing values) is the primary defence.

