# Phase status

## Phase
Phase: 4b — xdsview v0.7.0 migration and first release (D-031)
Base: not set (set at phase start)
Author: anthropic (claude-opus-5-5)
Advisory: none
Docs: docs/phases/phase-4b/
Status: not started

### Queue
- [ ] Q1 Design (`design.md`) and implementation plan ← CURRENT
- [ ] Q2 Cross-model review of design + plan (Codex) + triage
- [ ] Q3 Implementation
- [ ] Q4 Live check on kind, goldens reviewed
- [ ] Q5 Phase review (Codex) + triage, fixes
- [ ] Q6 Close, tag `v0.1.0`

### Exit gates
- [ ] tests/vet pass (observed)
- [ ] all queue items done or deferred
- [ ] design+plan cross-model review + triage
- [ ] phase cross-model review + triage
- [ ] decisions.md / design / plan in sync with code
- [ ] phase-4b-complete.md written, roadmap updated, tracked items closed

## Sub-phase
none

## Program
- Program file: https://github.com/DuncanDoyle/kgateway-tooling/blob/main/program.md (local: `../kgateway-tooling-claude/program.md`)
- Read 2026-11-27 at kgateway-tooling `3385480`: E15 available in xdsview `v0.7.0` (kgwctl#15); E7 Snellius part and `install.Scope.Excluded` in Snellius `v0.9.0` (kgwctl#16).
- xdsview D-023 (owner approved 2026-11-26): downstream builds replace the kgateway module with a wrapping module under their own name. The module set passed to `provenance.Compose` must be a seam a downstream build changes through `pkg/cli`. Never select claims by `Claim.Module` or `kgateway.ModuleName`; use `Resolve` and the assertions. Claim IDs mean something only within one `Compose` result.
- Waiting on: none for 4b. Phase 6 waits on E7 (Snellius part delivered) and E15 (delivered).
- Consumers: none

## Carry-over (deferred to later phases)
- Phase 4 deferred list: docs/phases/phase-4/phase-4-complete.md "Deferred / open".
- kgwctl#8: Snellius list-only and namespace-scoped reads; owner to schedule.
- kgwctl#13 (E14): live xDS convergence diagnostic; slot to be decided.
- `get`/`describe` for list-only kinds (Phase 2.1 ruling).
- `--only`, `--exclude`, `--fail-on`, repeatable `-n`, controller logs (D-015).
- A narrow `pkg/cli` facade for vendor cluster commands (D-015).
- A generated per-command reference (owner said yes 2026-11-21).

## Notes
- GitHub repo: https://github.com/DuncanDoyle/kgwctl (`kgwctl` remains a working name, D-006).
- 2026-11-06 Phase 1 closed: docs/phases/phase-1/phase-1-complete.md.
- 2026-11-20 Phase 2 closed: docs/phases/phase-2/phase-2-complete.md.
- 2026-11-21 Phase 3 closed: docs/phases/phase-3/phase-3-complete.md.
- 2026-11-27 Phase 4 closed: docs/phases/phase-4/phase-4-complete.md. Gates at 5ab78b9: `make test` (race, envtest) exit 0, 17 packages; `make lint` and `go mod tidy -diff` clean. Owner try-out on kind passed.
- Live cluster: kind `kgwctl-live` (kgateway v2.5.1), admin kubeconfig `hack/live/out/admin.kubeconfig`, context `kind-kgwctl-live`. Stop the other kind clusters on colima before a live check.
