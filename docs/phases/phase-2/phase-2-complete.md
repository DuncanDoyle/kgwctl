# Phase 2 — `check` (Complete)

**Completed:** 2026-11-20
**Range:** 967fb63..HEAD of `duncandoyle/phase2Design` at merge (base 980d32d before the rebase onto 967fb63)

## Summary
`kgwctl check` runs every Snellius check over the selected installs in one pass. It prints a report that is easy to paste into Slack or a ticket, or one JSON/YAML document. The exit code is 0 when nothing was found, 1 when problems were found, and 2 when kgwctl could not check.

`check --live` watches for a bounded time so grace periods and holds decide. Progress lines go to stderr and one report goes to stdout, with an "Observed during this run" section. Ctrl-C ends the run like `--timeout` (D-020).

The owner tried both on kind with kgateway v2.5.1 and Snellius's demo use cases (2026-11-20). `check` reported the findings Snellius expects. `--live` showed a fix applied mid-run (013 cleared) and the same route broken again (013 error), each in the same second kgateway changed the status.

## Sub-phases (phase record only)

| Sub-phase | Kind | Issues | Description |
|---|---|---|---|
| 2.1 | planned | — | Move to `github.com/DuncanDoyle/snellius` |
| 2.2 | planned | D-014, D-015, D-017 | `check`: report, formats, exit codes, vendor packs |
| 2.3 | planned | D-016 | `check --live`: one session, progress, holds |
| 2.4 | stabilization | phase-codex.md, 2.4-codex.md | Phase review fixes |
| 2.5 | stabilization | owner try-out, D-020 | Ctrl-C in `--live`; report write errors exit 2 |

## Reviews
- design-codex.md, -r2, -r3: 3 rounds on design and plan. 19 accepted, 1 deferred.
- phase-codex.md: 8 findings. 7 accepted and fixed in 2.4, 1 rejected.
- 2.4-codex.md, -r2, -r3: 6 accepted, then no findings.
- 2.5-codex.md, -r2, -r3: 7 accepted, 2 rejected, then no P1 or P2.

All reviews are cross-model: Codex reviewed Claude's work.

## Waivers
none

## Deliverables
- Commands: `check`, `check --live`.
- Docs: `README.md` check section, `docs/output.md` check envelope, `docs/rbac.yaml` additions for metrics and stats.
- Example: `examples/downstream` (a vendor pack adds a check, collector and kind).
- Scripts: `hack/live/`.
- Dependency: Snellius v0.8.2 (final release, D-018), replacing kgateway-ui.
- Decisions D-014 to D-018 and D-020.

## Notes for later phases
- `check` keeps one engine across recomputes; Phase 5 `analyze -f` can reuse it.
- Controller metrics and proxy stats come through the apiserver pod proxy (D-017). Phase 3 needs the proxy admin port, which is a different path.
- Candidate checks from the `glooctl check` comparison belong in Snellius (roadmap Phase 2 notes).
- A kind cluster sharing the colima VM with others can starve the controller; the report then shows KGWUI-005, -019 and -026.

## Deferred / open
- kgwctl#8: Snellius list-only and namespace-scoped reads (v0.7.0). Not used yet; owner to schedule.
- `get`/`describe` for list-only kinds (2.1 ruling).
- `--only`, `--exclude`, `--fail-on`, repeatable `-n`, controller logs (D-015, roadmap).
- `-o agent` output (roadmap, after the MVP).
- A narrow `pkg/cli` facade for vendor cluster commands (D-015, phase-codex.md Phase 1 #9).
- Snellius proposals, not filed (owner decides): collector → pack mapping, first-round completion (E10), finding → check provenance, a generic "could not check" result, transport errors without the API URL, "the UI" wording, proxy `update_failure` growth, missing GatewayClass, kgateway CRD drift.
