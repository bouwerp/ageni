---
name: test-fixing
description: Run tests and systematically fix all failing tests using smart error grouping. Use when user asks to fix failing tests, mentions test failures, runs test suite and failures occur, or requests to make tests pass.
---
# Test Fixing

**Portability:** Works with any coding agent. Use your environment’s commands to run tests, search code, and edit files. Follow the project’s docs (`AGENTS.md`, `CLAUDE.md`, etc.) for tooling choices.

Systematically identify and fix all failing tests using smart grouping strategies.

## When to Use

- Explicitly asks to fix tests ("fix these tests", "make tests pass")
- Reports test failures ("tests are failing", "test suite is broken")
- Completes implementation and wants tests passing
- Mentions CI/CD failures due to tests

## Systematic Approach

### 1. Initial Test Run

Discover how tests are run (see `test-driven-development` → framework detection): e.g. `make test`, `npm test`, `pytest`, `cargo test`. Run the project’s **full** suite (or the narrowest command the user asked for) to list failures.

Analyze output for:
- Total number of failures
- Error types and patterns
- Affected modules/files

### 2. Smart Error Grouping

Group similar failures by:
- **Error type**: ImportError, AttributeError, AssertionError, etc.
- **Module/file**: Same file causing multiple test failures
- **Root cause**: Missing dependencies, API changes, refactoring impacts

Prioritize groups by:
- Number of affected tests (highest impact first)
- Dependency order (fix infrastructure before functionality)

### 3. Systematic Fixing Process

For each group (starting with highest impact):

1. **Identify root cause**
   - Read relevant code
   - Check recent changes with `git diff`
   - Understand the error pattern

2. **Implement fix**
   - Apply code changes with your agent’s normal edit workflow
   - Follow project conventions (see repo docs and `test-driven-development` for command discovery)
   - Make minimal, focused changes

3. **Verify fix**
   - Run subset of tests for this group
   - Examples (use the stack’s real invocations):
     ```bash
     pytest tests/path/to/test_file.py -v
     pytest -k "pattern" -v
     npm test -- path/to/file.test.ts
     go test ./pkg/...
     ```
   - Ensure group passes before moving on

4. **Move to next group**

### 4. Fix Order Strategy

**Infrastructure first:**
- Import errors
- Missing dependencies
- Configuration issues

**Then API changes:**
- Function signature changes
- Module reorganization
- Renamed variables/functions

**Finally, logic issues:**
- Assertion failures
- Business logic bugs
- Edge case handling

### 5. Final Verification

After all groups fixed:
- Run the same full command used in step 1 (e.g. `make test`, `npm test`, `pytest`)
- Verify no regressions
- Check test coverage remains intact

## Best Practices

- Fix one group at a time
- Run focused tests after each fix
- Use `git diff` to understand recent changes
- Look for patterns in failures
- Don't move to next group until current passes
- Keep changes minimal and focused

## Example Workflow

User: "The tests are failing after my refactor"

1. Run full test command → 15 failures identified
2. Group errors:
   - 8 ImportErrors (module renamed)
   - 5 AttributeErrors (function signature changed)
   - 2 AssertionErrors (logic bugs)
3. Fix ImportErrors first → Run subset → Verify
4. Fix AttributeErrors → Run subset → Verify
5. Fix AssertionErrors → Run subset → Verify
6. Run full suite → All pass ✓