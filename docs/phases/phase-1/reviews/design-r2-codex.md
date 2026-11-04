# Review: Phase 1 design r2 + implementation plan (round 2)

**Reviewer:** Codex (codex exec, read-only)
**Author:** Claude
**Cross-model:** yes
**Scope:** design.md r2 (16aea93), implementation-plan.md (b4eee93)
**Date:** 2026-11-04

---

The design and plan need another revision before implementation. The main blockers are the lossy explorer status API, the fake-client delay interface, and the deadline strategy.

This was a read-only source review. I did not run tests or edit files. Library paths below are relative to this repository.

Round-1 disposition:

| # | Finding | r2 assessment |
|---|---|---|
| 1 | Optional inputs not covered by settle | **Design resolved.** Per-command readiness is explicit. The planned delay harness cannot exercise it correctly; see finding 2. |
| 2 | GatewayParameters hidden | **Resolved.** Unattributed kinds remain visible. |
| 3 | Experimental ListenerSet identity | **Resolved.** Real identities and the combined alias are distinguished. |
| 4 | Cancellation versus completed shutdown | **Resolved by narrowing the promise.** Eventual cleanup replaces synchronous shutdown. |
| 5 | One-shot health evaluation | **Resolved.** Checks run, and consumed model fields are identified. |
| 6 | Relations outside detected installs | **Resolved.** Reduced, declared-reference output is defined. |
| 7 | Non-403 failures and incomplete inputs | **Partially resolved.** Error states are defined, but dependency handling and timeout precedence remain incomplete; see finding 6. |
| 8 | Diagnostic candidates hidden | **Partially resolved.** The broader visibility rule helps, but its status-based hiding clause can still hide those candidates; see finding 5. |
| 9 | Mixed-parent summary | **Papered over.** The denominator is specified, but the proposed index projection cannot produce the required states; see finding 1. |
| 10 | Whole-load timeout | **Papered over.** A fixed REST timeout does not implement a command deadline across discovery requests; see finding 3. |
| 11 | Discovery/metadata and failure fixtures | **Partially resolved.** Four clients are included, but delay injection and several acceptance cases remain inadequate; see findings 2 and 10. |
| 12 | Goroutine-count acceptance | **Resolved in principle.** Watch shutdown is primary; counts are supplementary. Cancellation latency still lacks its promised assertion. |
| 13 | Future collector-backed extensions | **Resolved by scoping.** Phase 2 explicitly needs a one-shot acquisition contract. |

1. **P1 — `RouteParents` cannot produce the specified summaries from `explore.Index`.**

   **Location:** [design.md:105](docs/phases/phase-1/design.md:105), [implementation-plan.md:258](docs/phases/phase-1/implementation-plan.md:258).

   **Evidence:** `../kgateway-ui-claude/pkg/explore/rows.go:289–297` returns a pending link before assigning `Cond` when status is absent or stale. `Cond` is populated only for a current **False** condition at lines 321–325. Consequently, “stale from the deciding condition via `objects.IsStale`” cannot work. Another controller’s status is discarded by the controller filter at lines 283–287, so the index cannot distinguish `other` from missing status either.

   The index also is not the specified denominator: `rows.go:191–195` links the install’s assigned parents, while `link` deduplicates references and records duplicates separately at lines 161–174. `install/assign.go:19–25` explicitly limits owned parent lists to parents of that install.

   **Why:** The planned mixed-parent test cannot yield `[accepted, no status, stale, other]` through the prescribed mapping. Routes spanning installs, foreign parents and duplicate spec references can also get the wrong denominator.

   **Direction:** Separate attachment from per-condition status. Project over every spec parentRef, join all relevant install links, and obtain stale/controller/condition details from decoded status or a new lossless library API. Define handling of duplicate refs and multiple matching controller entries explicitly.

2. **P1 — The fake harness cannot independently delay proxy Pods as specified.**

   **Location:** [implementation-plan.md:65](docs/phases/phase-1/implementation-plan.md:65), lines 109–110.

   **Evidence:** `cache.ProxyPodsGVR` is the synthetic access key `pods#proxy` (`../kgateway-ui-claude/pkg/cache/cache.go:52–53`). Both controller and proxy informers actually call the same `Pods("").List`, distinguished by label selector (`cache.go:303–319`). No fake action targets `pods#proxy`.

   Mapping the synthetic key to ordinary `pods` without inspecting selectors delays core inputs too. Moreover, client-go invokes reactors while holding the fake’s mutex (`client-go@v0.36.2/testing/fake.go:134–150`), so sleeping inside a Kubernetes fake reactor blocks unrelated calls on that fake, including core namespace/deployment operations.

   **Why:** The required “core settles, proxy list remains pending” scenario is not reproducible through the stated `Delay(gvr, duration)` reactor implementation. The optional-deadline test can instead become a core timeout.

   **Direction:** Use context-aware client wrappers that delay outside the fake mutex and distinguish Pod lists by selector. Dispatch watch failures through `PrependWatchReactor`, separately from list reactors. Test the harness’s isolation before relying on it.

3. **P1 — The proposed discovery timeout still is not a whole-command deadline.**

   **Location:** [design.md:218](docs/phases/phase-1/design.md:218), [implementation-plan.md:133](docs/phases/phase-1/implementation-plan.md:133).

   **Evidence:** The plan sets `rest.Config.Timeout` once to the flag value. `kinds.Resolve` makes sequential group/version discovery calls (`../kgateway-ui-claude/pkg/kinds/kinds.go:90–115`). The real discovery client executes those requests with `context.TODO()` (`client-go@v0.36.2/discovery/discovery_client.go:413`).

   **Why:** Several individually successful slow requests can cumulatively exceed the command deadline; cancellation of the loader context does not cancel discovery. “Remaining time through `rest.Config.Timeout`” is not implemented by the proposed factory interface and fixed configuration.

   **Direction:** Bind discovery HTTP requests to the command context, or provide a deadline-aware discovery adapter. Test cumulative slow discovery and cancellation through real REST clients against an HTTP test server.

   There is also an unresolved boundary contradiction: the design checks the deadline before `Compute` (line 221), while plan line 110 requires successful output after waiting until that same deadline for optional inputs. Specify whether final in-memory computation is permitted after optional acquisition expires.

4. **P1 — Core settle can occur before an initial watch denial becomes observable.**

   **Location:** [design.md:178](docs/phases/phase-1/design.md:178), [implementation-plan.md:108](docs/phases/phase-1/implementation-plan.md:108).

   **Evidence:** Cache access becomes OK when the informer has synced and no error has yet been recorded (`../kgateway-ui-claude/pkg/cache/cache.go:185–194`). Settlement uses sync/error state at lines 382–388. In the classic LIST/WATCH path, client-go populates the store before opening the watch (`tools/cache/reflector.go:500–508,776–782`).

   **Why:** If LIST succeeds and the initial WATCH response is delayed before returning 403, the command can settle, snapshot and return success first. The planned immediate watch-denied reactor may pass without exposing this race.

   **Direction:** Obtain a library readiness contract that includes initial watch establishment or failure, or explicitly revise the one-shot/list-only behavior. Add a test that holds the initial WATCH response until after LIST synchronization, then returns Forbidden.

5. **P2 — The new hiding rule contradicts “hide only positively someone else’s.”**

   **Location:** [design.md:263](docs/phases/phase-1/design.md:263).

   **Evidence:** The rule hides a route when **either** every parent resolves to a hidden object **or** every status entry comes from another controller. Those status entries need not correspond to current spec parentRefs. The library preserves spec references and status entries separately (`../kgateway-ui-claude/pkg/objects/objects.go:204–215,231–235`).

   **Why:** A route retargeted from another controller to our Gateway, or to a nonexistent Gateway, can retain only the old controller’s status. This clause hides it despite the design explicitly retaining our diagnostic candidates.

   **Direction:** Give current spec ownership precedence. Foreign status alone should not prove ownership of the current spec. Cover retargeted routes and orphan routes with historical foreign status.

6. **P2 — Incomplete-input handling lacks dependency classification and timeout precedence.**

   **Location:** [design.md:300](docs/phases/phase-1/design.md:300), [implementation-plan.md:96](docs/phases/phase-1/implementation-plan.md:96).

   **Evidence:** The ownership dependency list omits Namespaces, although assignment uses namespace objects for scope membership (`../kgateway-ui-claude/pkg/install/assign.go:115–119`), and selector scope returns false when the Namespace is missing (`pkg/install/scope.go:45–51`).

   The plan also makes **any** pending core input a fatal timeout, while the design says unavailable ownership/other dependencies permit incomplete output. Its view tasks specify no consistent projection for relations derived from failed stores; the cache retains those objects regardless of access state (`pkg/cache/cache.go:456–462`).

   **Why:** Readable objects can be classified as unwatched or produce apparently definitive relations from unavailable evidence. A stalled unrelated core dependency can also prevent output that the permissions table promises to permit.

   **Direction:** Define command-specific required inputs and a dependency map for each derived field. Include Namespaces in scope dependencies, establish timeout precedence, and suppress or mark affected relations unknown. Add observable cases beyond Deployments-denied.

7. **P2 — Sub-phase 1.3’s declared prerequisite is insufficient.**

   **Location:** [implementation-plan.md:37](docs/phases/phase-1/implementation-plan.md:37).

   **Evidence:** It permits 1.3 to start after Task 1.2.1, but Task 1.3.1 consumes `RouteParents`, `PolicyAncestors` and `ObjectCondition` (line 333), which Task 1.2.2 creates.

   **Why:** Following the stated dependency graph starts `describe` without its required interfaces and behavior.

   **Direction:** Require Task 1.2.2 before 1.3, or extract the shared status projection into an earlier prerequisite.

8. **P2 — The downstream command test has no defined injection seam.**

   **Location:** [implementation-plan.md:185](docs/phases/phase-1/implementation-plan.md:185), lines 191–193.

   **Evidence:** The example consumes only `pkg/cli`; its tests may use `testenv`. But `testenv.Factory()` supplies a `load.Factory`, and the public options at line 136 contain no factory option. Factory injection exists only in `app.Config.NewClients` (line 125). The design expressly keeps fake injection internal.

   **Why:** The implementer cannot run the example’s real public root through those fakes without inventing another seam or bypassing the example. The planned tests also omit the required downstream policy appearing in `get`.

   **Direction:** Specify an internal test helper or a production-client HTTP test server that exercises the example’s actual registry and public constructor. Include the downstream policy visibility assertion.

9. **P2 — The RBAC verification cannot rerun the entire setup script as the read-only account.**

   **Location:** [implementation-plan.md:403](docs/phases/phase-1/implementation-plan.md:403).

   **Evidence:** The referenced script creates a cluster, installs CRDs and Helm resources, and applies fixtures (lines 377–380). The tested ClusterRole grants only the design’s `get/list/watch` permissions.

   **Why:** Using the ServiceAccount kubeconfig for setup fails on writes. Recreating the cluster can also discard the account being tested.

   **Direction:** Split privileged setup from command verification. Provision fixtures and the account first, then run only kgwctl operations with its kubeconfig.

10. **P3 — Several promised boundary tests are absent or replaced by weaker checks.**

   **Location:** [implementation-plan.md:142](docs/phases/phase-1/implementation-plan.md:142), lines 168–173 and 191–195.

   **Evidence and impact:**

   - The context test uses a test-only command and records the factory’s `Config.Host`. Design line 400 requires observing which server answered; the planned assertion cannot catch an actual command constructing another client with default context.
   - Only proxy-delay tests are scheduled. Design line 399 also requires delayed CRD lists and deadline-expired unknown output.
   - Lifecycle tests check stopped watches **after `Execute` returns**, but do not assert the cancellation-to-return bound required by design line 405.

   **Direction:** Add real-command, two-server context tests; independent delayed/denied CRD cases across formats; and a bounded cancellation-return test using requests that remain in flight until cancellation.
## Triage

| # | Pri | Verdict | Note |
|---|---|---|---|
| 1 | P1 | accept | Verified rows.go:283-325. §3.1: per-parent state decoded from status over deduplicated spec parentRefs; the index only adds `unwatched`/`declared`. |
| 2 | P1 | accept | Verified cache.go:52-53 (`pods#proxy`). Command tests move to envtest behind a fault-injecting HTTP proxy (selector-aware, delays outside any lock). Unit tests stay pure. |
| 3 | P1 | accept | §4.4: every request goes through a transport bound to the command context. The deadline governs I/O only; `Compute` always runs once on what loaded. |
| 4 | P1 | accept (scoped) | Watch is required RBAC (§6.3). List-allowed/watch-denied is outside support until R2; invariant tested: correct data or a denied error, never empty/partial output with exit 0. |
| 5 | P2 | accept | §6.2: routes and ListenerSets hide by current spec parents only; foreign status never hides them. Policies keep the status rule (no target decode until E7), noted as a limit. |
| 6 | P2 | accept | §6.3: dependency map per derived field, Namespaces added, timeout precedence (only requested kinds are fatal). |
| 7 | P2 | accept | Plan: 1.3 starts after 1.2.2. |
| 8 | P2 | accept | envtest gives a real kubeconfig, so the example runs through its public root with `--kubeconfig`; `get` assertion added. |
| 9 | P2 | accept | live-check split into `setup` (admin) and `verify` (any kubeconfig). |
| 10 | P3 | accept | Two proxies for two contexts; CRD delay/deny cases; cancellation-to-return bound with held requests. |
