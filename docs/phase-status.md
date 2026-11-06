# Phase status

## Phase
Phase: 1 — Foundation and resource inspection
Base: 779c421e8d8ed8a4a71847c5cc29bc61ffedbd09
Docs: docs/phases/phase-1/
Status: design approved 2026-11-05 (D-010); 1.1–1.3 complete; next sub-phase 1.4

### Queue
- [x] Q1 Owner reviews roadmap and decisions D-001–D-008 — approved 2026-11-04
- [x] Q2 Confirm program edge E2 (one-shot use of kgateway-ui `pkg/cache` + `pkg/engine`) with the kgateway-ui session — confirmed in program file 2026-11-04; tag requested in DuncanDoyle/kgateway-ui#16
- [x] Q3 Phase 1 design (`design.md`), incl. the maintainer proposal draft (program P-005) — r4, 372c23f..e14b8b8
- [x] Q4 Phase 1 implementation plan (`implementation-plan.md`) — r3, sub-phases 1.1–1.4
- [x] Q5 Cross-model review of design + plan, triage, owner approval — 4 Codex rounds; approved 2026-11-05 (D-010)
- [x] Q6 File R1–R4 cross-repo issues in kgateway-ui (design §11) — DuncanDoyle/kgateway-ui#17–#20
- [x] Q7 Sub-phase 1.1 — foundation — phase-1.1-complete.md; kgateway-ui v0.3.0
- [x] Q8 Sub-phase 1.2 — `get` — phase-1.2-complete.md; reviews/1.2-codex.md; D-012
- [x] Q9 Sub-phase 1.3 — `describe` — phase-1.3-complete.md; reviews/1.3-codex.md; D-013
- [ ] Q10 Sub-phase 1.4 — live check, scale, docs
- [x] Q11 Maintainer proposal (P-005): not filed, deferred by the owner (D-011)

### Exit gates
- [ ] tests/vet pass (observed)
- [ ] all queue items done or deferred
- [x] design+plan cross-model review + triage: docs/phases/phase-1/reviews/design-codex.md … design-r4-codex.md
- [ ] phase cross-model review + triage: docs/phases/phase-1/reviews/phase-codex.md
- [ ] decisions.md / design / plan in sync with code
- [ ] phase-1-complete.md written, roadmap updated, tracked items closed

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
- n/a

## Notes
- GitHub repo: https://github.com/DuncanDoyle/kgwctl (`kgwctl` remains a working name, D-006).
- 2026-11-05 Close blocked (1.1): (a) go.mod pins kgateway-ui 9f44baa, which lacks APIs 1.1 uses; build and CI need a go.work to 5737a14 (unpushed) until kgateway-ui publishes and tags it (#16); (b) under Go 1.26, TestNoAccumulation fails on a goroutine leak in kgateway-ui pkg/cache (watch.Filter in instrument), reported to the kgateway-ui session.
- 2026-11-05 Blocker (b) fixed upstream: kgateway-ui 2df2d02 (local, phase3Seams, in review) replaces watch.Filter. Against it, `go test -race -count=1 ./...` exits 0 on Go 1.27.3 and Go 1.26.0. Only (a) remains: publish + tag kgateway-ui, then pin go.mod, drop CI's workspace step (its checkout SHA moves to the tag).
- 2026-11-05 Blocker (a) resolved: kgateway-ui v0.3.0 tagged; go.mod uses it, CI workspace step dropped. Green with `-race` on Go 1.27.3 and 1.26.0. 1.1 closed.
- 2026-11-05 Sub-phase 1.2 closed: Codex review 5 findings fixed (351a9f7); D-012 records the `get` details settled in implementation.
- 2026-11-06 krp's library is renamed `xdsview` (v0.3.0, program `6ca3662`); E1/E5 consumers (Phase 3) use the new module path.
- 2026-11-06 Sub-phase 1.3 closed: Codex review 4 findings fixed (0ca5cad, 7b1fce1, dc4eea0); D-013 records the `describe` details.
