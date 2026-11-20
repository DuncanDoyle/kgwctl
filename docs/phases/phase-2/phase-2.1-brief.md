# Sub-phase 2.1 — Snellius upgrade

Kind: planned
Base: 2ed885af6831a27ea98cb7f7a5479a6f564ee5af
Issues: kgwctl#5, #6, #7 | Plan steps: implementation-plan.md Task 2.1.1

## Problem / goal
kgwctl builds on `github.com/DuncanDoyle/kgateway-ui v0.3.0`. Phase 2 needs Snellius `v0.7.0-alpha.1` (module renamed in `v0.5.0`; packs in `v0.6.0`; check holds, E9, in `v0.7.0-alpha.1`). Move with no behaviour change.

## Expected behaviour

| Input / action | Expected |
|---|---|
| `get`, `describe`, `version` in every format, all existing fixtures | output identical to Base (goldens unchanged) |
| `go list -deps ./cmd/kgwctl ./examples/downstream` | no `github.com/DuncanDoyle/kgateway-ui`; no Snellius `pkg/app` or `pkg/api` |
| a temporary import of Snellius `pkg/app` in `cmd/kgwctl` | `TestBinaryDeps` fails |

## Root cause (if known)
n/a (dependency move).

## Acceptance tests
Existing suite unchanged; `TestBinaryDeps` updated to the new module path and shown to fail on a forbidden import.

## Scope
In: go.mod/go.sum, imports, `internal/load` API adaptation, `internal/app/lifecycle_test.go`, `examples/downstream`.
Out: any `check` code; behaviour changes; `load.Load` semantics.

## Plan / design impact
none

## Definition of done
- [ ] full test suite passes (`make test`, `go vet ./...`, gofmt)
- [ ] goldens unchanged
- [ ] phase-2.1-complete.md written; phase-status.md updated
