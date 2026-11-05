# Phase status

## Phase
Phase: 1 — Foundation and resource inspection
Base: 779c421e8d8ed8a4a71847c5cc29bc61ffedbd09
Docs: docs/phases/phase-1/
Status: design approved 2026-11-05 (D-010); implementation sub-phase 1.1

### Queue
- [x] Q1 Owner reviews roadmap and decisions D-001–D-008 — approved 2026-11-04
- [x] Q2 Confirm program edge E2 (one-shot use of kgateway-ui `pkg/cache` + `pkg/engine`) with the kgateway-ui session — confirmed in program file 2026-11-04; tag requested in DuncanDoyle/kgateway-ui#16
- [x] Q3 Phase 1 design (`design.md`), incl. the maintainer proposal draft (program P-005) — r4, 372c23f..e14b8b8
- [x] Q4 Phase 1 implementation plan (`implementation-plan.md`) — r3, sub-phases 1.1–1.4
- [x] Q5 Cross-model review of design + plan, triage, owner approval — 4 Codex rounds; approved 2026-11-05 (D-010)
- [x] Q6 File R1–R4 cross-repo issues in kgateway-ui (design §11) — DuncanDoyle/kgateway-ui#17–#20
- [ ] Q7 Sub-phase 1.1 — foundation (plan §1.1) ← CURRENT — implemented on branch duncandoyle/phase1Foundation (87ab28e..dac5a0c); review reviews/1.1-codex.md; close blocked (Notes)
- [ ] Q8 Sub-phase 1.2 — `get`
- [ ] Q9 Sub-phase 1.3 — `describe`
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
Sub-phase: 1.1 — Foundation
Kind: planned
Base: d109da6e0c37af774744eed31806cc6e0ddd562e
Brief: docs/phases/phase-1/phase-1.1-brief.md

### Allowed work
Features within the approved design (plan Tasks 1.1.1–1.1.5).

### Not allowed
Design changes without a decisions.md entry and owner approval.

## Program
- Program file: https://github.com/DuncanDoyle/kgateway-tooling/blob/main/program.md (local: `../kgateway-tooling-claude/program.md`)
- Waiting on: E2 (Phase 1; kgateway-ui module tag, DuncanDoyle/kgateway-ui#16 — pseudo-version until then), E3 (Phase 5), E7 (Phase 6)
- Available: E1 — krp `v0.1.0` tagged 2026-11-04 (DuncanDoyle/kgwctl#1); used from Phase 3
- Available: E5 — krp `v0.2.0` origin mapping (DuncanDoyle/kgwctl#2); used from Phase 3/6
- Built, not published: E8 — kgateway-ui `pkg/setup` and `ctrlmetrics.Static` on phase3Seams (local, 2026-11-05); ships with the #16 tag. Phase 2 must recompute at `m.Next.At` so grace periods and holds mature; the Phase 1 loader already polls `Synced` and waits for the optional watches. Phase 1.2 may swap `internal/load/wiring.go` for `pkg/setup`.
- Consumers: none

## Carry-over (deferred to later phases)
- n/a

## Notes
- GitHub repo: https://github.com/DuncanDoyle/kgwctl (`kgwctl` remains a working name, D-006).
- 2026-11-05 Close blocked (1.1): (a) go.mod pins kgateway-ui 9f44baa, which lacks APIs 1.1 uses; build and CI need a go.work to 5737a14 (unpushed) until kgateway-ui publishes and tags it (#16); (b) under Go 1.26, TestNoAccumulation fails on a goroutine leak in kgateway-ui pkg/cache (watch.Filter in instrument), reported to the kgateway-ui session.
- 2026-11-05 Blocker (b) fixed upstream: kgateway-ui 2df2d02 (local, phase3Seams, in review) replaces watch.Filter. Against it, `go test -race -count=1 ./...` exits 0 on Go 1.27.3 and Go 1.26.0. Only (a) remains: publish + tag kgateway-ui, then pin go.mod, drop CI's workspace step (its checkout SHA moves to the tag).
