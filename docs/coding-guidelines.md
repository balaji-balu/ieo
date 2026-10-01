# Coding Guidelines — Intelligent Edge Orchestrator

Status: v1 · Referenced by: `CLAUDE.md`, `REVIEW.md`, `docs/process.md`

These guidelines apply in the execution loop (`docs/process.md` §3, stages 3–9). They are based on
John Ousterhout's *A Philosophy of Software Design* (APOSD), plus Go and repository conventions.

The goal is to **reduce complexity**. Complexity shows up as *change amplification* (one change
touches many places), *cognitive load* (too much to know before making a change) and *unknown
unknowns* (it is not obvious what must change). It comes from **dependencies** and **obscurity**.
Every rule below targets one of those two.

Each rule has an ID (for review comments: "violates G-A2"), the **red flag** that shows it is broken,
and the loop stage where it is applied.

## A. Module design

Applied at **Plan** (stage 3); checked at human **Review** (stage 7).

| ID | Rule | Red flag |
| --- | --- | --- |
| G-A1 | **Deep modules.** A module's interface should be much simpler than its implementation. Prefer few, powerful operations over many thin ones. | *Shallow module*: the interface is about as complex as what it does. |
| G-A2 | **Information hiding.** Each design decision (a wire format, a repo layout, a state encoding) lives in exactly one module. | *Information leakage*: the same knowledge coded in two places, e.g. CO and LO both parsing the manifest layout. |
| G-A3 | **Split by knowledge, not by order of execution.** | *Temporal decomposition*: `fetch.go` / `parse.go` / `apply.go` that all have to know the same format. |
| G-A4 | **Different layer, different abstraction.** Each layer adds meaning to what it wraps. | *Pass-through method or variable*: a function that only forwards its arguments, or a parameter threaded through layers that don't use it. |
| G-A5 | **Pull complexity downward.** The module deals with the complexity so its callers don't have to. Give sensible defaults. | *Overexposure*: callers must set knobs they don't understand to do the common thing. |
| G-A6 | **Somewhat general-purpose interfaces.** Keep policy (placement scoring, Margo status rules) out of generic mechanisms (scheduler, NATS wrapper, store). | *Special-general mixture*: business-specific cases in a general utility. |
| G-A7 | **Design it twice.** Every plan that adds or changes a module names at least two options and why one was chosen. | The plan shows only one design. |
| G-A8 | **Keep things together that belong together, and apart that don't.** Merge code that shares information or is always used together; separate general code from special-purpose code. | *Conjoined methods*: one function can't be understood without reading another. *Repetition*: the same code pattern appears in several places. |

Repository examples:

- One runtime interface (`Apply`, `Remove`, `Status`) hides Compose, containerd, wasm and helm from
  the rest of EN. LO never sees runtime-specific types (G-A1, G-A2).
- The State Manifest format (§8.1) is encoded and decoded by one package shared by CO and LO (G-A2).
- The LO reconciler owns the whole desired → actual cycle for a host (§8.5), not three packages that
  each handle one phase (G-A3).

## B. Errors and special cases

Applied at **Implement** (stage 5).

| ID | Rule | Red flag |
| --- | --- | --- |
| G-B1 | **Define errors out of existence.** Make operations idempotent: removing a deployment that's already gone succeeds; applying the same digest twice is a no-op (§7.6). | Callers each handle "already exists" / "not found" in their own way. |
| G-B2 | **Mask or aggregate errors at one level.** Retries, backoff and reconnects live in one place (e.g. the NATS or CO client), not in every caller. | The same `if err != nil { retry… }` block copied across packages. |
| G-B3 | **Wrap with context; never swallow.** `fmt.Errorf("apply %s: %w", deploymentID, err)`. Use `errors.Is/As` at boundaries. A deliberately ignored error has a comment saying why. | `_ = err`, log-and-return-nil, or an error message with no identifiers. |
| G-B4 | **Crash only on programmer errors.** No `panic` or `log.Fatal` outside `main`/startup. Runtime failures follow §14 recovery behavior. | `panic` in a request, sync or reconcile path. |
| G-B5 | **Few special cases.** Let the normal path handle edge cases (an empty list, a zero value) without extra branches. | Stacked `if len(x) == 0` / `if first` branches. |

## C. Comments, names and obviousness

Applied at **Implement**; checked at AI **Review**.

| ID | Rule | Red flag |
| --- | --- | --- |
| G-C1 | **Write the interface comment first.** Every exported type and function says what it does, its invariants, units and error behavior, before the body is written. If the comment is hard to write, the design is wrong. | *Hard to describe*: the comment needs "and also…" or lists special cases. |
| G-C2 | **Comments say what the code can't:** why, invariants, units, references. Implementation of a spec rule cites it: `// SPEC §8.5`. | *Comment repeats code*: `// increment i`. |
| G-C3 | **Interface comments describe use, not implementation.** | *Implementation contaminates interface*: a doc comment explaining internal maps or goroutines. |
| G-C4 | **Precise, consistent names.** A concept has one name everywhere, matching `SPEC.md` §4: `site`, `host`, `deployment`, `component`, `digest`, `manifestVersion`. | *Vague name* (`data`, `info`, `mgr`, `handle`); *hard to pick a name* (usually a sign of a muddled design). |
| G-C5 | **Obvious code.** A reader understands a function without tracing other files. Avoid hidden control flow (callbacks registered far away, goroutines started from getters). | *Nonobvious code*: the reviewer has to ask "what does this do?". |
| G-C6 | **Be consistent.** Follow the existing pattern in the package, even when you'd do it differently. Change a convention everywhere or nowhere. | Two styles of doing the same thing in one package. |

## D. Strategic programming

Applied throughout.

| ID | Rule |
| --- | --- |
| G-D1 | **Working code isn't enough.** Code that passes tests but makes the next change harder should not merge (the "tactical tornado" warning). |
| G-D2 | **Invest about 10–20% of each slice in design**, but only within the slice's scope (`docs/process.md` §6). |
| G-D3 | **Design problems outside the slice** become an issue or an ADR draft, not a wider PR. |
| G-D4 | **Leave code better than you found it**, in the files you are already changing. |

## E. Go and repository conventions

Mostly machine-checked (stage 6).

| ID | Rule |
| --- | --- |
| G-E1 | `gofmt`/`goimports` clean; `go vet` and `golangci-lint` clean on changed packages. |
| G-E2 | `context.Context` is the first parameter of anything that does I/O or blocks, and it is honored (cancellation, deadlines). |
| G-E3 | No package-level mutable state; dependencies are passed in explicitly (constructors, not `init()` globals). |
| G-E4 | Interfaces are defined where they are consumed, and kept small. Return concrete types. |
| G-E5 | Every goroutine has an owner and a way to stop; no goroutine leaks. Shared state is protected; tests run with `-race`. |
| G-E6 | Structured logs (zap) with the fields §13.1 requires: `deployment_id`, `digest`, `site_id`, `host_id`, `manifest_version`. |
| G-E7 | No secrets in code, logs, tests or fixtures (§15.4). Credentials come from config or the environment. |
| G-E8 | Generated code (`ent/`, protobuf) is regenerated with the tools, never edited by hand. |

## F. Tests

Applied at **Tests** (stage 4).

| ID | Rule |
| --- | --- |
| G-F1 | Tests exercise the **public interface**, not the internals, so refactors don't break them. |
| G-F2 | Each §17 bullet has a test named after it, e.g. `TestSpec_17_4_ReconcileIsIdempotent`, whose doc comment quotes the bullet's opening words: `// SPEC §17.4: "Reconciling twice…"` (ADR 0011). `go run ./tools/conformance` lists bullets without one. |
| G-F3 | A new test is seen **failing for the right reason** before the implementation is written. |
| G-F4 | Core Conformance tests are deterministic: fake registry, runtime and clock; no sleeps, no network. |
| G-F5 | Table-driven tests where cases share structure; each failure message names the case. |

## Who enforces what

| Layer | Rules | When |
| --- | --- | --- |
| Machine (fmt, vet, golangci-lint, `go test -race`, `go run ./tools/conformance`) | E, F2 (partly) | Locally before push; CI at stage 6 |
| AI reviewer (`/code-review`, guided by `REVIEW.md`) | B, C, E, plus A red flags visible in the diff | Stage 7 |
| Human reviewer | A (depth, boundaries, design it twice), D, spec fidelity | Stages 3 and 7 |

When the same finding shows up in two reviews, the Learn stage (stage 9) turns it into a
`CLAUDE.md` rule, or a lint rule if it can be automated, so it gets caught earlier and more cheaply.
