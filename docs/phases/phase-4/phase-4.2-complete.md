# Phase 4.2 — Command, scope, transport, cluster state (Complete)

**Completed:** 2026-11-23
**Range:** b4a586d..HEAD on `duncandoyle/phase4Design` (setup 237d5b9; work 9ec180d..6376149; close commit below)

## Summary
`kgwctl bundle` now writes a real archive from a cluster:
- **Command** (`internal/app/bundle.go`): flags, output file (`O_EXCL`, `0600`), `-o -`, `--list-collectors`, `--skip`, exit codes 0/2, Ctrl-C as the deadline, a stderr summary. The manifest server is `<apiserver>`.
- **Guard** (`internal/bundle/guard.go`): every bundle client refuses core `secrets` and `configmaps` before sending; error bodies (4xx/5xx) become a `Status` with code and reason only; a pods/proxy "connection refused" is classified from at most 4 KiB. An unreachable cluster exits 2 with no file left.
- **Scope** (`internal/app/bundle_scope.go`): `-n`, `-A`, `--install`, `--all-controllers`; ownership through `view.Filter`; discovery scope from Snellius `install.Scope` on the loaded model (union over installs; unknown scope filters nothing). `Unwatched` uses a local loop over `Scope.Contains` until Snellius v0.9.0 ships `Scope.Excluded`.
- **Inventory** (`internal/bundle/inventory.go`): proxy replicas by the full-name annotation; label-only Pods `unverified` (labels matched with kgateway's exact shortening, `safeLabelValue`); `Reidentify` gives `verified`, `changed`, `gone`, `reread-failed`.
- **Transport** (`internal/proxydump`): six fixed Envoy admin operations (`Op`); redirects refused; port-forward upgrade errors carry code and reason only.
- **Collectors**: `resources`, `workloads`, `install` (`internal/bundle/collect`), `version` (8 parallel `server_info` captures that stop before the deadline; `version.json` always written) and `check` (`check/check.json`, the same path as `kgwctl check -o json`).

Evidence at 6376149 (plus the docs in the close commit):
- `make test` (race, envtest) exits 0; `internal/app` takes 211 s.
- `go vet ./...` and gofmt are clean, and `go mod tidy -diff` is empty.
- Every new test failed before its change (TDD evidence in the SDD task reports). Mutation checks: ignoring `-n` fails `TestClusterStateScope`; unregistered collectors, raw error text and an unguarded config each fail the 4.2.5 tests; the old sequential version capture fails `TestVersionHungReplicas`.

## Sub-phases (phase record only)
n/a

## Reviews
Per-task reviews (Claude subagents, opus), each with scoped re-reviews of every fix round:

| Task | Rounds | Main findings fixed |
|---|---|---|
| 4.2.1 command | 1 | "no request" assertions passed vacuously; second Ctrl-C ignored in finalization; summary counted skips as errors |
| 4.2.2 guard, scope, inventory | 2 | unredacted load error on stderr; silent drops (non-replica errors, installs without a selector, long-named Pods); guard rewrote 3xx; exact label shortening |
| 4.2.3 transport | 1 | a port check that passed with no streams; `Op(0)` untested |
| 4.2.4 collectors | 1 | the `-n` test couldn't tell "never listed" from "filtered"; `--all-controllers -n` untested; only GatewayClass among cluster-scoped kinds (§3.2); GatewayClass version from discovery |
| 4.2.5 version, check | 2 | duplicated status mapping; hung proxies could lose all of `version.json`; unbounded `/version` call; a timing-based test |

A cross-model review is not required for a planned sub-phase; the Phase 4 review (Codex) covers 4.2.

## Waivers
none

## Deliverables
- `internal/app/bundle.go`, `bundle_scope.go`, `bundle_check.go`; `check.go` (`checkOnce` extracted).
- `internal/bundle/guard.go`, `inventory.go`; `internal/bundle/collect/` (resources, workloads, install, version).
- `internal/proxydump` operations; `internal/testenv` `Action.Body`; `load.ThrottleDefaults` exported for bundle clients.
- `pkg/bundle` `File`: long names get a hash suffix (review item P4-1).
- Design §4 (`install/_cluster/`) and §8.4 (long names) synced; plan Task 4.2.5 `run.log` wording corrected.

No new modules.

## Notes for later phases
- **Rulings (from the SDD ledger):**
  - Each task registers the collectors it creates; 4.2.2 wired guard, scope and inventory.
  - `Config.Collectors` carries downstream collectors; Task 4.4.1 builds `WithBundleCollectors` on it.
  - `run.log` is the progress log; item errors go to `errors.txt` only.
  - Cluster-scoped kinds: GatewayClass only (§3.2).
  - A capture cut short by the run's end is `timeout`.
- **Test time:** the `internal/app` race suite takes about 210 s. Bundle tests skip `check` and `version` when they don't need them.

## Deferred / open
- **P4-2, before Task 4.3.1:** intentional outcomes (`skipped: no install`, `disabled`). The `install` collector reports `ok` with no install today.
- **Task 4.3.3:** kubelet port-forward error-stream text and a malformed status line reach error strings; capture errors must go through `Sink.Error`, with a canary test.
- **Task 4.3.2:** the guard's ConnectionRefused cause doesn't reach the Snellius `PodProxyScraper`; the metrics collector needs its own call to tell `disabled` from `failed`.
- **4.3:** `--no-endpoints` is parsed but not wired. Live per-collector progress on stderr needs a runner callback (lines print after the run today); consider with the `writer.go` split.
- **Snellius v0.9.0:** replace the local `Unwatched` loop and the parse of Snellius's Source text with `Scope.Excluded`.
- **Minor (phase review to triage):**
  - Unannotated proxy Pods of excluded Gateways within `-n` add `unverified` lines.
  - `observed[].status` can say `ok` for an item later dropped by the size budget.
  - `builtinCollectors` is built twice (names, then real deps).
  - `check.json` finding text gets the JSON pattern pass but not the 512-byte cut.
  - Test-only: a duplicated proxy hint test; CONNECT/SOCKS5 test proxies duplicated across packages; no unit check that the version capture context ends before the run deadline.
