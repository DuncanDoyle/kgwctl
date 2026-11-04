# Review: Phase 1 design r3 + plan r2 (round 3)

**Reviewer:** Codex (codex exec, read-only)
**Author:** Claude
**Cross-model:** yes
**Scope:** design.md r3, implementation-plan.md r2 (71ef312)
**Date:** 2026-11-04

---

r3 and plan r2 still need revision: **two P1 and four P2 blockers** remain. This was a read-only source review; I ran no tests and changed no files.

Paths below are relative to kgwctl; library paths start with `../kgateway-ui-claude/`.

| Round-2 finding | Assessment | Evidence in r3 / plan r2 |
|---|---|---|
| 1. Lossy explorer parent-status projection | **Partially resolved** | Design:103–113 and plan:290–294 correctly switch to decoded status. However, the new deduplication key drops listener identity; see new finding 3. |
| 2. Fake harness cannot isolate proxy Pods | **Partially resolved** | Plan:65–88 replaces fake reactors with real clients and a streaming proxy. Its concrete selector misses the actual informer, and LIST-only faults miss WatchList; see findings 1 and 4. |
| 3. Whole-command discovery deadline | **Partially resolved** | Design:221–226 binds HTTP requests and explicitly permits final computation after expiry. Binding the transport does not cancel discovery’s client-go retry waits; see finding 2. |
| 4. Settle before initial WATCH denial | **Partially resolved** | Design:316 explicitly narrows supported permissions and allows either complete data or a denied error. That resolves the original unconditional guarantee by scoping. The held-WATCH acceptance test cannot exercise the intended race under default WatchList behavior. |
| 5. Foreign historical status hides retargeted routes | **Resolved** | Design:268 uses current spec parents exclusively; plan:271 adds retargeted-to-ours and retargeted-to-missing cases. The separate policy limitation is explicit at design:269. |
| 6. Dependency classification and timeout precedence | **Partially resolved** | Design:223–225 and :305–312 establish precedence and a dependency table. Plan:134–135 tests requested versus unrequested timeout. Actual ownership dependencies remain missing; see finding 5. |
| 7. Describe prerequisite | **Resolved** | Plan:41 now requires both Tasks 1.2.1 and 1.2.2. |
| 8. Downstream test injection seam | **Resolved** | Plan:213 uses the example’s actual constructor with a real kubeconfig; :220–222 covers detector, policy visibility and command registration. Harness defects remain separate. |
| 9. Read-only RBAC verification reruns privileged setup | **Resolved** | Plan:414–443 separates admin `setup` from `verify <kubeconfig>`. |
| 10. Missing boundary tests | **Partially resolved** | Plan:167/:202 switches context selection to real `version`; :196–200 adds optional-input faults; :224 adds bounded return. However, WatchList bypasses several fault injections, so those assertions do not yet prove their intended boundaries. |

1. **P1 — The new harness assumes LIST/WATCH, while production clients default to WatchList.**

   **Location:** [implementation-plan.md:73](docs/phases/phase-1/implementation-plan.md:73), :75, :129–135, :199, :224, :347.

   **Evidence:** The library explicitly preserves real-client WatchList support in `pkg/cache/cache.go:273–276`. In the installed client-go v0.36.2, `features/known_features.go:141–144` enables `WatchListClient` by default. The reflector initially requests `watch=true&sendInitialEvents=true`; a successful initial stream populates the cache without an ordinary LIST. See [client-go’s reflector implementation](https://github.com/kubernetes/client-go/blob/v0.36.2/tools/cache/reflector.go#L470).

   **Why:** Holding or failing only HTTPRoute, Pod or CRD **LISTs** can leave acquisition completely unaffected. `TestReturnAtDeadline` can return successful data instead of timing out. More seriously, `HoldWatchThenStatus` waits for a matching LIST to complete, but the initial WatchList request can prevent that LIST from ever happening. Flushing watch chunks, already specified at plan:88, does not fix this protocol mismatch.

   **Direction:** Distinguish initial WatchList from ordinary WATCH using `sendInitialEvents`. Apply acquisition faults to both classic LIST and initial WatchList. Exercise the post-LIST watch-denial race in an explicitly controlled classic-mode test, while retaining default-WatchList coverage. Assert that each intended fault actually matched traffic.

2. **P1 — Transport binding still cannot enforce the discovery deadline during retry waits.**

   **Location:** [design.md:222](docs/phases/phase-1/design.md:222), [implementation-plan.md:65](docs/phases/phase-1/implementation-plan.md:65), :136.

   **Evidence:** `ServerResourcesForGroupVersion` executes `Do(context.TODO())`; client-go’s retry machinery sleeps and throttles using that REST-call context, outside `RoundTrip`. See [discovery_client.go](https://github.com/kubernetes/client-go/blob/v0.36.2/discovery/discovery_client.go#L399) and [with_retry.go:195–230](https://github.com/kubernetes/client-go/blob/v0.36.2/rest/with_retry.go#L195).

   **Why:** A discovery response returning `429` with `Retry-After: 10` before a one-second command deadline can leave synchronous `kinds.Resolve` sleeping past that deadline. The transport wrapper cannot interrupt a sleep above it. The proposed slow-successful-response test exercises cancellation inside HTTP, so it misses this failure.

   **Direction:** Bind the command context at the discovery REST-operation boundary, including retries and throttling—for example, a discovery adapter whose relevant requests use `Do(ctx)`. Add a retry-after case exceeding the remaining deadline, with a bounded-return assertion.

3. **P2 — The new parent deduplication merges distinct listener attachments.**

   **Location:** [implementation-plan.md:291](docs/phases/phase-1/implementation-plan.md:291), corresponding design:103–106.

   **Evidence:** `objects.ParentRef` retains `SectionName` and `Port`, but `ParentRef.Ref()` explicitly drops both (`pkg/objects/objects.go:54–68`). The library matches status against the complete parent reference in `pkg/explore/rows.go:283–285`, and its duplicate detection likewise compares complete references at :164–170.

   **Why:** Two valid references to the same Gateway’s different listeners collapse into one denominator entry. If listener A accepts the route and listener B rejects it, the prescribed key cannot represent both outcomes. The exact-duplicate test at plan:301 does not expose this.

   **Direction:** Deduplicate and join status using the full defaulted `objects.ParentRef`, including section and port. Use `.Ref()` only to resolve the owning object. Add a same-Gateway, different-listener mixed-status case.

4. **P2 — The specified proxy-Pod selector never matches the library’s informer request.**

   **Location:** [implementation-plan.md:84](docs/phases/phase-1/implementation-plan.md:84), affecting :132–133 and :196–197.

   **Evidence:** The test delays requests whose selector contains `gateway.networking.k8s.io/gateway-name`. However, `install.UpstreamDetector.ProxyPodSelector()` returns:

   ```text
   app.kubernetes.io/component=proxy,kgateway=kube-gateway
   ```

   See `pkg/install/upstream.go:38–41`. The cache sends that selector directly (`pkg/cache/cache.go:304–319`). The gateway-name label is used elsewhere to identify proxy **Deployments**, not in this Pod informer’s query.

   **Why:** The selector-isolation harness test can pass while delaying no production informer. The subsequent optional-readiness tests then fail to establish that proxy Pods were held independently of core inputs.

   **Direction:** Derive the matching selector from the registered detector. Assert both that the proxy informer request matched the rule and that the controller-Pod informer proceeded independently.

5. **P2 — The new dependency table still permits definitive output from unavailable ownership evidence.**

   **Location:** [design.md:308](docs/phases/phase-1/design.md:308), :310–311; [implementation-plan.md:191](docs/phases/phase-1/implementation-plan.md:191).

   **Evidence:** Ownership and index dependencies omit controller Pods. Yet `install.Discover` attaches snapshot Pods at `pkg/install/install.go:109`, and `NamespaceSelectorScope` prefers a running Pod’s environment over the Deployment template (`pkg/install/scope.go:95–99,151–159`).

   Proxy-version dependencies also omit the inputs needed to enumerate owned Gateways: plan:191 iterates owned Gateways, while assignment requires GatewayClasses, Gateways and namespace scope (`pkg/install/assign.go:130–165`).

   **Why:** During a rollout, a denied Pod input can cause scope to fall back to a newer Deployment template and produce definitive ownership or attachment claims about the running controller. Separately, unavailable Gateway or namespace evidence can eliminate owned Gateways and make proxies appear absent despite readable Deployments and proxy Pods.

   **Direction:** Include controller Pods in scope-dependent outputs. Make proxy-version enumeration depend on readable ownership inputs, and represent unavailable enumeration as unknown. Cover Pod/template scope disagreement and unavailable Gateway ownership inputs.

6. **P2 — Fresh namespaces do not isolate the shared envtest server for cluster-wide commands.**

   **Location:** [implementation-plan.md:69](docs/phases/phase-1/implementation-plan.md:69), affecting :128, :195 and :272.

   **Evidence:** The prescribed isolation is one envtest per package with fresh namespaces per test. The library nevertheless watches cluster-wide and discovers installs across all returned Deployments. GatewayClasses are cluster-scoped. Envtest runs no namespace controller or garbage collector; deleting a namespace does not remove its remaining objects. The [official envtest guidance](https://book.kubebuilder.io/reference/envtest.html#testing-considerations) explicitly describes this limitation.

   **Why:** A healthy fixture can remain visible to a later “no install” test; successive version cases can accumulate installs; cluster-scoped fixture names can collide. New namespaces cannot produce the promised isolated cluster state, especially for `version` and `get -A`.

   **Direction:** Define explicit teardown of every fixture object, including cluster-scoped objects, and wait for removal before the next case. Alternatively, use separate API-server state. Shared-server cases must also prevent concurrent fixture and discovery-state mutations.
## Triage

| # | Pri | Verdict | Note |
|---|---|---|---|
| 1 | P1 | accept | Proxy rules match "acquisition" = classic LIST or initial WatchList (`sendInitialEvents=true`); every rule records hits and tests assert the rule matched. The held-watch race runs in classic mode via `clientfeatures.ReplaceFeatureGates` (WatchListClient off), restored after. |
| 2 | P1 | accept | §4.4: discovery goes through a context-bound adapter implementing `kinds.DiscoveryClient` with `Do(ctx)`; retries and throttling then honour the deadline. Test: 429 with Retry-After 10 under a 1s deadline. |
| 3 | P2 | accept | Deduplicate and join on the full defaulted `objects.ParentRef` (section, port); `.Ref()` only for ownership. Test: same Gateway, two listeners, mixed status. |
| 4 | P2 | accept | Verified upstream.go:38-41. Selector taken from the registered detector's `ProxyPodSelector()`. |
| 5 | P2 | accept | Verified scope.go:95-99. Controller Pods added to ownership/index inputs; proxy versions also depend on ownership inputs. |
| 6 | P2 | accept | testenv cleanup deletes every created object (cluster-scoped included) and waits until gone; namespaces are created once and reused; command tests don't run in parallel. |
