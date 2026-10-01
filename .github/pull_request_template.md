## Summary

<!-- What changes and why. Link the issue (process stage 1). -->

Closes #

Intent: INT-NNN <!-- from docs/intents/; "n/a" for tooling or docs-only PRs -->

## Spec sections affected

<!-- e.g. §8.5, §17.4. Write "None — no behavior change" for docs/tooling-only PRs. -->

- §

## Slice

<!-- Appendix B step (or smaller) this PR implements, and what is explicitly out of scope. -->

- Step:
- Out of scope:

## Design

<!-- For new or changed modules: the options considered and why this one (G-A7).
     Cite guideline IDs from docs/coding-guidelines.md where relevant. -->

## Tests

<!-- §17 tests added or updated, named after their bullet (G-F2).
     Confirm each new test was seen failing for the right reason first (G-F3). -->

- `TestSpec_17_…`

## Definition of done (docs/process.md §5)

- [ ] Spec sections affected are listed above
- [ ] `SPEC.md` (and `docs/system-overview.md`, if the design changed) updated in this PR
- [ ] §17 tests added or updated, named after the bullets they cover
- [ ] Local checks pass (commands in `CLAUDE.md`); CI green on the latest commit
- [ ] `/code-review` run; every finding resolved or answered
- [ ] `/security-review` run, if this touches §15 areas (TLS, credentials, enrollment, archive extraction, §9.2)
- [ ] ADR added for any implementation-defined choice
- [ ] §18 checklist items ticked where applicable
- [ ] No package added to `KNOWN_BROKEN`/`BROKEN_TESTS` in `scripts/go-packages.sh`; fixed packages removed
- [ ] Intent ID cited; its status updated if this PR completes a stage
