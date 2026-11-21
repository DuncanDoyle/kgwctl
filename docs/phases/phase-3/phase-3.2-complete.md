# Phase 3.2 — `proxy routes` (Complete)

**Completed:** 2026-11-21
**Range:** 9cc9685..9fb1d32 on `duncandoyle/phase3Design`

## Summary
`kgwctl proxy routes` shows one Gateway's listeners, virtual hosts and routes, with the effective HTTP filter chain per route in request order. Each filter carries its state and level, and the policy behind it where xdsview v0.4.0 knows it. It runs live over the 3.1 capture, or from a file with `--file [--gateway]`, without any Kubernetes call.

`--route`/`--rule` match only routes whose origin is exact, or inferred from the route name alone. Ambiguous and unresolved routes are listed, never shown as matches; ambiguity is judged on the readings of the route name itself. Non-active listeners and parser warnings stay visible. `-o json|yaml` prints one `ProxyRoutes` envelope (docs/output.md). A file that isn't a config dump exits 2.

Evidence: `make test` (race), `go vet`, `gofmt -l .` are clean at 9fb1d32, and `go list -deps ./cmd/kgwctl` has no charmbracelet or muesli module. Every task and fix item has fail-before and pass-after evidence in the SDD reports.

## Sub-phases (phase record only)
n/a

## Reviews
- Per-task reviews (Claude subagents): 3.2.1–3.2.3 approved. 3.2.3 took one fix round (`listeners: []`).
- reviews/3.2-codex.md: 3 P1, 1 P2, 2 P3, 2 P4, all accepted and fixed in one wave.
- reviews/3.2-codex-r2.md: 8 of 8 resolved. One new P2, the unresolved list over-reporting through a vhost prefix, is deferred to 3.3.1.

## Waivers
none

## Deliverables
28c629b, 920cd86, 38c565c, d44c31d, f987544, 31e9a32, 80ed1c4, 9fb1d32. New: `internal/proxyview`, `internal/app/proxy_routes.go`, the ProxyRoutes section in docs/output.md, the README `proxy routes` walkthrough. Dependency: xdsview v0.4.0 (E11, kgwctl#10).

## Deferred
- 3.3.1: match the unresolved marker only in the route-name part.
- xdsview, tracked in that repo and pending its owner:
  - policy attribution for transformation (`dynamic_modules/simple_mutations`), `rateLimit.local` and `global_disable/ext_auth`;
  - per-route-config parser skipping;
  - a route-line hook, so kgwctl can drop its render-order counter.
- Minor:
  - no per-candidate "exists in the cluster" mark for two or more surviving readings;
  - inline route configs share an empty key;
  - the ProxySource intro wording.
