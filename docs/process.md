# AI-Native SDLC — Intelligent Edge Orchestrator

Status: v1 · Referenced by: `constitution.md`

Map of how the process documents link: `docs/docs-map.md`.

## 1. Sources of truth

| Artifact | Role | Owner |
| --- | --- | --- |
| `docs/system-overview.md` | How the system works and why | Human |
| `SPEC.md` | Implementation contract (MUST/SHOULD, §17 tests, §18 checklist) | Human (agent drafts) |
| `docs/adr/` | Decisions on anything the spec leaves "implementation-defined" | Human (agent drafts) |
| Tests | The executable spec; the acceptance gate | Agent writes, human reviews |
| Code | Implements the spec; never the source of truth | Agent |
| `CLAUDE.md` | Rules every agent session follows | Human |
| `docs/coding-guidelines.md` | Coding rules (APOSD-based), cited by ID in plans and reviews | Human |
| `REVIEW.md` | AI review checklist and severity levels | Human |

Order of authority: Margo → `SPEC.md` → ADRs → code.

## 2. Principles

1. Spec first: no behavior change without a `SPEC.md` change in the same PR.
2. Tests before code: §17 tests are written and seen failing before implementation.
3. Small slices: one Appendix B step or smaller per PR, reviewable in about 15 minutes.
4. Traceable: code comments, test names, commits and PRs cite § numbers.
5. Machine-verified: CI gates every PR; the agent runs the same checks before pushing.
6. Humans own intent; agents own execution.
7. A vague result is fixed in the spec or tests, not only in the code.

## 3. The loop (every change)

| # | Stage | Output | Gate |
| --- | --- | --- | --- |
| 1 | Intent | Issue: problem, affected spec sections | Human agrees it's worth doing |
| 2 | Spec | `SPEC.md` edit: rules, `[IEO]`/`[Margo]` tag, §17 bullets; ADR if needed | Human approves spec diff |
| 3 | Plan | Agent plan: sections, files, tests, out-of-scope | Human approves plan |
| 4 | Tests | §17 tests written, failing for the right reason | Human reviews tests |
| 5 | Implement | Code until tests pass; `// SPEC §x.y` comments | All local checks green |
| 6 | Verify | CI: build, vet, lint, unit tests (+ §17.8 integration when available) | CI green |
| 7 | Review | AI: `/code-review`, `/security-review` (for §15 areas). Human: "matches spec?" | All threads resolved |
| 8 | Merge | Squash merge; §18 checklist updated | Spec, overview and code agree |
| 9 | Learn | Update `CLAUDE.md`/spec from what went wrong | — |

## 4. Roles

- **Human:** intent, spec, plan approval, test review, architecture, ADRs, merge.
- **Agent:** plans, tests, code, local checks, CI fixes, review responses, keeping the spec in sync.
- **AI reviewer:** correctness and security findings; never approves or merges.

## 5. Definition of done (per PR)

- [ ] Spec sections affected are listed in the PR
- [ ] `SPEC.md` (and the overview, if the design changed) updated in the same PR
- [ ] §17 tests added or updated, named after the bullets they cover
- [ ] CI green on the latest commit
- [ ] AI review findings resolved or answered
- [ ] ADR added for any implementation-defined choice
- [ ] §18 checklist items ticked where applicable

## 6. Guardrails

- Agents never push to `main`, skip or disable tests, or merge.
- Agents stay inside the slice named in the plan.
- Security-sensitive areas (§15: TLS, credentials, archive extraction) always get `/security-review` and human review.
- No secrets in prompts, logs, tests or fixtures.
- Intelligent features (placement scoring, anomaly detection, assistants) advise; the deterministic core decides, with a baseline fallback.

## 7. Repo enablers

| Enabler | Purpose |
| --- | --- |
| `CLAUDE.md` | Agent rules, repo map, commands to run |
| `docs/coding-guidelines.md` | Rules applied at Plan, Implement and Review |
| `REVIEW.md` | Guides `/code-review`: what to check, severity, red flags |
| `.github/workflows/ci.yaml` | PR gate: build, vet, golangci-lint, `go test -race` |
| SessionStart hook | Cloud sessions can run the same checks |
| PR template | Definition of done as checkboxes |
| ADR template | Consistent decision records |
| Conformance script | Lists §17 bullets with no matching test |

## 8. Metrics

- Conformance coverage: % of §17 bullets with a passing test
- Spec drift: PRs where behavior changed without a spec change (target 0)
- CI green on first push
- Review rework: rounds of fixes per PR

## 9. Build sequence

Setup (§7 enablers) → Appendix B steps 1–6, one slice per PR → intelligent features, each entering as a new `[IEO]` spec section.
