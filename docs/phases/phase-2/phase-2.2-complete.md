# Phase 2.2 — `check` (Complete)

**Completed:** 2026-11-10
**Range:** 7bd30eb..33b45e9 on `duncandoyle/phase2Design` (interleaved with early 2.3 work; see the ledger)

## Summary
`kgwctl check` runs every Snellius check over the selected installs and prints a paste-friendly report or one JSON/YAML document, with exit codes 0/1/2. On kind with kgateway v2.5.1 it takes about 4s; a least-privilege reader bound to `docs/rbac.yaml` gets the same report as an admin. A vendor pack adds its own check, collector and kind with no kgwctl change (`examples/downstream`).

## Sub-phases (phase record only)
n/a

## Reviews
Each task had a Codex task review (cross-model), with fix rounds until no Critical/Important finding remained. Review files are in the git-ignored SDD workspace; their findings and fixes are summarised here:

| Task | Codex findings (Important+) | Fixed in |
|---|---|---|
| 2.2.1 observe | lossy shutdown; repeated Fail blocks completion; fallback used an older model; barrier test; then: no recompute after resettle | cbe8807, 3355199, 42b6fec |
| 2.2.2 view | malformed `--install` selected all installs | 80c9362 |
| 2.2.3 print | cluster text could inject terminal controls; `/dev/null` counted as a terminal | a2eb56a |
| 2.2.4 app | exit-2 coverage gaps | e7602b3 |
| 2.2.5 downstream | none (no separate review; covered by the phase review) | — |
| 2.2.6 docs/script | no separate review (docs/script); the controller ran it on kind | — |

Found by running the binary on kind (controller): pending checks listed as "not checked"; `since` equal to kgwctl's own start time; `-n` hid the controller-down finding (005). Fixed in e7602b3 and 33b45e9; design §3.1 and §3.3 updated.

## Waivers
none

## Deliverables
- `internal/observe` (session: cache, engine loop, registry collectors, completion rule), `internal/view/check.go`, `internal/print/check.go` + `term.go`, `internal/app/check.go` + `exit.go`, `examples/downstream` vendor pack, `docs/rbac.yaml`, `docs/output.md` (Check kind), README `check` section, `hack/live-check.sh` scenarios (controller down, missing Service, reader RBAC).
- Live check on kind (kgateway v2.5.1, Gateway API v1.7.0): verify passed for admin and reader; controller-down shows KGWUI-005 under `-n live`; missing Service shows KGWUI-013.

## Deferred
- Snellius API to map a collector to its pack, so a pack's collector doesn't delay `check` by 6s on clusters without that pack's installs (interim: such a silent collector is not reported as incomplete). Proposed to the owner with E10.
- E10: first-round completion signal from collectors (design §4.1 quiet period is an approximation).
- Catalog-based ref explanations (discovery uses Phase 1's memo, no Catalog listing).
- `--all-controllers` has no effect in `check` yet.
- testenv: one consistent Gateway API CRD set (check tests relabel experimental CRDs).
- `get`/`describe` for list-only kinds (2.1 ruling).
