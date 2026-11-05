# Phase 1.1 — Foundation (Complete)

**Completed:** 2026-11-05
**Range:** d109da6e0c37af774744eed31806cc6e0ddd562e..HEAD of `duncandoyle/phase1Foundation` at merge

## Summary
`kgwctl version` works end to end against a live API server. It prints the client, controller, proxy and Gateway API CRD versions, marks proxy skew, and prints `unknown (<state>)` for denied or late inputs instead of wrong data. Cluster flags (`--kubeconfig`, `--context`, `--request-timeout`) bind every request, discovery included. JSON and YAML use the `kgwctl.kgateway.dev/v1alpha1` envelope. `pkg/cli` lets a vendor `main` add kinds, detectors and commands; `examples/downstream` proves it.

## Sub-phases (phase record only)
n/a

## Reviews
- reviews/1.1-codex.md — 5 findings: 4 accepted (1 P1, 1 P2 blocker, 2 P3), 1 deferred (P4 → Task 1.2.1); plus 1 author item (Go 1.26 check). Fix wave `dac5a0c`, scoped re-review clean.
- Per-task reviews (5 tasks, each clean after 1 fix round) and the final whole-branch review are in the SDD ledger (not in git).

## Waivers
none

## Deliverables
- `cmd/kgwctl`, `pkg/cli` (public extension API, D-009), `internal/{app,load,view,print,testenv}`, `examples/downstream`, `Makefile` (`test` runs `-race`), CI.
- Dependencies: `github.com/DuncanDoyle/kgateway-ui v0.3.0` (E2), cobra/pflag, client-go v0.36.2; controller-runtime envtest test-only.
- Verified: `make vet lint test build` green on Go 1.27.3; `go test -race -count=1 ./...` green on Go 1.26.0.

## Notes for later phases
- Loader rulings that bind later work: QPS 50 / burst 300 when unset (R7); `--request-timeout 0` = no deadline (R8); klog silenced once per process (R9).
- kgateway-ui v0.3.0 ships `pkg/setup` (E8). `internal/load/wiring.go` copies the old `pkg/app` wiring; switch to `pkg/setup` in 1.2.
- Phase 2 (`check`) must recompute at `m.Next.At` so grace periods and holds mature (kgateway-ui E8 note).
- Test CRDs: kgateway v2.5.0 with Gateway API v1.7.0 (R6); older experimental XListenerSet CRDs come in through `CRDDirs` in 1.2.

## Deferred / open
- Task 1.2.1 — requested GVR missing from `Inputs` is skipped (1.1-codex #5): resolver guarantee or loader rejection, with a test.
- Task 1.2.3 — un-skip `TestDownstreamPolicyInGet`.
- 1.2 (Low) — `load.go` comment names the AtomicFIFO dependency; `TestLoadPostDeadlineFailure` comment says its 500 fires, it doesn't.
