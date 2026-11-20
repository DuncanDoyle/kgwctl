# Phase 3 — `proxy routes`: Design

**Status:** draft r1, 2026-11-20
**Base:** 44be3b6

## 1. Goal and scope
Show what one Gateway's Envoy actually runs, mapped back to the Gateway API and kgateway objects that produced it. The lead question is filter execution order for a route: does a transformation run before or after ext_authz, when are headers set or removed, does rate limit see what it needs. Today the answer needs knowledge of kgateway's translator and a config dump read by hand.

In scope:
- `kgwctl proxy routes`: listeners, filter chains, virtual hosts and routes for one Gateway, and the effective HTTP filter chain per route, as a tree or JSON/YAML.
- Narrowing to one HTTPRoute and rule.
- Two sources: a live proxy over port-forward, or a config dump file.
- `kgwctl proxy dump` (proposed): write the raw config dump, for a ticket or for `--file` later.

### Anti-goals
- No Envoy writes. kgwctl sends one `GET /config_dump` and nothing else to the admin API.
- No second Envoy renderer or parser. xdsview owns parsing, the effective chain and the text tree (D-022).
- No comparison across replicas, and no intended vs accepted diff (xdsview Phase 7, later).

## 2. Evidence
- Owner (2026-11-20): filter order is the hardest thing to see; Gateway API and kgateway policies don't make it explicit.
- Support research (internal tickets and threads, not cited in this public repo): a config dump is the first ask in every product and is read by hand. Recurring questions, most frequent first: filter execution order; whether a policy reached a route; which route matches, especially with delegation; what the proxy has vs what the controller says; listener and TLS matching. Customers already attach dumps to tickets, so a file source matters.
- Measured on kind, kgateway v2.5.1 (2026-11-20):
  - Envoy admin listens on 127.0.0.1:19000 only. `pods/proxy` to 19000: connection refused. The 9091 stats listener serves `/stats/prometheus`; `/config_dump` there: NotFound.
  - A dump of the `team-a` Gateway: 134 KB, one listener (`listener~80`), five virtual hosts, route names that embed the HTTPRoute, namespace and rule index (`…-httproute-orders-team-a-1-0-matcher-0`).
- Prior art: glooctl `proxy dump` shells out to `kubectl port-forward`. krp (xdsview's CLI) uses client-go port-forward. Neither restricts or documents the admin write access a port-forward gives.

## 3. Commands

### 3.1 `kgwctl proxy routes`
```bash
kgwctl proxy routes <gateway> [-n <ns>]                 # live, one Ready replica
kgwctl proxy routes <gateway> --route <ns>/<name> [--rule <i>]
kgwctl proxy routes --file dump.json [--gateway <ns>/<name>]
kgwctl proxy routes <gateway> --pod <pod>               # a specific replica, Ready or not
kgwctl proxy routes <gateway> -o json|yaml
```
- `<gateway>` is required live. With `--file` it is optional: it only labels origins.
- `--route` narrows to one HTTPRoute's routes; `--rule` to one rule (xdsview `filter.Filter`).
- The header names the source: Gateway, pod, capture time, or the file path.

Tree (shape, final text from xdsview's renderer):
```
Gateway team-a/team-a · pod team-a-86d49b69c4-m86x6 · 2026-11-20 19:40:12 UTC
listener~80 (active) · Gateway listener http
└─ vhost orders.example (listener~80~orders_team-a_example)
   └─ route 0 · HTTPRoute team-a/orders rule 0 · PathPrefix /orders → team-a/orders:8080
      filters (request order):
        1 io.solo.transformation   on (route)    TrafficPolicy team-a/headers
        2 envoy.filters.http.ext_authz enabled by route · TrafficPolicy team-a/auth
        3 envoy.filters.http.router
```
Response order is the reverse and is shown on request (`--response`) or in JSON.

### 3.2 `kgwctl proxy dump` (proposed)
```bash
kgwctl proxy dump <gateway> [-n <ns>] [--pod <pod>] > dump.json
```
Writes Envoy's config dump unchanged. Envoy redacts the fields it marks sensitive, such as private keys. The help text says the dump still holds topology and policy config.

### 3.3 Exit codes
0 printed; 2 could not read the dump (no Ready pod, RBAC, timeout, too large, parse error). No exit 1: the command reports, it does not judge.

## 4. Architecture
```
source (kgwctl)          parse + annotate (xdsview)        render (xdsview / kgwctl)
live: port-forward  ─┐   parser.Parse → origin.Annotate  ─┐  render.Text  (tree)
file: os.Open       ─┴─► → filter.Filter (--route/--rule) ─┴► kgwctl envelope (-o json|yaml)
                         EffectiveChain per route
```

### 4.1 Live source
- Pod: the Gateway's pods by `gateway.networking.k8s.io/gateway-name=<name>` in its namespace; the Ready, non-terminating one with the oldest start time, then name order, so repeated runs pick the same replica. `--pod` overrides and allows an unready pod.
- Transport: client-go SPDY port-forward to 19000 on an OS-assigned loopback port, closed on success, error and cancellation. Use xdsview `source/kube` if it accepts kgwctl's pod choice; otherwise a kgwctl `source.Source` on client-go `portforward` (left to the builder after reading `source/kube`).
- Request: plain `GET /config_dump`, no query parameters, redirects refused.
- Limits: one deadline over discovery, tunnel and read, default 30 s (`--timeout`); 64 MiB body cap enforced before the parser reads it. Over the cap: exit 2 with the size and a hint to use `proxy dump` + `--file`.
- Errors name the RBAC to add (D-021), like Phase 2's "Not checked" lines.

### 4.2 Origins
kgwctl passes `origin.Kgateway` the Gateway ref and the known HTTPRoutes from its own one-shot load, so route origins resolve without xdsview's cluster-wide `kube.HTTPRoutes` list. With `--file` and no cluster access, origins come from route names and kgateway's `route_source` metadata, and may be Ambiguous; the output says so.

### 4.3 Listener state
Active, warming, draining and rejected state come from xdsview (E11). A warming-only or rejected listener shows its state and error, never an empty tree.

## 5. Inputs and permissions (D-021)
- `--file`: no Kubernetes access.
- Live: the base reader role (get/list pods, Gateway API kinds), plus `create pods/portforward` in the Gateway's namespace, as a separate, optional Role in `docs/rbac.yaml`.
- Docs wording: "Live `proxy routes` requires `create pods/portforward`. kgwctl sends only `GET /config_dump`, but the grant allows any TCP forwarding to Pods in the namespace, including Envoy admin operations that change or stop the proxy. RBAC cannot narrow it to a port or path. Bind it only to trusted operators."

## 6. Output contract (`-o json|yaml`)
Envelope `{apiVersion: "kgwctl/v1alpha1", kind: "ProxyRoutes", source: {gateway, pod, capturedAt | file}, snapshot, effectiveChains, warnings}`. `snapshot` is xdsview's `model.EnvoySnapshot` as is; `effectiveChains` is keyed by route name. The schema version follows xdsview's: a breaking model change bumps ours. Documented in `docs/output.md`.

## 7. Dependencies
- xdsview `v0.3.0` (E1, E5): source, parser, filter, origin, model. Same Go, client-go and cobra as kgwctl; no conflict with Snellius v0.8.2.
- xdsview next minor (E11, xdsview#27): `pkg/envoy/render` (stdlib only), `EffectiveChain`, listener state. Expected shapes, from the xdsview session (2026-11-20):
  - `render.Text(w io.Writer, snap *model.EnvoySnapshot, opts render.Options) error`
  - `EffectiveChain(hcm model.HCMConfig, vh model.VirtualHost, r model.Route) model.EffectiveChain`, entries `{filter, requestPos, state: on|enabled-by-route|enabled-by-vhost|disabled-by-route, source: route|vhost|hcm, policy?}`
  - `model.Listener.State` (active|warming|draining), `ErrorState`, `LastUpdated`.
- kgwctl must not compile charmbracelet modules; a test asserts `go list -deps ./cmd/kgwctl` has none.

## 8. Testing
- Golden dumps from kind, kgateway v2.5.1, in `testdata/`: the demo use cases plus one Gateway with TrafficPolicies that add transformation, ext_authz and rate limit, so filter order and per-route enablement are exercised.
- Command-boundary tests: file source, `--route`/`--rule`, JSON envelope, exit 2 paths (cap exceeded, parse error, no Ready pod, RBAC denied, deadline).
- Live test on kind behind a build tag, using the least-privilege role plus the port-forward Role, like Phase 2's `hack/live`.

## 9. Requests to other repos
- xdsview#27 (E11): renderer, effective chain, listener state. Waits for the owner to start that phase.

## 10. Proposed sub-phases
| Sub-phase | Kind | Content | Waits on |
|---|---|---|---|
| 3.1 | planned | Sources (file, live port-forward, limits, pod choice), `proxy dump`, RBAC Role, JSON envelope on xdsview v0.3.0 | none |
| 3.2 | planned | Tree and effective chain per route on xdsview E11 | xdsview release with E11 |
| 3.3 | planned | Live check on kind with policies, README, docs | 3.2 |

## 11. Left to the builder
- Whether to reuse xdsview `source/kube` or write a kgwctl source (4.1).
- Flag names beyond those above, and the help text.

## 12. Open items (owner)
1. `proxy dump` in Phase 3 or Phase 4 (support bundle)?
2. An upstream kgateway opt-in diagnostics endpoint for config_dump (Codex, 2026-11-20): a fixed GET path, authenticated, admin kept on loopback. Not a dependency.
3. 3.2 waits on xdsview; 3.1 can ship first.
