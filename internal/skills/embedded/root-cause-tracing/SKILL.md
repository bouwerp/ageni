---
name: root-cause-tracing
description: Trace errors and unexpected behaviour from symptom back to the original trigger — across stack traces, logs, async boundaries, and data flow. Use when debugging deep failures, flaky tests, or "something downstream broke" incidents.
version: 1.0.1
---

# Root cause tracing

**Portability:** Product-agnostic debugging workflow for any coding agent with access to logs, code, and tests.

**Intent:** Move from **what failed** (exception site) to **why it was invoked** (first incorrect assumption, missing guard, or external trigger).

> **Note:** The name appears in community skill lists (e.g. superpowers-style workflows). There is no single canonical upstream file; this is a practical consolidation for agents.

## When to use

- Stack traces point to generic code (framework, helper) — not the business logic that started the bad path.
- Failures are intermittent (ordering, timing, shared state).
- The user sees a late symptom (500, empty UI) but the bug originated earlier.

## Workflow

### 1. Capture the failure precisely

- Full stack trace (deepest cause first).
- Request/job id, correlation id, timestamp window.
- Inputs that reproduce (minimal if possible).

### 2. Classify the failure site

| Site type | Next step |
|-----------|-----------|
| Validation / assertion | Inspect caller — who passed bad data? |
| Null / undefined | Trace where optional value should have been set |
| Network / IO | Distinguish timeout vs 4xx/5xx; check retries and idempotency |
| Database | Migration vs query vs constraint — trace to writer |
| UI | Event handler vs render — trace to state source |

### 3. Walk backward along the causal chain

Ask repeatedly: **What called this, and with what state?**

- Follow stack frames upward to the first *application* frame you control.
- For async: map promise/task boundaries; find the scheduling point.
- For events: find producer of the event payload (queue, webhook, user action).

### 4. Stop at the first broken invariant

The root cause is usually the **earliest** point where:

- An assumption was false (empty list treated as success),
- An error was swallowed,
- Data crossed a boundary without validation,
- Non-determinism (time, random, iteration order) leaked.

### 5. Verify

- Add logging or a failing test at the trigger level (see `test-driven-development`).
- Fix at the source; avoid only patching the symptom unless the symptom layer is the correct place to harden.

## Anti-patterns

- Fixing only the deepest exception if it is a side effect of earlier bad data.
- Adding broad try/catch without rethrow or structured error context.
- Blaming the last deploy without time-correlating traffic or data changes.

## Quick checklist

```
[ ] Repro or exact error text captured
[ ] Deepest + full stack recorded
[ ] First owned frame in the stack identified
[ ] Data at that boundary inspected
[ ] Earlier producer of that data found
[ ] First false invariant identified
[ ] Fix + regression test or guard at trigger
```
