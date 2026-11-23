# Sub-phase 4.2 — Command, scope, transport, cluster state

Kind: planned
Base: b4a586d
Issues: none | Plan steps: Tasks 4.2.1–4.2.5, plus the 4.1 review items placed in Tasks 4.2.1 and 4.2.4 (P4-1)

## Problem / goal
4.1 built the bundle core with no cluster access. 4.2 adds the `kgwctl bundle` command and its cluster side: a request guard on every client, scope and ownership filters, a proxy replica inventory, allowlisted Envoy admin operations, and the collectors for resources, workloads, install metadata, version and `check`. After 4.2, `kgwctl bundle` writes a real archive from a cluster. Logs, metrics and Envoy captures follow in 4.3.

## Expected behaviour

| Input / action | Expected |
|---|---|
| `-o` names an existing file, or an unwritable directory | Exit 2 before any cluster request; the existing file unchanged |
| No `-o` | `kgwctl-bundle-<timestamp>.tar.gz`, with archive root `kgwctl-bundle-<timestamp>`; the context name in no member name |
| `-o -` | A valid gzip tar on stdout; the summary on stderr |
| `--list-collectors` | Built-in and downstream collector names; no cluster request |
| Unknown `--skip` value, or `--max-size 16Mi` | Exit 2 before any request |
| Interrupt mid-run | Bundle written, exit 0, running collectors `timeout`, no spool left |
| Any client asks for core `secrets` or `configmaps` (list, get, watch; namespaced or cluster-wide; also through a registry alias) | Refused before sending; zero such requests at the server; an error line; exit 0 |
| A non-2xx response with a body | Error carries code and reason only; the body is in no member |
| No `-n` or `-A` | All namespaces |
| `-n team-a` | `team-a` plus install namespaces; no `team-b` member |
| `--install x/y` that isn't detected | Exit 2 |
| No install detected | Controller collectors `skipped: no install` |
| A controller with a discovery selector that excludes `team-b` | `team-b` resources left out, counted under `excluded` as `scope`; the manifest names `team-b` |
| Two installs, one watching `team-b` | `team-b` objects kept unless `--install` picks the other |
| Scope unknown (`envFrom` only) or Namespaces unreadable | Nothing scope-excluded; the manifest says why |
| Another controller's GatewayClass, Gateway and routes | Left out as `ownership`; present with `--all-controllers` |
| A long Gateway name (shortened Pod label) | Replicas matched by the full-name annotation; a Pod without it is `unverified` |
| Not-ready, terminating, Pending or restarted proxy Pods | Kept, with state and per-container restarts |
| A Pod replaced between inventory and capture | `changed`; deleted → `gone` |
| An Envoy admin operation | Exactly its design §6.3 request line on port 19000; a redirect refused without the `Location` value |
| Cancel during a capture through a CONNECT or SOCKS5 proxy | Capture ends; tunnel and proxy connection closed |
| `proxy dump` with a refused upgrade | Same RBAC hint as today |
| Two replicas reporting different Envoy versions | Two `observed` entries in `cluster/version.json` |
| `check` inside the bundle | `check/check.json` equals `kgwctl check -o json` with the same `-n`, apart from timestamps and sanitized reasons |
| An object name of 253 bytes | A bounded, collision-safe member name (inferred: rule settled in Task 4.2.4) |
| Canaries in the context name, `--redact-header`, `-o` path, a map key, `last-applied-configuration`, a TrafficPolicy header modifier, a GatewayExtension `headersToAdd`, an Event message | In no member or member name |

## Root cause (if known)
n/a (new feature).

## Acceptance tests
As listed in plan Tasks 4.2.1–4.2.5: command tests through the root command and the test proxy (`Proxy.Requests()`); HTTP and SPDY boundary tests for the guard and transport; envtest archive-boundary tests with `bundletest.Extract` and `AssertNoCanary`.

## Scope
In: `internal/app/` (bundle command, scope, `check` extraction), `internal/bundle/` (guard, inventory), `internal/bundle/collect/` (resources, workloads, install, version), `internal/proxydump/` (operations, status errors), `internal/testenv/proxy.go` (`Action.Body`).
Out: logs, metrics, Envoy capture collectors, RBAC partial-run tests (4.3); the `writer.go` split (before 4.3); the extension point, README and live check (4.4); the xdsview v0.5.0 bump; Snellius `Scope.Excluded` (v0.9.0, not released: keep the local loop).

## Plan / design impact
none (design and plan r6, owner-approved 2026-11-22). 4.1 review item P4-1 is settled inside Tasks 4.2.1 and 4.2.4.

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] `make test` (race, envtest), `go vet ./...`, gofmt clean, deps check, `go mod tidy -diff` empty
- [ ] phase-4.2-complete.md written; phase-status.md updated
