# Phase 1.3 — `describe` (Complete)

**Completed:** 2026-11-06
**Range:** c18aae579bc634402c400cc7f3b42e70d92c3b32..HEAD of `duncandoyle/phase1Describe` at merge

## Summary
`kgwctl describe <kind> <name>` shows one object in full. The output has five sections:
1. object info;
2. a spec summary for Gateways, ListenerSets and HTTPRoutes;
3. every status block, naming the controller that wrote it;
4. relations from the owning install's index, or the declared references when no install owns the object;
5. the inputs that could not be read.

`-o json|yaml` emits a structured, versioned document. `docs/output.md` documents its schema, and a test fails when the doc and the Go types drift apart.

## Sub-phases (phase record only)
n/a

## Reviews
- reviews/1.3-codex.md — 4 findings (3 P2, 1 P3). All 4 accepted and fixed in 0ca5cad, 7b1fce1 and dc4eea0. The scoped Codex re-review found all resolved and no new P1/P2.
- Per-task reviews are in the SDD ledger (not in git):
  - 1.3.1 had a subagent task review and two fix rounds: structured JSON (R32), the pending list (R30), and parent states with the index down (R33).
  - 1.3.2 was checked by the controller and by the sub-phase Codex review.

## Waivers
none

## Deliverables
- `internal/view/describe.go`, `internal/print/describe.go`, `internal/app/describe.go`, 14 golden cases × 3 formats, `delegation-modes.yaml` fixture.
- `describe` in `TestExitCodeMatrix`.
- `docs/output.md` and `TestOutputDocMatchesSchema`.
- No new dependencies.
- Verified gates:
  - `make vet lint build test` green;
  - `go test -race -shuffle=on` green on Go 1.27.3 and 1.26.0;
  - no controller-runtime in the binary.

## Notes for later phases
- D-013 records the `describe` details settled during implementation. Design §3.2 matches it.
- The JSON schema is now a contract. Adding fields is fine; renaming or removing one bumps `schemaVersion`.
- The envelope fields are documented but not reflection-checked. Enum values in `docs/output.md` are not tested.

## Test fixes at close
Two intermittent failures in the full suite, outside 1.3's own code, were fixed at their root before the merge:
- `TestLoadWaitsForProxyPods` asserted that core inputs settle within 300ms; under `-race` with parallel packages they took about 1.1s. The test now holds proxy Pods until the core-settled hook releases them, so it checks order, not time.
- A klog data race: `Execute` set klog's logger on first call, while an earlier test's watch goroutines read it. `internal/quiet.Klog` now sets it once, called by `Execute` and at the start of `testenv.Run`. Production is unchanged: `Execute` still runs before any goroutine.

## Deferred / open
- Small overlap with `get` remains: backend lists and parent names, about 10 lines. It is not worth a round.
