# Phase 1 — Foundation and resource inspection (Complete)

**Completed:** 2026-11-06
**Range:** 779c421e8d8ed8a4a71847c5cc29bc61ffedbd09..HEAD of `duncandoyle/phase1Close` at merge

## Summary
`kgwctl` is a read-only CLI with three commands: `version`, `get` and `describe`. They read Gateway API and kgateway objects through kgateway-ui's model, loaded once per command.

`get` shows each object's status per parent and hides only what another controller owns. `describe` shows one object with every status block and its resolved relations. JSON output is versioned and documented. Vendor builds extend the CLI through `pkg/cli`.

The commands match kubectl on a live kgateway v2.5.0 install, work with a least-privilege role, and answer `get httproutes -A` on 5,000 routes in 0.37 s.

## Sub-phases (phase record only)

| Sub-phase | Kind | Issues | Description |
|---|---|---|---|
| 1.1 | planned | 1.1-codex.md (5) | module, `pkg/cli`, one-shot load, `version`, envtest harness |
| 1.2 | planned | 1.2-codex.md (5), D-012 | `get`: kind resolution, hiding rule, status cells, all formats |
| 1.3 | planned | 1.3-codex.md (4), D-013 | `describe`, `docs/output.md` |
| 1.4 | planned | — | live check on kind, scale benchmark, README, RBAC |
| 1.5 | stabilization | phase-codex.md Q12–Q18, 1.5-codex.md | phase review fixes |

## Reviews
- design-codex.md … design-r4-codex.md — 4 rounds on design and plan, closed with no findings.
- 1.1-codex.md, 1.2-codex.md, 1.3-codex.md — sub-phase reviews; every accepted finding fixed.
- phase-codex.md — 9 findings. 7 were fixed in 1.5 and 2 deferred to Phase 2.
- 1.5-codex.md — 3 findings. 1 was fixed and 2 deferred to Phase 2.

All reviews are cross-model: Codex reviewed Claude's work.

## Waivers
none

## Deliverables
- Commands: `version`, `get`, `describe`, `completion`.
- Public API: `pkg/cli` (D-009).
- Docs: `README.md`, `docs/output.md`, `docs/rbac.yaml`.
- Scripts: `hack/live-check.sh`, `hack/scale/`.
- Dependencies: kgateway-ui v0.3.0, cobra/pflag, client-go v0.36.2; envtest is test-only.
- Decisions D-008 to D-013.

## Notes for later phases
- Phase 2 (`check`) runs on the same one-shot load. Time-based checks mature only if one engine is kept across recomputes.
- kgateway v2.5.0 no longer ships HTTPListenerPolicy. kgwctl skips unserved kinds.

## Deferred / open
- phase-codex.md #8 → Phase 2: keep one engine across recomputes (`check`).
- phase-codex.md #9, 1.5-codex.md #2–3 → Phase 2: a narrow `pkg/cli` facade for vendor cluster commands, plus lifecycle tests for embedders.
- Upstream requests still open in kgateway-ui: #17 (DirectResponse, GatewayExtension, ReferenceGrant), #18 (list-only access), #19 (namespace-scoped listing), #20 (Cache stop/wait).
- `describe` could say why routes and policies show `Observed generation: <none>`.
