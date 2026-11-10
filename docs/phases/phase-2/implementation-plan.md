# Phase 2 — `check`: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `kgwctl check` and `kgwctl check --live`: every Snellius health check over the selected installs, a paste-friendly report, `-o json|yaml`, exit codes 0/1/2.

**Architecture:** A new `internal/observe` package runs one Snellius cache, engine (`Engine.Run`) and the registry's collectors for one command. `check` stops once every input is in; `--live` keeps running until `--timeout` or Ctrl-C and diffs published models for progress. `internal/view` maps the model to kgwctl's own Check view types; `internal/print` renders text. `load.Load` stays unchanged for `get`/`describe`/`version`.

**Tech Stack:** Go 1.26, cobra v1.10.x, client-go v0.36.x, `github.com/DuncanDoyle/snellius v0.7.0-alpha.1`, sigs.k8s.io/yaml, `k8s.io/utils/clock/testing` (tests only, already indirect via client-go).

**Spec:** [`design.md`](design.md) (r3). Read it first. §n refers to it.

## Global Constraints

- No vendor or edition names anywhere (D-004); the downstream example uses `vendorctl`, `example.com`.
- No new module dependencies (design §9). No TUI, colour or table library. TTY detection via `os.File.Stat()` and `ModeCharDevice`.
- No binary dependency on Snellius `pkg/app`; no kgwctl package imports `pkg/app` or `pkg/api` directly (`pkg/api` is transitive through `pkg/extension`). `TestBinaryDeps` guards both.
- No kgateway special cases in `check`: no filter on `Impl`, no table keyed by finding code or check name (§6.1).
- No timing derived from cluster timestamps (D-016). Times come from the engine or kgwctl's own clock.
- Read-only. No port-forward. `pods/proxy` GET only (D-017).
- Exit codes: `check` 0/1/2 per §3.4; `get`/`describe`/`version` stay 0/1.
- Data to stdout; progress, warnings and errors to stderr. JSON/YAML is exactly one document.
- Envelope `schemaVersion: "kgwctl.kgateway.dev/v1alpha1"`, kind `Check`.
- No ANSI escapes unless the stream they go to is a terminal and `NO_COLOR` is unset.
- Code comments 1–2 lines; Go doc comments on exported identifiers.
- Every `kubectl`/`helm` command in scripts passes `--context`/`--kube-context`; kind clusters use their own `--kubeconfig`.

## Review Focus

1. **A cluster with no kgateway install.** The user expects exit 2 and "no install found", never a clean report. Covered in Task 2.2.4.
2. **`pods/proxy` forbidden** (the common case for a restricted support account). The user expects 001/007/024/025/026 under "Not checked" with the error text, other findings intact, exit code unaffected. Covered in Tasks 2.2.1 and 2.2.4.
3. **Collectors never publish because discovery found no install, or the scrape hangs.** The user expects `check` to finish at `--request-timeout`, not hang. Covered in Task 2.2.1.
4. **Ctrl-C during `--live`.** The user expects a report and exit 2 after the first Ctrl-C, an immediate exit after the second, and no leaked watches. Covered in Task 2.3.2.
5. **A vendor pack's unknown check code with a hold.** The user expects it rendered like any core check. Covered in Task 2.2.5.

## Test harness

Command tests use `internal/testenv` (Phase 1): envtest with Gateway API and kgateway CRDs behind the fault-injecting proxy, production clients, kubeconfig file. envtest runs no Pods, Services' endpoints or kubelets, so:
- controller metrics and proxy stats come from injected fakes: `observe.Options.Scraper` (→ `extension.CollectorEnv.Scraper`) and a registry `ProxyStats` source in tests;
- the testenv proxy gains subresource matching (`Match{Resource: "pods", Subresource: "proxy"}`) so `pods/proxy` can be denied;
- the fake clock drives the engine and collector tickers but not network timeouts (`context.WithTimeout` in the collectors): network cancellation tests use short real-time bounds;
- Pods, Deployments, EndpointSlices and Events are created as fixtures with status set through the status subresource where needed;
- time-based checks use `observe.Options.Clock` (a `clocktesting.FakeClock` passed to `setup.Options.Clock` and the collectors).

## Sub-phases

| Sub-phase | Delivers | Tasks |
|---|---|---|
| 2.1 | Snellius `v0.7.0-alpha.1`, no behaviour change | 2.1.1 |
| 2.2 | `check` one-shot, exit codes, RBAC, docs, vendor pack | 2.2.1–2.2.6 |
| 2.3 | `check --live` | 2.3.1–2.3.3 |

Sub-phases land in order. Each ends with `make test`, `go vet ./...`, gofmt clean.

---

## Sub-phase 2.1 — Snellius upgrade

### Task 2.1.1: Move to `github.com/DuncanDoyle/snellius v0.7.0-alpha.1`

**Files:**
- Modify: `go.mod`, `go.sum`, every `.go` importing `github.com/DuncanDoyle/kgateway-ui/...`, `internal/app/lifecycle_test.go` (guard strings), `internal/load/load.go` (API changes), `docs/phase-status.md` (Program block mentions only if stale)

**Interfaces:** none new. Behaviour of `get`, `describe`, `version` unchanged.

- [ ] **Step 1:** `go get github.com/DuncanDoyle/snellius@v0.7.0-alpha.1`; drop the kgateway-ui requirement; rewrite imports.
- [ ] **Step 2:** Fix compile errors against the tag's API (`setup.Options`, `setup.Config`, `engine.Config`, `install.Install.Impl`, removed `ctrlmetrics.Static`). Keep `load.Load` semantics: `NoEvents: true`, no change signal, one `Compute`.
- [ ] **Step 3:** `TestBinaryDeps`: transitive ban on `github.com/DuncanDoyle/snellius/pkg/app` (and sub-packages) and any `kgateway-ui` package; a separate check that no kgwctl package imports Snellius `pkg/app` or `pkg/api` directly. Prove each fails on a temporary violating import, then revert.
- [ ] **Step 4:** `make test` and `go vet ./...` pass with unchanged goldens. If a golden must change, stop and report why (it would be a behaviour change).
- [ ] **Step 5:** Commit `build: move to github.com/DuncanDoyle/snellius v0.7.0-alpha.1 (kgwctl#5–#7)`.

---

## Sub-phase 2.2 — `check`

### Task 2.2.1: `internal/observe`: one running cache, engine and collectors

**Files:**
- Create: `internal/observe/observe.go`, `internal/observe/observe_test.go`, `internal/observe/main_test.go` (testenv `TestMain`)

**Interfaces:**
- Consumes: `load.Clients`, `extension.Registry`, Snellius `setup`, `cache`, `engine`, `extension.CollectorEnv`, `collect.Store`.
- Produces:
  - `observe.Options struct { MetricsInterval time.Duration /* default 10s */; Quiet time.Duration /* default 2s, design §4.1 */; FirstPublishWait time.Duration /* default 6s */; Clock clock.WithTicker /* nil: real */; Scraper ctrlmetrics.Scraper /* nil: PodProxyScraper */; Logger *slog.Logger }`
  - `observe.Start(ctx context.Context, c load.Clients, reg *extension.Registry, o Options) (*Session, error)` — `ctx` is the session context (design §4.5); the caller bounds the wait for `Start` with its acquisition deadline (`Start` runs `setup.Build` and may block on discovery).
  - `(*Session).Current() *engine.Published` — latest published model, nil before the first.
  - `(*Session).Models() <-chan *engine.Published` — every published model in publication order (unbounded queue fed from `OnLoop` `EvPublished`); closed after `Stop`.
  - `(*Session).Changed() <-chan struct{}` — coalesced (buffer 1) notification for rendering.
  - `(*Session).Complete() bool` — true once §4.1 item 4 holds (see below).
  - `(*Session).Inputs() map[schema.GroupVersionResource]load.Input` — access state per watched input, Phase 1 `load.Input` semantics, plus one pseudo-input per collector named by the collector's `Name()` with state ok/failed/loading.
  - `(*Session).Stop()` — cancels everything and returns after the cache, collectors and engine loop have exited. Idempotent.

Behaviour (§4.1, §4.3, §6.1):
- `setup.Build` with `NoEvents`, `NoCRDs`, `NoEndpoints` false and `Clock: o.Clock`. Keep `cfg.Cache.Changes` (the engine's trigger). `cfg.Engine.Snapshots` = the cache.
- Collectors: `reg.PackCollectors(env)` plus `reg.Collectors()`, run by kgwctl's own runner (not `Registry.RunCollectors`, which has no completion handle): one goroutine each; `Deps.Publish`/`Deps.Fail` wrapped per collector, writing `<name>/<key>` to `cfg.Collected` as `RunCollectors` does and recording first publish/fail and last publish time; `s.Stop(name)` when `Start` returns; panics recovered and logged; all goroutines joined by `Stop()`. `env` gets `Kube`, `InCluster: false`, `Scraper: o.Scraper`, `Interval`, `MaxAge = setup.MetricsMaxAge(Interval)`, `Clock`, `Installs`/`Current` from the engine, `Discovered` closed after the first published model with at least one install over a synced cache, `Collected = cfg.Collected`, `ControllerLogs: false`. A `Start` that returns before publishing (controller logs while off) counts as finished.
- `go Engine.Run(ctx, safety)` with `safety = MetricsInterval`; `OnLoop` runs on Run's goroutine, so on `EvPublished` it only appends to the queue behind `Models()` (never blocks). `Stop()` does not wait for a compute in flight (design §4.3).
- Quiet timer: kgwctl's own timer (on `o.Clock`), re-armed by every collector publish; on expiry it calls `cfg.Changes.Notify()` so the engine computes from settled inputs.
- **Complete** (design §4.1 item 4) = snapshot `Synced`; every optional watch key present in `Snapshot().Access` (proxy pods, CRDs, Events, Services, EndpointSlices, list-only kinds) is OK or has `Err`; every collector has published, failed or returned, or `FirstPublishWait` has passed since the first model with an install (a collector without targets never publishes), **or** the latest model has no install; the quiet timer has fired since the last collector publish; and a model whose compute **started** after that firing (an `EvRunStarted` after it, matched to the next `EvPublished`) has been published, with no collector publish in between.
- Never filter installs by `Impl`.

- [ ] **Step 1: Write the failing tests** (through testenv):
  - `TestObserveCompleteHealthy`: healthy fixture + fake scraper returning zero rejects; `Complete()` becomes true within 10s; `Current().Model.Installs` has one install; every collector input ok.
  - `TestObserveNoInstall`: no controller Deployment; `Complete()` true with zero installs (no hang on collectors).
  - `TestObservePodsProxyDenied`: `observe.Options.Scraper` nil and the testenv proxy returns 403 for `pods/*/proxy`; `Complete()` true; the collector input is `failed` with the 403 text; the metrics-based checks report `Unavailable` (not OK).
  - `TestObserveScrapeHangs`: fake scraper blocks until ctx ends, `FirstPublishWait` 1s: `Complete()` is false before 1s and true after it; the metrics checks are not OK (not passed); `Stop()` returns within 2s.
  - `TestObserveStopNoLeaks`: after `Stop()`, the testenv proxy has no open watches and connections; goroutine count back to baseline within a 5s poll (a compute in flight may still finish; reuse `waitNoWatches`).
  - `TestObserveHoldMatures`: fixture with a ready controller Deployment and 10 routes whose status never arrives; step the fake clock in steps (≤ 10s) past 60s: `resource status` raises KGWUI-003 warnings; `status progress` then has a non-nil `Hold` with `Count >= 10`; step through 5m: KGWUI-018 is raised.
  - `TestObserveModelsInOrder`: every published model reaches `Models()` in `Seq` order, including when the reader is slow.
  - `TestObserveCollectorReturnsEarly`: a registry collector whose `Start` returns nil at once does not block `Complete()`.
  - `TestObserveSilentCollector`: a registry collector that never publishes (no targets) delays `Complete()` by at most `FirstPublishWait` (set to 1s in the test).
  - `TestObserveQuietRecompute`: after the last publish, a model is published after the quiet timer fires, with no other trigger.
  - `TestObserveDiscoverySlow`: discovery delayed beyond the caller's deadline → the caller's wait for `Start` ends at the deadline; `Stop`/cancel leaves no goroutines.
  - `TestObserveOutlivesRequestTimeout`: a session kept for 3s with clients created under the session context keeps receiving models (no 30s-style client cancellation; use a 1s acquisition deadline to prove separation).
- [ ] **Step 2:** `go test ./internal/observe/...` — FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `go test -race ./internal/observe/...` — PASS.
- [ ] **Step 5:** Commit `feat(observe): run cache, engine and collectors for one command`.

### Task 2.2.2: Check view types and model mapping

**Files:**
- Create: `internal/view/check.go`, `internal/view/check_test.go`
- Modify: `internal/view/output_doc_test.go`, `docs/output.md`

**Interfaces:**
- Consumes: `engine.Model`, `health.Report`, `health.Finding`, `health.CheckResult`, `diagnose` (grouping, `Fix`, next steps via `reg.ImplFor(inst).Commands`), Phase 1 `view.Filter` and `load.Input`.
- Produces (JSON tags per design §7):
  - `view.Check struct { Context string; GeneratedAt time.Time; Installs []InstallCheck; Incomplete []load.Input `json:"incomplete,omitempty"`; Observation *Observation `json:"observation,omitempty"` }`
  - `view.InstallCheck struct { Install InstallRef; State string; Counts Counts; Findings []CheckFinding; Groups []CheckGroup; Checks []CheckStatus }`
  - `view.CheckGroup struct { ID string; Cause *Ref; Findings []string /* finding IDs */ }` — from Snellius `diagnose` groups as they are; no cause link between findings.
  - `view.CheckFinding struct { ID, Code, Slug, Severity string; Resource *Ref; Message, Detail string; Since *time.Time; Group string; Evidence json.RawMessage; NextSteps []NextStep; Fix *Fix }` (`omitempty` on optional fields)
  - `view.CheckStatus struct { Name string; OK, Pending bool; Unavailable string; Error, Detail string; Hold *Hold }`
  - `view.Hold struct { Until time.Time; Count int; Reason string }`
  - `view.Observation struct { StartedAt, EndedAt time.Time; StopReason string; Observed []ObservedFinding }`, `view.ObservedFinding struct { ID, Code, Severity string; Resource *Ref; FirstSeen time.Time; ClearedAt *time.Time; Severities []SeverityAt }`, `view.SeverityAt struct { Severity string; At time.Time }`
  - `view.BuildCheck(m *engine.Model, reg *extension.Registry, sel CheckSelect) view.Check`
  - `view.CheckSelect struct { Context string; Namespace string /* "" = all */; Install string; AllControllers bool }`
  - `(view.Check).ExitCode() int` — 1 if any shown finding is error or warning, else 0 (exit 2 is decided by the command).
- Order: installs by ID; findings by severity (error, warning, info), then diagnosis order; checks by name.
- `-n` (`CheckSelect.Namespace`): keep findings whose resource namespace matches, plus findings without a namespace. `Counts` reflect kept findings; `State` stays the engine's.
- Evidence: marshal `Finding.Facts` to `json.RawMessage`; omit when empty.
- No per-code or per-check-name logic.

- [ ] **Step 1: Write the failing tests** (unit, over models built through testenv fixtures or hand-built `engine.Model` values):
  - `TestBuildCheckOrdersBySeverity`, `TestBuildCheckNamespaceFilter` (namespaced finding dropped, install-level kept, counts and `ExitCode` follow), `TestBuildCheckHold` (hold copied with UTC times), `TestBuildCheckUnavailable`, `TestBuildCheckGroups` (a Snellius diagnosis group maps to `Groups` with its cause ref and member IDs, and each member's `Group`), `TestBuildCheckFix` (013 gets `Fix`), `TestBuildCheckUnknownCode` (a finding with code `EXAMPLE-001` maps without special casing), `TestCheckExitCode` (info only → 0).
  - `TestOutputDocMatchesSchema` covers `view.Check`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement and document the `Check` kind in `docs/output.md` (fields, exit codes 0/1/2 for `check`). **Step 4:** PASS.
- [ ] **Step 5:** Commit `feat(view): check view and model mapping`.

### Task 2.2.3: Text report

**Files:**
- Create: `internal/print/check.go`, `internal/print/check_test.go`, `internal/print/term.go` (TTY + `NO_COLOR` helper), goldens under `internal/print/testdata/check/`

**Interfaces:**
- Produces:
  - `print.IsTerminal(f *os.File) bool`; `print.ColorEnabled(f *os.File) bool` (terminal and `NO_COLOR` unset)
  - `print.CheckText(w io.Writer, c view.Check, now time.Time, color bool) error`
  - `print.CheckHint(c view.Check, now time.Time) string` — "" or the §3.1 hint: the earliest `hold.until` as "next decision at HH:MM:SS (in …)"; never an upper bound.

Behaviour: layout per design §3.3 rules 1–8. Sections omitted when empty. "Passed: N checks" counts OK, no hold, available. Hold rows: `name  N held  decides HH:MM:SS  reason`. Not-checked rows show the check's `Error`/`Detail` text (no per-check remedy table) and point at `docs/rbac.yaml` when the text contains "forbidden".

- [ ] **Step 1: Write failing golden tests:** clean install; errors + warnings with a diagnosis group printed under "Likely cause" and next steps; holds + hint; not checked (forbidden); two installs; `color=false` output contains no `\x1b`.
- [ ] **Step 2–4:** FAIL, implement, PASS (`-update` flag regenerates goldens, as Phase 1).
- [ ] **Step 5:** Commit `feat(print): check text report`.

### Task 2.2.4: `check` command and exit codes

**Files:**
- Create: `internal/app/check.go`, `internal/app/check_test.go`, `internal/app/exit.go`, fixtures `internal/app/testdata/check/*.yaml`, goldens `internal/app/testdata/check/*.golden`
- Modify: `internal/app/root.go` (register `check`; `Execute` honours `ExitError`), `internal/app/exitcodes_test.go`, `pkg/cli/cli.go` (`logIgnored` no longer lists collectors and ProxyStats)

**Interfaces:**
- Produces:
  - `app.ExitError struct { Code int; Err error }` with `Error()`/`Unwrap()`. `Execute` returns `Code`; prints `Error:` only when `Err != nil`.
  - `newCheckCmd(reg *extension.Registry, now func() time.Time, opts checkTestHooks) *cobra.Command` (test hooks carry the fake scraper/clock; production passes zero values).
- Flags: `-o text|json|yaml` (default text). `--live` and `--timeout` are added in 2.3, not before.
- Flow (design §4.5): `f.RESTConfig()` → clients bound to the session context (`cmd.Context()` with cancel; not `Flags.Context`) → `observe.Start` in a goroutine, waited for until the acquisition deadline (`--request-timeout` from command start) → wait until `Complete()`, the same deadline, or ctx done → `view.BuildCheck(Current().Model, …)` → `Stop()` → render → exit code.
- Exit 2 (`ExitError{Code:2}`) per §3.4: ctx cancelled (Ctrl-C); deadline with snapshot not `Synced`; no install after filtering ("no kgateway install found" plus Phase 1's `get` hint); any install whose `cluster access` check is not OK. With a model available, the report is printed before returning 2.
- At the deadline with the model available but not complete: report what is there; unfinished inputs go to `Incomplete` and the text's Not checked section; exit from findings.

- [ ] **Step 1: Write failing command tests** (testenv; fake scraper/clock via hooks):
  - Theme fixtures with golden text and expected exit: `healthy` (0), `controller-down` (005, 1), `route-broken` (013 for a route to a missing Service, in its own diagnosis group with the Service as cause, 1), `xds-rejected` (fake scraper reports active rejects → 001, 1), `proxy-disconnected` (fake proxy stats → 026, 1). One-shot runs show 003 inside grace as info, so the exit code comes from 013.
  - `TestCheckNoInstall` → exit 2, stderr names the hint.
  - `TestCheckPodsProxyDenied` → findings unchanged, metrics checks under Not checked, exit equals the findings' exit.
  - `TestCheckCoreDenied` (httproutes 403) → exit 2 with the report printed.
  - `TestCheckJSONOneDocument` (`-o json` decodes as exactly one envelope, kind `Check`), YAML likewise.
  - `TestCheckNamespaceFilter`.
  - `TestCheckDeadline` (scraper hangs, `--request-timeout 2s`) → returns in < 4s, collector under Not checked.
  - `TestCheckDiscoveryTimeout` (discovery delayed past `--request-timeout 1s`) → exit 2 within 2s, empty stdout.
  - Exit matrix rows for `check` in `exitcodes_test.go` (every format same code; stdout empty only on usage/setup errors).
  - `TestNoAccumulation` style: 10 runs of `check`, no growth in watches/connections/goroutines.
- [ ] **Step 2–4:** FAIL, implement, PASS.
- [ ] **Step 5:** Commit `feat(app): check command with exit codes 0/1/2`.

### Task 2.2.5: Vendor pack in the downstream example

**Files:**
- Modify: `examples/downstream/main.go`, `examples/downstream/main_test.go`, `examples/downstream/testdata/*`

**Interfaces:**
- Consumes: `extension.WithPack`, `health.Check`, `extension.Collector`.
- The example registers a pack `example` with: its detector (existing), the `ExamplePolicy` kind (existing), one collector `example-probe` that lists Pods labelled `app=example-server` through `CollectorEnv.Kube` each interval and publishes their readiness (the cache does not watch those Pods), and one check `example server` that reads that value: `EXAMPLE-001` (warning) when no such Pod exists; a hold (`ctx.Hold`, count = unready Pods) for 30s after an unready Pod is first seen, then `EXAMPLE-001`; OK when one is Ready.

- [ ] **Step 1: Write failing tests** through the public `vendorctl` root: absent server Pod → `EXAMPLE-001`, exit 1; unready Pod, fake clock before 30s → `checks[]` has `example server` with a `hold`, exit 0; Ready Pod → OK, exit 0; text renders the finding and the hold; the collector input is ok.
- [ ] **Step 2–4:** FAIL, implement, PASS.
- [ ] **Step 5:** Commit `test(downstream): vendor pack check and collector in check`.

### Task 2.2.6: RBAC, README, live check

**Files:**
- Modify: `docs/rbac.yaml`, `README.md`, `hack/live-check.sh`, `hack/live/fixtures.yaml`

- [ ] **Step 1:** `docs/rbac.yaml`: add every kind and subresource the checks read at the Snellius tag (compare with Snellius `deploy/helm/snellius/templates/clusterrole.yaml` at `v0.7.0-alpha.1`): `events`, `services`, `discovery.k8s.io/endpointslices`, `referencegrants`, list-only kgateway kinds, `pods/proxy` get. Update the header comment ("no */proxy" → only `pods/proxy` get, for controller metrics and Envoy stats). No Secrets, ConfigMaps or `pods/portforward`.
- [ ] **Step 2:** README: a `check` section (what it shows, exit codes 0/1/2, Not checked, still deciding, `--live` placeholder filled in 2.3) and the permissions note.
- [ ] **Step 3:** `hack/live-check.sh verify`: run `check` text and json against the healthy fixture and one scenario per priority theme (scale the controller to 0; a route to a missing Service; a Gateway config Envoy rejects if kgateway has one reproducible, else skip with a note), recording outputs and exit codes under `hack/live/out/check-*`. Also run as a ServiceAccount bound to `docs/rbac.yaml` and assert no Not-checked rows caused by "forbidden".
- [ ] **Step 4:** Run it against kind if Docker is available; record the result in the sub-phase complete note. If Docker is unavailable, say so there; don't claim it ran.
- [ ] **Step 5:** Commit `docs: check RBAC, README and live check`.

---

## Sub-phase 2.3 — `check --live`

### Task 2.3.1: Model diff events

**Files:**
- Create: `internal/observe/diff.go`, `internal/observe/diff_test.go`

**Interfaces:**
- Produces:
  - `observe.Event struct { At time.Time; Kind EventKind; Install string; Code, Severity, Check string; Resource *view.Ref; Hold *view.Hold; Input string; Text string }`
  - `observe.EventKind` constants: `FindingRaised`, `FindingChanged` (same ID, new severity or message), `FindingCleared`, `HoldStarted`, `HoldChanged`, `HoldEnded`, `InputAvailable`, `InputUnavailable`
  - `observe.Diff(prev, next view.Check, at time.Time) []Event` — pure; keyed by install ID + finding ID, check name, input resource.
  - `observe.Tracker` keeps first-seen/cleared/severity history per finding ID: `(*Tracker).Add(c view.Check, at time.Time) []Event`, `(*Tracker).Observed() []view.ObservedFinding` — every finding with a transition during the run: raised after the first model, cleared, or changed severity (also one present from the start and still present).

- [ ] **Step 1: Failing table tests** for each event kind, a severity change under the same ID (info → warning), a hold count change, and a finding that clears and returns (one Observed entry with the latest first-seen, or two — pick one and document it in the Go doc).
- [ ] **Step 2–4:** FAIL, implement, PASS. **Step 5:** Commit `feat(observe): diff published models into progress events`.

### Task 2.3.2: `--live`, `--timeout`, progress and Ctrl-C

**Files:**
- Modify: `internal/app/check.go`, `internal/app/check_test.go`, `internal/app/root.go` (signal handling), `internal/print/check.go`
- Create: `internal/print/progress.go`, `internal/print/progress_test.go`

**Interfaces:**
- Produces: `print.Progress(w io.Writer, e observe.Event) error` (one timestamped line, §3.2), `print.Heartbeat(w io.Writer, elapsed, total time.Duration, c view.Check) error`.
- Flags: `--live` (bool), `--timeout` (duration, default 6m, only valid with `--live`; otherwise usage error exit 1).
- Flow: as 2.2.4, then keep the session until `--timeout` (kgwctl's clock; a fake clock in tests) or the first Ctrl-C. For every model from `Models()` in order: build the view and feed the `Tracker`; write its events to stderr if `print.IsTerminal(os.Stderr)`; heartbeat every 30s. At stop: `Stop()`, set `Observation{StartedAt, EndedAt, StopReason: "timeout"|"interrupted", Observed}`, print one report to stdout.
- Ctrl-C: the first interrupt stops observing and reports (exit 2); after it, restore default signal handling so a second interrupt kills the process. `Execute`'s `signal.NotifyContext` is adapted so `check --live` can do this; other commands keep today's behaviour.
- `--request-timeout` still bounds the initial acquisition; `--timeout` bounds the observation.

- [ ] **Step 1: Failing tests** (command level, fake clock and scraper):
  - `TestLiveTimeout`: `--live --timeout 30s` with the fake clock; returns after the clock passes 30s; exit from findings; JSON has `observation.stopReason: timeout`.
  - `TestLiveHoldDecides`: ready controller, 10 never-statused routes; step the clock in ≤ 10s steps through grace and then the 5m hold; final report has 003 (warning) and 018; Observed shows 003's info → warning change and 018 raised; progress (forced terminal writer in the test hook) shows the `WAITING` line, then the changed and raised lines.
  - `TestLiveClears`: a finding present at start, fixture fixed mid-run (create the missing Service) → final report without it, Observed shows it cleared.
  - `TestLiveInterrupt`: cancel the command context mid-run → report printed, exit 2, `stopReason: interrupted`.
  - `TestLiveJSONOneDocument` and `TestLiveNoANSIOffTerminal`.
  - `TestLiveNoLeaks`: no watches/connections after return; goroutines back to baseline within a 5s poll.
  - `TestLiveIntermittent`: a finding that appears and clears between two renders still appears in Observed.
  - `TestTimeoutWithoutLive` → exit 1 usage error.
- [ ] **Step 2–4:** FAIL, implement, PASS. **Step 5:** Commit `feat(app): check --live with progress and observed section`.

### Task 2.3.3: Docs and live check for `--live`

**Files:** `README.md`, `docs/output.md` (observation block), `hack/live-check.sh`

- [ ] **Step 1:** README `--live` section: when to use it (still deciding, verify a fix, rollout, intermittent), what it prints, Ctrl-C, exit codes.
- [ ] **Step 2:** live-check: `check --live --timeout 3m` while a route's backend Service is created mid-run shows the finding clear and appear in Observed; record output. (018 needs a controller that is ready but not writing status, which kind can't produce reliably; covered by envtest.)
- [ ] **Step 3:** Run if Docker is available; record honestly.
- [ ] **Step 4:** Commit `docs: check --live`.

---

## Phase close prerequisites (not done by the implementer)

Codex phase review (`reviews/phase-codex.md`), triage, `phase-2-complete.md`, roadmap and program updates, closing kgwctl#5–#7: after the owner has tried `check` (owner, 2026-11-10).
