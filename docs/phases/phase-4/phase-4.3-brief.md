# Sub-phase 4.3 — Logs, metrics, Envoy

Kind: planned
Base: dd98ce7
Issues: none | Plan steps: Tasks 4.3.1–4.3.4; before them, the `internal/bundle/writer.go` split and review item P4-2 (4.1)

## Problem / goal
After 4.2, `kgwctl bundle` collects cluster state, versions and `check`. Support also needs what each controller and proxy replica was doing: container logs, controller and proxy metrics, and per-replica Envoy captures (config dump, stats, certs, clusters). 4.3 adds those collectors and proves the bundle on restricted RBAC: a reader-only user still gets exit 0 with a partial bundle and one grant line per missing grant.

## Expected behaviour

| Input / action | Expected |
|---|---|
| A controller or proxy container | `logs/<container>.log` from `Window.Since`, with timestamps, tail and byte limits; `.previous.log` only when the container restarted; init containers included |
| `--skip logs` | No `logs/` member, collector `skipped` (`flag`), no `pods/log` request, no `errors.txt` line |
| A log stream cut by the seal | Complete lines plus one truncation line; an open PEM block redacted; collector `timeout` |
| A Pod deleted or recreated after its stream or capture | The item carries `gone` or `changed` |
| A not-running replica | Its items `skipped: not running`, not `failed` |
| Proxy metrics port refused and not declared | `disabled`; refused with a declared port → `failed`; 403 → `denied` |
| Metrics with an HTML body, or a Bearer label value | Body dropped with an error; the label value `[redacted]` |
| Each proxy replica | `config_dump.json` (with EDS, or without under `--no-endpoints`), `stats.txt`, `certs.json`, `clusters.txt` unless `--no-endpoints`; an unverified replica gets no request |
| Two replicas with different config | Both kept under their own pod; the extracted dump loads in `proxy routes --file` |
| A kubelet error-stream message or a malformed admin status line with a canary | The canary is in no member (carried over from 4.2.3) |
| `pods/log` and `pods/portforward` denied | Exit 0; affected items `denied`; one grant line per grant and namespace; resources, workloads, install, version and `check` present |
| `-n team-a`, all collectors | Requests match the design §3.2 table; no `secrets` or `configmaps` request in any run |

## Root cause (if known)
n/a (new feature).

## Acceptance tests
As listed in plan Tasks 4.3.1–4.3.4: HTTP-boundary tests for the log source and metrics scraper, archive-boundary tests through the production writer with `bundletest.Extract` and `AssertNoCanary`, and envtest command tests with denial rules (`RuleHits.N() > 0`) and `Proxy.Requests()`.

## Scope
In: `internal/bundle/writer.go` split into files (behaviour unchanged); the Sink contract change for P4-2 (`pkg/bundle`, after a decisions.md entry and owner approval); `internal/bundle/collect/` (logs, metrics, scraper, proxyadmin); `internal/app/bundle.go` (hooks, built-ins, `--no-endpoints`); `docs/rbac.yaml`, README permissions line.
Out: the extension point `WithBundleCollectors`, README bundle section, live check (4.4); Snellius `Scope.Excluded` (v0.9.0); the xdsview v0.5.0 bump; enterprise support (a downstream build, not this repo).

## Plan / design impact
P4-2 changes the public `pkg/bundle` Sink contract: a decisions.md entry and owner approval before Task 4.3.1. The `writer.go` split moves code only.

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] `make test` (race, envtest), `go vet ./...`, gofmt clean, deps check, `go mod tidy -diff` empty
- [ ] phase-4.3-complete.md written; phase-status.md updated
