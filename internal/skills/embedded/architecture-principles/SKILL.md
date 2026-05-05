---
name: architecture-principles
description: Consult before making design decisions — choosing boundaries, deciding whether to split a class/module, picking an abstraction, evaluating whether DDD or a particular architecture style is warranted. Triggers "is this violating SOLID?", "should I extract this?", "bounded context", "clean architecture", "am I over-engineering?", "what's the right abstraction here?". Covers SOLID, clean-code rules-of-thumb, DDD vocabulary, architecture styles, and — crucially — when each is and isn't worth the cost. Distinct from code-review (reactive smell detection) and review-implementing (applying someone else's feedback).
version: 1.0.0
---

# Architecture principles

**Portability:** Language- and framework-agnostic. The principles apply; the syntax doesn't.

A decision aid for design choices *before and during* coding. Prefers the smallest design that meets today's requirement plus one near-term change, and explicitly resists applying principles for their own sake.

## Core stance

> **Principles are lenses, not laws.** Apply them when the cost of not applying them is about to bite. Don't refactor for SOLID because "the rule says so" — refactor because the current shape is already hurting.

Three questions, in order, before invoking any principle below:

1. **What's actually going wrong right now?** (Change is painful? Reading is hard? Tests are hard to write?)
2. **Which principle addresses exactly that pain?**
3. **What's the smallest change that relieves it?**

If step 1 has no concrete answer, stop. You're about to over-engineer.

## When to use this skill

- Deciding whether to extract a class, module, or service
- Naming a new abstraction or choosing between two candidate shapes
- Evaluating whether DDD / hexagonal / clean-architecture patterns are worth their weight for this project
- Reviewing a design proposal (yours or someone else's) before implementation
- You catch yourself writing "-Manager", "-Helper", "-Util", "Abstract-", "-Factory-Factory"
- The user asks about SOLID, DDD, clean code, design patterns, or architecture trade-offs

## When NOT to use this skill

- The task is a narrow bugfix with no design surface area
- Throwaway scripts, glue code, or prototypes
- The codebase has strong existing conventions — follow those first
- You're reviewing *already-written* code for smells — that's `code-review`
- You're applying a reviewer's feedback to code — that's `review-implementing`

---

## SOLID (pocket reference)

Five principles from Robert Martin. Each has a decision hint and a "skip when" clause.

### S — Single Responsibility

> A module should have one reason to change.

"Reason to change" = one stakeholder or axis of variation. A class that serves both the UI and the persistence layer has two reasons; they'll pull it in opposite directions.

**Apply when:** you keep editing the same class for unrelated reasons.
**Skip when:** the "two reasons" are actually the same team / same lifecycle — premature splitting adds coordination cost.

### O — Open / Closed

> Open for extension, closed for modification.

Usually achieved via polymorphism, strategy objects, or plugin points. Means you can add behaviour without editing the existing code path.

**Apply when:** a third variant is coming and you can name it.
**Skip when:** you only have one or two variants and speculation about a third is fiction. YAGNI wins.

### L — Liskov Substitution

> Subtypes must be usable wherever their supertype is, without surprising the caller.

If `Square extends Rectangle` but `setWidth` also changes height, you've broken Liskov. Violations usually show up as `if (x instanceof …) throw` or copy-pasted test suites.

**Apply when:** inheritance is involved.
**Skip when:** using composition instead (usually the better choice).

### I — Interface Segregation

> Clients shouldn't be forced to depend on methods they don't use.

One fat interface with 15 methods forces every implementer to stub the 12 they don't care about, and every mock to account for all 15.

**Apply when:** interface consumers use disjoint subsets.
**Skip when:** there's only one implementer or all consumers use most methods.

### D — Dependency Inversion

> Depend on abstractions, not concretions. High-level policy shouldn't know about low-level mechanism.

The business rule depends on `PaymentGateway` (interface), not `StripeAPI` (concrete). Lets you test without Stripe and swap providers.

**Apply when:** the concrete dependency is external, slow, expensive, or uncertain.
**Skip when:** the dependency is internal, stable, and test-friendly — injecting an interface is just ceremony.

---

## Clean-code essentials

Rules-of-thumb from Robert Martin's *Clean Code* and Ousterhout's *A Philosophy of Software Design*. These fight with each other at the margins — apply judgement.

- **Names reveal intent.** If a name needs a comment to explain it, rename it. Long names for concepts used locally are fine; short names for widely-used concepts are better.
- **Small functions beat big ones — until they don't.** Extract when a function has *conceptual* layers. Don't extract just to hit a line count; fragmenting a linear 40-line algorithm into ten 4-line helpers *harms* readability.
- **DRY — but only for real duplication.** Two pieces of code that happen to look alike today aren't duplication; they're coincidence. Extract when they *will* change together.
- **YAGNI (You Aren't Gonna Need It).** Don't build for speculative futures. Every abstraction has a carrying cost: tests, docs, mental model, review time.
- **KISS (Keep It Simple, Stupid).** The simplest thing that could possibly work, not the simplest thing you could think of.
- **Command-Query Separation (CQS).** A function either changes state or returns a value — not both. `stack.pop()` is a classic violation; it's usually tolerable but name it honestly.
- **Tell, don't ask.** Prefer `order.markShipped()` over `if (order.status == …) order.status = …` at the call site. Keeps invariants where the data lives.
- **Minimize mutable state.** Immutable data is easier to reason about, test, and parallelize. Mutation is a capability, not a default.

### Ousterhout vs. Martin

They disagree on function size. Ousterhout argues for **deep modules** (small interface, large implementation); Martin argues for **small functions everywhere**. Ousterhout is right more often in practice — fragmentation creates its own complexity.

---

## DDD vocabulary (strategic + tactical)

DDD is expensive. Use the vocabulary to communicate; use the full machinery only when the domain is genuinely complex and you have domain experts to talk to.

### Strategic

- **Bounded context** — an explicit boundary within which a single domain model is consistent. `Order` in the Shipping context may be a different shape from `Order` in Billing.
- **Ubiquitous language** — names in the code match names the domain experts use, within one bounded context.
- **Context map** — how bounded contexts relate: shared kernel, customer/supplier, anti-corruption layer, conformist, open-host, published language.

**Apply when:** two parts of the system disagree on what `User` / `Order` / `Account` means; integration with external systems feels consistently painful; non-dev stakeholders disagree on terminology.
**Skip when:** the whole system is one team, one domain, and the terminology is already shared — explicit contexts are ceremony.

### Tactical

- **Entity** — has identity that persists across state changes (`User` with an ID).
- **Value object** — identity is its value; immutable (`Money`, `EmailAddress`, `DateRange`). Two `Money(5, USD)` are equal.
- **Aggregate** — a cluster of entities/values with an **aggregate root** that enforces invariants. Transactions never cross aggregate boundaries.
- **Aggregate root** — the only entity outside can reference; everything inside is reached through it.
- **Domain event** — something meaningful happened (`OrderPlaced`, `PaymentFailed`). Names are past-tense verbs.
- **Domain service** — operation that doesn't naturally belong to an entity or value (e.g. `TransferFunds` spanning two accounts).
- **Repository** — collection-like interface for loading/saving aggregates, hiding persistence.
- **Factory** — encapsulates construction of a complex aggregate; use when the constructor can't express the invariant.

**Apply when:** invariants span multiple entities; the domain has meaningful concepts beyond CRUD; multiple operations keep reimplementing the same invariant check.
**Skip when:** the domain is mostly CRUD — a thin service layer over an ORM is simpler and honest about what's there.

### Red flag: **anemic domain model**

Entities are just data bags with getters/setters; all behaviour lives in services. Symptom: every change requires editing three layers. Fix by moving logic onto the entity so invariants live with the data.

---

## Architecture styles at a glance

Each style is a trade-off between coupling, deployment complexity, and team independence. Pick the *least* architecture that fits.

| Style | Core idea | Use when | Avoid when |
|---|---|---|---|
| **Layered** (presentation / app / domain / infra) | Strict top-down dependencies | Small-to-mid apps, one team | Business logic ends up in controllers ("skinny domain") |
| **Hexagonal / ports-and-adapters** | Domain core surrounded by ports (interfaces) and adapters (impls) | You want the core testable without IO; multiple delivery mechanisms (HTTP + CLI + queue) | Trivial CRUD — overhead per feature is real |
| **Clean architecture** | Hexagonal + explicit use-case layer + dependency rule (deps point inward) | Medium-to-large apps, long-lived, multiple teams | Small apps — the ritual dominates the payoff |
| **Modular monolith** | One deploy, strong internal module boundaries | Most startups most of the time | You genuinely need independent deploy / scale / data |
| **Microservices** | Independent deploys, separate data stores | Multiple teams with different release cadences, independent scaling needs, strict failure isolation | You don't have the platform maturity — distributed transactions, debugging, and deploys will crush you |
| **Event-driven** | Components communicate via events | Loose coupling across contexts; async flows | You need read-after-write consistency on the same call |

**Default:** Start with a modular monolith in a layered or hexagonal shape. Extract services when a module repeatedly proves it has a different release/scale/team profile — not before.

---

## Decision heuristics

### "Should I extract this into a new class / module?"

Extract when **two** of these are true:
- It has its own reason to change (new owner, different cadence).
- It has a distinct responsibility that can be named in one noun.
- It's tested or testable independently.
- It's reused in ≥2 places *now* (not hypothetically).

Don't extract because the file is long. Don't extract because "it feels like a lot." Don't extract on the first occurrence — wait for the second.

### "Should I introduce an interface / abstraction?"

Introduce when:
- There are ≥2 real implementations, or an imminent second one you can name.
- The dependency crosses a boundary you can't test without mocking (network, filesystem, clock, randomness).
- The consumer needs to swap implementations at runtime.

Don't introduce an interface for a single implementation "just in case." You can always extract later; you can rarely remove an abstraction without pain.

### "Should I use DDD patterns?"

Use them when:
- The domain has rules that aren't obvious from CRUD operations.
- Stakeholders use different words for the same thing, or the same word for different things.
- You have (or can get) access to domain experts.

Don't use them for an admin panel over an ORM.

### "Is this over-engineered?"

Signs that yes:
- Abstractions have only one implementer.
- Folder structure has more levels than the domain has concepts.
- You wrote a `-Factory` for something that has a simple constructor.
- Reviewers keep asking "what does this class do?"
- Adding a new feature requires editing ≥3 layers that each just forward the call.

### "Is this under-engineered?"

Signs that yes:
- One module imports from half the others.
- Same business rule implemented in multiple places, subtly different.
- Tests require booting the whole system.
- Bug reports consistently cite the same "god" file.

---

## Anti-patterns to avoid

- **God class / god module** — one place that "knows everything." Split by reason-to-change.
- **Anemic domain model** — data classes with no behaviour. Push logic onto the entity.
- **Premature abstraction / speculative generality** — interfaces, hooks, and extension points with one implementer and no concrete second use case.
- **Leaky abstraction** — the abstraction forces callers to know internals (e.g., "pass `null` to skip caching"). Redesign the seam.
- **Primitive obsession** — passing raw `string`/`int` everywhere for concepts that deserve types (`UserId`, `Currency`, `EmailAddress`). Value objects fix this.
- **Circular dependencies** — module A imports B imports A. Usually reveals a missing abstraction or misplaced code.
- **Shotgun surgery** — one logical change requires edits in many places. The thing that changes together should live together.
- **Feature envy** — method on class A mostly calls methods on class B. Move the method to B.
- **Cargo-cult clean architecture** — four folders of interfaces, one implementer each, for a CRUD admin. The ritual without the payoff.
- **"-Manager" / "-Helper" / "-Util"** — names that carry no information. If you can't name it better, you probably haven't isolated a real concept.

---

## Quick cross-references

- Writing or modifying code that needs self-review after: `code-review`
- Applying feedback a reviewer gave you: `review-implementing`
- Writing tests first (TDD): `test-driven-development`
- Reducing complexity in existing code: `simplify` (user skill)
- Root-causing a bug that hints at a design problem: `root-cause-tracing`

## References

- Robert C. Martin — *Clean Code* (naming, functions, smells) and *Clean Architecture* (dependency rule, use cases)
- John Ousterhout — *A Philosophy of Software Design* (deep modules, information hiding; often corrects *Clean Code*'s small-function obsession)
- Eric Evans — *Domain-Driven Design* (the original; dense)
- Vaughn Vernon — *Implementing Domain-Driven Design* (more practical)
- Martin Fowler — [refactoring.com catalogue](https://refactoring.com/catalog/), [bliki](https://martinfowler.com/bliki/)
- Michael Feathers — *Working Effectively with Legacy Code* (seams, characterization tests)
- Gregor Hohpe — *Enterprise Integration Patterns* (for event-driven and async)
