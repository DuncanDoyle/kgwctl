# Phase 3 — Data plane (`proxy dump`, `proxy routes`) (Complete)

**Completed:** 2026-11-21
**Range:** 44be3b6..HEAD of `duncandoyle/phase3Design` (merged into main)

## Summary
`kgwctl proxy dump <gateway>` streams one Envoy replica's config dump. It picks the oldest Ready replica, or `--pod`. `kgwctl proxy routes` shows a Gateway's listeners, virtual hosts and routes, with each route's effective HTTP filter chain in request order. Each filter names the policy behind it. It reads live or from a saved dump with `--file`, which calls no Kubernetes API. `--route` and `--rule` narrow it to one HTTPRoute, and only origins that are certain count as a match. Ambiguous and unresolved routes are listed, never matched. Non-active listeners and parser warnings stay visible. `-o json|yaml` prints a `ProxyRoutes` envelope (docs/output.md).

The lead question has an answer from a live tree. On kgateway v2.5.1, a TrafficPolicy transformation runs after ext_authz.

Access is a client-go port-forward to Envoy admin (D-021). kgwctl sends one `GET /config_dump` on the port-forward data stream in process, and opens no local listener. It returns by `--request-timeout` even when a credential plugin ignores the deadline. The tunnel closes on every path, also behind an HTTP or SOCKS5 proxy. The optional `kgwctl-proxy-portforward` Role in docs/rbac.yaml grants it; the base reader role is unchanged. Envoy parsing, the effective chain and rendering live in xdsview (D-022, E11).

## Sub-phases (phase record only)
| Sub-phase | Kind | Issues | Description |
|---|---|---|---|
| 3.1 | planned | — | Capture and `proxy dump` (phase-3.1-complete.md) |
| 3.2 | planned | kgwctl#10 | `proxy routes` on xdsview v0.4.0 (phase-3.2-complete.md) |
| 3.3 | planned | — | Live check on kind and docs; xdsview v0.4.2 (phase-3.3-complete.md) |
| 3.4 | stabilization | — | Phase review fixes: no listener, deadlines, recovery advice, live RBAC, xdsview v0.4.3 (phase-3.4-complete.md) |

## Reviews
- reviews/design-codex.md (+ -r2 to -r5): design and plan, 5 rounds; owner go-ahead 2026-11-21.
- reviews/3.1-codex.md, -r2: 9 findings accepted and fixed; one new P2 rejected.
- reviews/3.2-codex.md, -r2: 8 findings accepted and fixed; one new P2 deferred to 3.3 and fixed there.
- reviews/phase-codex.md: 8 findings; 7 accepted (5 fixed in 3.4, 2 fixed in xdsview v0.4.3), 1 deferred (E6, xdsview#32).
- reviews/3.4-codex.md, -r2, -r3: 6 + 4 accepted and fixed, 2 rejected, 1 deferred.

## Waivers
none

## Deliverables
- Packages: `internal/proxydump` (pod selection, port-forward stream, `Fetch`, `FetchGateway`, `Await`, `ReadCapped`), `internal/proxyview` (selection rule, envelope), `internal/app/proxy.go`, `internal/app/proxy_routes.go`.
- Docs: README `proxy dump` and `proxy routes`, docs/output.md `ProxyRoutes`, docs/rbac.yaml port-forward Role, D-021, D-022, D-023.
- Tests and fixtures: `testdata/dumps/` goldens, `hack/live-check.sh` steps `proxy`, `routes`, `goldens` and `goldens-check`, `hack/live/proxy-fixtures.yaml`.
- Dependencies: xdsview v0.4.3 (E1, E11); golang.org/x/net becomes direct; client-go port-forward brings k8s.io/streaming, moby/spdystream and gorilla/websocket as indirect modules.
- Upstream: kgateway#15067 (HTTPRoute route-name collision; `route_source` metadata).

## Notes for later phases
- Phase 4 captures a known Pod with `proxydump.Fetch` and a Gateway's replica with `FetchGateway`. Both own the deadline and release late captures. A context-blind credential plugin leaves one parked goroutine per capture until it returns.
- Route attribution without `enableRouteSourceMetadata` relies on route names, which collide (kgateway#15067). Colliding routes are listed as ambiguous, never matched.
- Test-only seams in production code: `proxyTestHooks.WrapTransport` and `portStream.waiting`, both nil in production.
- The live fixtures stay on kind `kgwctl-live` in namespaces `kgwctl-proxy` and `proxy`.

## Deferred / open
- E6 (xdsview#32, xdsview Phase 8): enterprise test corpus and enterprise transformation type out of xdsview core. Needed before Phase 7.
- A test that cancels an established capture through a CONNECT or SOCKS5 proxy: Phase 4, which reuses the capture.
- kgwctl#8: Snellius list-only and namespace-scoped reads; owner to schedule.
- Ideas awaiting the owner: a spike on controller `/snapshots/xds` plus proxy version gauges as a sync check; a Snellius recipe for a secured admin listener without port-forward.
