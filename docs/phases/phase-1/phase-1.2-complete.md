# Phase 1.2 — `get` (Complete)

**Completed:** 2026-11-05
**Range:** a66aa1d56de9bd44e203c350503a184b0f341d7b..HEAD of `duncandoyle/phase1Get` at merge

## Summary
`kgwctl get` lists Gateway API and kgateway objects kubectl-style: one table per kind, `-o wide`, and the stored objects as a `v1` `List` in YAML or JSON. Kind names resolve like kubectl, plus the `ls` alias over both ListenerSet APIs and `all`. Status cells summarise conditions per object and per parent (`1/4 (1 stale, 1 no status, 1 other)`) from what the controller wrote. Only objects another controller positively owns are hidden; orphans, unwatched routes and unaccepted policies stay visible. Unreadable inputs never yield a partial table: a requested kind that is denied, failed or late is exit 1 with empty stdout; an unreadable relation input prints `unknown (<input>: <state>)` and a warning. A vendor build's own policy kind shows up in `get`.

## Sub-phases (phase record only)
n/a

## Reviews
- reviews/1.2-codex.md — 5 findings (1 P1, 1 P2, 3 P3): 5 accepted, fixed in 351a9f7; scoped Codex re-review: all resolved, no new P1/P2.
- Per-task reviews (1.2.0–1.2.3 by a fresh subagent, 1.2.4 by the controller: an 88-line test-only diff) are in the SDD ledger (not in git). 1.2.1–1.2.3 each needed one fix round.

## Waivers
none

## Deliverables
- `internal/resolve` (kind resolution), `internal/view/{filter,status,get}.go`, `internal/print/{table,rawlist}.go`, `internal/app/get.go`, exit-code matrix, fixtures and 70+ goldens.
- `internal/load` builds on kgateway-ui `pkg/setup` (Task 1.2.0, added by the brief); a requested kind the cache never watched fails the load.
- Test data: experimental XListenerSet CRD from Gateway API v1.4.1, installed for every env in packages that use it.
- No new dependencies.
- Verified: `make vet lint build test` green; `go test -race -shuffle=on` green on Go 1.27.3 and 1.26.0; `go list -deps ./cmd/kgwctl` has no controller-runtime.

## Notes for later phases
- D-012 records the `get` details settled during implementation; design §3.1 and §6.2 match it. `describe` (1.3) reuses `view.RouteParents`, `PolicyAncestors`, `ObjectCondition` and `Filter`.
- `pkg/setup` gaps worked around locally: no way to skip the proxy-Pod watch, and `Build` re-runs discovery (local memo). Both could become kgateway-ui options.

## Deferred / open
- `TestGetWatchDeniedAfterSync` orders events by a 1s hold against a 200ms delay: a slow machine gives a false failure, never a false pass. Switch to an `Until` channel if it flakes.
- The "relations unknown" warning also prints for a non-wide route table with no index-derived cell (errs on the safe side).
