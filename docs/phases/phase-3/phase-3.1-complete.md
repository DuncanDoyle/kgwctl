# Phase 3.1 — Capture and `proxy dump` (Complete)

**Completed:** 2026-11-21
**Range:** c38fe47..3358391 on `duncandoyle/phase3Design`

## Summary
`kgwctl proxy dump <gateway>` streams one Envoy replica's config dump over a client-go port-forward. It picks the oldest Ready replica every time, or `--pod`, which must be a replica of the Gateway. It sends one `GET /config_dump`, refuses redirects on both the Envoy request and the Kubernetes upgrade, and ends at `--request-timeout`. The tunnel closes on every path, also behind an HTTP or SOCKS5 proxy (owner, 2026-11-21). A reader without `create pods/portforward` gets exit 2 with the Role to add. Usage errors exit 2. `docs/rbac.yaml` adds the optional `kgwctl-proxy-portforward` Role, with the admin-write warning above it.

Evidence:
- `make test`, `go vet ./...` and `gofmt -l .` are clean at 3358391. The race run of the proxydump and app subsets passes.
- Each code item failed before its change and passed after. Per-task evidence is in the SDD reports; the fix wave is in reviews/3.1-codex-r2.md.
- On kind (kgateway v2.5.1, Envoy 1.39.6), `proxy dump` exits 0 with the port-forward Role and exits 2 naming `pods/portforward` as reader only.
- Golden dumps `demo.json`, `policies.json` and `collide-meta.json` pass `hack/live-check.sh goldens-check`: no inline secret material, and no unredacted credential keys.
- The owner reviews the golden diffs before push.

## Sub-phases (phase record only)
n/a

## Reviews
- Per-task reviews (Claude subagents): 3.1.1–3.1.4 approved; minors parked or carried.
- reviews/3.1-codex.md: 3 P1, 2 P2, 2 P3, 2 P4, all accepted and fixed in one wave.
- reviews/3.1-codex-r2.md: 7 of 9 resolved. The new P2 (proxy TLS config) was rejected: discovery already uses the same TLS config through the same proxy. The golden predicate gap was parked to 3.2.1. The close evidence is this record.

## Waivers
none

## Deliverables
3fd630d, fc68939, 444d3fc, b8f017a, eabab0c, d7a4e6f, 36b6a25, dc68086, a1cb9e3, 716ae90, 3358391.
New: `internal/proxydump`, `internal/app/proxy.go`, `hack/live/proxy-fixtures.yaml`, `testdata/dumps/`. Dependencies: golang.org/x/net becomes direct; client-go port-forward brings k8s.io/streaming, moby/spdystream and gorilla/websocket as indirect modules.

## Deferred
- 3.2: the golden predicate for the filter-disable route. xdsview 6.4 wording "(chosen from known HTTPRoutes)" and bracketed IPv6.
- Minor: a full clientset per Dial; a stop wait of up to 1 s after the deadline. The "Gateway … not found" capitalisation is plan-mandated.
- The fixtures stay on kind in namespaces kgwctl-proxy and proxy, for 3.2 and 3.3.
