---
name: secrets-management
description: Guardrails and tools for working with secrets (API keys, passwords, tokens) without loading them into the agent's context. Provides safe execution and leak scanning. Use this when the agent needs to run commands that require secrets or when exploring files that might contain sensitive data.
version: 1.0.0
---

# Secrets Management

**Mandate:** Coding agents must **never** load secrets into context. If a secret appears in your chat history, it has been leaked.

This skill provides a safety layer for handling sensitive information while maintaining productivity.

## Core Guardrails

1.  **Read-Check-Skip:** Before using `read_file` or `grep_search` on a file, check if it is likely to contain secrets.
    -   Check `.gitignore`: If a file is ignored, it often contains environment-specific secrets.
    -   File Names: Never read `.env`, `*.pem`, `id_rsa`, `credentials`, `secrets.json`, or similar.
2.  **No Echo:** Never `echo` a secret environment variable to see its value.
3.  **No Commit:** Never stage or commit changes to files containing secrets unless specifically instructed (and even then, double-check if they should be in `.gitignore`).

## Safe Execution Workflow

If a command (e.g., `npm test`, `python script.py`, `aws cli`) requires secrets, use the `run_with_secrets.py` tool. This tool loads secrets from a source and executes the command while redacting the secrets from the output.

```bash
python3 skills/secrets-management/scripts/run_with_secrets.py \
  --source .env \
  --command "npm test"
```

### Supported Sources
-   `.env`: Standard environment files (default).
-   `--secrets-json path/to/file.json`: For structured secret files.

## Leak Scanning

Before reading a file you suspect might have secrets, or before committing code that might have accidentally included secrets, run the scan tool:

```bash
python3 skills/secrets-management/scripts/scan_leaks.py path/to/scan
```

The tool will report the line and type of secret detected **without** showing the value.

## What to do if a Secret is Leaked

If you accidentally load a secret into context (e.g., a command failed and printed an API key):
1.  **Acknowledge the leak** immediately to the user.
2.  **Suggest Rotation**: Strongly recommend the user rotates the leaked secret.
3.  **Checkpoint**: Stop current work if the environment is compromised until the user confirms how to proceed.

## Example: Testing a Private API

**User:** "Run the integration tests for the Stripe integration."
**Agent Strategy:**
1.  Check if `STRIPE_SECRET_KEY` is needed.
2.  Verify it's in `.env` (without reading the value).
3.  Run tests using the safe wrapper.

```bash
# Check if key is defined in .env (safe search for keys, not values)
grep "^STRIPE_SECRET_KEY=" .env

# Run tests
python3 skills/secrets-management/scripts/run_with_secrets.py --command "npm test"
```
