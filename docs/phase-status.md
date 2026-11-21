# Phase status

## Phase
Phase: 4 — Support bundle
Base: tbd (main HEAD after the Phase 3 merge)
Docs: docs/phases/phase-4/
Status: not started

### Queue
- [ ] Q1 Phase 4 design (`design.md`)
- [ ] Q2 Phase 4 implementation plan
- [ ] Q3 Cross-model review of design + plan, triage; owner go-ahead

### Exit gates
- [ ] tests/vet pass
- [ ] all queue items done or deferred
- [ ] design+plan cross-model review + triage
- [ ] phase cross-model review + triage
- [ ] decisions.md / design / plan in sync with code
- [ ] phase-4-complete.md written, roadmap updated, tracked items closed

## Sub-phase
n/a

## Program
- Program file: https://github.com/DuncanDoyle/kgateway-tooling/blob/main/program.md (local: `../kgateway-tooling-claude/program.md`)
- Read 2026-11-21 at kgateway-tooling `7288049`.
- Consumed: E1 and E11 — xdsview `v0.4.3` (kgwctl#1, kgwctl#10).
- Waiting on: E7 (Phase 6). E6 (xdsview#32) before Phase 7.
- Consumers: none

## Carry-over (deferred to later phases)
- kgwctl#8: Snellius list-only and namespace-scoped reads; owner to schedule.
- `get`/`describe` for list-only kinds (Phase 2.1 ruling).
- `--only`, `--exclude`, `--fail-on`, repeatable `-n`, controller logs (D-015).
- A narrow `pkg/cli` facade for vendor cluster commands (D-015).
- Phase 3 (reviews/3.4-codex.md P3-4): a test that cancels an established capture through a CONNECT or SOCKS5 proxy, when Phase 4 reuses the capture.

## Notes
- GitHub repo: https://github.com/DuncanDoyle/kgwctl (`kgwctl` remains a working name, D-006).
- 2026-11-06 Phase 1 closed: docs/phases/phase-1/phase-1-complete.md.
- 2026-11-20 Phase 2 closed: docs/phases/phase-2/phase-2-complete.md.
- 2026-11-21 Phase 3 closed: docs/phases/phase-3/phase-3-complete.md.
