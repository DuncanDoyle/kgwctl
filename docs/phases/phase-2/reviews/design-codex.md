Cross-model: YES (author: Claude Opus 5.5, reviewer: Codex, command: codex exec --sandbox read-only)
Scope: design (design.md r2 + implementation-plan.md draft)
Diff: n/a (design review)
Timing: on time
Round: 1

Reviewed against **Snellius `v0.7.0-alpha.1`**; all library citations below refer to that tag. No files edited or tests run.

1. **P1 — The planned dependency guard cannot pass.**  
   **Location:** implementation-plan.md:17,66.  
   **Evidence:** `pkg/extension/deps.go:13` imports `"github.com/DuncanDoyle/snellius/pkg/api"`; `pack.go:12` does likewise.  
   **Why:** `TestBinaryDeps` uses transitive `go list -deps`. Using the required registry necessarily brings in `pkg/api`, even without importing it directly in kgwctl.  
   **Change:** Keep the transitive prohibition on `pkg/app`; prohibit *direct kgwctl imports* of `pkg/api` separately. Removing the transitive API dependency requires a Snellius release.

2. **P1 — Disabled logging prevents ordinary runs from completing.**  
   **Location:** implementation-plan.md:92–94; design §4.1.  
   **Evidence:** `pkg/extension/pack.go:230` includes `controllerLogsCollector`; `kgateway.go:75–77` implements `if !l.env.ControllerLogs { return nil }`. Neither callback runs. Also, `Registry.RunCollectors(ctx, d, s)` returns no completion handle and replaces `d.Publish`/`d.Fail` (`deps.go:140–161`).  
   **Why:** With an install present and logs disabled as specified, “every collector published or failed” never becomes true. Supplied callback wrappers also cannot observe publications through the stock runner.  
   **Change:** Implement the stated equivalent runner, with per-collector callback wrappers, join handles, panic/error handling, and an explicit terminal outcome for return-before-publication.

3. **P1 — One publication does not mean every install was collected.**  
   **Location:** implementation-plan.md:94; design §4.1 item 4.  
   **Evidence:** `pkg/ctrlmetrics/scrape.go:283–298` starts concurrent install scrapes and, after *each* result, calls `publish(ValueKey, out, ...)`.  
   **Why:** The first result marks the collector complete while another install’s scrape remains outstanding. One-shot output can omit that install’s xDS evidence and vary with scrape order. With only vendor installs, the kgateway metrics collector has no targets and publishes nothing.  
   **Change:** Require a first-round completion contract covering all applicable installs, including an empty round. At this tag, per-collector callbacks alone cannot establish that generically; request a library contract rather than decode kgateway values in kgwctl.

4. **P1 — `causedBy: <finding ID>` is not supplied by diagnosis.**  
   **Location:** design:226; implementation-plan.md:120,133.  
   **Evidence:** `pkg/diagnose/group.go:87–90` defines `Cause *objects.Ref` and `Members []int`; `pkg/health/finding.go:86–87` exposes a *group ID*. Neither identifies a cause finding.  
   **Why:** Group membership does not establish that one member caused another. `TestBuildCheckCausedBy` would require invented CLI reasoning, conflicting with D-003 and generic rendering.  
   **Change:** Export group IDs, cause resource refs, and membership in the Check schema. Render groups using those facts; defer finding-to-finding causality until the library supplies it.

5. **P1 — Coalesced notifications can erase intermittent failures.**  
   **Location:** implementation-plan.md:85,235; design §3.2, §4.2.  
   **Evidence:** `pkg/engine/loop.go:193–206` publishes each model and invokes `EvPublished` with that publication. `Published` is immutable (`loop.go:33–39`).  
   **Why:** A buffer-one notification followed by `Current()` can skip a model containing a failure when a later model clears it before consumption. “Observed during this run” then loses exactly the intermittent evidence it promises to retain.  
   **Change:** Update the tracker from every `EvPublished.Published` in publication order. Coalesce terminal rendering notifications only after preserving transitions.

6. **P2 — Finding updates need an event, not just appearance/disappearance.**  
   **Location:** implementation-plan.md:218–223,241.  
   **Evidence:** `pkg/health/identity.go:24–28` excludes severity from finding identity. `pkg/health/checks.go:298–308` changes KGWUI-003 from info during grace to warning afterwards.  
   **Why:** The same ID remains present, so the proposed event set emits no warning transition. The live test’s expected warning line is unsupported by its diff contract.  
   **Change:** Add a finding-updated event for severity/message/evidence changes; retain initial and subsequent severity transitions in observation tracking.

7. **P2 — Hold times are earliest decisions, not completion bounds.**  
   **Location:** design:64,110,118; implementation-plan.md:147.  
   **Evidence:** `pkg/health/finding.go:142–143`: “Until is the earliest moment a held item is decided.” `store.go:300–303` takes the minimum deadline and sums counts.  
   **Why:** Taking the latest exposed `hold.until` still cannot justify “up to 4m48s”: other items within that check may decide later.  
   **Change:** Say “next decision in …” or “next decision at …”; avoid an upper bound on all pending work.

8. **P2 — Waiting for `Engine.Run` does not join its computation.**  
   **Location:** design §4.3; implementation-plan.md:88,93,102.  
   **Evidence:** `pkg/engine/loop.go:151` launches `go func() { done <- e.compute() }()`; `177–178` returns immediately on cancellation. `compute()` takes no context (`engine.go:320`).  
   **Why:** Joining the scheduler goroutine does not establish the promised absence of background engine work. There is no exported engine wait method in this contract.  
   **Change:** Specify a verified computation-drain barrier, or request a library join API. Test cancellation while a computation is actually running.

9. **P2 — Acquisition and observation contexts are underspecified.**  
   **Location:** implementation-plan.md:166,235–237.  
   **Evidence:** `setup.Build` synchronously calls `kinds.Resolve` and `mon.Refresh` (`pkg/setup/setup.go:131,149`); its signature has no context (`114`).  
   **Why:** Starting the acquisition timeout only after `observe.Start` leaves setup unbounded. Conversely, reusing Phase 1’s timeout-bound clients cancels live I/O after 30 seconds because `load.BindContext` binds their lifetime to that context.  
   **Change:** Define separate session-lifetime cancellation and acquisition-wait deadlines, with context-bound discovery covering both setup calls. Add delayed-discovery and live-beyond-30-seconds tests.

10. **P3 — The proposed timing tests do not establish KGWUI-018’s prerequisites.**  
    **Location:** implementation-plan.md:103,241,254.  
    **Evidence:** `pkg/health/freshness.go:91–96` requires ten stuck objects or a percentage threshold with minimum three; `185–193` suppresses 018 when the controller Deployment is unready. Its five-minute hold starts when evaluated (`195–202`).  
    **Why:** One never-statused route is insufficient; a scaled-down controller suppresses 018. Jumping directly six minutes also need not execute the intermediate evaluation that starts its hold.  
    **Change:** Use a ready controller and enough stuck resources; observe the grace transition and hold before advancing through its deadline.

11. **P3 — Fake clock and envtest assumptions need narrower tests.**  
    **Location:** implementation-plan.md:38–41,100–103,177.  
    **Evidence:** Metrics uses real `context.WithTimeout` (`pkg/ctrlmetrics/scrape.go:226`); proxy stats does too (`pkg/proxystats/collector.go:162,242`), despite injected ticker clocks. `PodProxyScraper` requests a named subresource (`scrape.go:116–117`).  
    **Why:** Clock injection does not advance every timeout. The existing testenv proxy rejects named/subresource paths from classification, so it cannot inject the planned pods/proxy denial as written.  
    **Change:** Add explicit subresource matching; test health timing with fake clocks and network cancellation with bounded real-time tests.

12. **P4 — The downstream example’s server Pods are invisible by default.**  
    **Location:** implementation-plan.md:190–192; design §6.1.  
    **Evidence:** `pkg/cache/cache.go:100–109` watches Pods only through configured selectors; `pkg/setup/setup.go:153–157` derives those selectors from detectors. `health.InputSet.Kinds` declares fingerprint inputs (`store.go:13–17`), not additional watches.  
    **Why:** Registering a check for `app=example-server` does not make those Pods available in the snapshot.  
    **Change:** Have the example collector read those Pods through `CollectorEnv.Kube` and publish readiness for the check. Test absent, unready, and ready states through the public downstream CLI.
## Triage
| # | Finding | Source | Decision | Note |
|---|---|---|---|---|
| 1 | Guard can't ban `pkg/api` transitively | reviewer | accept | Verified `pkg/extension/deps.go:13`. Transitive ban on `pkg/app`; direct-import ban on `pkg/app` and `pkg/api` (design §8, Task 2.1.1); 2.1 implementer told. |
| 2 | Logs collector returns without publishing; `RunCollectors` has no handle | reviewer | accept | Verified `deps.go:140-161`. Own runner; a return before publishing counts as finished (design §4.1 item 2). |
| 3 | First publish ≠ every install collected | reviewer | accept | No generic signal at the tag (not-yet-collected is `AvailFailing` with text, `xds.go:128`). Quiet period of 2s as an approximation; leftovers show as Not checked, never passed; E10 proposed to the owner (design §4.1, §15). |
| 4 | `causedBy` not supplied by diagnosis | reviewer | accept | Groups with cause ref and members instead (design §3.3, §7). |
| 5 | Coalesced notifications lose intermittent failures | reviewer | accept | `Models()` delivers every `EvPublished` in order (design §4.2). |
| 6 | Severity change under one ID has no event | reviewer | accept | `FindingChanged`; Observed keeps severity changes. |
| 7 | `hold.until` is not an upper bound | reviewer | accept | Hint says "next decision at"; earliest `until`. |
| 8 | `Engine.Run` doesn't join compute | reviewer | accept | Result channel is buffered (`loop.go:137`), so no leak; `Stop()` waits bounded for an in-flight compute via `EvRunStarted`/`EvPublished` (design §4.3). |
| 9 | Acquisition vs session contexts | reviewer | accept | Design §4.5: session-bound clients, acquisition deadline covering `setup.Build`. Tests added. |
| 10 | 018 test prerequisites | reviewer | accept | Ready controller, 10 stuck routes, clock stepped in small steps. |
| 11 | Fake clock doesn't reach network timeouts; proxy can't match subresources | reviewer | accept | Subresource matching in testenv; real-time bounds for network tests. |
| 12 | Example server Pods invisible to the cache | reviewer | accept | Example collector lists them via `CollectorEnv.Kube`. |
