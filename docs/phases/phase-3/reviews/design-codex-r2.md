Cross-model: YES (author: Claude Opus 5.5, reviewer: Codex, command: codex exec --sandbox read-only)
Scope: phase 3 design r2 + implementation plan
Diff: 44be3b6..0080d0f
Timing: on time

**Round one:** r2 adopts all 11 dispositions. It assigns capture to kgwctl, gates the complete routes view on E11, defines per-HCM output, qualifies response order and raw-dump redaction, and specifies OSS fixtures and policy provenance. The findings below identify where those fixes remain incomplete in the plan. This was a static review of `44be3b6..HEAD` and the pinned sources.

### P1 · Correctness or unsafe behaviour

- **Resolved origin can still select the wrong route.** xdsview v0.3.0 reduces ambiguous name splits to the candidates present in `Known` and marks a sole survivor `Inferred`. If Envoy still serves a deleted route and its colliding counterpart remains in Kubernetes, the planned pipeline can report the deleted route’s auth chain under the surviving route’s name. E11 needs an explicit rule for when an inferred origin may drive selection. [Plan:154](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/implementation-plan.md:154)
- **The port-forward deadline can expire while the upgrade remains open.** Client-go’s stock SPDY dialer creates its own request, and the upgrade transport blocks reading headers from the socket. Closing the port-forward stop channel only helps after negotiation. The plan needs cancellation of the upgrade connection itself and bounded waiting for credential exec plugins, which the existing lifecycle suite already exercises for other commands. [Plan:110](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/implementation-plan.md:110) · [lifecycle_test.go:222](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/internal/app/lifecycle_test.go:222)
- **The golden capture step can commit credentials.** `grep -c '"private_key"'` counts matching lines. It cannot verify redaction or detect credentials in other fields, which r2 explicitly says can survive. The plan needs controlled fixture inputs and a content check before committing raw captures. [Plan:141](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/implementation-plan.md:141) · [Design:62](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/design.md:62)

### P2 · Design and plan mismatches

- **File replay can contact Kubernetes.** The plan skips `load.Load` only when the cluster is unreachable. D-021 and §5 make `--file` offline, so a reachable ambient cluster must have no effect on replay. The live/file parity check also needs a metadata-enabled capture because kgateway disables `route_source` by default. Ordinary dumps can lose exact route selection offline. [Plan:159](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/implementation-plan.md:159) · [Plan:174](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/implementation-plan.md:174) · [D-021](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/decisions.md:137)
- **The collision fixture cannot prove the expected result as written.** The second namespace needs an explicit `parentRef.namespace` and listener `allowedRoutes` permission. Once both routes attach, v0.3.0 keeps both candidates ambiguous even with both in `Known`. Split exact-selection and ambiguity cases so each has a valid expected result. [Plan:139](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/implementation-plan.md:139) · [Plan:155](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/implementation-plan.md:155)
- **The RBAC step misses two parts of the contract.** The stock SPDY upgrader flattens non-101 responses into text, so the proposed `IsForbidden` check needs typed status preservation before the upgrader consumes the response. Step 4 also puts the admin-write warning in the README alone. D-021 requires it in `docs/rbac.yaml`, where operators will copy the grant. [Plan:122](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/implementation-plan.md:122) · [D-021:141](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/decisions.md:141)
- **3.1 assumes an open scope decision.** The plan ships `proxy dump` and uses it to generate every golden, while r2 still asks the owner whether it belongs in Phase 3 or 4. Deferral changes both fixture collection and the oversized-dump recovery path. [Plan:47](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/implementation-plan.md:47) · [Design:130](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/design.md:130)

### P3 · Test gaps

- **Teardown tests count callbacks without observing closure.** The fake `stop` count and standalone `ReadCapped` tests can pass while a response body or port-forward remains open. Exercise an in-flight cancellation and a cap breach through the command, observing the connection or listener close. [Plan:90](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/implementation-plan.md:90)
- **Two round-one acceptance checks still lack coverage.** Ordinary envtest requests reach the API server as admin, so rerunning `check` tests cannot prove reader-only access. The routes plan also lacks a malformed-section case asserting a visible warning alongside the surviving routes in tree and JSON. [Plan:121](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/implementation-plan.md:121) · [proxy.go:89](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/internal/testenv/proxy.go:89) · [Plan:154](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/implementation-plan.md:154)

### P4 · Later-phase fragility

- **Phase 4 still names the superseded provider.** The roadmap points bundle collection at krp `source`, while r2 assigns reusable per-pod capture to kgwctl. That leaves the next phase with conflicting integration instructions for replica identity and partial failures. [Roadmap:52](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/roadmap.md:52) · [Design:77](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/design.md:77)
## Triage

| # | Finding | Decision |
|---|---|---|
| P1-1 | Inferred origin from `Known` can select a deleted colliding route | accept: design r3 §4.2 and plan 3.2.1: Exact or name-only Inferred drive a match; a tie broken by `Known` is marked "(inferred from cluster routes)". Raised with xdsview (its Phase 6 matches single-candidate Inferred). |
| P1-2 | Port-forward upgrade ignores the deadline; exec plugins | accept: verified against xdsview `portforward.go:187-228` (same client-go gap, #25). Plan 3.1.3: ctx-bound upgrade, `TestProxyDumpUpgradeHangs`, exec plugin lifecycle case. |
| P1-3 | Golden capture can commit credentials | accept: plan 3.1.4: controlled inputs (no TLS, no Secrets), jq content check over all keys, human diff review, stop on failure. |
| P2-1 | `--file` can contact Kubernetes; parity needs metadata | accept: verified (`enableRouteSourceMetadata: false` by default). Plan 3.2.2: file never calls Kubernetes, tested with a reachable envtest; `collide-meta.json` captured with metadata on; 3.3.1 parity uses it. |
| P2-2 | Collision fixture can't prove its result | accept: plan 3.1.4 sets `parentRefs[].namespace` and `allowedRoutes: All`; 3.2.1 splits exact (`collide-meta.json`) and ambiguity (`collide.json`) cases. |
| P2-3 | Forbidden flattened by the upgrader; warning missing in rbac.yaml | accept: plan 3.1.3: read the upgrade status before the upgrader; warning verbatim above the Role in `docs/rbac.yaml`. |
| P2-4 | 3.1 assumes `proxy dump` before the owner decides | accept: plan sub-phase table states the fallback if the owner moves it to Phase 4. Owner decision still open (design §12.1). |
| P3-1 | Teardown tests count callbacks only | accept: plan 3.1.2 uses a real loopback listener and observes server-side close. |
| P3-2 | Reader-only `check` and malformed-section cases missing | accept: `TestCheckAsReader` (envtest `AddUser` + `kgwctl-reader`); malformed-section case in 3.2.1. |
| P4-1 | Roadmap Phase 4 still names krp `source` | accept: roadmap Phase 4 now points at kgwctl's `Capture`. |
