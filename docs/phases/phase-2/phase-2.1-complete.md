# Phase 2.1 — Snellius upgrade (Complete)

**Completed:** 2026-11-10
**Range:** 2ed885af6831a27ea98cb7f7a5479a6f564ee5af..d5a6fec on `duncandoyle/phase2Design`

## Summary
kgwctl builds on `github.com/DuncanDoyle/snellius v0.7.0-alpha.1`. `get`, `describe` and `version` behave as before: no golden or test expectation changed. No API adaptation was needed beyond imports.

## Sub-phases (phase record only)
n/a

## Reviews
- No separate sub-phase review (planned sub-phase; the phase review covers it). The controller read the diff and reran `make test`, `go vet`, gofmt: all pass.

## Waivers
none

## Deliverables
- go.mod/go.sum on Snellius `v0.7.0-alpha.1`; imports rewritten.
- `TestBinaryDeps`: transitive ban on Snellius `pkg/app` and any kgateway-ui package. New `TestNoDirectServerImports`: no kgwctl package imports `pkg/app` or `pkg/api`. Both shown to fail on a temporary violating import.
- Ruling: `internal/resolve` skips `ListOnly` kinds (DirectResponse, GatewayExtension, ReferenceGrant), keeping Phase 1 behaviour (phase-status Notes).

## Deferred
- `get`/`describe` for list-only kinds (owner to schedule).
- Closing kgwctl#5–#7: at phase close.
