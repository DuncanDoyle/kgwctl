Cross-model: YES (author: Claude Opus 5.5, reviewer: Codex, command: codex exec --sandbox read-only)
Scope: design (design.md r3 + implementation-plan.md)
Diff: n/a (design review)
Timing: on time
Round: 2


Reviewed against **Snellius `v0.7.0-alpha.1`**. Library paths below refer to that tag. No files edited or tests run.

**Round-1 disposition**

| # | Status | Why |
|---|---|---|
| 1 | Resolved | Design §8 and plan:17,68 distinguish the transitive `pkg/app` ban from direct-import bans. |
| 2 | Resolved | The own runner can set exported `Deps.Publish`/`Fail` (`pkg/extension/deps.go:30–34`). Pack collectors use those callbacks normally (`pkg/extension/kgateway.go:32–66`); they require no `RunCollectors`-only facility. Early return is explicitly terminal. |
| 3 | Partly | The revision acknowledges partial collection and preserves Not checked, but quiet time cannot establish first-round completion. Vendor-only completion also remains broken; see below. |
| 4 | Resolved | The revised schema matches exported facts: `Group.ID`, `Cause`, and `Members` indexes into the original report (`pkg/diagnose/group.go:83–100`). `InstallModel.Diagnosis` is exported (`pkg/engine/engine.go:125–132`). Resolve indexes before namespace filtering or sorting. |
| 5 | Resolved | An unbounded ordered queue can preserve every publication. `OnLoop` runs synchronously on Run’s goroutine (`pkg/engine/engine.go:86–87`), so its callback must enqueue without waiting for the consumer. The specified queue supports this; directly sending to `Models()` would stall the engine. |
| 6 | Partly | `FindingChanged` and severity history were added, but the Tracker’s inclusion rule still excludes an initially present finding that changes severity and remains present. |
| 7 | Resolved | The hint uses the earliest exposed deadline and makes no completion-bound claim. |
| 8 | Not resolved | Counting `EvRunStarted` against `EvPublished` cannot detect completion of a computation discarded after cancellation. |
| 9 | Resolved | Session-bound clients and an acquisition deadline covering setup are now separate. Implementation should use the existing context-aware discovery adapter as well as transport binding, so discovery retries/throttling inherit cancellation. |
| 10 | Resolved | Tests now require a ready controller, ten stuck routes, and intermediate clock steps through grace and hold. |
| 11 | Resolved | Subresource matching and real-time network cancellation tests are explicitly planned. |

Round-1 #12 was P4 and is excluded as requested.

**Remaining/new findings**

1. **P2 — Shutdown still lacks an observable computation-drain barrier.**  
   **Location:** design:172; implementation-plan:96,254.  
   **Evidence:** `pkg/engine/loop.go:150–151` emits start and launches computation; `177–178` returns on cancellation; only consuming the result emits `EvPublished` (`193–206`). The buffered channel prevents a blocked result send, but supplies no completion notification after Run returns.  
   **Impact:** Stop can always wait the full two seconds for an already finished computation, or return while a slower computation remains active. The no-background-work guarantee and in-flight shutdown test are unsupported.  
   **Change:** Specify a real drain contract/barrier, preferably a library join API; explicitly reconcile bounded shutdown with computations exceeding that bound.

2. **P2 — Quiet-period expiry does not cause the required final computation.**  
   **Location:** design:159; implementation-plan:96–97,101.  
   **Evidence:** `pkg/engine/loop.go:179–206` computes from triggers, scheduled deadlines and safety events; kgwctl’s quiet deadline is absent. Collectors publish every interval (`pkg/ctrlmetrics/scrape.go:273–308`), while the plan uses that same interval for engine safety.  
   **Impact:** After inputs settle, the model normally predates the two-second quiet deadline. The next periodic publication can reset quiet before the safety computation, making completion depend on scheduling and potentially delaying it until request timeout.  
   **Change:** Arm an explicit quiet timer, trigger computation when it expires, and require a publication covering that acquisition generation; rearm if inputs change.

3. **P2 — Vendor-only installs still force acquisition timeout.**  
   **Location:** design:157–160; implementation-plan:95–97,199–201.  
   **Evidence:** The kgateway metrics collector filters installs to its pack (`pkg/extension/kgateway.go:49–50`). With zero applicable installs, its loop publishes nothing and stays running (`pkg/ctrlmetrics/scrape.go:281–308`).  
   **Impact:** “Every collector published, failed or returned” never holds when a vendor install exists without a kgateway install. The no-install exception does not apply.  
   **Change:** Require a generic empty-round/applicability outcome from the library, or explicitly design a bounded partial-acquisition fallback that does not claim completeness.

4. **P2 — The required 013/003 diagnosis group is not supplied by the library.**  
   **Location:** design:104–107; implementation-plan:179.  
   **Evidence:** Missing-backend grouping claims only attributed 013 (`pkg/diagnose/group.go:293–298`). 003 joins a shared cause only when 018 exists (`348–364`); otherwise the fallback uses its own resource (`431–435`).  
   **Change:** Correct the example and fixture expectation to separate groups, or test genuine shared grouping using 018 with 003.

5. **P2 — Observed inclusion contradicts the severity-transition test.**  
   **Location:** implementation-plan:230,250.  
   **Evidence:** Finding identity excludes severity (`pkg/health/identity.go:24–28`); 003 changes severity under that identity (`pkg/health/checks.go:298–308`).  
   **Impact:** A finding present initially that becomes warning and remains present fails the specified Observed inclusion rule, despite the test requiring its transition history.  
   **Change:** Include initially present findings whose severity changes, and test that exact case.
## Triage
| # | Finding | Source | Decision | Note |
|---|---|---|---|---|
| 1 | No compute-drain barrier | reviewer | accept, adjusted | No library join exists; a compute does no I/O and ends on its own. kgwctl doesn't wait; leak tests poll with a bound (design §4.3). A library join API is not requested. |
| 2 | Quiet expiry doesn't trigger a compute | reviewer | accept | kgwctl's quiet timer calls `Changes.Notify()`; Complete needs a model after it (design §4.1). |
| 3 | Vendor-only installs: kgateway collector never publishes | reviewer | accept | First-publish wait of 6s after the first model with an install (design §4.1); E10 stays proposed. |
| 4 | 013/003 group not supplied | reviewer | accept | Example and `route-broken` fixture corrected. |
| 5 | Observed inclusion vs severity change | reviewer | accept | Observed = every finding with a transition (design §4.2). |
| r1-3 | Quiet period ≠ first round | reviewer | defer: E10 (owner) | Approximation stays; leftovers show as Not checked, never passed. |
