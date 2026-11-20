# Phase 3 — `proxy routes`: Design

**Status:** draft r3, 2026-11-20 (reviewed: reviews/design-codex.md, design-codex-r2.md)
**Base:** 44be3b6

## 1. Goal and scope
Show what one Gateway's Envoy actually runs, mapped back to the Gateway API and kgateway objects that produced it. The lead question is filter execution order for a route: does a transformation run before or after ext_authz, when are headers set or removed, does rate limit see what it needs. Today the answer needs knowledge of kgateway's translator and a config dump read by hand.

In scope:
- `kgwctl proxy routes`: listeners, filter chains, virtual hosts and routes for one Gateway, and the effective HTTP filter chain per route, as a tree or JSON/YAML.
- Narrowing to one HTTPRoute and rule.
- Two sources: a live proxy over port-forward, or a config dump file.
- `kgwctl proxy dump`: write the raw config dump, for a ticket or for `--file` (proposed; open item 1).

### Anti-goals
- No Envoy writes. kgwctl sends one `GET /config_dump` and nothing else to the admin API.
- No second Envoy renderer or parser. xdsview owns parsing, narrowing, the effective chain and the text tree (D-022).
- No comparison across replicas, and no intended vs accepted diff (xdsview Phase 7, later).
- No response-order claims kgwctl can't back (§3.1).

## 2. Evidence
- Owner (2026-11-20): filter order is the hardest thing to see; Gateway API and kgateway policies don't make it explicit.
- Support research (internal tickets and threads, not cited in this public repo): a config dump is the first ask in every product and is read by hand. Recurring questions, most frequent first: filter execution order; whether a policy reached a route; which route matches, especially with delegation; what the proxy has vs what the controller says; listener and TLS matching. Customers already attach dumps to tickets, so a file source matters.
- Measured on kind, kgateway v2.5.1 (2026-11-20):
  - Envoy admin listens on 127.0.0.1:19000 only. `pods/proxy` to 19000: connection refused. The 9091 stats listener serves `/stats/prometheus`; `/config_dump` there: NotFound.
  - A dump of the `team-a` Gateway: 134 KB, one listener (`listener~80`), five virtual hosts, route names that embed the HTTPRoute, namespace and rule index (`…-httproute-orders-team-a-1-0-matcher-0`).
- Route names are ambiguous: HTTPRoute `orders` in `team-a` and `orders-team` in `a` both encode `httproute-orders-team-a-`. xdsview v0.3.0 `filter.Filter` falls back to that substring when a route has no `route_source` metadata (`filter.go:142-182`). kgateway attaches that metadata only with the Helm value `enableRouteSourceMetadata: true` (default `false`).
- Prior art: glooctl `proxy dump` shells out to `kubectl port-forward`. krp (xdsview's CLI) uses client-go port-forward. Neither restricts or documents the admin write access a port-forward gives.

## 3. Commands

### 3.1 `kgwctl proxy routes`
```bash
kgwctl proxy routes <gateway> [-n <ns>]                 # live, one Ready replica
kgwctl proxy routes <gateway> --route <ns>/<name> [--rule <i>]
kgwctl proxy routes --file dump.json [--gateway <ns>/<name>]
kgwctl proxy routes <gateway> --pod <pod>               # a specific replica of this Gateway, Ready or not
kgwctl proxy routes <gateway> -o json|yaml
```
- `<gateway>` is required live. With `--file` it is optional and only labels origins.
- `--route` narrows to one HTTPRoute's routes, `--rule` to one rule. Narrowing uses the resolved origin, not the route name (§4.2).
- The header names the source: Gateway, pod, capture time, or the file path.
- Listeners that are warming, draining or rejected always show, with their state and error, even when narrowing removes their routes (§4.3).

Tree (shape; the final text is xdsview's renderer):
```
Gateway team-a/team-a · pod team-a-86d49b69c4-m86x6 · 2026-11-20 19:40:12 UTC
listener~80 (active) · Gateway listener http
└─ vhost orders.example (listener~80~orders_team-a_example)
   └─ route 0 · HTTPRoute team-a/orders rule 0 · PathPrefix /orders → team-a/orders:8080
      filters (request order):
        1 io.solo.transformation        on                 TrafficPolicy team-a/headers
        2 envoy.filters.http.ext_authz  enabled by route   TrafficPolicy team-a/auth
        3 envoy.filters.http.router
```
Response order: Envoy runs encoder filters in reverse, and only filters with an encoder take part. kgwctl shows a response order only where xdsview knows which filter types have an encoder; for other types it says "unknown on the response path" rather than guessing. Until xdsview carries that, the output shows request order only.

### 3.2 `kgwctl proxy dump`
```bash
kgwctl proxy dump <gateway> [-n <ns>] [--pod <pod>] > dump.json
```
Streams Envoy's config dump unchanged, with the same deadline but no size cap, because it does not parse. Help text and README: "Envoy redacts only the fields it knows are sensitive, and only in strongly typed config. The dump can still contain credentials, tokens or keys in other config. Check it before you attach it to a ticket." Default redaction is Phase 4's job (support bundle).

### 3.3 Exit codes
0 printed; 2 could not read the dump (no Ready pod, `--pod` not a replica of the Gateway, RBAC, timeout, too large, parse error). No exit 1: the command reports, it does not judge.

## 4. Architecture
```
capture (kgwctl)             parse + annotate + narrow (xdsview)      render
live: port-forward ─┐        parser.Parse → origin.Annotate          ─┐ render.Text (tree)
file: os.Open      ─┴─► Capture ─► → narrow by origin (--route/--rule) ─┴► kgwctl envelope (-o json|yaml)
                             EffectiveChain per route, per HCM
```

### 4.1 Capture
kgwctl writes its own source; xdsview v0.3.0 `source/kube` returns only a reader, picks the first Ready pod, and `source.Admin` follows redirects.
- Result: `Capture{Pod (ns/name, or empty for a file), CapturedAt, Body io.ReadCloser}`. The same type serves `proxy dump` and, later, Phase 4's per-pod collection with its own redaction and partial failures.
- Pod: pods labelled `gateway.networking.k8s.io/gateway-name=<name>` in the Gateway's namespace. Pick the Ready, non-terminating pod with the oldest start time, then by name, so repeated runs pick the same replica. `--pod` must carry the Gateway's label, or exit 2.
- Transport: client-go SPDY port-forward to 19000 on an OS-assigned loopback port. One HTTP client with redirects refused. The tunnel closes on success, error, cancellation and an oversized body.
- Request: plain `GET /config_dump`, no query parameters.
- Limits: one deadline over discovery, tunnel and read, default 30 s (the global `--request-timeout`). Parsing (`proxy routes`, live or `--file`) caps the body at 64 MiB, enforced before the parser reads it, raised with `--max-size`. Over the cap: exit 2 with the size and the flag.
- Errors name the RBAC to add (D-021).

### 4.2 Origins and narrowing
kgwctl passes `origin.Kgateway` the Gateway ref and the known HTTPRoutes from its own one-shot load, so route origins resolve without xdsview's cluster-wide `kube.HTTPRoutes` list. `--route`/`--rule` narrow on the resolved origin (an E11 addition to xdsview, §7). A route drives a match only when its origin is Exact, or Inferred from the route name alone. One that needed the known routes to break a tie (xdsview `RouteAnnotation.NarrowedByKnown`) is not a match: Envoy may still serve a deleted route whose name collides with a live one. kgwctl lists it as ambiguous, with "one candidate exists in the cluster". A route whose origin stays Ambiguous is never shown as a match; the output lists it as ambiguous. `--file` never calls Kubernetes, even when a cluster is reachable; origins come from route names and `route_source` metadata only, so a dump without metadata can leave colliding routes ambiguous.

### 4.3 Listener state
Active, warming, draining and rejected state come from xdsview (E11). Non-active listeners survive narrowing. A warming-only or rejected listener shows its state and error, never an empty tree. Sections xdsview skipped with parser warnings are printed as warnings in the tree and in JSON, so a partial result never looks complete.

## 5. Inputs and permissions (D-021)
- `--file`: no Kubernetes access.
- Live: the base reader role (get/list pods and Gateway API kinds), plus `create pods/portforward` in the Gateway's namespace, as a separate, optional Role in `docs/rbac.yaml`. `check` keeps working with the base role alone.
- Docs wording: "Live `proxy routes` and `proxy dump` require `create pods/portforward`. kgwctl sends only `GET /config_dump`, but the grant allows any TCP forwarding to Pods in the namespace, including Envoy admin operations that change or stop the proxy. RBAC cannot narrow it to a port or path. Bind it only to trusted operators."

## 6. Output contract (`-o json|yaml`)
Follows `docs/output.md`: `{schemaVersion: "kgwctl.kgateway.dev/v1alpha1", kind: "ProxyRoutes", item}`. `item` is `{source: {gateway, pod, capturedAt} | {file}, snapshot, warnings}`. `snapshot` is xdsview's `model.EnvoySnapshot` as is, with the effective chain attached to each route inside its HCM, so a route shared by two filter chains keeps two chains. A breaking xdsview `model` change bumps kgwctl's schema version. The JSON contract ships in 3.2 with E11, not earlier.

## 7. Dependencies
- xdsview `v0.3.0` (E1, E5): parser, origin, model. Same Go, client-go and cobra as kgwctl; no conflict with Snellius v0.8.2.
- xdsview `v0.4.0` (E11, xdsview#27), its Phase 6 design r2 (xdsview `15b3fe1`, awaiting the owner):
  - `pkg/envoy/render`: stdlib-only deterministic text, generic `Style` and `Extra` hooks.
  - `pkg/envoy/chain`: `Effective(hcm, vh, r)` and `Annotate(snap, mapper)`, attaching `Route.Chain` per route inside its HCM. Entries carry a state (on, off, enabled, disabled, unknown), a `Level` (route, virtualHost, routeConfig), `Request` and `Response` sides (yes, no, unknown) and `Contributions []{Kind: extension|policy, Ref, Field}`. `RequestOrder()` and `ResponseOrder()` drop "no" sides; unknown stays, marked "?". `Conditional` marks a baseline that a weighted backend can override.
  - `filter.Filter` narrows on Exact or single-candidate Inferred origins, never Ambiguous; `filter.Ambiguous(snap, opts)` names the ambiguous routes. kgwctl adds the tie-break marker of §4.2.
  - Listener state (active, warming, draining, rejected) with error and last update; non-active listeners survive narrowing.
- kgwctl must not compile charmbracelet modules; a test asserts `go list -deps ./cmd/kgwctl` has none.

## 8. Testing
- Goldens: dumps captured from kind running kgateway OSS v2.5.1 only, so they are redistributable (E6): the demo use cases plus one Gateway with TrafficPolicies that add transformation, ext_authz and rate limit, a route that disables a filter, and two HTTPRoutes whose names collide (`orders`/`team-a` and `orders-team`/`a`).
- Command-boundary tests: file source; `--route`/`--rule` on the colliding routes; JSON envelope; parser warnings surfaced; non-active listener under narrowing; exit 2 for cap exceeded, parse error, no Ready pod, `--pod` outside the Gateway, RBAC denied, deadline.
- Capture tests against a fake admin server: a redirect is refused, the tunnel closes after cancellation and after an oversized body.
- Live test on kind behind a build tag: `proxy routes` with the base role plus the port-forward Role; `check` still passes with the base role alone.

## 9. Requests to other repos
- xdsview#27 (E11): renderer, effective chain, listener state; plus, from this review, narrowing on resolved origin, response-path knowledge per filter type, policy contributions as a list, and non-active listeners surviving narrowing. Waits for the owner to start that phase.

## 10. Proposed sub-phases
| Sub-phase | Kind | Content | Waits on |
|---|---|---|---|
| 3.1 | planned | Capture (file, live port-forward, limits, pod choice), `proxy dump`, RBAC Role, golden dumps | none (xdsview v0.3.0) |
| 3.2 | planned | `proxy routes`: tree, effective chain, narrowing, JSON contract | xdsview release with E11 |
| 3.3 | planned | Live check on kind with policies, README, docs | 3.2 |

`proxy routes` ships whole in 3.2. 3.1 publishes no `proxy routes` output, so no JSON contract or tree goes out without listener state and origin narrowing.

## 11. Left to the builder
- Flag names beyond those above, and the help text.

## 12. Open items (owner)
1. `proxy dump` in Phase 3 (recommended: it is the recovery path for an oversized dump and the ticket workflow) or Phase 4?
2. An upstream kgateway opt-in diagnostics endpoint for config_dump (Codex, 2026-11-20): a fixed GET path, authenticated, admin kept on loopback. Not a dependency.
