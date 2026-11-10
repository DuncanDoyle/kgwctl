# Phase status

## Phase
Phase: 2 — `check`
Base: 980d32d
Docs: docs/phases/phase-2/
Status: implementation

### Queue
- [x] Q1 Phase 2 design (`design.md`), taking in the carry-over below — approved 2026-11-10
- [x] Q2 Phase 2 implementation plan
- [x] Q3 Cross-model review of design + plan (3 rounds), triage; owner go-ahead 2026-11-10
- [x] Q4 Sub-phase 2.1 Snellius upgrade
- [ ] Q5 Sub-phase 2.2 `check` ← CURRENT
- [ ] Q6 Sub-phase 2.3 `check --live`

### Exit gates
- [ ] tests/vet pass (observed)
- [ ] all queue items done or deferred
- [x] design+plan cross-model review + triage: docs/phases/phase-2/reviews/design-codex.md (+ -r2, -r3)
- [ ] phase cross-model review + triage: docs/phases/phase-2/reviews/phase-codex.md
- [ ] decisions.md / design / plan in sync with code
- [ ] phase-2-complete.md written, roadmap updated, tracked items closed

## Sub-phase
Sub-phase: 2.2 — `check`
Kind: planned
Base: 7bd30ebb0040d3b2cf70710b7d8c2adfc625f1d7
Brief: docs/phases/phase-2/phase-2.2-brief.md

### Allowed work
features within the approved design

### Not allowed
design changes without decisions.md + user approval

### Queue
- [ ] Task 2.2.1 `internal/observe` ← CURRENT
- [ ] Task 2.2.2 check view and mapping
- [ ] Task 2.2.3 text report
- [ ] Task 2.2.4 `check` command and exit codes
- [ ] Task 2.2.5 vendor pack in the downstream example
- [ ] Task 2.2.6 RBAC, README, live check

## Program
- Program file: https://github.com/DuncanDoyle/kgateway-tooling/blob/main/program.md (local: `../kgateway-tooling-claude/program.md`)
- Read 2026-11-10 at kgateway-tooling `10b77d2`. Open `cross-repo` issues: kgwctl #1–#7 (all "available" notices, none blocking). Snellius has two open from kgwctl, not needed for Phase 2: #18 list-only one-shot snapshot, #19 namespace-scoped listing for restricted users.
- Available: E8 — Snellius `v0.6.0`: `engine.Compute` one-shot; controller metrics via `ctrlmetrics.CollectOnce` + `Publish` (kgwctl#5–#7). go.mod still on kgateway-ui `v0.3.0`; Phase 2 moves to `github.com/DuncanDoyle/snellius`.
- Available: E3 — Snellius analyzer over a snapshot, all 26 checks (kgwctl#4); Phase 2 runs all of them (Phase 5 keeps `analyze -f`).
- Available (pre-release): E9 — check holds, `CheckResult.Hold` in Snellius `v0.7.0-alpha.1` (snellius#33, closed). Used by `check --live`. A kgwctl release needs a final Snellius release.
- Available: E2 (Phase 1), E1, E5 (Phase 3/6).
- Waiting on: E7 (Phase 6).
- Consumers: none

## Carry-over (deferred to later phases)
- Phase 2 (taken by design §4): `check` keeps one engine across recomputes so grace periods and holds mature; `load.Load` returns a model only (reviews/phase-codex.md#8).
- Later phase (D-015): a narrow `pkg/cli` facade for vendor cluster commands — flags, REST config, deadline, load (reviews/phase-codex.md#9, D-009 follow-up).

## Notes
- 2026-11-10 Ruling (2.1): Snellius's catalogue now has list-only kinds (DirectResponse, GatewayExtension, ReferenceGrant; kgwctl#17). `get`/`describe` keep Phase 1 behaviour and skip `ListOnly` kinds, because `load.Load` doesn't wait for them and could print a false empty list. Supporting them in `get`/`describe` is a follow-up for a later sub-phase or phase (owner to schedule).
- GitHub repo: https://github.com/DuncanDoyle/kgwctl (`kgwctl` remains a working name, D-006).
- 2026-11-06 Phase 1 closed: docs/phases/phase-1/phase-1-complete.md.
