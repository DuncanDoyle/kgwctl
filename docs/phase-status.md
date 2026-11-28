# Phase status

## Phase
Phase: 5 — Full analysis
Base: n/a (recorded at phase start)
Author: n/a
Advisory: none
Docs: docs/phases/phase-5/
Status: not started — the owner decides when to start it

### Queue
n/a

### Exit gates
- [ ] tests/vet pass (observed)
- [ ] all queue items done or deferred
- [ ] design+plan cross-model review + triage
- [ ] phase cross-model review + triage
- [ ] decisions.md / design / plan in sync with code
- [ ] phase-5-complete.md written, roadmap updated, tracked items closed

## Sub-phase
n/a

## Program
- Program file: https://github.com/DuncanDoyle/kgateway-tooling/blob/main/program.md (local: `../kgateway-tooling-claude/program.md`)
- Consumed in Phase 4b: E15 (xdsview `v0.7.1`, kgwctl#15 and #17) and the Snellius part of E7 (`v0.9.0`, kgwctl#16).
- xdsview D-023 (owner approved 2026-11-26): downstream builds replace the kgateway module with a wrapping module under their own name. The module set passed to `provenance.Compose` is a seam a downstream build changes through `pkg/cli`. Never select claims by `Claim.Module` or `kgateway.ModuleName`; use `Resolve` and the assertions.
- Waiting on: none for Phase 5. Phase 6 needs E7 and E15, and both are delivered.
- Consumers: none

## Carry-over (deferred to later phases)
- Phase 4 deferred list: docs/phases/phase-4/phase-4-complete.md "Deferred / open".
- Phase 4b deferred list: docs/phases/phase-4b/phase-4b-complete.md "Deferred / open".
- kgwctl#8: Snellius list-only and namespace-scoped reads; the owner schedules it.
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
- 2026-11-27 Phase 4 closed: docs/phases/phase-4/phase-4-complete.md.
- 2026-11-28 Phase 4b closed: docs/phases/phase-4b/phase-4b-complete.md. Gates at d5ffd66: `make test` (race, envtest) exit 0 on 18 packages; `make lint`, `go mod tidy -diff` and `goldens-check` clean; `routes` live check passed. Released as `v0.1.0`.
- Live cluster: kind `kgwctl-live` (kgateway v2.5.1), admin kubeconfig `hack/live/out/admin.kubeconfig`, context `kind-kgwctl-live`. Stop the other kind clusters on colima before a live check.
