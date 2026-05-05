---
name: test-driven-development
description: Use when implementing any feature or bugfix, before writing implementation code — and whenever writing tests, adding coverage, or using TDD. Covers red-green-refactor, mandatory fail-then-pass verification, framework detection, patterns across languages, edge cases, and test quality. Aligns with strict test-first discipline (no production code without a failing test first).
version: 1.1.0
---

# Test-Driven Development

**Portability:** Applies to any language, test framework, and coding agent. Discover commands from project files (`package.json`, `Cargo.toml`, CI config, etc.) rather than assuming a single stack.

A structured approach to writing tests and implementing code using the red-green-refactor cycle. Tests are written **before** implementation to define expected behaviour, preventing the agent from writing tests that merely confirm broken code.

## Core Principle

> **Tests define behaviour. Implementation satisfies tests. Never the reverse.**
>
> An agent writing tests after implementation will unconsciously confirm whatever the code does — including bugs. Test-first prevents this.

### Strict test-first (iron law)

For new behaviour, treat this as non-negotiable unless the user explicitly opts out (throwaway spikes, generated-only code, or pure config):

**No production code without a failing test first.** If implementation was written before the test, delete the implementation and restart from RED — do not “keep it as reference” or adapt it while writing tests; that is still test-after.

**Mandatory verification:**

1. **RED:** Run the test; it must **fail** for the right reason (missing behaviour), not a typo or setup error.
2. **GREEN:** Implement the minimum to pass; run again — it must **pass**.
3. **REFACTOR:** Improve code and tests with the suite still green.

If a new test passes immediately, it is not proving anything — fix the test until it fails, then proceed.

This discipline is compatible with the detailed workflows below and matches the spirit of [obra/superpowers test-driven-development](https://github.com/obra/superpowers/tree/main/skills/test-driven-development).

---

## When to Use This Skill

- Implementing any new feature or function
- Fixing a bug (write the failing test first, then fix)
- Adding test coverage to existing code
- The user asks to "write tests", "add tests", "use TDD", or "test this"
- Refactoring code that lacks tests (add tests first, then refactor)

## When NOT to Use This Skill

- Pure configuration changes, documentation, or formatting
- Throwaway scripts or one-off explorations
- Changes to generated code that has its own test generation

---

## The Red-Green-Refactor Cycle

```
RED:      Write a failing test that defines the expected behaviour
          Run it → confirm it FAILS
          (If it passes, the test is wrong — it tests nothing)

GREEN:    Write the MINIMUM code to make the test pass
          Run it → confirm it PASSES
          (No extra features, no cleanup, no "while I'm here")

REFACTOR: Clean up the implementation and the test
          Run tests → confirm they still PASS
          (Improve structure without changing behaviour)

REPEAT for the next behaviour.
```

### Why This Order Matters for Agents

- **RED proves the test works.** A test that never fails catches nothing.
- **GREEN prevents over-engineering.** Writing only what's needed avoids YAGNI violations.
- **REFACTOR with a safety net.** Passing tests make refactoring safe.
- **Context pollution prevention.** If you write implementation first, your knowledge of the implementation leaks into the tests, producing tests that confirm whatever you built.

---

## Step 0: Discover the Test Setup

Before writing any tests, discover how the project runs tests.

### Framework Detection

Check these files in order:

| File | What to Look For |
|------|-----------------|
| `package.json` | `scripts.test` value, `devDependencies` for `jest`/`vitest`/`mocha` |
| `vitest.config.*` / `jest.config.*` | Framework config |
| `pyproject.toml` | `[tool.pytest]` section, deps for `pytest` |
| `Cargo.toml` | `[dev-dependencies]`, built-in `#[test]` |
| `go.mod` | Built-in `testing`, `testify` in requires |
| `pom.xml` / `build.gradle` | `<scope>test</scope>` deps, `testImplementation` |
| `Gemfile` | `group :test` for `rspec`/`minitest` |
| CI config (`.github/workflows/`) | Test commands in CI steps |

### Test Commands by Framework

| Framework | Run All | Run One File | Run One Test |
|-----------|---------|-------------|-------------|
| **Jest** | `npx jest` | `npx jest path/to/file.test.ts` | `npx jest -t "test name"` |
| **Vitest** | `npx vitest run` | `npx vitest run path/to/file.test.ts` | `npx vitest -t "test name"` |
| **pytest** | `pytest` | `pytest tests/test_api.py` | `pytest tests/test_api.py::test_login` |
| **Go** | `go test ./...` | `go test ./pkg/mypackage/` | `go test -run TestName ./pkg/` |
| **Cargo** | `cargo test` | `cargo test --test name` | `cargo test test_name -- --exact` |
| **JUnit/Maven** | `mvn test` | `mvn test -Dtest=TestClass` | `mvn test -Dtest=TestClass#method` |
| **JUnit/Gradle** | `gradle test` | `gradle test --tests TestClass` | `gradle test --tests "TestClass.method"` |
| **RSpec** | `bundle exec rspec` | `rspec spec/file_spec.rb` | `rspec spec/file_spec.rb:42` |
| **PHPUnit** | `vendor/bin/phpunit` | `vendor/bin/phpunit tests/File.php` | `vendor/bin/phpunit --filter method` |

### File Naming Conventions

| Language | Test File Pattern | Test Directory |
|----------|------------------|---------------|
| TypeScript/JS | `*.test.ts`, `*.spec.ts` | `__tests__/`, `tests/`, `test/` |
| Python | `test_*.py` | `tests/` |
| Go | `*_test.go` | Same directory as source |
| Rust | `#[cfg(test)] mod tests` inline; `tests/*.rs` for integration | `tests/` for integration |
| Java | `*Test.java` | `src/test/java/` |
| Ruby | `*_spec.rb` (RSpec), `test_*.rb` (Minitest) | `spec/`, `test/` |

### Establish a Green Baseline

**Before writing any new tests, run the existing suite.** If tests are already failing, note which ones and why. Never write new tests on top of a broken suite.

```
1. Run full test suite
2. If all pass → proceed
3. If some fail → note failures, assess if they're related to your task
4. Do not attempt to fix unrelated failures
```

---

## Writing Tests

### The Arrange-Act-Assert Pattern

Every test follows three phases:

```
ARRANGE: Set up test data and preconditions
ACT:     Execute the single behaviour under test
ASSERT:  Verify the expected outcome
```

Keep exactly **one Act** per test. If you need to test two behaviours, write two tests.

### Test Naming

Use plain English that describes the behaviour:

| Language | Convention | Example |
|----------|-----------|---------|
| JS/TS | `it('should ...')` or `test('...')` | `it('should reject expired tokens')` |
| Python | `test_` prefix, snake_case | `test_reject_expired_token` |
| Go | `Test` prefix, PascalCase | `TestRejectExpiredToken` |
| Rust | `#[test]`, snake_case | `fn test_reject_expired_token()` |
| Java | descriptive method name | `shouldRejectExpiredToken()` |

**Match the project's existing convention.** Sample 3-5 existing test files before writing new ones.

### What to Test

For each function or behaviour, write tests for:

1. **Happy path** — normal expected input produces correct output
2. **Edge cases** — boundary values, empty inputs, single elements
3. **Error cases** — invalid input, missing data, unauthorized access
4. **Null/absent values** — null, undefined, None, empty string, empty collection

### Edge Case Categories

| Category | Examples |
|----------|---------|
| **Boundaries** | 0, -1, 1, MAX_INT, MIN_INT, empty string, max-length string |
| **Empty/null** | `null`, `undefined`, `None`, `[]`, `{}`, `""` |
| **Special chars** | Unicode, emoji, RTL text, newlines, tabs, zero-width chars |
| **Adversarial** | SQL injection strings, XSS payloads, path traversal, extremely long input |
| **State** | Concurrent access, invalid state transitions, retry/idempotency |
| **Time** | Leap years, DST transitions, midnight, epoch boundaries, timezones |

### Parameterized / Table-Driven Tests

When testing the same logic with multiple inputs, use parameterized tests:

**Jest/Vitest:**
```javascript
test.each([
  [0, 0, 0],
  [1, 2, 3],
  [-1, 1, 0],
])('add(%i, %i) returns %i', (a, b, expected) => {
  expect(add(a, b)).toBe(expected);
});
```

**pytest:**
```python
@pytest.mark.parametrize("a,b,expected", [(0,0,0), (1,2,3), (-1,1,0)])
def test_add(a, b, expected):
    assert add(a, b) == expected
```

**Go (table-driven):**
```go
tests := []struct{ name string; a, b, want int }{
    {"zeros", 0, 0, 0},
    {"positive", 1, 2, 3},
    {"mixed", -1, 1, 0},
}
for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
        if got := Add(tt.a, tt.b); got != tt.want {
            t.Errorf("Add(%d,%d) = %d, want %d", tt.a, tt.b, got, tt.want)
        }
    })
}
```

---

## Test Isolation

Each test must be independent. No test relies on another test's output or side effects.

### Setup and Teardown

| Framework | Before Each | After Each | Before All | After All |
|-----------|------------|-----------|-----------|----------|
| Jest/Vitest | `beforeEach()` | `afterEach()` | `beforeAll()` | `afterAll()` |
| pytest | `@pytest.fixture` | yield-based cleanup | `scope="session"` | session cleanup |
| Go | `t.Cleanup(func)` | `t.Cleanup` (LIFO) | `TestMain(m)` | `TestMain` |
| JUnit 5 | `@BeforeEach` | `@AfterEach` | `@BeforeAll` | `@AfterAll` |
| RSpec | `before(:each)` | `after(:each)` | `before(:all)` | `after(:all)` |

### Test Data: Use Factories, Not Shared Fixtures

Create test data within each test using factory functions:

```python
def make_user(**overrides):
    defaults = {"name": "Test User", "email": "test@example.com", "active": True}
    return User(**{**defaults, **overrides})

# Each test overrides only what it cares about
def test_inactive_user_cannot_login():
    user = make_user(active=False)
    assert not user.can_login()
```

This is clearer than shared fixtures because the test data is visible in the test.

---

## Mocking

### When to Mock

- External third-party APIs you don't control
- Non-deterministic inputs (random, clocks, UUIDs)
- Slow or expensive resources (payment gateways, email services)
- Failure scenarios hard to reproduce (network timeouts, disk full)

### When NOT to Mock

- Internal code you own — test real interactions
- Simple value objects and data structures
- Database queries — use a test database or Testcontainers instead

### Anti-Patterns

- **Over-mocking:** Mocking the code under test itself
- **Implementation coupling:** Mocking internal method calls that break on refactoring
- **Testing the mock:** Verifying mock call counts instead of actual behaviour

**Rule of thumb:** Mock at architectural boundaries only. If you're mocking more than 2 dependencies, the code under test may need restructuring.

---

## Testing Errors and Exceptions

**Jest/Vitest:**
```javascript
expect(() => divide(1, 0)).toThrow('Cannot divide by zero');
await expect(fetchUser(-1)).rejects.toThrow(NotFoundError);
```

**pytest:**
```python
with pytest.raises(ZeroDivisionError, match="division by zero"):
    divide(1, 0)
```

**Go:**
```go
_, err := Divide(1, 0)
if !errors.Is(err, ErrDivisionByZero) {
    t.Errorf("expected ErrDivisionByZero, got %v", err)
}
```

**Rust:**
```rust
#[test]
#[should_panic(expected = "division by zero")]
fn test_divide_by_zero() { divide(1, 0); }
```

---

## Async Testing

**Jest/Vitest:**
```javascript
it('fetches user', async () => {
  const user = await fetchUser(1);
  expect(user.name).toBe('Alice');
});
```

**pytest-asyncio:**
```python
@pytest.mark.asyncio
async def test_async_fetch():
    result = await fetch_data(url)
    assert result["status"] == "ok"
```

**Fake timers (Jest/Vitest):**
```javascript
vi.useFakeTimers();
debounce(callback, 300)();
vi.advanceTimersByTime(300);
expect(callback).toHaveBeenCalledOnce();
vi.useRealTimers();
```

---

## Bug-to-Test Workflow

When fixing a bug, always write the regression test first:

```
1. Understand the bug: what is the expected vs actual behaviour?
2. Write a test that reproduces the exact bug scenario
3. Run the test → confirm it FAILS (proves the bug exists)
4. Fix the bug with the minimal change
5. Run the test → confirm it PASSES (proves the fix works)
6. The test permanently guards against regression
```

Every bug becomes a permanent regression test. This encodes institutional memory.

---

## Test Quality

### Meaningful Assertions

Every test must assert something specific. A test that runs code without checking results catches nothing.

```
BAD:  test runs without error                → catches nothing
OK:   expect(result).toBeDefined()           → catches null/undefined only
GOOD: expect(result.status).toBe('active')   → catches wrong status
BEST: expect(result).toEqual({ id: 1, status: 'active', name: 'Alice' })
                                              → catches any field regression
```

### Verify Tests Can Fail

After writing a test, confirm it actually catches bugs:

```
1. Temporarily break the code (e.g., return wrong value)
2. Run the test → must FAIL
3. Restore the code
4. Run the test → must PASS
```

If the test passes on broken code, the test is useless — rewrite it.

### The 40/60 Rule

Invest at least 40% of test cases in edge cases, error paths, and adversarial inputs. Happy-path-only testing is the signature of weak test suites.

### Coverage vs Quality

| Metric | What It Shows | Limitation |
|--------|-------------|-----------|
| Line coverage | Code was executed | Doesn't mean it was verified |
| Branch coverage | Both if/else paths taken | Catches ~30% more than line coverage |
| Mutation score | Tests detect injected bugs | Gold standard for test quality |

A test suite with 93% line coverage can have only 34% mutation score — meaning two-thirds of injected bugs go undetected. Use mutation testing to find these gaps:

| Language | Tool | Command |
|----------|------|---------|
| JS/TS | Stryker | `npx stryker run` |
| Python | mutmut | `mutmut run` |
| Java | PITest | `mvn pitest:mutationCoverage` |
| Rust | cargo-mutants | `cargo mutants` |

---

## Test Types and When to Use Each

### The Test Pyramid

```
         /  E2E  \          Few, slow, expensive
        / Integration \     Some, moderate speed
       /    Unit Tests  \   Many, fast, cheap
```

| Type | Speed | When to Run | Use For |
|------|-------|------------|---------|
| **Unit** | Milliseconds | Every edit | Pure logic, calculations, transformations |
| **Integration** | Seconds | At commit boundaries | API endpoints, database queries, multi-component flows |
| **E2E** | Minutes | In CI only | Critical user journeys, deployment verification |

**Agent guidance:**
- Write unit tests in the inner development loop (fast feedback)
- Run integration tests before committing
- Leave E2E tests for CI pipelines
- When in doubt between unit and integration, prefer integration — it catches real interaction bugs that mocked unit tests miss

---

## Database Testing

**Transaction rollback** (preferred for isolation):
```python
@pytest.fixture
def db(engine):
    conn = engine.connect()
    tx = conn.begin()
    session = Session(bind=conn)
    yield session
    tx.rollback()
    conn.close()
```

**Testcontainers** (for real database behaviour):
Spin up a real Postgres/MySQL/Redis in Docker per test suite. Accurate behaviour with full isolation.

**In-memory SQLite:** Fast but behaviour differs from production databases. Use only for simple cases.

---

## API Testing

**Pattern:** Test your own API against a real (or in-container) server. Mock only external third-party services.

**Node.js (supertest):**
```javascript
const res = await request(app)
  .post('/api/login')
  .send({ email: 'user@test.com', password: 'pass123' })
  .expect(200);
expect(res.body.token).toBeDefined();
```

**Python (TestClient):**
```python
client = TestClient(app)
response = client.get("/users/1")
assert response.status_code == 200
```

**Go (httptest):**
```go
req := httptest.NewRequest("GET", "/api/users", nil)
w := httptest.NewRecorder()
handler.ServeHTTP(w, req)
assert.Equal(t, http.StatusOK, w.Code)
```

---

## Anti-Patterns to Avoid

| Anti-Pattern | Why It Fails | Do Instead |
|-------------|-------------|-----------|
| Writing tests after implementation | Tests confirm bugs, not behaviour | Write tests first (red-green-refactor) |
| Tests without assertions | Always pass, catch nothing | Every test asserts specific outcomes |
| Testing implementation details | Breaks on refactor | Test public API and behaviour |
| Shared mutable state between tests | Order-dependent, flaky | Each test creates its own data |
| Excessive mocking | Tests mock behaviour, not real code | Mock at boundaries only |
| Chasing 100% coverage | Diminishing returns, brittle tests | Target 80% with high mutation score |
| Logic in tests (loops, conditionals) | Tests should be straightforward | Use parameterized tests instead |
| Snapshot testing for logic | Confirms current output, not correctness | Use specific assertions |
| Ignoring flaky tests | Erodes trust in the suite | Quarantine, investigate, fix |
| Not converting bugs to tests | Same bug can recur | Every fix includes a regression test |

---

## Handling Flaky Tests

When you encounter a test that passes and fails inconsistently:

```
1. Note it as flaky — do NOT "fix" by weakening assertions or adding sleeps
2. Retry once to confirm flakiness
3. If it passes on retry → continue, but flag for investigation
4. Common causes: timing/race conditions, shared state, external service dependency
5. Fix root cause, not symptoms
```

---

## Quick Reference

```
DISCOVER:
  Detect framework from project files
  Run existing tests → establish green baseline
  Sample existing test files → learn naming conventions

RED:
  Write failing test defining expected behaviour
  Run → confirm FAIL
  If it passes → test is wrong, rewrite

GREEN:
  Write MINIMUM code to pass the test
  Run → confirm PASS
  No extras, no cleanup yet

REFACTOR:
  Clean up implementation and tests
  Run → confirm still PASS

FOR BUGS:
  Write test reproducing the bug → confirm FAIL
  Fix the bug → confirm PASS
  Regression test is permanent

QUALITY CHECK:
  Break the code → test must FAIL
  40%+ tests cover edge cases and errors
  Use mutation testing for real quality measurement
```
