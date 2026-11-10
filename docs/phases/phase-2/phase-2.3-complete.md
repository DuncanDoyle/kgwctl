# Phase 2.3 — `check --live` (Complete)

**Completed:** 2026-11-10
**Range:** 33b45e9..1e1bb0b on `duncandoyle/phase2Design` (2.3.1 started earlier, interleaved with 2.2)

## Summary
`kgwctl check --live` keeps one session for `--timeout` (default 6m): progress lines on a terminal's stderr, one report on stdout with an "Observed during this run" section, two-step Ctrl-C (report, then exit). On kind it showed a grace period ending (003 info → warning), holds deciding (023), and a fix applied mid-run (013 cleared).

## Sub-phases (phase record only)
n/a

## Reviews
| Task | Codex findings (Important+) | Fixed in |
|---|---|---|
| 2.3.1 diff | message-only change not a transition; nondeterministic order | bc6ee94 |
| 2.3.2 live | acquisition-time changes (accepted partly: start of tracking is reported); Ctrl-C during shutdown; inputs per publication | fa78264 |
| 2.3.3 docs/script | no separate review | — |

Found on kind by the controller: detail-only changes (013 "does not exist" → "has no port 8080") were invisible; fixed in 1e1bb0b. Observed entries carry the finding message (33b45e9).

## Waivers
none

## Deliverables
`internal/observe/diff.go`, `internal/app/live.go`, `internal/print/progress.go`, Observed section in text and JSON, README `--live`, live-check `--live` scenario (passed on kind).

## Deferred
Phase review findings → stabilization 2.4.
