# 0011. How a TestSpec_ test names the §17 bullet it covers

Status: Proposed · Date: 2026-10-01 · Spec: §17 · Guideline: G-F2

## Context
G-F2 asks for one test per §17 bullet, named after it (`TestSpec_17_4_ReconcileIsIdempotent`). The
conformance tool (`go run ./tools/conformance`, roadmap 0.4) must say which bullets have no test.
Bullets have no IDs, and a free-text name can't be matched to a bullet reliably.

## Decision
- A §17 bullet is a top-level `- ` item under a `### 17.N` heading of `SPEC.md`. Prose and tables are
  not bullets (ADR 0010, §17.10). A heading marked `(RECOMMENDED)` is an integration profile; the
  others are Core.
- A `TestSpec_17_N_…` test's doc comment quotes the opening words of each bullet it covers:

  ```go
  // SPEC §17.4: "Reconciling twice with no change"
  func TestSpec_17_4_ReconcileIsIdempotent(t *testing.T) {
  ```

- A quote covers the bullet in its section whose text starts with the quote, after collapsing
  whitespace on both sides. It must match exactly one bullet; quote more words until it does. A quote
  may wrap across comment lines.
- One test may quote several bullets (a table-driven test). At least one quote must be from the
  section in the test's name.
- A test with no quote, or a quote that matches no bullet or several, covers nothing and is reported.
- `-strict` fails on a Core bullet without a test or on any test that covers nothing. Integration
  gaps are listed but never fail.

## Consequences
- No bullet IDs to keep up. Rewording a bullet's opening words turns its test into a reported
  orphan, which shows the spec change reached the tests (spec drift is visible).
- The requirement sits next to the test that checks it.
- Quotes add a line per test, and must be updated with the bullet's wording.

## Options considered
- Bullet ordinal in the name (`TestSpec_17_4_03_…`): inserting or reordering a bullet silently
  points tests at the wrong bullet.
- Count tests per section against bullets: can't say which bullet is missing.
