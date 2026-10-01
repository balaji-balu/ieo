# CLAUDE.md

Rules for every agent session in this repository. The process is in `docs/process.md`; coding rules
are in `docs/coding-guidelines.md`; review rules are in `REVIEW.md`.

## Sources of truth

Project principles: `constitution.md` (nothing here may contradict it).

Order of authority: Margo specification → `SPEC.md` → `docs/adr/` → code. Code is never the source
of truth.

- `SPEC.md` — implementation contract (MUST/SHOULD, §17 tests, §18 checklist, Appendix B order).
- `docs/intents/` — why a change exists: problem, outcome, acceptance criteria (`INT-NNN`).
- `docs/system-overview.md` — how the system works and why.
- `docs/adr/` — decisions on anything the spec calls "implementation-defined" (index in `docs/adr/README.md`).
- `docs/roadmap.md` — the ordered list of slices; work only on the slice named in the issue.

## Working rules

1. **Spec first.** No behavior change without a `SPEC.md` change in the same PR. If the spec is
   ambiguous or silent, stop and propose a spec edit or ADR; don't decide it in code.
2. **Plan before code.** State the § sections, files, tests and out-of-scope items, with at least two
   design options for any new or changed module (G-A7). Wait for approval.
3. **Tests before code.** Write the §17 tests first, named after their bullet
   (`TestSpec_17_4_…`), and show them failing for the right reason.
4. **Small slices.** One Appendix B step or smaller per PR. Stay inside the approved slice; record
   anything else as an issue or ADR draft.
5. **Traceability.** Cite § numbers in code comments (`// SPEC §8.5`), test names, commits and PRs;
   commits and PRs also cite the intent ID (`INT-NNN`) they serve.
6. **Follow `docs/coding-guidelines.md`.** Cite rule IDs (G-A1…) when explaining a design choice.
7. **Verify locally before every push** (commands below). Never push red.
8. **Build new, don't patch old (ADR 0002).** New code never imports the Git-based packages; delete
   old code in the PR that replaces it.
9. **Windows-friendly (ADR 0009).** The maintainer tests on Windows 11: no shell scripts (helpers are
   Go programs under `tools/`), use `filepath`, and keep Unix-only calls inside the EN runtime package.
10. **Intents.** A plan cites the intent ID it serves. Agents never set an intent's status to
    `accepted`; a human does.

## Guardrails

- Never push to `main`, merge, skip/disable tests, or weaken a test to make it pass.
- Never edit generated code by hand (`ent/` is generated; see `docs/contributing.md`).
- No secrets in prompts, code, logs, tests or fixtures.
- §15 areas (TLS, credentials, enrollment, archive extraction, §9.2 safety invariants) always get
  `/security-review` and human review.
- Intelligent features (placement scoring, anomaly detection, assistants) advise; the deterministic
  core decides, with a baseline fallback.

## Repository map

Go module `github.com/balaji-balu/ieo` (Go 1.25).

| Path | Contents |
| --- | --- |
| `cmd/co`, `internal/co`, `pkg/co` | Central Orchestrator (CO) |
| `cmd/lo`, `internal/lo` | Local Orchestrator (LO): reconciler, boltstore, watcher, actuators |
| `cmd/en`, `internal/en`, `pkg/en` | Edge Node agent (EN): runtime plugins, lifecycle, heartbeat |
| `cmd/edgectl` | Operator CLI |
| `internal/natsbroker`, `internal/streammanager` | NATS messaging |
| `internal/git*`, `internal/ocifetch` | Git-based delivery (being replaced, Appendix B step 2) and OCI fetch |
| `ent/`, `db/`, `atlas.hcl` | ent schema (generated), migrations |
| `pkg/model`, `pkg/deployment`, `pkg/application` | Shared domain types |
| `proto/` | Protobuf definitions |
| `configs/` | Component configs and FSM definitions |
| `deploy/` | Compose and Helm deployment |
| `tests/` | e2e tests, fixtures, seeds |

## Commands

Same checks as CI (`.github/workflows/ci.yaml`):

```sh
go build $(scripts/go-packages.sh)
go test -race -vet=off -count=1 $(scripts/go-packages.sh test)
# gofmt, goimports, govet, … on changed lines only:
golangci-lint run --new-from-merge-base=origin/main $(scripts/go-packages.sh test)
golangci-lint run --tests=false --new-from-merge-base=origin/main $(scripts/go-packages.sh no-test)
```

Local stack: `docker-compose -f docker-compose.dev.yaml up -d` (NATS, Postgres), then
`go run ./cmd/co`, `./cmd/lo`, `./cmd/en`. Schema changes: see `docs/contributing.md`.

Known baseline issues: in `scripts/go-packages.sh`, `KNOWN_BROKEN` packages don't compile and
`BROKEN_TESTS` packages have test files that don't compile; the rest of the legacy code has lint and
`go vet` findings. CI is a ratchet: it skips what is broken and lints only changed lines. Never add a
package to either list; a slice that fixes one removes it.

## Pull requests

- List the affected § sections; update `SPEC.md` (and the overview if the design changed).
- Include the §17 tests added or updated, and tick §18 checklist items where they apply.
- Run `/code-review` before asking for human review; resolve or answer every finding.

## Learning

When a review finding repeats, or a session goes wrong in a way a rule would have prevented, propose
an addition to this file or `docs/coding-guidelines.md` in the same PR (process stage 9).
