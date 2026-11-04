# Review: Phase 1 design (round 1)

**Reviewer:** Codex (codex exec, read-only)
**Author:** Claude
**Cross-model:** yes
**Scope:** docs/phases/phase-1/design.md, maintainer-proposal.md at 372c23f
**Date:** 2026-11-04

---

The design needs revision before implementation planning. I found 13 issues below. This was a source review; I did not run builds or tests, and changed no files.

Evidence paths beginning `../` are relative to the kgwctl repository.

1. **P1 — Waiting for `Settled()` does not make `version` inputs complete.**  
   **Design:** [§4, lines 154–157](docs/phases/phase-1/design.md:154), supporting §3.3’s proxy and CRD versions.  
   **Evidence:** `../kgateway-ui-claude/pkg/cache/cache.go:349–355,365–394` excludes proxy Pods and CRDs from the core readiness check. Their access states are recorded independently at `491–509`. `pkg/process/kube.go:50–59` refuses to observe proxy processes until `ProxyPodsGVR` is OK.  
   **Why:** A successful command can nondeterministically omit proxy versions or CRD annotations simply because those lists finish after the core lists.  
   **Direction:** Define readiness per command. `version` must additionally wait for the required optional inputs to become available or fail, within the command deadline.

2. **P1 — The default ownership filter excludes GatewayParameters entirely.**  
   **Design:** [§5, lines 168–170](docs/phases/phase-1/design.md:168) and §6.2, lines 189–192.  
   **Evidence:** `../kgateway-ui-claude/pkg/kinds/kinds.go:30–32` explicitly says parameter kinds are never attributed. `pkg/install/assign.go:44–63` has no parameters collection in `Owned`; `pkg/kinds/upstream.go:32` categorizes GatewayParameters as parameters.  
   **Why:** With an install detected, the promised `get gatewayparameters` and `describe gatewayparameters` cannot return these objects under the stated default rule.  
   **Direction:** Define an explicit visibility rule for non-attributed kinds, preferably through a shared library API. Do not treat absence from `Owned` as evidence that an object belongs to another controller.

3. **P1 — The experimental ListenerSet identity is wrong.**  
   **Design:** [§3, line 79](docs/phases/phase-1/design.md:79) and §5, line 169.  
   **Evidence:** `../kgateway-ui-claude/pkg/kinds/upstream.go:21–22` registers `ListenerSet/listenersets` in the standard group, but **`XListenerSet/xlistenersets`** in the experimental group. `pkg/kinds/kinds.go:66–78` deduplicates by group and kind.  
   **Why:** These are not one kind served in two groups. The stated resolver rule will not naturally make bare `listenerset` select both, or recognize the experimental kind’s actual name.  
   **Direction:** Specify an intentional category alias covering both identities, while retaining their real kind/resource names and group-qualified selectors.

4. **P1 — Cancellation does not provide the promised shutdown guarantee.**  
   **Design:** [§4, line 157](docs/phases/phase-1/design.md:157), §6.4, line 221.  
   **Evidence:** `../kgateway-ui-claude/pkg/cache/cache.go:365–394` starts informer and readiness goroutines and returns immediately. Cancellation requests termination but offers no production join operation. `pkg/cache/bench_hooks.go:1–12` contains a wait only behind the `bench` build tag, and only for optional watches.  
   **Why:** `Execute` can return while library goroutines are still shutting down. The literal “no goroutine outlives the command” guarantee cannot be implemented by the proposed teardown.  
   **Direction:** Request a cache shutdown/join contract, or explicitly promise eventual bounded cleanup instead. Do not equate `cancel()` with completed shutdown.

5. **P1 — One-shot computation does not make all health results unknown.**  
   **Design:** [§4, line 162](docs/phases/phase-1/design.md:162).  
   **Evidence:** `../kgateway-ui-claude/pkg/engine/engine.go:374–385` evaluates configured checks and uses their results in diagnosis, explorer and overview construction. `pkg/health/checks.go:155–165` can immediately report an unready controller from Deployment state, without metrics or a second observation.  
   **Why:** The hand-wired loader cannot assume that health evaluation is inert. Its results influence the model even though Phase 1 does not print a health report.  
   **Direction:** Distinguish unavailable metrics and time-dependent checks from immediately observable checks. Specify which checks run and which model fields Phase 1 consumes.

6. **P2 — `describe` has no defined relation source outside detected installs.**  
   **Design:** [§3.2, lines 109–114](docs/phases/phase-1/design.md:109), §6.2, lines 193–195.  
   **Evidence:** `../kgateway-ui-claude/pkg/engine/engine.go:321–325,382–385` builds an index per detected, selected install. `pkg/explore/rows.go:78–96` builds its relation rows from that install’s ownership and assignment.  
   **Why:** `--all-controllers` can expose objects with no corresponding index; with no detected install there are no install indexes at all. The unconditional relation sections cannot follow the stated architecture.  
   **Direction:** Define reduced `describe` behavior for these cases, or request a library-supported controller-independent relation view. Also distinguish policy ancestors decoded from status from relations supplied by `explore.Index`.

7. **P2 — The access-error policy handles only Forbidden, although other failures also settle.**  
   **Design:** [§6.3, lines 208–215](docs/phases/phase-1/design.md:208).  
   **Evidence:** `../kgateway-ui-claude/pkg/cache/cache.go:134–140,185–194` records arbitrary failures in `Access.Err`; only 403 sets `Denied`. Lines `382–388` consider any recorded error settled. Lines `456–462` still copy informer-store objects regardless of access state. Program E2, `../kgateway-tooling-claude/program.md:48–49`, requires reporting per-kind access errors.  
   **Why:** Authentication, transport and server failures could become successful empty output or output from unavailable stores. Denied ownership dependencies can also make a readable requested kind appear unowned.  
   **Direction:** Define outcomes for every unavailable input, including ownership dependencies. Separate absent, loading, denied and failing states; decide when to fail versus emit explicitly incomplete output.

8. **P2 — The default filter hides several of the diagnostic candidates motivating the command.**  
   **Design:** [§6.2, line 192](docs/phases/phase-1/design.md:192), compared with §2, lines 35 and 48.  
   **Evidence:** `../kgateway-ui-claude/pkg/install/assign.go:28–40,51–56` distinguishes missing-parent, declared and no-parent candidates from owned routes. Lines `236–250` separately classify missing-parent and unwatched routes. `pkg/explore/rows.go:201–215` deliberately includes these diagnostic rows.  
   **Why:** A typo naming a nonexistent Gateway—the sort of route no controller can report on—is excluded by “owned objects plus routes naming an owned parent.” The library already exposes candidates that this rule discards.  
   **Direction:** Specify visibility for each candidate category and ambiguous ownership. If these cases require `--all-controllers`, state that limitation explicitly and reconsider whether the default fulfills the troubleshooting goal.

9. **P2 — Mixed parent status has no implementable summary rule.**  
   **Design:** [§3.1, lines 94–99](docs/phases/phase-1/design.md:94).  
   **Evidence:** `../kgateway-ui-claude/pkg/explore/rows.go:283–297` selects matching controller status and treats missing or non-current entries as pending. Lines `299–325` then derive attachment and deciding conditions. `pkg/objects/objects.go:345–349` evaluates staleness per condition.  
   **Why:** The design does not define precedence or denominators when one parent is accepted, another has no status, another is stale, or several controllers report on the same parent. A bare `1/2` can conceal uncertainty, contrary to the anti-goal.  
   **Direction:** Add an input/output table covering these combinations, including whether counts represent spec references, status entries or controller-filtered parents. Identify the shared-library source for each summary.

10. **P2 — The whole-load timeout lacks a strategy for discovery and synchronous computation.**  
    **Design:** [§6.4, line 219](docs/phases/phase-1/design.md:219), §4, lines 153–160.  
    **Evidence:** `../kgateway-ui-claude/pkg/kinds/kinds.go:61–64,87–109` exposes discovery without a context argument. `pkg/engine/engine.go:269–272,276–279` computes synchronously without a context; it also calls extension-supplied sources and checks.  
    **Why:** A timeout around waiting for cache settlement does not bound the whole load. Discovery can exceed it, while computation cannot be interrupted through the proposed API. A timeout before cache startup also has no “unsettled kinds” to report.  
    **Direction:** Define precisely what the deadline bounds, how discovery requests inherit it, and how timeout/cancellation during computation is handled without leaving background work.

11. **P3 — The command-test fixtures do not exercise the production discovery and CRD boundaries.**  
    **Design:** [§10, lines 284–298](docs/phases/phase-1/design.md:284).  
    **Evidence:** `../kgateway-ui-claude/pkg/app/app.go:139–145,188–190,211–212` uses separate discovery and metadata clients in addition to dynamic and Kubernetes clients. `pkg/cache/crds.go:70–82` reads CRDs through metadata.  
    **Why:** Seeding only the two named fake clients cannot validate unserved-kind discovery or CRD version output. A healthy live run also will not reliably expose the optional-input readiness race.  
    **Direction:** Include discovery and metadata test doubles, delayed proxy/CRD lists, non-403 failures, list-allowed/watch-denied behavior, and observable assertions on complete versus explicitly unknown output. Add a two-context fixture to verify actual context selection.

12. **P3 — Goroutine-count equality is a fragile cleanup acceptance test.**  
    **Design:** [§10, line 296](docs/phases/phase-1/design.md:296).  
    **Evidence:** `../kgateway-ui-claude/pkg/cache/cache.go:365–394` starts concurrent asynchronous work; no production completion signal accompanies cancellation.  
    **Why:** A process-wide count can pass while a command-owned goroutine leaks because unrelated work exits, or fail during legitimate asynchronous cleanup. It measures a mechanism without proving the intended lifecycle behavior.  
    **Direction:** Assert that command-owned watches and requests terminate, repeated invocations do not accumulate work, and cancellation returns within a bound. Use leak detection as supplementary evidence.

13. **P4 — The reused extension registry does not make future vendor checks work automatically.**  
    **Design:** [§7, lines 249–252](docs/phases/phase-1/design.md:249), alongside §4’s prohibition on starting collectors.  
    **Evidence:** `../kgateway-ui-claude/pkg/extension/deps.go:104–117,137–161` defines collectors as long-running sources. `pkg/extension/extension.go:105–117` only registers checks. `pkg/engine/engine.go:302–309,374–376` supplies collected values separately to evaluation.  
    **Why:** A vendor check registered through `WithChecks` may require collector data that the CLI never obtains. The promised semver extension surface therefore needs more than forwarding the registry when Phase 2 and later diagnostics arrive.  
    **Direction:** Define which registry capabilities the CLI supports, how unsupported capabilities are reported, and a one-shot acquisition contract for check inputs. Exercise a downstream detector, process source and data-dependent check—not just a fake kind and command.

The basic reuse choices are supported by the code: one-shot `Compute`, controller-version label/image fallback, typed Gateway/ListenerSet/HTTPRoute views, CRD annotations, and exclusion of Secrets/ConfigMaps. The proposed JSON-envelope ownership and additive-field versioning also agree with krp’s `cmd/krp/output.go:15–24` and decision D-005; matching its exact envelope is not required.
## Triage

| # | Pri | Verdict | Note |
|---|---|---|---|
| 1 | P1 | accept | Verified cache.go:349-355 (optional watches outside settle). §4.1 readiness per command; `version` waits for proxy Pods and CRDs or prints them unknown. |
| 2 | P1 | accept | Verified kinds.go:30-32. §6.2 rewritten: the filter applies only to attributed kinds; parameters and backends are always shown. |
| 3 | P1 | accept | Verified upstream.go:21-22. §3 alias `listenersets` covers ListenerSet and XListenerSet. |
| 4 | P1 | accept | §4.3: cancel signals, no join promised; request R4 for a cache stop/wait contract. |
| 5 | P1 | accept | §4.2 lists the model fields Phase 1 consumes; checks run as the UI runs them, results not printed. |
| 6 | P2 | accept | §3.2: reduced describe for unattributed objects (decoded spec/status refs, labelled). |
| 7 | P2 | accept | §6.3 rewritten: OK / loading / denied / failed per input, requested vs dependency inputs. |
| 8 | P2 | accept | Verified assign.go:28-56, 236-250. §6.2 hides only objects positively owned by another controller. |
| 9 | P2 | accept | §3.1 summary table: denominator, per-parent states, sources. |
| 10 | P2 | accept | §4.4: one deadline; discovery via rest.Config timeout; Compute is CPU-only in CLI wiring. |
| 11 | P3 | accept | §10: discovery and metadata fakes, delayed optional lists, non-403, watch-denied, two contexts. |
| 12 | P3 | accept | §10: behavioural lifecycle tests; goroutine count only supplementary. |
| 13 | P4 | accept | §7: supported registry capabilities listed; collector-backed checks need a Phase 2 one-shot contract. |
