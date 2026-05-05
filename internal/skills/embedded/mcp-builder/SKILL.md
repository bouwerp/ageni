---
name: mcp-builder
description: Guide for building high-quality MCP (Model Context Protocol) servers so LLMs can use external APIs safely — Python (MCP Python SDK / FastMCP-style patterns) or Node/TypeScript (MCP TypeScript SDK). Use when designing tools, transports (stdio/SSE/HTTP), validation, errors, pagination, and evaluations.
version: 1.0.1
---

# MCP server development

**Portability:** MCP applies to **OpenCode, Claude Code, Cursor**, and other hosts that load MCP servers; **Gemini CLI / Pi** may use different extension models — still use this skill for server design and code quality. Load spec and SDK docs via HTTPS (browser, `curl`, or your agent’s fetch capability).

**Upstream concepts:** Aligned with [Prat011/awesome-llm-skills — mcp-builder](https://github.com/Prat011/awesome-llm-skills/tree/master/mcp-builder); that repo includes extended reference markdown files — retrieve them when you need full checklists.

## Phase 1 — Research and plan

### Agent-centric tools

- **Workflows, not thin wrappers:** Prefer tools that complete a task (e.g. “create incident and link to user”) over one-to-one REST mirrors.
- **Context budget:** Default to concise output; offer optional “detailed” shapes. Truncate large lists; use pagination parameters.
- **Actionable errors:** Tell the agent what to try next (e.g. “Use `status=open` to narrow results”).
- **Consistent naming:** Prefix related tools (`jira_get_issue`, `jira_add_comment`).

### Read the spec and SDKs

- MCP overview for LLMs: [modelcontextprotocol.io/llms-full.txt](https://modelcontextprotocol.io/llms-full.txt)
- Python SDK README: [raw.githubusercontent.com/modelcontextprotocol/python-sdk/main/README.md](https://raw.githubusercontent.com/modelcontextprotocol/python-sdk/main/README.md)
- TypeScript SDK README: [raw.githubusercontent.com/modelcontextprotocol/typescript-sdk/main/README.md](https://raw.githubusercontent.com/modelcontextprotocol/typescript-sdk/main/README.md)

Study the target API end-to-end: auth, rate limits, pagination, idempotency, error formats.

### Plan artifacts

- Tool list with inputs/outputs (Pydantic or Zod schemas).
- Shared client layer (HTTP, retries, auth refresh).
- Limits: max rows, max string length, timeouts.

## Phase 2 — Implement

- **Validation:** Strict models; document every field with examples in the tool description.
- **Async I/O:** Use async for network-bound operations.
- **Hints:** Use protocol tool annotations where supported (`readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`).
- **Security:** No secrets in logs; least-privilege tokens; avoid shelling out with user-controlled strings.

### Testing servers safely

Long-running servers block the foreground. Prefer:

- Short subprocess with `timeout` for smoke tests, or
- Run the server in another terminal/session and drive it with a small client, or
- A dedicated evaluation harness.

For Python: `python -m py_compile server.py`. For TypeScript: `npm run build`.

## Phase 3 — Review

- DRY shared logic; consistent response shapes; full typing; every external call handles errors.

## Phase 4 — Evaluations

Create ~10 independent, read-only, realistic multi-step questions with single verifiable answers to confirm agents can chain your tools. (See upstream `reference/evaluation.md` in awesome-llm-skills mcp-builder for XML templates and examples.)

## Language choice

- **Python:** Fast iteration, Pydantic — good for glue and internal tools.
- **TypeScript:** Strong fit when the ecosystem is already Node or you need strict JSON schemas with Zod.
