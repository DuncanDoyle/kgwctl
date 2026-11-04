# Review: Phase 1 design r4 + plan r3 (round 4)

**Reviewer:** Codex (codex exec, read-only)
**Author:** Claude
**Cross-model:** yes
**Scope:** design.md r4, implementation-plan.md r3 (e14b8b8)
**Date:** 2026-11-04

---

All six round-3 findings are **resolved in the revised design and plan**. I found **no new P1/P2 issues** that block implementation or prevent a planned test from proving its claim.

| Round-3 finding | Assessment | Evidence |
|---|---|---|
| 1. Harness assumes LIST/WATCH | **Resolved** | [Plan:76](docs/phases/phase-1/implementation-plan.md:76) makes acquisition rules cover both LIST and initial WatchList. Rule-hit assertions prevent silent bypasses. [Plan:85](docs/phases/phase-1/implementation-plan.md:85) disables WatchList for classic-mode tests and restores the gates afterward; [plan:362](docs/phases/phase-1/implementation-plan.md:362) applies that helper to the held-watch race. This is compatible with the v0.36.2 [`ReplaceFeatureGates` API](https://github.com/kubernetes/client-go/blob/v0.36.2/features/features.go#L115-L148). |
| 2. Discovery retry waits escape the deadline | **Resolved** | [Plan:66](docs/phases/phase-1/implementation-plan.md:66) specifies a `kinds.DiscoveryClient` adapter issuing `Do(ctx)`, so the REST operation’s retry and throttling waits receive the command context. [Plan:149](docs/phases/phase-1/implementation-plan.md:149) adds the previously missing `429 / Retry-After: 10` bounded-return case. |
| 3. Parent deduplication merges listeners | **Resolved** | [Design:103](docs/phases/phase-1/design.md:103) and [plan:305](docs/phases/phase-1/implementation-plan.md:305) require the full defaulted `ParentRef`, including section and port. This matches the library’s complete-reference comparison. [Plan:316](docs/phases/phase-1/implementation-plan.md:316) tests two listeners on one Gateway with mixed acceptance. |
| 4. Proxy-Pod selector misses the informer | **Resolved** | [Plan:84](docs/phases/phase-1/implementation-plan.md:84) derives the selector from the registered detector. [Plan:94](docs/phases/phase-1/implementation-plan.md:94) requires a real cache run, independent controller-Pod progress, delayed proxy-Pod readiness and positive rule hits. That matches the library’s actual selector wiring. |
| 5. Missing ownership dependencies | **Resolved** | [Design:309](docs/phases/phase-1/design.md:309) adds controller Pods to ownership inputs; the following rows include them in index dependencies and make proxy enumeration depend on ownership availability. [Plan:287](docs/phases/phase-1/implementation-plan.md:287) tests denied controller Pods, and [plan:214](docs/phases/phase-1/implementation-plan.md:214) requires unavailable Gateway enumeration to produce unknown proxies rather than an empty list. |
| 6. Namespaces do not isolate envtest | **Resolved** | [Plan:86](docs/phases/phase-1/implementation-plan.md:86) requires explicit deletion and confirmed removal of every created object, including cluster-scoped objects, with serial tests and retained namespaces. [Plan:96](docs/phases/phase-1/implementation-plan.md:96) verifies cleanup. This addresses envtest’s [lack of controller-manager and garbage collection](https://book.kubebuilder.io/reference/envtest.html#testing-considerations). |

These are design-level resolutions, not verified implementation results. I inspected the sibling library and installed client-go v0.36.2 sources; I ran no tests and edited no files.
## Triage

No new findings. All round-3 findings confirmed resolved. Design-review loop closed.
