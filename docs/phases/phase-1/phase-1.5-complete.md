# Phase 1.5 — Phase review fixes (Complete)

**Completed:** 2026-11-06
**Range:** e6a8ef4..b4e784b

## Summary
The seven accepted Phase 1 review findings are fixed:
- A hanging exec credential plugin no longer holds a command past `--request-timeout`.
- `version` applies `--install`.
- `--all-controllers` keeps input-failure warnings.
- Object conditions and parent entries share one stale rule.
- The README states the output contract accurately.
- The harness tests check order, not elapsed time.
- `pkg/cli` documents that `Execute` replaces klog's logger.

## Sub-phases (phase record only)
n/a

## Reviews
- reviews/1.5-codex.md — Q12–Q14 and Q16–Q18 resolved. Q15 was partly resolved: the brief asked for `Unknown (<Reason>)` in parent cells, which the design's parent vocabulary has no state for. Ruled: the cell stays `no status`, and describe shows the reason (b4e784b). 2 embedding follow-ups deferred to Phase 2.

## Waivers
none

## Deliverables

| Q | Commit | Root cause → fix |
|---|---|---|
| Q12 (P1) | 124ecdf | client-go runs exec plugins without a context → discovery runs where `Load` can return at the deadline |
| Q13 (P1) | b1fddd5 | `loadVersion` parsed `--install` but never applied it → `view.Version` applies the pin as get/describe do |
| Q14 (P2) | 394f2e1 | `NewFilter` dropped every warning under `--all-controllers` → only the hiding explanation is dropped |
| Q15 (P2) | 69bdd96, b4e784b | parent entries used `IsStale`, conditions `StaleCondition` → one rule; describe shows a no-status parent's reason |
| Q16 (P2) | 3eb8cd1 | README claimed `incomplete` for every command and GatewayClass in `all` → corrected |
| Q17 (P3) | 7edd22c | 300 ms wall-clock asserts → hold/release order |
| Q18 (P4) | 3eb8cd1 | undocumented process-wide klog replacement → `cli.Execute` doc |

## Notes for later phases
- An abandoned discovery goroutine can outlive `Load` while an exec plugin hangs. That is harmless for the CLI, but it matters for embedders (Phase 2 carry-over).

## Deferred / open
- reviews/1.5-codex.md #2, #3 → Phase 2 carry-over (with phase-codex.md #9).
