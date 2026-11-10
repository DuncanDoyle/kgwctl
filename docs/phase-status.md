# Phase status

## Phase
Phase: 2 — `check`
Base: 980d32d
Docs: docs/phases/phase-2/
Status: design

### Queue
- [x] Q1 Phase 2 design (`design.md`), taking in the carry-over below — approved 2026-11-10
- [ ] Q2 Phase 2 implementation plan ← CURRENT
- [ ] Q3 Cross-model review of design + plan, triage, owner approval

### Exit gates
- [ ] tests/vet pass (observed)
- [ ] all queue items done or deferred
- [ ] design+plan cross-model review + triage: docs/phases/phase-2/reviews/design-codex.md
- [ ] phase cross-model review + triage: docs/phases/phase-2/reviews/phase-codex.md
- [ ] decisions.md / design / plan in sync with code
- [ ] phase-2-complete.md written, roadmap updated, tracked items closed

## Sub-phase
Sub-phase: 2.1 — Snellius upgrade
Kind: planned
Base: 2ed885af6831a27ea98cb7f7a5479a6f564ee5af
Brief: docs/phases/phase-2/phase-2.1-brief.md

### Allowed work
features within the approved design (here: the dependency move only)

### Not allowed
design changes without decisions.md + user approval

### Queue
- [ ] Task 2.1.1 move to Snellius v0.7.0-alpha.1 ← CURRENT

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
- GitHub repo: https://github.com/DuncanDoyle/kgwctl (`kgwctl` remains a working name, D-006).
- 2026-11-06 Phase 1 closed: docs/phases/phase-1/phase-1-complete.md.
