# Phase 3.3 — Live check and docs (Complete)

**Completed:** 2026-11-21
**Range:** e398be9..87bb3c8 on `duncandoyle/phase3Design`

## Summary
`proxy routes` is proven on a real kgateway (v2.5.1, Envoy 1.39.6). As reader plus the port-forward Role, with `route_source` metadata on, `--route kgwctl-proxy/orders` shows `global_disable/ext_auth`, `ext_auth/kgwctl-proxy/authz`, `ratelimit/local` and `dynamic_modules/simple_mutations` in request order. Each names its TrafficPolicy, now that kgwctl is on xdsview v0.4.2.

The view from a `proxy dump` file is identical apart from the header. Without metadata, both views list the colliding routes as ambiguous. A reader without the Role gets exit 2. `enableRouteSourceMetadata` ends at false.

The README answers the lead question from the live tree: on kgateway v2.5.1, the TrafficPolicy transformation runs after ext_authz, so it can't set a header that ext_authz checks. Design §3.1 says its sketch order is illustrative.

The "origin unresolved" list no longer matches inside a vhost prefix. kgwctl's selection rule (design §4.2) keeps its own demotion and guard, because xdsview's `filter.Filter` still selects routes that `Known` settled.

Evidence:
- `make test`, `go vet` and `gofmt -l .` are clean at 87bb3c8.
- No charmbracelet or muesli module is in the binary.
- `hack/live-check.sh goldens-check` and `routes` pass.

## Sub-phases (phase record only)
n/a

## Reviews
- Per-task reviews (Claude subagents): 3.3.1 and 3.3.2 were approved, with minors parked.
- This planned sub-phase gets its cross-model review in the Phase 3 review.

## Waivers
none

## Deliverables
9ede799, 65c8024, f58a492, 1955a7b, da89f6b, 87bb3c8. Dependency: xdsview v0.4.2 (E11 plus #28 and #29).

## Deferred
Minor items:
- two comments in `internal/proxyview/view.go` exceed two lines;
- the live check's policy assertion is a substring match;
- a vhost name that itself contains a full `-route-<i>-` marker still matches the unresolved list.
