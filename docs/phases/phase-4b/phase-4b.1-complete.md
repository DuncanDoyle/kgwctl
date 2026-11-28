# Phase 4b.1 — Migration, strategy, seam (Complete)

**Completed:** 2026-11-27
**Range:** df2a99e..b890fe3 on `duncandoyle/phase4b`

## Summary
- `proxy routes` runs on xdsview v0.7.0: parse, runtime filter chains, provenance from modules, selection, then pruning under `--route`.
- `-o json|yaml` prints kgwctl's own `ProxyRoutes` payload (D-034). It holds no raw `typedConfig`, `typedPerFilterConfig` or `metadata`, no claim IDs and no module names.
- Every envelope is `kgwctl.kgateway.dev/v1alpha2`. Only `ProxyRoutes` changes shape. The bundle's `cluster/version.json` has no envelope and is unchanged.
- Warnings are one list: in JSON `warnings`, and once at the end of the text, each naming its subject once.
- `--select exact|strict|known`, default `strict` (D-032; `exact` added by the owner during the sub-phase).
- `cli.WithProvenanceModules` lets a downstream build replace the module set (D-033). An AST test keeps kgwctl from selecting claims by module name.
- README explains `--select` in a new subsection, "How `--route` picks routes".

Evidence at b890fe3: `make test` (race, envtest) exit 0, 17 packages; `make lint` and `go mod tidy -diff` clean.

## Sub-phases (phase record only)
n/a

## Reviews
Per-task Claude reviews (opus or sonnet), each fix round with a scoped re-review:

| Task | Rounds | Main findings fixed |
|---|---|---|
| 4b.1.1 migration | 0 | none Important; warning subject printed twice (fixed in 4b.1.2) |
| 4b.1.2 `--select` | 1 (+ `exact`) | location default for unknown warning codes; strategy checked before parsing; a circular test |
| 4b.1.3 module seam | 1 | duplicated fixture; hygiene scan scope; a negative self-test |
| 4b.1.4 README | 0 | five wording fixes, applied by the controller |

A cross-model review is not required for a planned sub-phase; the Phase 4b review (Codex) covers 4b.1.

## Waivers
none

## Deliverables
- `internal/proxyview` (view, payload, text, warnings, strategies, `proxyviewtest.Wrap`), `internal/app/proxy_routes.go`, `internal/print/envelope.go`, `pkg/cli` `WithProvenanceModules`, `examples/downstream`.
- Goldens: every non-`ProxyRoutes` golden changes only in `schemaVersion`; `ProxyRoutes` goldens follow design §3.3 (review list in the 4b.1.1 task report). New goldens for `known` and `exact`.
- Docs: README `proxy routes` and `Extending`, docs/output.md `ProxyRoutes`, design r5 plus `exact` (DD-4, R27–R29), D-032 to D-034.
- Dependency: xdsview v0.4.3 → v0.7.0. No new modules.

## Notes for later phases
- Every recorded dump has route names with two readings, so on recorded data `exact` and `strict` give the same result. The case where `exact` rejects a name-only route is tested on a hand-built dump.
- `pkg/cli` exposes `provenance.Module`, so a downstream build uses kgwctl's xdsview minor.

## Deferred / open
- Minor (phase review to triage): the payload coverage test lists intent only; the doc test checks field names only; `export_test.go` and `ErrCompose` were not in the plan; one long test comment (lifecycle_test.go).
