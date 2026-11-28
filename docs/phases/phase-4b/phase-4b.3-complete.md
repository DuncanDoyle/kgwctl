# Phase 4b.3 — Phase review fixes (Complete)

**Completed:** 2026-11-28
**Range:** 5645777..d5ffd66 on `duncandoyle/phase4b`

## Summary
All eight accepted findings of reviews/phase-codex.md are fixed:
- `hack/release.sh` claims the tag directory atomically. Its printed install check uses fresh `GOPATH`, `GOMODCACHE` and `GOBIN` directories.
- `--select exact --rule` keeps exact index evidence from any agreeing claim, and requires both the rule and the match index to be known.
- Text lists each distinct use of a shared route source once (DD-5). The key is the same per-reading projection that JSON prints, so text and JSON agree on what counts as distinct. A merged line keeps `(partly from name)` when any merged use carries it.
- Text marks an incomplete reading with `(incomplete)`. docs/output.md says where the marker appears.
- `selection` in JSON says whether the cluster's HTTPRoutes were available for `known`.
- The trailer label comes from xdsview v0.7.1 (`provenance.AboveRouteLabel`, "policies recorded above the route"), which makes no claim about inheritance (D-024).
- The README warns what `proxy routes` output can still contain before it is shared.
- The payload coverage test includes `model.Ref`.

xdsview moved from v0.7.0 to v0.7.1 (kgwctl#17). kgwctl keeps its `indexCertainty` normalisation (DD-4, DD-6), because the library prefers an unset index certainty on a tie and would drop an explicit `Inferred` marker.

Evidence at d5ffd66:
- `make test` (race, envtest) exits 0 on 18 packages.
- `make lint` and `go mod tidy -diff` are clean.
- `hack/live-check.sh goldens-check` passes.
- `hack/live-check.sh routes` passes on kind `kgwctl-live` (kgateway v2.5.1, Envoy 1.39.6, `enableRouteSourceMetadata=false`).
- No recorded-dump golden changed after round 1.

## Sub-phases (phase record only)
n/a

## Reviews
All rounds are cross-model (Codex gpt-6.2-sol reviewing Claude Opus subagents).

| Review | Findings | Outcome |
|---|---|---|
| reviews/4b.3-codex.md (r1) | 2 P1, 3 P2, 1 P3 accepted; 1 deferred to xdsview | Fixed in round 2 |
| reviews/4b.3-codex-r2.md | 1 P1, 2 P2 | Fixed in round 3 |
| reviews/4b.3-codex-r3.md | 2 P2 (text dedup key) | Owner chose a structural key (option A); fixed in round 4 with the v0.7.1 bump |
| reviews/4b.3-codex-r4.md | 1 P1 (merged line loses the name-derived marker), 2 doc P2 | Fixed in round 5 |
| reviews/4b.3-r5-consult-codex.md | Consult | Codex agreed with the round-5 fix and with release without another review round. The owner approved on 2026-11-28 |

Every finding from r2 on can only be triggered by a downstream provenance module. No recorded kgateway dump triggers them.

## Waivers
none

## Deliverables
- `hack/release.sh`, `internal/proxyview` (view, text, payload, coverage), `internal/app/proxy_routes.go` and tests, goldens.
- README `proxy routes`, docs/output.md (`selection`, the `(incomplete)` marker), design.md (DD-4, DD-5, DD-6, R24, DD-22).
- `go.mod`: xdsview v0.7.0 → v0.7.1.

## Notes for later phases
- JSON omits `indexCertainty` when it equals the reading's certainty, so JSON cannot tell an explicit `Inferred` from an unset one, while text can. The schema allows emitting it if a JSON consumer needs the difference (reviews/4b.3-r5-consult-codex.md #2).
- The main route tree's multi-candidate label comes from xdsview and shows no `(incomplete)` marker. JSON and the selection lists do show it.

## Deferred / open
- r1: one item deferred to xdsview, the "inherited policies" wording. It was delivered in v0.7.1.
