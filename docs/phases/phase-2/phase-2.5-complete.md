# Phase 2.5 — Ctrl-C in `check --live` (Complete)

**Completed:** 2026-11-20
**Range:** d095f1b..c9f2164 on `duncandoyle/phase2Design`

## Summary
A deliberate Ctrl-C during `check --live` now ends the run like `--timeout` (D-020, owner's choice after the try-out). The report prints, stderr says `Stopped by Ctrl-C.`, and the findings decide the exit code. Exit 2 stays for a plain `check`, or `--live` before an install was found. The text footer says "stopped by Ctrl-C" or "stopped at --timeout"; JSON keeps `stopReason`. A report that could not be written now exits 2 instead of 1 (review finding). The roadmap gains the candidate checks from the `glooctl check` comparison.

Acceptance tests failed before the fix (exit 2, no stderr line; `TestLiveWriteError` exit 1) and pass after. `make test` with `-race` and `go vet` pass. On kind, SIGINT after 8 s gave exit 1, `Stopped by Ctrl-C.` and the new footer.

## Sub-phases (phase record only)
n/a

## Reviews
- reviews/2.5-codex.md (round 1: 2 P1, 2 P2, 2 P3; 4 accepted, 2 rejected), 2.5-codex-r2.md (round 2: 2 P2 doc, 1 P3; all accepted), 2.5-codex-r3.md: no P1 or P2 remaining.

## Waivers
none

## Deliverables
b81e141 (setup), 4c32114, 2e7c84d, c9f2164.

## Deferred (owner)
- Snellius rule proposals from the `glooctl check` comparison (not filed; ask first): proxy xDS `update_failure` growth, a missing GatewayClass, kgateway CRD drift against the installed chart.
- kgwctl#8 (list-only and namespace-scoped reads): a later sub-phase, after the Phase 2 close.
