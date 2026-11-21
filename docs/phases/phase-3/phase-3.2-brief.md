# Sub-phase 3.2 — `proxy routes`

Kind: planned
Base: 82a0809
Issues: kgwctl#10 (xdsview v0.4.0, E11) | Plan steps: Tasks 3.2.1–3.2.3

## Problem / goal
`proxy dump` hands support a raw config dump that they still read by hand. The lead question is which HTTP filters run for a route, in what order, and which policy put each one there. 3.2 adds `kgwctl proxy routes`: one Gateway's listeners, virtual hosts and routes, with the effective filter chain per route, mapped back to Gateway API and kgateway objects, as a tree or JSON/YAML.

## Expected behaviour

| Input / action | Expected |
|---|---|
| `kgwctl proxy routes proxy -n kgwctl-proxy`, reader + port-forward Role | Header (Gateway, pod, capture time), then the tree from xdsview `render.Text`; exit 0 |
| `--route kgwctl-proxy/orders` on a dump with route metadata (`collide-meta.json`) | Only that HTTPRoute's routes, its filters in request order with their policies |
| `--route kgwctl-proxy/orders` on a dump without metadata (`policies.json`) | No match; both colliding routes listed as ambiguous |
| A colliding route that only the known HTTPRoutes settle (`NarrowedByKnown`) | Not a match; listed as ambiguous with "one candidate exists in the cluster" |
| `--file dump.json [--gateway ns/name]` | Same view from the file, no Kubernetes call even with a reachable cluster; header names the file |
| `--gateway` without `--file` | Exit 2, usage |
| `--response` | Response order too, only encoder-capable filters, unknown sides marked |
| Warming, draining or rejected listener | Shown with its state and error, also when narrowing drops its routes |
| A section the parser skipped | A warning in the tree and in JSON, next to the surviving routes |
| Body larger than `--max-size` (default 64Mi), live or file | Exit 2 naming `--max-size` and `proxy dump`; the tunnel closed |
| `-o json` / `-o yaml` | One `{schemaVersion: kgwctl.kgateway.dev/v1alpha1, kind: ProxyRoutes, item}` document (design §6) |
| Every 3.1 capture failure | Exit 2, as for `proxy dump` |

## Root cause (if known)
n/a (new feature).

## Acceptance tests
Command boundary (`internal/app`) and `internal/proxyview`, on the 3.1 goldens and hand-edited copies, as listed in plan Tasks 3.2.1–3.2.3.

## Scope
In: `internal/proxyview/`, `internal/app/proxy.go` (routes subcommand), `internal/app` tests, `docs/output.md`, `README.md`, `go.mod` (xdsview v0.4.0), `internal/app/lifecycle_test.go` (dependency ban).
Out: `hack/live-check.sh` routes step (3.3), changes to xdsview, `check`.

## Plan / design impact
none (design r4; plan 3.2 confirmed against v0.4.0 in 82a0809).

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] full test suite passes; no charmbracelet module in `go list -deps ./cmd/kgwctl`
- [ ] phase-3.2-complete.md written; phase-status.md updated
