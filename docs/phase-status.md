# Phase status

## Phase
Phase: 4 — Support bundle
Base: 38acaa0
Author: anthropic (claude-opus-5-5 controller; Claude subagent implementers)
Advisory: none
Docs: docs/phases/phase-4/
Status: implementing; sub-phase 4.2 open

### Queue
- [x] Q1 Phase 4 design (`design.md`) r1, with research notes (prior art, reuse, threats) — not committed, in .agents-tmp/
- [x] Q1b Roadmap: Phase 6 shows evidence, not effective policy; `proxy diff` replaced by a convergence candidate (D-024)
- [x] Q2 Phase 4 implementation plan (4 planned sub-phases)
- [x] Q3 Cross-model review of design + plan: 5 rounds (reviews/design-codex.md, -r2 to -r4 + triage); r5 at f4d2102
- [x] Q4 Owner go-ahead on design r5 and §12 items 1–6 (2026-11-22); D-025, D-026 accepted
- [x] Q5 Design r6 (owner additions: ownership and discovery-scope filters, `--skip logs`): Codex reviews r5–r9 + triage (reviews/design-codex-r5.md … -r9.md); no open P1/P2
- [x] Q6 Sub-phase 4.1 (contract, redaction, writer, runner, threat model) — phase-4.1-complete.md; Codex reviews 4.1-codex.md … -r4.md
- [x] Q7 Sub-phase 4.2 (command, guard, scope, inventory, Envoy transport, cluster-state collectors) — phase-4.2-complete.md
- [ ] Q8 Sub-phase 4.3 (logs, metrics, Envoy capture, RBAC and partial runs); split internal/bundle/writer.go first; settle P4-2 before Task 4.3.1 ← CURRENT
- [ ] Q9 Sub-phase 4.4 (extension point, README, live check)

### Exit gates
- [ ] tests/vet pass
- [ ] all queue items done or deferred
- [x] design+plan cross-model review + triage
- [ ] phase cross-model review + triage
- [ ] decisions.md / design / plan in sync with code
- [ ] phase-4-complete.md written, roadmap updated, tracked items closed

## Sub-phase
none (4.2 closed 2026-11-23: docs/phases/phase-4/phase-4.2-complete.md)

## Program
- Program file: https://github.com/DuncanDoyle/kgateway-tooling/blob/main/program.md (local: `../kgateway-tooling-claude/program.md`)
- Read 2026-11-22 at kgateway-tooling `56c8868` (main after the owner-approved history rewrite; E7 text merged, P-008, P-009, E13, E14). Phase 4 waits on no edge. The program session asked the bundle to capture per-replica convergence inputs (§5 of the design).
- Consumed: E1 and E11 — xdsview `v0.4.3` (kgwctl#1, kgwctl#10).
- 2026-11-23 at kgateway-tooling `4deab4d`: P-010 and E15, the xdsview Phase 9 contract. kgwctl migrates `proxy routes` when that tag ships; Phase 6 also waits on E15. Planned in `docs/roadmap.md`; no Phase 4 change.
- Waiting on: E7 and E15 (Phase 6). E15 (`proxy routes` migration). E6 (xdsview#32) before Phase 7. Snellius `install.Scope.Excluded` (DuncanDoyle/snellius#66, draft PR #67, unreleased): Task 4.2.2 keeps a local loop over `Scope.Contains` until a release ships it.
- Consumers: none

## Carry-over (deferred to later phases)
- kgwctl#8: Snellius list-only and namespace-scoped reads; owner to schedule.
- `get`/`describe` for list-only kinds (Phase 2.1 ruling).
- `--only`, `--exclude`, `--fail-on`, repeatable `-n`, controller logs (D-015).
- A narrow `pkg/cli` facade for vendor cluster commands (D-015).
- Phase 3 (reviews/3.4-codex.md P3-4): a test that cancels an established capture through a CONNECT or SOCKS5 proxy, when Phase 4 reuses the capture.
- E13: xdsview v0.5.0 is tagged (2026-11-22); bump it and update the `proxy routes` text and JSON goldens (certainty added). In docs, call `effectiveChain` the runtime filter chain; the JSON field keeps its name.

## Notes
- GitHub repo: https://github.com/DuncanDoyle/kgwctl (`kgwctl` remains a working name, D-006).
- 2026-11-06 Phase 1 closed: docs/phases/phase-1/phase-1-complete.md.
- 2026-11-20 Phase 2 closed: docs/phases/phase-2/phase-2-complete.md.
- 2026-11-21 Phase 3 closed: docs/phases/phase-3/phase-3-complete.md.
- 2026-11-22 Sub-phase 4.1 closed: docs/phases/phase-4/phase-4.1-complete.md.
- 2026-11-23 Sub-phase 4.2 closed: docs/phases/phase-4/phase-4.2-complete.md. Gates at 6376149 plus docs: `make test` (race, envtest) exit 0 (internal/app 211 s); `go vet ./...` and gofmt clean; `go mod tidy -diff` empty.
