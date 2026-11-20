Cross-model: YES (author: Claude Opus 5.5, reviewer: Codex, command: codex exec --sandbox read-only)
Scope: phase 3 design r3 + implementation plan r2
Diff: 0080d0f..10089cd
Timing: on time

**Two round-2 P1s and one P2 remain open.**

### Round-2 verification

- **P1-1 open.** [design.md:85](docs/phases/phase-3/design.md:85) excludes origins inferred using `Known` from selection. [implementation-plan.md:158](docs/phases/phase-3/implementation-plan.md:158) allows a marked match, and the live check repeats that expectation at [line 183](docs/phases/phase-3/implementation-plan.md:183). xdsview v0.3.0 collapses a sole `Known` survivor into ordinary `Inferred` (`pkg/envoy/origin/kgateway.go:93–111`). A marker still lets a deleted route’s filter chain appear under the surviving route’s identity. The single-survivor case is absent from the acceptance tests.
- **P1-2 open.** [implementation-plan.md:110](docs/phases/phase-3/implementation-plan.md:110) cites xdsview’s `ctxDialer`, whose socket-close hook applies only to direct connections (`pkg/envoy/source/kube/portforward.go:187–219`). With a kubeconfig or environment proxy, a stalled upgrade can still hold the connection after the deadline. The exec-plugin test is specified.
- **P2-4 partially resolved.** [implementation-plan.md:47](docs/phases/phase-3/implementation-plan.md:47) supplies alternate capture instructions if `proxy dump` moves to Phase 4. The recovery message and parity check still require the deferred command at [line 170](docs/phases/phase-3/implementation-plan.md:170) and [line 183](docs/phases/phase-3/implementation-plan.md:183).
- **P3-1 partially resolved.** [implementation-plan.md:90](docs/phases/phase-3/implementation-plan.md:90) observes actual closure on deadline and explicit body close. The cap test at [line 96](docs/phases/phase-3/implementation-plan.md:96) still checks `ReadCapped` alone. It can pass while `proxy routes` leaks the tunnel on a cap breach.
- **Resolved in the plan.** P1-3 adds controlled fixtures and a pre-commit content review. P2-1 makes file replay offline and uses a metadata-enabled parity capture. P2-2 fixes cross-namespace attachment and splits exact selection from ambiguity. P2-3 preserves forbidden status and puts the warning in the RBAC manifest. P3-2 adds reader-only and malformed-section cases. P4-1 points Phase 4 at kgwctl’s `Capture`.

### New findings

- **P2 · Incomplete step.** [implementation-plan.md:168](docs/phases/phase-3/implementation-plan.md:168) omits the file-only `--gateway <ns>/<name>` promised at [design.md:36](docs/phases/phase-3/design.md:36). This drops offline Gateway origin labels. It also leaves the parity check underspecified because xdsview includes Gateway context in listener and route-config origins.
- **P3 · Test gap.** [implementation-plan.md:121](docs/phases/phase-3/implementation-plan.md:121) points the kubeconfig at a stalled TCP server and checks elapsed time and socket closure. Gateway lookup and Pod listing precede the upgrade, so those assertions can pass on a discovery timeout. Serve those reads successfully, stall only the port-forward POST, and assert that it arrived.

Static review only. The pinned clone lacked `15b3fe1`, so I checked the E11 assumptions against the kgwctl design and plan.
## Triage

| # | Finding | Decision |
|---|---|---|
| P1-1 (r2) | Design excludes, plan marks a `Known` tie-break match | accept: one rule, exclusion. A `NarrowedByKnown` route (xdsview `5d82113`) is not a match; listed as ambiguous with "one candidate exists in the cluster". Design §4.2, plan 3.2.1, 3.3.1 aligned; single-survivor test added. |
| P1-2 (r2) | Socket-close hook misses proxied connections | accept: plan 3.1.3: `Fetch` returns at the deadline regardless (dial in a goroutine); the stuck socket ends at process exit, acceptable for a CLI (xdsview#26). `TestProxyDumpUpgradeHangsViaProxy` added. |
| P2-4 (r2) | Recovery message and parity still need `proxy dump` | accept: both name the `kubectl port-forward` + `curl` fallback if the owner moves `proxy dump`. |
| P3-1 (r2) | Cap breach through the command untested | accept: `TestProxyRoutesCapClosesTunnel` in 3.2.2. |
| P2 new | `--file --gateway` missing from the plan | accept: added to 3.2.2 and the 3.3.1 parity run. |
| P3 new | Upgrade-hang test can pass on a discovery timeout | accept: envtest serves discovery; only the port-forward request stalls; the test asserts it arrived. |
