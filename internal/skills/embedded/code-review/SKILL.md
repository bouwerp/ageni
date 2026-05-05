---
name: code-review
description: Self-review your own freshly written or modified code for quality, simplicity, correctness, security, and performance. Trigger phrases "simplify this", "check quality", "clean up", or after finishing a non-trivial change. Multi-pass analysis covering reuse, complexity, security, performance, and AI-generated code smells. Distinct from review-implementing which is for applying feedback from a human reviewer.
version: 1.0.0
---

# Code Review

A structured post-implementation review for code changes. Run this after completing any non-trivial implementation to catch over-engineering, duplication, security issues, performance problems, and code smells before the code is committed.

## When to Use This Skill

- After implementing a feature or fix (before committing)
- When the user asks to "simplify", "review", "clean up", or "check quality"
- After a large refactoring
- When reviewing a PR or diff
- Proactively, when you notice code growing complex during implementation

## When NOT to Use This Skill

- Single-line changes, typo fixes, config updates
- Pure documentation changes
- Changes already reviewed and approved

---

## Review Process

Run three review passes in sequence. Each pass examines the changed code from a different angle.

### Pass 1: Reuse and Duplication

Check whether the new code duplicates existing functionality or misses reuse opportunities.

**Checklist:**
- [ ] Search existing utility/helper directories for functions that could replace new code
- [ ] Check if any new functions duplicate logic already present elsewhere in the codebase
- [ ] Look for hand-rolled implementations of common operations (string manipulation, path handling, date formatting, type guards, validation) where a library function or existing utility exists
- [ ] Check if inline logic could be replaced with an existing function call
- [ ] Look for parallel implementations across files encoding the same concept differently
- [ ] Verify no copy-paste from elsewhere in the codebase that should be a shared function

**The Rule of Three:** Don't extract an abstraction until there are 3 instances. Two similar blocks is coincidence — three reveals a pattern. Premature abstraction is worse than duplication.

**Exceptions — abstract immediately:**
- Security-sensitive logic (auth, validation, sanitization) — must be centralized
- Configuration values — single source of truth
- Narrow utilities with clear, stable contracts (parsing, formatting)

---

### Pass 2: Simplicity and Quality

Check whether the code is as simple as it can be while remaining correct and readable.

**Complexity checks:**
- [ ] Functions longer than 50 lines — candidate for extraction
- [ ] Functions with more than 4 parameters — candidate for a config/options object
- [ ] Nesting deeper than 3 levels — use guard clauses / early returns to flatten
- [ ] Cyclomatic/cognitive complexity is high — decompose the function

**Over-engineering checks:**
- [ ] Factory, strategy, builder, or observer pattern with only one implementation — inline it
- [ ] Abstract base class with a single concrete class — remove the abstraction
- [ ] Generic type parameters where a concrete type would suffice — simplify
- [ ] Code parameterized for cases that don't exist yet (YAGNI) — remove the parameters
- [ ] Wrapper/adapter that just delegates to another function — remove the indirection
- [ ] New module/class for something a function would handle — use a function

**Dead code checks:**
- [ ] Unused imports
- [ ] Unused variables, functions, or class methods
- [ ] Unreachable code after return/throw/break
- [ ] Commented-out code blocks (use version control, not comments)
- [ ] Feature flags that are permanently on or off
- [ ] TODO/FIXME comments pointing to removed functionality

**Naming checks:**
- [ ] Names communicate purpose — not `data`, `result`, `temp`, `item`, `val`
- [ ] Boolean variables use `is_`/`has_`/`can_`/`should_` prefixes
- [ ] Functions are verb phrases, classes are noun phrases
- [ ] Names follow project/language conventions (camelCase, snake_case, etc.)
- [ ] No abbreviations that obscure meaning

**Comment checks:**
- [ ] No comments explaining *what* the code does — simplify the code instead
- [ ] Comments explain *why* — business context, non-obvious constraints, gotchas
- [ ] No redundant doc comments on obvious methods
- [ ] No AI-generated navigation marker comments
- [ ] Regex and complex algorithms have explanation comments

**Error handling checks:**
- [ ] No empty catch blocks swallowing errors
- [ ] No catch-all `catch (Exception e)` when specific types should be caught
- [ ] No catch-and-rethrow without adding value (remove the try-catch)
- [ ] No try-catch around code that cannot actually throw
- [ ] Errors are handled at the appropriate layer, not at every layer
- [ ] Error messages include enough context for debugging

---

### Pass 3: Security and Performance

Check for vulnerabilities and performance issues in the changed code.

**Security checks (OWASP-aligned):**
- [ ] No string concatenation in SQL/NoSQL queries — use parameterized queries
- [ ] No user input flowing to `exec()`, `system()`, `eval()`, or shell commands
- [ ] No user input rendered in HTML without escaping (XSS)
- [ ] No hardcoded credentials, API keys, tokens, or secrets in code
- [ ] No sensitive data in logs (passwords, tokens, PII)
- [ ] Authorization checked on all routes and endpoints — default deny
- [ ] IDOR prevention: ownership verification on resource access (`WHERE id = ? AND owner_id = ?`)
- [ ] File uploads validated (type, size, content) — no path traversal
- [ ] Cryptography: no MD5, SHA-1, DES, RC4 — use AES-256, bcrypt/Argon2id
- [ ] JWT: reject `none` algorithm, enforce expiration
- [ ] Cookies: HttpOnly, Secure, SameSite flags set
- [ ] No `console.log`/`print` of sensitive data left in production code

**Performance checks:**
- [ ] No database queries inside loops (N+1 problem) — use batch/DataLoader
- [ ] No full table scans where indexed lookups would work
- [ ] No `SELECT *` — select only needed columns
- [ ] No unbounded data structures (lists/maps that grow without limits)
- [ ] No expensive computation in hot paths or render loops
- [ ] No missing pagination for large datasets
- [ ] No redundant computation (same value calculated multiple times)
- [ ] No blocking operations in async contexts
- [ ] Resources are cleaned up (file handles, connections, event listeners, timers)
- [ ] No O(n²) algorithms where O(n log n) alternatives exist

**Magic values:**
- [ ] No magic numbers — use named constants
- [ ] No hardcoded strings that should be configuration (URLs, timeouts, limits)
- [ ] No hardcoded file paths that vary by environment

---

## Language-Specific Checks

Apply the relevant language checks after the three main passes.

### TypeScript / JavaScript
- [ ] Use optional chaining (`?.`) and nullish coalescing (`??`) instead of verbose null checks
- [ ] Use `??` not `||` when `0`, `""`, or `false` are valid values
- [ ] Use `as const` for literal types, `satisfies` for type-safe object literals
- [ ] Remove type annotations the compiler can infer (return types, obvious variable types)
- [ ] Prefer discriminated unions over type assertions
- [ ] No `any` types — use `unknown` and narrow

### Python
- [ ] Use comprehensions instead of explicit loops for simple transforms
- [ ] Use `pathlib.Path` instead of `os.path`
- [ ] Use `@dataclass` or `NamedTuple` instead of manual `__init__`/`__repr__`
- [ ] Use f-strings instead of `.format()` or `%`
- [ ] Use `contextlib` / `with` for resource management
- [ ] Remove unused imports (`autoflake`)

### Go
- [ ] Run `gofmt` / `goimports`
- [ ] Handle every error — never discard with `_`
- [ ] Error strings: lowercase, no punctuation
- [ ] Indent error flow, keep happy path at minimal indentation
- [ ] Interfaces in consuming package, not implementing package — keep small (1-2 methods)
- [ ] Use `crypto/rand` never `math/rand` for security-sensitive values
- [ ] Wrap errors with `%w` for `errors.Is()` / `errors.As()` chains

### Rust
- [ ] No unnecessary `.clone()` — use references
- [ ] No `.unwrap()` — use `?`, `.unwrap_or()`, `.unwrap_or_default()`
- [ ] Use iterators instead of manual index loops
- [ ] Use `if let` / `while let` for single-arm matches
- [ ] Run `cargo clippy` and address warnings

### Java
- [ ] Use Streams API instead of imperative loops where appropriate
- [ ] Use `Optional` instead of null checks
- [ ] Override `hashCode` when overriding `equals`
- [ ] Keep everything `private` by default
- [ ] Order exception handlers from most specific to least specific

---

## Test Quality Checks

If the change includes tests, review them too.

- [ ] Tests assert meaningful outcomes, not just "no error thrown"
- [ ] Test names describe the behavior being tested (`should_reject_expired_token`)
- [ ] Edge cases and boundary values are covered
- [ ] Error paths are tested (invalid input, unauthorized access, timeouts)
- [ ] Tests are independent — no shared mutable state between tests
- [ ] No logic in tests (loops, conditionals) — tests should be straightforward
- [ ] Tests actually fail when the code breaks (not just green-path theater)
- [ ] No hardcoded test data that duplicates production constants

---

## Applying Fixes

After identifying issues:

1. **Prioritize by severity:**
   - **Critical** — security vulnerabilities, data loss risks, correctness bugs → fix immediately
   - **High** — N+1 queries, missing error handling, duplicated logic → fix now
   - **Medium** — naming, complexity, dead code → fix now if quick, else note for later
   - **Low** — minor style, optional simplifications → fix if trivial

2. **Fix one issue at a time.** Don't batch unrelated changes.

3. **Run tests after each fix.** Never assume a "safe" simplification doesn't break something.

4. **Don't introduce new features during review.** Review is for simplifying and fixing, not for adding.

---

## Common AI-Generated Code Smells

These are patterns AI agents frequently introduce. Watch for them specifically.

| Smell | Detection | Fix |
|-------|-----------|-----|
| **Over-engineering** | Factory/strategy/builder for a single use case | Inline to a function |
| **Excessive comments** | Comments restating what code does | Delete them; simplify code if needed |
| **Verbose boilerplate** | 2-3x more code than needed for the requirement | Rewrite using language idioms |
| **Cargo cult code** | Patterns copied without understanding (try-catch around non-throwing code, defensive null checks on non-nullable values) | Remove the unnecessary code |
| **Missing guard clauses** | Deep nesting instead of early returns | Invert conditions, return early |
| **Inconsistent error handling** | Different error patterns in the same module | Standardize to one approach |
| **Speculative generality** | Parameterized for hypothetical future needs | Remove unused parameters/generics |
| **God function** | One function handling routing, logic, IO, error handling | Extract into focused functions |
| **Domain collapse** | Each iteration degrades structure slightly | Restore clean boundaries |
| **Hallucinated APIs** | Using functions/methods that don't exist | Verify against actual library docs |

---

## Review Summary Template

After completing the review, present findings concisely:

```markdown
## Review Summary

### Critical
- [Issue and fix applied]

### Improvements Made
- [Simplification, deduplication, or fix applied]

### Noted (not fixed)
- [Low-priority items for future consideration]

### Verified
- Tests pass
- No security issues found
- No performance regressions
```

---

## Quick Reference

```
PASS 1 — REUSE:
  Does this duplicate existing code?
  Could an existing utility replace this?
  Is there a library function for this?

PASS 2 — SIMPLICITY:
  Is this the simplest solution that works?
  Can any abstractions be removed?
  Is there dead code to remove?
  Are names clear? Are comments necessary?
  Is error handling appropriate (not excessive)?

PASS 3 — SECURITY & PERFORMANCE:
  Any injection risks? Hardcoded secrets?
  Any N+1 queries? Unbounded structures?
  Any missing auth checks? Data exposure?

THEN:
  Apply language-specific checks
  Review tests if present
  Fix critical/high issues
  Run tests after each fix
  Present summary
```
