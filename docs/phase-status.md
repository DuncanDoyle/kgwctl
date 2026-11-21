# Phase status

## Phase
Phase: 3 — Data plane (`proxy routes`)
Base: 44be3b6
Docs: docs/phases/phase-3/
Status: implementation

### Queue
- [x] Q1 Phase 3 design (`design.md`) r1: ab40189
- [x] Q1b xdsview E11: v0.4.0 released 2026-11-21 (kgwctl#10)
- [x] Q2 Phase 3 implementation plan (r4)
- [x] Q3 Cross-model review of design + plan (5 rounds, reviews/design-codex*.md), owner go-ahead 2026-11-21
- [x] Q4 Sub-phase 3.1 capture and `proxy dump` (phase-3.1-complete.md)
- [x] Q5 Sub-phase 3.2 `proxy routes` (phase-3.2-complete.md)
- [x] Q6 Sub-phase 3.3 live check and docs (phase-3.3-complete.md)
- [ ] Q7 Phase review (Codex), triage ← CURRENT
- [ ] Q8 Phase close

### Exit gates
- [x] tests/vet pass (observed 2026-11-21 at 87bb3c8)
- [ ] all queue items done or deferred
- [x] design+plan cross-model review + triage: reviews/design-codex.md (+ -r2 to -r5)
- [ ] phase cross-model review + triage
- [ ] decisions.md / design / plan in sync with code
- [ ] phase-3-complete.md written, roadmap updated, tracked items closed

## Sub-phase
n/a (3.3 complete: docs/phases/phase-3/phase-3.3-complete.md)

## Program
- Program file: https://github.com/DuncanDoyle/kgateway-tooling/blob/main/program.md (local: `../kgateway-tooling-claude/program.md`)
- Read 2026-11-20 at kgateway-tooling `dcab679`.
- Available: E1 — xdsview `v0.3.0` (`github.com/DuncanDoyle/xdsview/pkg/envoy/...`, formerly krp; kgwctl#1).
- Available: E11 — xdsview `v0.4.0` (2026-11-21, kgwctl#10).
- Waiting on: E7 (Phase 6).
- Consumers: none

## Carry-over (deferred to later phases)
- kgwctl#8: Snellius list-only and namespace-scoped reads; owner to schedule.
- `get`/`describe` for list-only kinds (Phase 2.1 ruling).
- `--only`, `--exclude`, `--fail-on`, repeatable `-n`, controller logs (D-015).
- A narrow `pkg/cli` facade for vendor cluster commands (D-015).

## Notes
- GitHub repo: https://github.com/DuncanDoyle/kgwctl (`kgwctl` remains a working name, D-006).
- 2026-11-06 Phase 1 closed: docs/phases/phase-1/phase-1-complete.md.
- 2026-11-20 Phase 2 closed: docs/phases/phase-2/phase-2-complete.md.
