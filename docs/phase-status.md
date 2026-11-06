# Phase status

## Phase
Phase: 2 — `check`
Base: n/a (set at phase start)
Docs: docs/phases/phase-2/
Status: not started

### Queue
- [ ] Q1 Phase 2 design (`design.md`), taking in the carry-over below
- [ ] Q2 Phase 2 implementation plan
- [ ] Q3 Cross-model review of design + plan, triage, owner approval

### Exit gates
- [ ] tests/vet pass (observed)
- [ ] all queue items done or deferred
- [ ] design+plan cross-model review + triage: docs/phases/phase-2/reviews/design-codex.md
- [ ] phase cross-model review + triage: docs/phases/phase-2/reviews/phase-codex.md
- [ ] decisions.md / design / plan in sync with code
- [ ] phase-2-complete.md written, roadmap updated, tracked items closed

## Sub-phase
n/a

## Program
- Program file: https://github.com/DuncanDoyle/kgateway-tooling/blob/main/program.md (local: `../kgateway-tooling-claude/program.md`)
- Waiting on: E3 (Phase 5), E7 (Phase 6)
- Available: E2 — kgateway-ui `v0.3.0` tagged 2026-11-05 (#16 closed); go.mod uses it
- Available: E1 — krp `v0.1.0` tagged 2026-11-04 (DuncanDoyle/kgwctl#1); used from Phase 3
- Available: E5 — krp `v0.2.0` origin mapping (DuncanDoyle/kgwctl#2); used from Phase 3/6
- Available: E8 — kgateway-ui `v0.3.0` `pkg/setup`, `ctrlmetrics.Static` (Phase 2). Phase 2 must recompute at `m.Next.At` so grace periods and holds mature. 1.2 switches `internal/load/wiring.go` to `pkg/setup`.
- Consumers: none

## Carry-over (deferred to later phases)
- Phase 2: `check` keeps one engine across recomputes so grace periods and holds mature; `load.Load` returns a model only (reviews/phase-codex.md#8).
- Phase 2: a narrow `pkg/cli` facade for vendor cluster commands — flags, REST config, deadline, load (reviews/phase-codex.md#9, D-009 follow-up).

## Notes
- GitHub repo: https://github.com/DuncanDoyle/kgwctl (`kgwctl` remains a working name, D-006).
- 2026-11-06 Phase 1 closed: docs/phases/phase-1/phase-1-complete.md.
