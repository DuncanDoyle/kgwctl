# Phase 4b.2 — Snellius, version, release, live check (Complete)

**Completed:** 2026-11-27
**Range:** b89232e..2601620 on `duncandoyle/phase4b`

## Summary
- Snellius v0.9.0: `install.Scope.Excluded` replaces the local loop in the bundle's discovery scope, with the same output. A policy with current `Accepted=False` and `Attached=Pending` gives KGWUI-002 through `check` and the bundle's `check.json`. No existing golden changed.
- The client version comes from ldflags, else from Go build info (module version and `vcs.revision`), else `dev`/`unknown`. `go install …@v0.1.0` prints `v0.1.0`.
- `hack/release.sh <tag>` builds linux and darwin for amd64 and arm64, with checksums over exactly the four archives. A real build refuses a dirty tree, a HEAD that is not the tag commit, a branch-only name and a non-empty output directory. It builds with `GOWORK=off GOFLAGS=-mod=readonly`. It only prints the publish steps.
- `hack/live-check.sh` checks the new `proxy routes` output on kind under `exact`, `strict` and `known`, with route metadata on and off, and requires empty stderr on healthy runs.

Evidence at 2601620: `make test` (race, envtest) exit 0, 18 packages; `make lint` and `go mod tidy -diff` clean. Live on kind `kgwctl-live` (kgateway v2.5.1): `routes`, `goldens-check`, `proxy` and `bundle` exit 0; `enableRouteSourceMetadata` restored to `false`.

## Sub-phases (phase record only)
n/a

## Reviews
Per-task Claude reviews (opus or sonnet), each fix round with a scoped re-review:

| Task | Rounds | Main findings fixed |
|---|---|---|
| 4b.2.1 Snellius v0.9.0 | 0 | none |
| 4b.2.2 client version | 0 | none |
| 4b.2.3 release script | 1 (+1 controller line) | a git-ignored `go.work` could change the build; untracked-file check; refusal message; `+` in tags; `GOFLAGS` from `go env -w` |
| 4b.2.4 live check | 1 | healthy runs did not assert empty stderr; matched route identity in JSON |

A cross-model review is not required for a planned sub-phase; the Phase 4b review (Codex) covers 4b.2.

## Waivers
none

## Deliverables
- `go.mod`: Snellius v0.8.2 → v0.9.0. No new modules.
- `internal/app/bundle_scope.go`, `testdata/check/policy-rejected.yaml` and its tests.
- `cmd/kgwctl/buildinfo.go` and tests.
- `hack/release.sh`, `.gitignore`, README Install and Development lines.
- `hack/live-check.sh` (`routes`, `goldens-check`).

## Notes for later phases
- The `shasum` fallback in `hack/release.sh` is untested here, because the build host has `sha256sum`.
- A real build that fails midway leaves its output directory; the script says so, and the next run asks for it to be removed.

## Deferred / open
- Minor: the rejected-policy tests assert finding codes only, not the policy they name.
- Minor: tar headers carry the builder's user and group; the printed publish steps assume the repo root as working directory.
- Minor: an over-long comment in `expect_json`.
