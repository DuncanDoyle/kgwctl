# Phase 4b — xdsview v0.7.x migration and first release (Complete)

**Completed:** 2026-11-28
**Range:** cd2fbaa..HEAD of `duncandoyle/phase4b` (merged into main with Phase 4)

## Summary
- `proxy routes` runs on the xdsview v0.7.1 contract (E15). The pipeline is: parse, runtime filter chains, provenance from modules, explicit selection, then pruning under `--route`. kgwctl never guesses a route's origin.
- `--select exact|strict|known` picks how `--route` matches. The default is `strict` (D-032). The README explains it in "How `--route` picks routes".
- `cli.WithProvenanceModules` lets a downstream build replace the module set (D-033). No claim is selected by module name (xdsview D-023), and an AST test enforces this.
- `-o json|yaml` prints kgwctl's own `ProxyRoutes` payload (D-034). Every envelope is `kgwctl.kgateway.dev/v1alpha2`.
- Snellius v0.9.0 (E7): `install.Scope.Excluded` replaces the local loop. Rejected policies surface as KGWUI-002.
- The client version comes from ldflags, else from Go build info. `hack/release.sh` builds four archives with checksums from a clean tree at the tag (D-035).
- The release is tagged `v0.1.0`.

## Sub-phases (phase record only)
| Sub-phase | Kind | Issues | Description |
|---|---|---|---|
| 4b.1 | planned | #14, #15 | Migration, `--select`, payload, envelope, seam (phase-4b.1-complete.md) |
| 4b.2 | planned | #16 | Snellius, version, release script, live check (phase-4b.2-complete.md) |
| 4b.3 | stabilization | #17 | Phase review fixes, xdsview v0.7.1 (phase-4b.3-complete.md) |

## Reviews
- reviews/design-codex.md (+ -r2 to -r4): design and plan, 4 rounds. The owner approved on 2026-11-27.
- Planned sub-phases 4b.1 and 4b.2: per-task Claude reviews. The phase review covers them.
- reviews/phase-codex.md: 8 findings (1 P1), all accepted and fixed in 4b.3.
- reviews/4b.3-codex.md (+ -r2 to -r4) and 4b.3-r5-consult-codex.md: 5 fix rounds. Round 3 used the owner's option A, and the owner approved round 5 on Codex's advice.

## Waivers
none

## Deliverables
- Packages: `internal/proxyview` (view, payload, text, warnings, coverage), `internal/app/proxy_routes.go`, `internal/print/envelope.go`, `pkg/cli` `WithProvenanceModules`, `examples/downstream`, `cmd/kgwctl/buildinfo.go`, `internal/app/bundle_scope.go`.
- Scripts: `hack/release.sh`, and `hack/live-check.sh` (`routes`, `goldens-check`).
- Docs: README (`--select`, sharing warning, Install), docs/output.md (`ProxyRoutes`, `selection`, v1alpha2), and decisions D-031 to D-035.
- Dependencies: xdsview v0.4.3 → v0.7.1, Snellius v0.8.2 → v0.9.0. No new modules.

Evidence at the close (d5ffd66):
- `make test` (race, envtest) exits 0 on 18 packages.
- `make lint` and `go mod tidy -diff` are clean.
- `hack/live-check.sh goldens-check` passes.
- `hack/live-check.sh routes` passes on kind `kgwctl-live` (kgateway v2.5.1).

## Notes for later phases
- Phase 6 joins on `RouteConfigUse.Source`. The text output deduplicates; JSON does not.
- krp narrows with `--route`, while kgwctl narrows with the cluster's HTTPRoutes, so `known` can differ between the two tools (D-032).
- JSON omits a redundant `indexCertainty`, so it cannot tell an explicit `Inferred` from an unset one. The unreleased-schema rule no longer holds after `v0.1.0`: emitting it later is an additive change.
- Snellius visibility strategies (`require-dump-access` and others) are a separate, security-related concept. They are drafted in xdsview Phase 9 notes, not in kgwctl.

## Deferred / open
- The parked Minors in the 4b.1 and 4b.2 records: test assertions, tar user and group, the untested `shasum` fallback, and long comments.
- The main route tree's multi-candidate label shows no `(incomplete)` marker. It comes from xdsview.
- The Phase 4 deferred list (phase-4-complete.md) is unchanged.
