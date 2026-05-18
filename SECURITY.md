# Security Model

This document describes how ageni protects secret values (API keys, tokens, passwords) at every layer of the runtime.

## Threat Model

| Threat | Mitigation |
|--------|-----------|
| Secret value in LLM context window | Schema design (tools accept aliases only) + Redactor backstop |
| Secret in tool output echoed back to LLM | Output Redactor scrubs all tool results before they enter context |
| Malicious file read leaking a private key | Path blocker rejects reads of `.env`, `id_rsa`, `*.pem`, keyring dirs, vault files |
| Secret in heap memory (core dump / swap) | `memguard.Enclave`: XSalsa20Poly1305 encrypted in-process; `LockedBuffer` is mlock'd + has guard pages |
| Secret in plaintext config file | OS keychain via `99designs/keyring`; age-encrypted file vault for headless/CI |
| Prompt injection extracting a credential | Schema + Redactor layers make the value unreachable by construction |

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│  LLM context window  (aliases only — values NEVER here) │
└───────────────────────────┬─────────────────────────────┘
                            │ tool calls (alias names)
┌───────────────────────────▼─────────────────────────────┐
│  Tool layer  (internal/secrets/tools.go)                 │
│  list_secrets · run_with_secret · http_with_auth         │
│  request_secret_store                                    │
│  - accepts aliases, resolves internally                  │
│  - injects values at fork/HTTP boundary only             │
│  - scrubs output via Redactor before return              │
└───────────────────────────┬─────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────┐
│  Registry scrubber  (internal/tools/registry.go)         │
│  - ALL tool output scrubbed as backstop                  │
└───────────────────────────┬─────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────┐
│  Secret Store  (internal/secrets/store.go)               │
│  - values held as memguard.Enclave in-process            │
│  - persisted in OS keychain (single JSON blob)           │
│  - age-encrypted file fallback for headless/CI           │
│  - env vars seeded transiently (not persisted)           │
└─────────────────────────────────────────────────────────┘
```

## Layers in Detail

### Layer 1 — Schema design
Tool inputs accept only `secret_alias` (a name string). There is no tool schema that accepts or returns a credential value. An LLM following its instructions cannot extract a value through a tool call.

### Layer 2 — Path blocker
`read_file` and `grep` reject paths that match known sensitive patterns before the file is opened. Blocked patterns include: `.env`, `id_rsa`, `id_ed25519`, `*.pem`, `*.key`, `*.p12`, `identity.age`, `secrets.age`, `keyring/`, `credentials`.

### Layer 3 — Output Redactor (`internal/secrets/redactor.go`)
Every known secret value is registered with a `Redactor` at startup. The redactor is called on all tool output before it reaches the LLM. It replaces values with `[REDACTED:<alias>]`. Values are sorted by length descending to prevent partial-match bypass.

### Layer 4 — In-memory protection (`memguard`)
Secret values are stored as `memguard.Enclave` objects (XSalsa20Poly1305 encrypted) in the Go heap. When a value must be used (API call, subprocess injection) it is opened into a `LockedBuffer` (mlock'd, guard pages, canaries) and immediately destroyed after use with `defer buf.Destroy()`. `memguard.CatchInterrupt()` ensures secure wipe on Ctrl-C; `defer memguard.Purge()` ensures wipe on clean shutdown.

### Layer 5 — Durable storage
Secrets are persisted as a single JSON blob in the OS keychain (`99designs/keyring`) which maps to:
- macOS: Keychain
- Linux: GNOME libsecret / Secret Service
- Linux KDE: KWallet
- Windows: DPAPI
- Headless/CI: passphrase-encrypted file at `~/.ageni/keyring/` (set `AGENI_KEYRING_PASSPHRASE`)

The single-blob pattern minimises OS keychain unlock prompts to once per session.

An age-encrypted file vault (`~/.ageni/secrets.age`) is available as an alternative headless backend via `FileVault`.

## What is Out of Scope

- **Secret rotation**: If a key is compromised, rotate it via the provider's dashboard and re-run `ageni init`.
- **Multi-user / team secrets sharing**: ageni is a single-user tool. Use Doppler, Vault, or similar for team secrets, and run ageni inside their environment (`doppler run -- ageni`).
- **HSM / TPM**: Future work.
- **External secret managers**: Vault, Infisical, Doppler, AWS Secrets Manager — ageni doesn't integrate directly, but their CLI wrappers inject env vars that ageni picks up via `loadFromEnv()`.

## Reporting a Vulnerability

Open a GitHub issue with the label `security`. For critical issues involving credential exposure, email the maintainer directly before opening a public issue.
