# Sub-phase 4.5 — Phase review fixes

Kind: stabilization
Base: f39a936
Issues: none | Source: reviews/phase-codex.md triage, findings 1–5, 8–12, 14, 15

## Problem / goal
The Phase 4 Codex review found gaps in the bundle's safety invariants, outcomes and bounds. The triage accepted eleven findings (ranks P2–P3). 4.5 fixes them so Phase 4 can close. Each finding's evidence and file references are in `reviews/phase-codex.md`; the verifier's notes are in `.agents-tmp/phase-codex-verify.md`.

## Expected behaviour

| # | Input / action | Expected |
|---|---|---|
| 1 | Bundle against a server URL whose path contains `api/v1` (e.g. an apiserver proxy prefix); a client requests a Secret or ConfigMap | The guard refuses the request; it never reaches the transport |
| 4 | A guarded request gets a 3xx response with a Kubernetes `Status` body holding a pattern-free credential | The body never reaches client-go; the item records a status-only error; no credential in `check.json` or any member (D-026 "non-2xx") |
| 2 | An invalid label selector from install detection; under `--all-controllers`, a proxy whose full-name annotation is not a valid Gateway name | `manifest.json` holds a fixed reason code for the selector failure and no unvalidated owner name; no credential from either input survives |
| 3 | An install detector returns an error containing a credential | Nothing of it reaches stderr; diagnostics go through the bundle's sanitizer or are suppressed, as bundle `check` does |
| 5 | `--timeout=1s` against a slow apiserver; `--request-timeout=0` with a blocked inventory read | The run ends within `--timeout` (plus the seal), with a sealed archive; install detection and inventory count against the same clock |
| 8 | A blocked discovery request with a small `--request-timeout` | Discovery for `resources` and `install` gives up after `--request-timeout`, not after the remaining run budget |
| 9 | A served kind outside the Snellius registry (UDPRoute) with a parent Gateway outside the selected install, or with a backend Service in another namespace | It gets the same ownership filter and workload-scope rules as registry kinds (design §3.2) |
| 10 | A downstream collector writes a Kubernetes object (`apiVersion` + `kind`) through `Sink.JSON`, including a Secret | Same outcome as `Sink.Object` for that object: Secret and ConfigMap refused and recorded as errors, kind rules applied (inferred) |
| 11 | `check` sees a denied input that no other collector sees (e.g. `--skip resources`, or `-n` with namespace-scoped RBAC) | `check.json` is kept; the manifest records the denial and `errors.txt` has the grant line |
| 12 | No install detected | stderr warns; controller collectors record `skipped: no install` in the manifest (design §3.2, §5). With `--all-controllers`, the warning text matches what actually runs |
| 14 | `hack/live-check.sh bundle`, reader-only | Fails when a reader replica present in the cluster is missing from the reader archive, or a required reader artifact (`check.json`, per-replica metrics) is missing; the canary scan also covers captured stderr |
| 15 | The reader role in `docs/rbac.yaml` gains an extra verb, a subresource or `resourceNames` | `TestReaderRoleCoversServedKinds` fails |

## Root cause (if known)
Per finding in `reviews/phase-codex.md` and `.agents-tmp/phase-codex-verify.md`. Summary:
1. `coreResource` takes the first `api/v1` pair anywhere in the path (`internal/bundle/guard.go`).
2. Selector errors and annotation values are copied into the manifest unsanitized (`bundle_scope.go`, `inventory.go`).
3. Snellius's engine logger writes to command stderr during `loadBundle` (`bundle.go`).
4. The guard passes 3xx bodies through (`guard.go`).
5. Detection and inventory get fresh request contexts before `Run` starts the `--timeout` clock (`bundle.go`).
8. Built-in discovery uses context-free client-go calls (`resources.go`, `install.go`).
9. `Keep` and workload scope use the bootstrap registry; the collector also enumerates non-registry kinds.
10. `Sink.JSON` applies only generic JSON rules.
11. An incomplete check report is written as `ok` without recording its denials.
12. Nothing emits `skipped: no install`; the empty-inventory metrics collector returns `ok`.
14. The reader check derives its expectations from the reader manifest.
15. The guard test asserts required verbs, not the absence of others.

## Acceptance tests
At the highest practical boundary, each failing before the change:
- 1, 4: guard tests at the HTTP boundary with a prefixed server URL and a 3xx `Status` body; for 4, through bundle `check` to `check.json`. `TestGuardPassesRedirects` changes to match.
- 2, 3, 5, 8, 9, 11, 12: `internal/app` command tests on the extracted archive (manifest, `errors.txt`) and captured stderr.
- 10: `examples/downstream` acceptance test with a Secret and a credential-bearing object sent through `JSON`.
- 14: `hack/live-check.sh bundle` on kind `kgwctl-live`, run by the controller; a negative case shown by editing the expectation locally, not committed.
- 15: `TestReaderRoleCoversServedKinds` against a role fixture with an extra verb, subresource and `resourceNames`.

## Scope
In: `internal/bundle` (guard, inventory, sink, run, collect), `internal/app` (bundle, bundle_scope, bundle_check), `examples/downstream`, `internal/testenv/rbac_guard_test.go`, `hack/live-check.sh`.

Out:
- bounded acquisition while listing (finding 6): carry-over, needs a design amendment
- `capturedAt` skew (13): P4 note
- short `--timeout` skipping Envoy replicas (7): rejected
- 4.4.1 M2–M5, 4.3 and 4.2 minors, the xdsview bump, Snellius `Scope.Excluded`

## Plan / design impact
none expected. Finding 10's outcome is inferred; if the owner prefers refusing objects in `Sink.JSON` over routing them, that is a `decisions.md` entry.

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] full test suite passes (`make test`, `make lint`, `go mod tidy -diff`), run by the controller
- [ ] live check passes on kind, admin and reader-only
- [ ] cross-model review of `git diff f39a936..HEAD` + triage
- [ ] phase-4.5-complete.md written; phase-status.md updated
