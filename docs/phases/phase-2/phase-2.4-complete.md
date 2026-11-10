# Phase 2.4 — Phase review fixes (Complete)

**Completed:** 2026-11-10
**Range:** 3aef403..ee18094 on `duncandoyle/phase2Design`

## Summary
The Codex phase review's accepted findings are fixed: `check` no longer hangs on a credential plugin that ignores cancellation; the report says "checks run" instead of "passed" and lists partial coverage; `--live` reports a finding kgwctl lost sight of as "no longer observed", not cleared; API server URLs, host and resolver addresses are redacted; check availability changes show in progress; Observed entries keep evidence, next steps and fix; the live script asserts the observation. Also fixed: `load.Load` waits for list-only kinds (a latent flake from the 2.1 upgrade).

## Sub-phases (phase record only)
n/a

## Reviews
- reviews/2.4-codex.md (round 1: 2 partly resolved, 2 new P2), 2.4-codex-r2.md (round 2: 1 P1 redaction loop, 1 P2), 2.4-codex-r3.md: "No P1/P2 remaining."
- Phase review #5 (exit 2 via the `cluster access` check name) rejected: no generic library signal at the tag; proposed to the owner.

## Waivers
none

## Deliverables
13ef78a, 268ce0d, 4625f00, b7b4678, cacf1a1, 57bf9fe, 9591dd2, ee18094.

## Deferred (owner)
Library proposals for Snellius: collector → pack mapping; first-round completion (E10); finding → check provenance; a generic "could not check" result; transport errors without the API server URL; "the UI" wording in some check details.
