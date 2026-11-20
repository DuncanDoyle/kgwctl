# Phase status

## Phase
Phase: 3 — Data plane (`proxy routes`)
Base: 44be3b6
Docs: docs/phases/phase-3/
Status: design

### Queue
- [x] Q1 Phase 3 design (`design.md`) r1: ab40189
- [ ] Q1b xdsview E11 (xdsview#27): agreed by the xdsview session; starts when the owner confirms there. Blocks 3.2 only
- [x] Q2 Phase 3 implementation plan (r4)
- [ ] Q3 Cross-model review of design + plan (5 rounds, reviews/design-codex*.md; last finding fixed), owner go-ahead ← CURRENT

### Exit gates
- [ ] tests/vet pass
- [ ] all queue items done or deferred
- [ ] design+plan cross-model review + triage
- [ ] phase cross-model review + triage
- [ ] decisions.md / design / plan in sync with code
- [ ] phase-3-complete.md written, roadmap updated, tracked items closed

## Sub-phase
n/a

## Program
- Program file: https://github.com/DuncanDoyle/kgateway-tooling/blob/main/program.md (local: `../kgateway-tooling-claude/program.md`)
- Read 2026-11-20 at kgateway-tooling `dcab679`.
- Available: E1 — xdsview `v0.3.0` (`github.com/DuncanDoyle/xdsview/pkg/envoy/...`, formerly krp; kgwctl#1).
- Requested: E11 — xdsview renderer, effective chain, listener state (xdsview#27, 2026-11-20).
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
