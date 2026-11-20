Cross-model: YES (author: Claude Opus 5.5, reviewer: Codex, command: codex exec --sandbox read-only)
Scope: phase 3 design r1 (plan not yet written)
Diff: 44be3b6..ab40189
Timing: on time

## P1 — Correctness or unsafe behaviour

- **§3.1 and §4.2 — Route selection can return the wrong route.** xdsview `filter.go:143–181` uses exact metadata when present and otherwise matches name substrings, ignoring the origin already resolved using `Known`. Without metadata, `team-a/orders` and `a/orders-team` encode the same `orders-team-a` suffix. The CLI can report another route’s auth or transformation chain even when live annotation resolved the ambiguity. [Design §4.2](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/design.md:79)
- **§3.1 and §7 — Reversing the request chain gives a false response chain.** Envoy invokes encoder-capable filters on the response path. Reversing the decoder list can include decoder-only filters and omit encoder-only filters, directly undermining the command’s execution-order diagnosis. [Design §3.1](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/design.md:53), [Envoy HTTP filter ordering](https://www.envoyproxy.io/docs/envoy/v1.40.0/intro/arch_overview/http/http_filters)
- **§3.2 — Raw ticket exports can expose credentials.** The proposed help mentions topology and policy configuration and presents Envoy redaction as reliable. Envoy documents best-effort redaction that depends on strongly typed configuration. An unchanged dump can still contain sensitive values, so the ticket-export guidance needs to say so explicitly. [Design §3.2](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/design.md:55), [Envoy admin documentation](https://www.envoyproxy.io/docs/envoy/latest/operations/admin)

## P2 — Design gaps and contract mismatches

- **§4.1 and §11 — v0.3.0 reuse is already ruled out by the stated contract.** `source/kube/portforward.go:40–91,304–321` accepts cluster options only, picks the first Ready match and returns only a reader. `source/admin.go:21–30` uses the default redirect-following client. The design needs a source that provides the actual selected pod, validates `--pod` membership in the Gateway, and enforces the single-request rule. Otherwise output can identify the wrong replica or follow a redirect. [Design §4.1](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/design.md:72)
- **§4.1 and §12 — The oversized-dump recovery is undefined.** The live cap points users to `proxy dump` plus `--file`, but `dump` is optional and its cap is unspecified. xdsview `parser.go:42–49` reads the entire file into memory. Sharing the cap makes the workaround fail at collection. Exempting file input without a resource policy moves the memory risk to replay. [Design §4.1](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/design.md:76)
- **§4.3 and §10 — Listener diagnostics can vanish.** v0.3.0 parses only `active_state`, and `filter.go:85–97` removes listeners with no matching virtual host. Even after E11 adds listener state, a narrowed view can hide the rejection that explains a missing route. Define what survives narrowing and what status 3.1 can report before E11. The roadmap still lists Phase 3 as having no outstanding dependency. [Design §4.3](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/design.md:82), [Roadmap Phase 3](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/roadmap.md:40)
- **§6 — JSON cannot uniquely identify every effective chain.** xdsview `parser.go:132–137` can attach the same RDS routes to multiple HCMs. A map keyed by route name then collapses routes whose filter chains can differ. The proposed envelope also diverges from the documented `schemaVersion`/`item` convention. Settle both before 3.1 publishes a machine contract. [Design §6](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/design.md:90), [Output conventions](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/output.md:5)

## P3 — Test gaps

- **§8 — Test the privilege boundary and the diagnostic result.** The live happy path and generic timeout/RBAC cases leave redirect refusal, tunnel teardown after cancellation or an oversized body, and `check` working with the existing reader role unverified. Add observable cases for those boundaries and for malformed sections that xdsview skips with warnings. Otherwise a partial result can look like a complete view. [Design §8](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/design.md:101), [D-017 and D-021](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/decisions.md:137), [Reader RBAC](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/rbac.yaml:1)

## P4 — Later-phase fragility

- **§4.1 — Phase 4 needs reusable per-pod capture.** `source.Source` returns only a config-dump stream, while the bundle needs dumps and stats across replicas with partial failures and default redaction. Define a reusable capture boundary with pod identity and per-capture errors so the bundle can apply its own redaction before packaging. [Roadmap Phase 4](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/roadmap.md:49)
- **§4.2 and §7 — Phase 6 needs policy provenance.** xdsview `origin/kgateway.go:231–241,257–285` maps filter instances to GatewayExtensions and separately records route policy contributions by field. A single `policy?` per filter loses that distinction and can misattribute inherited or merged policy. [Design §7](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/design.md:93), [Roadmap Phase 6](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/roadmap.md:69)
- **§7 and §8 — Phase 7 has an open fixture provenance dependency.** The no-Charm build test covers compile dependencies. E6 separately tracks enterprise fixtures still present in the v0.3.0 clone. Specify OSS-only, redistributable goldens before copying demo captures into kgwctl. [Program E6](/Users/ddoyle/Development/claude/kgateway-tooling-claude/program.md:38), [Roadmap Phase 7](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/roadmap.md:78)
## Triage

| # | Finding | Decision |
|---|---|---|
| P1-1 | `--route` narrowing by name substring can pick another route | accept: verified (`filter.go:142-182`; `orders`/`team-a` vs `orders-team`/`a`). r2 §4.2: narrow on resolved origin, Ambiguous never shown as a match; asked of xdsview (E11). Colliding-names golden in §8. |
| P1-2 | Reversed request chain is a false response chain | accept: r2 §3.1 shows response order only where xdsview knows a filter type has an encoder; asked of xdsview. |
| P1-3 | Raw dump can still hold credentials | accept: r2 §3.2 help text says redaction is partial; default redaction stays Phase 4. |
| P2-1 | xdsview `source/kube` can't satisfy pod identity, `--pod` check, no redirects | accept: verified. r2 §4.1: kgwctl writes its own capture; §11 choice removed. |
| P2-2 | Oversized-dump recovery undefined | accept: r2 §4.1/§3.2: `proxy dump` streams uncapped; parsing capped at 64 MiB with `--max-size`. |
| P2-3 | Listener diagnostics vanish under narrowing; 3.1 has no state | accept: r2 §4.3 non-active listeners survive narrowing (asked of xdsview); `proxy routes` moves whole to 3.2, so 3.1 publishes no view without state. Roadmap Waits on updated. |
| P2-4 | JSON keyed by route name collapses shared routes; envelope off-convention | accept: verified (`parser.go:132-137` clones RDS per HCM). r2 §6: chain attached per route within its HCM; `schemaVersion`/`kind`/`item` envelope. |
| P3-1 | Privilege boundary and partial results untested | accept: r2 §8 adds redirect, teardown, `check` with base role, parser warnings. |
| P4-1 | Phase 4 needs reusable per-pod capture | accept: r2 §4.1 `Capture{Pod, CapturedAt, Body}`. |
| P4-2 | One policy ref per filter loses provenance | accept: r2 §7 asks xdsview for a list of policy contributions. |
| P4-3 | Fixture provenance (E6) | accept: r2 §8 goldens from kgateway OSS on kind only. |
