# Phase 1 — Foundation and resource inspection: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A read-only `kgwctl` with `version`, `get` and `describe` over the kgateway-ui model, plus the `pkg/cli` extension API for vendor builds.

**Architecture:** `cmd/kgwctl` is a thin `main` over `pkg/cli`. `internal/load` runs the kgateway-ui cache and engine once per command. `internal/view` turns the model into plain view structs, and `internal/print` renders them. All library decisions (ownership, attachment, conditions) come from kgateway-ui (D-003).

**Tech Stack:** Go 1.26, cobra v1.10.x, client-go v0.36.x, `github.com/DuncanDoyle/kgateway-ui` (tagged per R0, DuncanDoyle/kgateway-ui#16), sigs.k8s.io/yaml.

**Spec:** [`design.md`](design.md) (r2). Read it first. Section numbers below (§n) refer to it.

## Global Constraints

- Module path `github.com/DuncanDoyle/kgwctl`. The name `kgwctl` appears only in `pkg/cli` defaults and `cmd/kgwctl` (D-006).
- No vendor or edition names anywhere (D-004). The downstream example uses `vendorctl`, `example.com`.
- Dependencies are exactly design §9. No `cli-runtime`, controller-runtime, gateway-api types, colour or table libraries in the binary; `controller-runtime/pkg/envtest` only in tests and `internal/testenv`, checked by `TestBinaryDeps` (Task 1.1.5: `go list -deps ./cmd/kgwctl` contains no `controller-runtime`).
- Never import kgateway-ui `pkg/app` or `pkg/api` directly. `pkg/extension` is allowed.
- Never start `engine.Run`, `ctrlmetrics.Collector.Run`, `kinds.Monitor.Run` or proxy stats loops (§4).
- Read-only: no create, update, delete, patch or port-forward calls. Secrets and ConfigMaps are never listed.
- Exit codes: `0` success, `1` error, in every output format (§8).
- Data to stdout. Errors and warnings to stderr only.
- Versioned JSON/YAML envelope: `schemaVersion: "kgwctl.kgateway.dev/v1alpha1"`, `kind`, `item` (§8).
- Code comments: 1-2 lines, Go doc comments on exported identifiers.
- Every `kubectl`/`helm` command in scripts passes `--context`/`--kube-context`; kind clusters use their own `--kubeconfig`.

## Review Focus

1. **A requested kind whose LIST fails with a non-403 error** (auth expired, 500). The user expects an error naming the kind and exit 1, never an empty table. Covered in Task 1.2.3.
2. **A route with two parentRefs: one accepted, one never statused.** The user expects `1/2 (1 no status)`, never `True`. Covered in Task 1.2.2.
3. **A kubeconfig whose current context differs from `--context`.** The user expects the flag to win for every request, including discovery. Covered in Task 1.1.3.
4. **`version` on a cluster whose proxy Pods list arrives after core settle.** The user expects proxy versions present, not missing. Covered in Task 1.1.4.
5. **A route retargeted from another controller's Gateway to ours, still carrying the old status.** The user expects it visible, with its new parent shown as `no status`. Covered in Task 1.2.1.

## Test harness (all command tests)

Command tests use `internal/testenv` (Task 1.1.1): envtest with the Gateway API and kgateway CRDs, behind a fault-injecting `httptest` reverse proxy, and a kubeconfig file pointing at the proxy. Production clients are used throughout, with no fake clientsets. The envtest binaries come from `setup-envtest use <client-go minor> -p path`. `make test` exports `KUBEBUILDER_ASSETS`, and CI does the same.

## Sub-phases

Each sub-phase is one reviewable change. 1.1 must land first. 1.3 consumes the filter and status cells from 1.2, so it starts after Tasks 1.2.1 and 1.2.2 are merged.

| Sub-phase | Delivers | Tasks |
|---|---|---|
| 1.1 | module, `pkg/cli`, load, `version`, downstream example, test harness | 1.1.1–1.1.5 |
| 1.2 | `get`: kind resolution, hiding rule, status summaries, all formats | 1.2.1–1.2.4 |
| 1.3 | `describe` and the output schema doc | 1.3.1–1.3.2 |
| 1.4 | live check on kind, scale benchmark, RBAC and usage docs | 1.4.1–1.4.3 |

**Precondition for 1.1:** R0 (kgateway-ui module tag). Until it exists, use a pseudo-version of kgateway-ui `main` in `go.mod` and a local `go.work` for co-development (program protocol 3). Swap to the tag before the 1.1 close.

---

## Sub-phase 1.1 — Foundation

### Task 1.1.1: Module, clients, envtest harness with fault proxy

**Files:**
- Create: `go.mod`, `internal/load/clients.go`, `internal/load/transport.go`, `internal/load/transport_test.go`, `internal/testenv/testenv.go`, `internal/testenv/proxy.go`, `internal/testenv/testenv_test.go`, `internal/testenv/testdata/crds/` (the Gateway API standard + experimental CRDs at the version kgateway's go.mod pins, derived per AGENTS §14, plus the kgateway CRDs from the matching release's Helm chart; the README records both sources and versions), `internal/testenv/testdata/healthy.yaml`
- Create: `Makefile` (targets `build`, `test` (which sets `KUBEBUILDER_ASSETS` via `setup-envtest`), `vet`, `lint` = `go vet` + `gofmt -l`)

**Interfaces:**
- Produces:
  - `load.Clients struct { Discovery discovery.DiscoveryInterface; Dynamic dynamic.Interface; Kube kubernetes.Interface; Metadata metadata.Interface }`
  - `load.NewClients(ctx context.Context, cfg *rest.Config) (load.Clients, error)`: copies `cfg`, sets `WrapTransport` to `load.BindContext(ctx)`, and builds all four clients.
  - `load.Discovery(ctx context.Context, c discovery.DiscoveryInterface) kinds.DiscoveryClient`: implements `ServerResourcesForGroupVersion` with `c.RESTClient().Get().AbsPath(<"/api/v1" or "/apis/<gv>">).Do(ctx)`, decoding the `APIResourceList`. `load.Load` passes it to `kinds.Resolve` (design §4.4).
  - `load.BindContext(ctx) func(http.RoundTripper) http.RoundTripper`: every request's context is cancelled when `ctx` ends (merge via `context.AfterFunc`). Requests started after `ctx` ended fail immediately with `ctx.Err()`.
  - `testenv.Env struct { Kubeconfig string /* file path */; Proxies []*Proxy; Admin *rest.Config }`
  - `testenv.New(t testing.TB, opts testenv.Options) *testenv.Env`
    - one envtest per test package (`TestMain`), with a fresh set of namespaces per test;
    - `Options{ Fixtures []string; Contexts int /* default 1 */ }`;
    - fixtures are applied as admin; any `status:` in a fixture is written through the status subresource.
  - `testenv.Proxy` (an `httptest` reverse proxy to envtest):
    - `Rule(m Match, a Action) *RuleHits`, where:
      - `Match{ Kind string; Resource string; LabelSelector string }`;
      - `Kind` is `acquire` (a classic LIST, or a WATCH with `sendInitialEvents=true`), `watch` (a WATCH without it) or `discovery`;
      - `Resource` is e.g. `"pods"`, `"httproutes.gateway.networking.k8s.io"`, or `"*"` for discovery;
      - `LabelSelector` is the parsed selector, compared for equality (`""` matches any);
      - `Action{ Delay time.Duration; Status int; RetryAfter int; HoldWatchThenStatus int }`.
    - a delay sleeps in the proxy goroutine, never under a lock;
    - `HoldWatchThenStatus` keeps a plain watch open until the matching LIST completed plus 200ms, then returns that status;
    - `RuleHits.N()`: tests assert it is above zero for every rule they set, and `Env` fails the test at cleanup if a rule never matched;
    - `Requests() []string` and `OpenWatches() int`, for observing traffic.
  - `testenv.ProxyPodSelector(reg *extension.Registry) string`: the registered detectors' `ProxyPodSelector()` string, which is what the cache sends.
  - `testenv.ClassicWatch(t)`: disables the `WatchListClient` feature gate via `k8s.io/client-go/features.ReplaceFeatureGates` and restores it in `t.Cleanup`.
  - Isolation (design §10): `Env` cleanup deletes every object it created (cluster-scoped included) and polls until each is gone. Namespaces are created if absent and never deleted. Tests using `testenv` do not call `t.Parallel()`.

- [ ] **Step 1: Write the failing tests**:
  - `TestEnvServesFixture`: `healthy.yaml` holds:
    - one kgateway controller Deployment and Pod, labelled as `install.UpstreamDetector` expects, with `app.kubernetes.io/version: 2.4.5`;
    - one GatewayClass, one Gateway, one HTTPRoute.

    Using `load.NewClients` on the kubeconfig, discovery lists `httproutes`, a dynamic LIST returns 1 item, and the CRDs' metadata carries `gateway.networking.k8s.io/bundle-version`.
  - `TestProxyDelaysOnlyMatchingSelector`: a 1s acquire delay on pods with `testenv.ProxyPodSelector(extension.New())`. After a real `cache.Start`, the controller-Pod informer settles in <300ms, the proxy-Pod one only after 1s, and the rule's hits are above 0.
  - `TestProxyMatchesWatchList`: with the default feature gates, an acquire 403 on httproutes makes the httproutes input `denied` with hits above 0. With `ClassicWatch`, the same holds.
  - `TestCleanupRemovesObjects`: after a test that applied `healthy.yaml` cleans up, a fresh LIST of every fixture kind (GatewayClasses included) is empty.
  - `TestBindContextCancelsInFlight`: a proxy delay of 5s and a ctx cancelled after 100ms make the request return `context.Canceled` in <500ms.
  - `TestBindContextAfterDone`: a request after cancel fails without reaching the proxy (`Requests()` empty).
- [ ] **Step 2:** `make test`. Expected: FAIL (packages missing).
- [ ] **Step 3:** Implement. The proxy proxies watches as streaming responses (flush per chunk).
- [ ] **Step 4:** `make test`. Expected: PASS.
- [ ] **Step 5:** Commit `build: module, context-bound clients, envtest harness`.

### Task 1.1.2: One-shot load with readiness, input states and deadline

**Files:**
- Create: `internal/load/load.go`, `internal/load/wiring.go` (the only copy of kgateway-ui `pkg/app` wiring, app.go:188-244, with a header comment naming the source and #13), `internal/load/load_test.go`

**Interfaces:**
- Consumes: `load.Clients`, `testenv`.
- Produces:
  - `load.Needs struct { Requested []schema.GroupVersionResource; ProxyPods, CRDs bool }`
  - `load.State string`, with constants `StateOK="ok"`, `StateLoading="loading"`, `StateDenied="denied"`, `StateFailed="failed"`
  - `load.Input struct { Resource string /* "httproutes.gateway.networking.k8s.io" */; GVR schema.GroupVersionResource; State State; Reason string }`
  - `load.Result struct { Model *engine.Model; Served []kinds.Served; Inputs map[schema.GroupVersionResource]load.Input }`
  - `(*Result).Unavailable() []load.Input`: not-ok inputs, sorted by Resource.
  - `load.Load(ctx context.Context, c load.Clients, reg *extension.Registry, needs load.Needs) (*load.Result, error)`
  - `load.TimeoutError struct { Stage string /* "discovery" | "loading" */; Pending []string }`

Behaviour (§4, §4.1, §4.4):
- Check `reg.Err()` and `health.ValidateOrder(reg.Checks)` first.
- The caller owns the deadline on `ctx`.
- `Load` starts the cache on a child context and always cancels it before returning.
- `load.Load(ctx, c, reg, needs)` takes `needs.Requested []schema.GroupVersionResource` (the kinds the command prints) in addition to `ProxyPods` and `CRDs`.
- After start, poll `Snapshot()` until the core inputs have settled (`Settled()`) and the needed optional inputs (`cache.ProxyPodsGVR`, CRDs) have a final access state.
- At the deadline:
  - if a **requested** kind is still pending, return `TimeoutError{Stage:"loading"}`;
  - any other pending input becomes `StateLoading` in `Inputs`, with no error.
- `Compute` then runs once on what loaded (design §4.4).
- Discovery errors caused by the context ending return `TimeoutError{Stage:"discovery"}`.
- Engine config:
  - `Metrics`: a static source returning "no data" (a type in `wiring.go`);
  - `Verifier`: `reg.Verifier`, or the managed-count verifier;
  - `Process`: `reg.Process`;
  - `Checks`: `reg.Checks`;
  - no `Triggers`, `OnLoop` or `KindsChange`.

- [ ] **Step 1: Write the failing tests**:
  All of these go through `testenv`, with proxy rules for the faults.
  - `TestLoadHealthy`: one install; `Installs[0].Resolved.Version == "2.4.5"` (fixture label); every input `ok`.
  - `TestLoadDeniedKind`: Pods LIST 403 gives `Inputs[pods].State == "denied"`.
  - `TestLoadFailedKind`: httproutes LIST 500 gives state `failed` with the reason text.
  - `TestLoadWatchDenied`: under `ClassicWatch`, an httproutes `watch` 403 (immediate) gives a state other than `ok`. Under default gates, an `acquire` 403 does the same.
  - `TestLoadWaitsForProxyPods`: a 300ms delay on pods with the proxy selector and `Needs{ProxyPods:true}` gives proxy Pods `ok`, and core inputs settle before 300ms.
  - `TestLoadOptionalPastDeadline`: a 2s proxy-Pods delay with a 500ms deadline gives a nil error, proxy Pods `loading`, and a non-nil `Model`.
  - `TestLoadRequestedTimeout`: httproutes delayed past the deadline with httproutes in `Requested` gives `*TimeoutError{Stage:"loading", Pending:["httproutes.gateway.networking.k8s.io"]}`.
  - `TestLoadUnrequestedTimeout`: the same delay with httproutes not requested gives a nil error and httproutes `loading`.
  - `TestLoadSlowDiscovery`: a 400ms delay per discovery request and a 1s deadline give `*TimeoutError{Stage:"discovery"}`, returned in <1.5s.
  - `TestLoadDiscoveryRetryAfter`: discovery returns 429 with `Retry-After: 10` and the deadline is 1s, giving `*TimeoutError{Stage:"discovery"}` in <1.5s.
- [ ] **Step 2:** `go test ./internal/load/...`. Expected: FAIL.
- [ ] **Step 3:** Implement `load.go` and `wiring.go`.
- [ ] **Step 4:** `go test -race ./internal/load/...`. Expected: PASS.
- [ ] **Step 5:** Commit `feat(load): one-shot model load with readiness and input states`.

### Task 1.1.3: Root command, cluster flags, `pkg/cli`

**Files:**
- Create: `internal/app/root.go`, `internal/app/root_test.go`, `pkg/cli/cli.go`, `pkg/cli/cli_test.go`, `cmd/kgwctl/main.go`

**Interfaces:**
- Consumes: `load.NewClients`, `testenv` (with `Contexts: 2`).
- Produces:
  - `app.Config struct { Name string; Registry *extension.Registry; Commands []*cobra.Command; Build BuildInfo; Out, Err io.Writer }`. There is no client seam: tests point `--kubeconfig` at `testenv`.
  - `app.BuildInfo struct { Version, Commit, GoVersion string }`
  - `app.NewRoot(cfg app.Config) *cobra.Command`
  - `app.Flags`, which carries the global flags:
    - `--kubeconfig`, `--context`;
    - `-n/--namespace`, `-A/--all-namespaces`;
    - `--request-timeout` (default `30s`);
    - `--install`, `--all-controllers`.
  - `(*app.Flags) RESTConfig() (*rest.Config, string /*namespace*/, error)`: clientcmd loading rules with overrides. The command passes it to `load.NewClients` with the command context.
  - `(*app.Flags) Context(parent) (context.Context, context.CancelFunc)` with the deadline.
  - `app.Execute(root) int`: runs the command and prints `Error: <msg>` to stderr on error. Returns 0 or 1. Silences cobra's usage-on-error.
  - `pkg/cli`: `Option`, `WithName`, `WithRegistry`, `WithCommands`, `WithBuildInfo`, `BuildInfo` (alias of the app type), `NewRootCommand(opts ...Option) *cobra.Command`, `Execute(*cobra.Command) int`.
    - Defaults: name `kgwctl`, registry `extension.New()`.
    - Ignored registry capabilities each log one `slog.Debug` line (§7).
  - `main.go`: `os.Exit(cli.Execute(cli.NewRootCommand(cli.WithBuildInfo(...))))`, with ldflags-set vars.

- [ ] **Step 1: Write the failing tests**:
  - `TestContextFlagWins`: `testenv` with two contexts. The current context is `one` (proxy 0); run with `--context two`. Proxy 1 receives every request, discovery included, and proxy 0 receives none. Until `version` exists in 1.1.4, use a test-only subcommand that calls `load.Load`. 1.1.4 switches this test to `version`.
  - `TestNamespaceDefaultsToContext`: context two has namespace `team-a`, so `RESTConfig` returns `team-a`. `-n x` overrides it.
  - `TestExecuteExitCodes`: a subcommand returning an error gives exit 1, `Error: boom` on Err, nothing on Out.
  - `TestCLIName`: `NewRootCommand(WithName("vendorctl")).Use == "vendorctl"`, and `completion bash` output contains `vendorctl`.
- [ ] **Step 2:** `go test ./internal/app/... ./pkg/cli/...`. Expected: FAIL.
- [ ] **Step 3:** Implement. Add `completion` through cobra's default completion command, with its name taken from `cfg.Name`.
- [ ] **Step 4:** Run the tests again. Expected: PASS.
- [ ] **Step 5:** Commit `feat(cli): root command, cluster flags and pkg/cli`.

### Task 1.1.4: `version`

**Files:**
- Create: `internal/view/version.go`, `internal/print/envelope.go`, `internal/print/version.go`, `internal/app/version.go`, `internal/app/version_test.go`, `internal/app/testdata/version/*.golden`, fixtures `skew.yaml`, `two-installs.yaml`

**Interfaces:**
- Consumes: `load.Load` with `Needs{ProxyPods:true, CRDs:true}`, `app.Flags`.
- Produces:
  - `view.VersionView struct { Client BuildInfo; Installs []InstallVersion; GatewayAPICRDs CRDVersion; Incomplete []load.Input }` (JSON tags in camelCase)
  - `InstallVersion { ID, Display, Controller string; Replicas, Ready int; Proxies []ProxyVersion }`
  - `ProxyVersion { Gateway string /*ns/name*/; Version string; Pods int; DiffersFromController bool }`
  - `CRDVersion { BundleVersion, Channel string; Unknown string /* reason when not ok */ }`
  - `print.Envelope(w io.Writer, format string /* "json"|"yaml" */, kind string, item any) error`
  - `print.SchemaVersion = "kgwctl.kgateway.dev/v1alpha1"`
  - `print.VersionText(w io.Writer, v view.VersionView)`: the layout of design §3.3; unknown fields print `unknown (<state>)`.
- Proxy sets come from `reg.Process.Proxies(resolved, gw, snapshot)` for each owned Gateway. The CRD version comes from `Snapshot.CRDs` (`cache.CRDMeta.BundleVersion`, `.Channel`).

- [ ] **Step 1: Write the failing golden tests**, `TestVersion/<case>/<format>` for formats `text`, `json`, `yaml` and these cases:
  - `skew`: one proxy group at v2.2.4, marked as differing;
  - `two-installs`;
  - `proxy-pods-delayed`: a 300ms delay, versions present;
  - `proxy-pods-past-deadline`: a delay past `--request-timeout 1s`, proxies `unknown (loading)`, exit 0;
  - `pods-denied`: proxies show `unknown (denied)`, a stderr warning, exit 0;
  - `crds-delayed`: a 300ms delay on the CRDs metadata LIST, CRD version present;
  - `crds-denied`: `Gateway API CRDs: unknown (denied)`, exit 0;
  - `gateways-denied`: `Proxies: unknown (gateways: denied)`, never an empty list, exit 0;
  - `client-only`: `--client`, and the proxy sees no requests.
- [ ] **Step 2:** `go test ./internal/app/ -run TestVersion`. Expected: FAIL.
- [ ] **Step 3:** Implement. Golden files are written by `go test ./internal/app/ -run TestVersion -update`.
- [ ] **Step 4:** Inspect the goldens by eye against §3.3, then run the tests. Expected: PASS.
- [ ] **Step 5:** Commit `feat: version command`.

### Task 1.1.5: Downstream example and lifecycle tests

**Files:**
- Create: `examples/downstream/main.go`, `examples/downstream/main_test.go`, `internal/app/lifecycle_test.go`, `.github/workflows/ci.yaml` (`go vet`, `gofmt -l`, `go test -race ./...`, `go build ./cmd/... ./examples/...`, with actions pinned by full SHA)

**Interfaces:**
- Consumes: `pkg/cli` only, in the example. Its test uses `testenv` for a server and kubeconfig, and calls the example's `newRoot() *cobra.Command`, the same function `main` uses, with `--kubeconfig`.
- The example registers:
  - `ExamplePolicy` (group `policy.example.com`, category policy, CRD in the example's `testdata/`);
  - a detector whose controller name is `example.com/gateway-controller`;
  - a `hello` command printing `hello`.

- [ ] **Step 1: Write the failing tests**:
  - `TestDownstreamVersionShowsInstall`: the example's detector install appears in `version -o json`.
  - `TestDownstreamPolicyInGet`: an `ExamplePolicy` with no ancestors appears in `get examplepolicies -A`.
  - `TestDownstreamCommand`: `hello` prints `hello`.
  - `TestCancelOnEveryExit`: success, error and timeout runs each reach `Proxy.OpenWatches() == 0` within 2s after `Execute` returns.
  - `TestReturnAtDeadline`: an httproutes acquire `Delay: 1h` (covering LIST and WatchList), then `get httproutes --request-timeout 1s`, returns exit 1 within 2s of start, and the rule's hits are above 0.
  - `TestBinaryDeps`: `go list -deps ./cmd/kgwctl ./examples/downstream` contains no `sigs.k8s.io/controller-runtime` and no kgateway-ui `pkg/app`.
  - `TestNoAccumulation`: 20 in-process `version` runs, then `OpenWatches()==0`. As supplementary evidence only, `runtime.NumGoroutine` is at most the count after 1 run + 5.
- [ ] **Step 2:** `go test ./examples/... ./internal/app/ -run 'Downstream|Cancel|Accumulation'`. Expected: FAIL.
- [ ] **Step 3:** Implement the example and CI workflow.
- [ ] **Step 4:** Tests PASS; `go build ./...` ok.
- [ ] **Step 5:** Commit `feat: downstream example and lifecycle tests`.

**1.1 close:** go.mod on the kgateway-ui tag (R0), `make test vet lint` green, and a sub-phase complete file per the SDLC.

---

## Sub-phase 1.2 — `get`

### Task 1.2.1: Kind resolution and the hiding rule

**Files:**
- Create: `internal/resolve/resolve.go`, `internal/resolve/resolve_test.go`, `internal/view/filter.go`, `internal/view/filter_test.go`, fixtures `other-controller.yaml`, `orphans.yaml`, `listenersets.yaml`

**Interfaces:**
- Produces:
  - `resolve.Target struct { Label string /* table title */; Kinds []kinds.Served }`
  - `resolve.Kinds(served []kinds.Served, modelled []kinds.Descriptor, arg string) ([]resolve.Target, error)`:
    - accepts comma lists, `all` (every served kind except GatewayClass), plural, singular, Kind and `resource.group`, case-insensitive;
    - the short names of §3: `gwc gw ls hr gr tr tlsr tp lp bcp be gwp btlsp`;
    - alias `listenersets|listenerset|ls` → `ListenerSet` + `XListenerSet`;
    - `xlistenersets` → only `XListenerSet`.
  - `resolve.UnknownKindError{Name}` and `resolve.NotServedError{Name}`: modelled but not served by this cluster.
  - `view.Scope struct { Namespace string; AllNamespaces bool; Install string; AllControllers bool; Selector labels.Selector }`
  - `view.NewFilter(r *load.Result, s view.Scope) (*view.Filter, []string /* warnings */)`
  - `(*Filter) Visible(k kinds.Served, obj *unstructured.Unstructured) bool`: the §6.2 table.
  - `(*Filter) InstallOf(obj) (install.Resolved, bool)`: from `Installs[].Owned`.
  - `(*Filter) Ours(controllerName string) bool`: any detected (or `--install`-selected) install's controller.

- [ ] **Step 1: Write the failing tests.**
  - `TestResolve`: table-driven; each row is input → labels:
    - `gw` → `[Gateway]`
    - `ls` → `[ListenerSet]` with both kinds
    - `xlistenersets` → XListenerSet only
    - `httproutes.gateway.networking.k8s.io` → HTTPRoute
    - `gateways,hr` → two targets
    - `all` → every served kind except GatewayClass
    - `directresponses` → `UnknownKindError`
    - `tcproutes` on a cluster without TCPRoute → `NotServedError`
  The filter tests build their `load.Result` with `testenv` + `load.Load`, never by hand:
  - `TestFilterHidesOtherController`: another controller's GatewayClass, its Gateway and an HTTPRoute whose only parent is that Gateway are not visible. With `AllControllers` they are visible.
  - `TestFilterKeepsOrphans`: a route to a missing Gateway, a route with no parentRefs, a policy with no ancestors, GatewayParameters and a Backend are all visible with an install present.
  - `TestFilterRetargetedRoute`: a route whose spec parent is now our Gateway, or a missing Gateway, and whose only status entry comes from another controller, is visible.
  - `TestFilterNoInstall`: no install leaves everything visible, with a warning containing `no kgateway install detected`.
  - `TestFilterOwnershipInputDown`: table-driven over Deployments, Namespaces, GatewayClasses, Gateways and controller Pods (selector from the detector) each denied. Each leaves everything visible, with a warning naming that input.
  - `TestFilterInstallFlag`: `--install a/x` hides objects owned only by install `b`.
- [ ] **Step 2:** Run. Expected: FAIL.
- [ ] **Step 3:** Implement. `Visible` follows design §6.2. Routes and ListenerSets use spec parentRefs only (`objects.RouteParentRefs`, `objects.ListenerSetParent`). GatewayClasses use `objects.ControllerName`, and policies use `objects.PolicyAncestors`. It never infers attachment.
- [ ] **Step 4:** PASS.
- [ ] **Step 5:** Commit `feat(view): kind resolution and visibility rule`.

### Task 1.2.2: Status cells

**Files:**
- Create: `internal/view/status.go`, `internal/view/status_test.go`, fixture `mixed-parents.yaml`

**Interfaces:**
- Produces:
  - `view.ObjectCondition(obj *unstructured.Unstructured, condType string) string`: returns `True`, `False (<Reason>)`, `Unknown (<Reason>)`, `<no status>` or `<stale>`.
  - `view.ParentState string`, with values `accepted`, `rejected`, `no status`, `stale`, `other`, `unwatched`, `declared`.
  - `view.ParentSummary struct { Parent objects.ParentRef; State ParentState; Reason string; Controller string }`
  - `view.RouteParents(r *load.Result, f *Filter, route *unstructured.Unstructured, condType string) []ParentSummary`, implementing the design §3.1 table exactly:
    - the denominator is the spec parentRefs, deduplicated on the full `objects.ParentRef` value (group, kind, namespace, name, sectionName, port). `.Ref()` is used only to find the owning install;
    - states come from the `objects.RouteParents` entries for each parent and `condType` (`Accepted` or `ResolvedRefs`), via `f.Ours` and `objects.IsStale`;
    - the owning install's `explore.Index` is consulted only for parents with no entry from ours, mapping `explore.AttachmentUnwatched`→`unwatched` and `explore.AttachmentDeclared`→`declared`;
    - when the index inputs are not ok (design §6.3), that lookup is skipped.
  - `view.PolicyAncestors(...) []ParentSummary`: from `objects.PolicyAncestors`.
  - `view.Summarize(ps []ParentSummary) string`: `True` when all are accepted. Otherwise `<a>/<n>` plus a parenthesised list of non-zero counts in the fixed order rejected, stale, no status, unwatched, declared, other. A single rejected entry adds its reason: `0/1 (1 rejected: NotAllowedByListeners)`. An empty list gives `<no status>`.

- [ ] **Step 1: Write the failing tests**:
  - `TestSummarize`: table-driven over the §3.1 combinations, including `1/3 (1 stale, 1 no status)`.
  - `TestRouteParentsMixed` (testenv): on `mixed-parents.yaml` (parent A accepted, B no entry, C stale, D only another controller), the states are `[accepted, no status, stale, other]` and the cell is `1/4 (1 stale, 1 no status, 1 other)`.
  - `TestRouteParentsDuplicateRef`: the same parent listed twice counts once (`1/1`, cell `True`).
  - `TestRouteParentsTwoListeners` (testenv): parentRefs to `gw` with `sectionName: a` and `sectionName: b`, with a accepted and b rejected (`NotAllowedByListeners`), give the cell `1/2 (1 rejected: NotAllowedByListeners)`.
  - `TestRouteParentsTwoOfOurs`: two installs both write an entry for one parent; the entry from the install owning the parent decides.
  - `TestRouteParentsUnwatched` (testenv): a route in a namespace outside the install's scope, with no entries, gets `unwatched`.
  - `TestObjectConditionStale`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** PASS.
- [ ] **Step 5:** Commit `feat(view): status cells`.

### Task 1.2.3: Tables and the `get` command

**Files:**
- Create: `internal/view/get.go`, `internal/print/table.go`, `internal/print/rawlist.go`, `internal/app/get.go`, `internal/app/get_test.go`, `internal/app/testdata/get/*.golden`

**Interfaces:**
- Produces:
  - `view.Table struct { Title string; Headers []string; Rows [][]string }`
  - `view.GetTables(r *load.Result, f *Filter, targets []resolve.Target, wide bool, name string) ([]view.Table, []*unstructured.Unstructured, error)`. The columns are those of design §3.1:
    - `ROUTES` and `LISTENERSETS` come from the Gateway's `explore` row (`Index.FindGateway`, `GatewayRow`), and are `unknown (<input>: <state>)` when an index input is not ok (design §6.3);
    - attached counts come from the library, never recounted;
    - `INSTALL` appears when `len(Installs) > 1`;
    - `GROUP` appears for the ListenerSet alias.
  - `print.Tables(w io.Writer, ts []view.Table)`: tabwriter, a blank line between tables, `No resources found in <ns> namespace.` on stderr when empty, as kubectl does.
  - `print.RawList(w io.Writer, format string, objs []*unstructured.Unstructured) error`: a `v1` `List` with `managedFields` stripped.
  - `get <kinds> [name] [-o table|wide|yaml|json] [-l selector]`: rows sorted by namespace, then name.
- Errors (exit 1):
  - the requested kind is not ok, giving `cannot read <resource>: <state>: <reason>`;
  - the name is not found, giving `<resource> "<name>" not found`;
  - `UnknownKindError` or `NotServedError`.

- [ ] **Step 1: Write the failing golden tests**, `TestGet/<case>/<format>` over the formats `table`, `wide`, `yaml`, `json` and these fixtures:
  - healthy;
  - orphans;
  - mixed-parents;
  - two-installs;
  - other-controller (with and without `--all-controllers`);
  - listenersets;
  - parameters-and-backends.

  Then `TestGetErrors`, where each case is one row asserting the exit code, stderr text and empty stdout:
  - httproutes 500;
  - httproutes 403;
  - watch denied, immediate;
  - unknown kind;
  - unserved kind;
  - not found;
  - requested-kind timeout (`TimeoutError` message names `loading` and the pending resource).

  Then `TestGetWatchDeniedHeld`, under `ClassicWatch`: the httproutes watch is held past LIST sync, then 403, over 10 runs. Each run is either exit 0 with stdout equal to the healthy golden, or exit 1 with `denied` on stderr and empty stdout (design §6.3 invariant). Then `TestGetIndexInputDown`: Namespaces denied, `get gateways` exit 0, ROUTES shows `unknown (namespaces: denied)`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** Check the goldens by eye against §3.1, then PASS.
- [ ] **Step 5:** Commit `feat: get command`.

### Task 1.2.4: Exit-code matrix

**Files:**
- Create: `internal/app/exitcodes_test.go`

- [ ] **Step 1:** `TestExitCodeMatrix`. For each command (`get`, `version`; `describe` is added in 1.3) × every format × {success, not found, denied, failed, timeout}, assert that the exit code is the same across formats and that stdout is empty on error. `version` has no not-found case.
- [ ] **Step 2:** Run. Fix any mismatch in the command code, not in the test.
- [ ] **Step 3:** Commit `test: exit-code matrix`.

**1.2 close:** green gates and a sub-phase complete file.

---

## Sub-phase 1.3 — `describe`

### Task 1.3.1: Describe view and text output

**Files:**
- Create: `internal/view/describe.go`, `internal/view/describe_test.go`, `internal/print/describe.go`, `internal/app/describe.go`, `internal/app/describe_test.go`, `internal/app/testdata/describe/*.golden`

**Interfaces:**
- Consumes: `Filter`, `RouteParents`, `PolicyAncestors`, `ObjectCondition`, `print.Envelope`.
- Produces:
  - `view.DescribeView struct { Object ObjectInfo; Spec *SpecSummary; Conditions []ConditionBlock; Relations Relations; Incomplete []load.Input }`
  - `ConditionBlock { Scope string /* "object" | "listener <name>" | "parent <ref>" | "ancestor <ref>" */; Controller string; Conditions []ConditionRow }`
  - `ConditionRow { Type, Status, Reason, Message, LastTransition string; ObservedGeneration int64; Stale bool }`
  - `Relations { Resolved bool; Parents []ParentSummary; AttachedRoutes []ListenerRoutes; NotAccepted []RouteRef; ListenerSets []string; DelegationParents, DelegationChildren []string; Backends []string; Declared *DeclaredRefs }`. `Resolved=false` means unindexed: only `Declared` is filled, and the text heading is "Declared references (not resolved)".
  - `view.Describe(r *load.Result, f *Filter, t resolve.Target, ns, name string) (*view.DescribeView, error)`. Spec summary for Gateway, ListenerSet and HTTPRoute only, via `objects.DecodeGateway`, `DecodeListenerSet` and `DecodeHTTPRoute`.
  - `print.DescribeText(w io.Writer, v *view.DescribeView)`
  - `describe <kind> <name> [-n ns] [-o text|yaml|json]`. A target resolving to more than one kind (the `ls` alias) uses the first that has the name. It is an error when two kinds both have it: `ambiguous: name exists as ListenerSet and XListenerSet; use listenersets.<group>`.

- [ ] **Step 1: Write the failing golden tests**, `TestDescribe/<case>/<format>` over text, json and yaml:
  - Gateway (healthy): listeners, attached routes per listener, a not-accepted route with its deciding condition;
  - HTTPRoute with mixed parents;
  - HTTPRoute with a missing parent (unresolved; Declared lists the parentRef);
  - a delegation child;
  - a policy with ancestors;
  - a policy with none;
  - GatewayParameters (no spec summary, conditions only);
  - other-controller Gateway under `--all-controllers` (unresolved);
  - Pods denied (Incomplete lists pods);
  - Namespaces denied on a Gateway ("Relations unknown", listing namespaces; conditions still shown).
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** Check the goldens by eye against §3.2, then PASS. Add `describe` to `TestExitCodeMatrix`.
- [ ] **Step 5:** Commit `feat: describe command`.

### Task 1.3.2: Output schema doc

**Files:**
- Create: `docs/output.md`

- [ ] **Step 1:** Document the envelope, `Describe` and `Version` items (every field, type and meaning), the `incomplete` list, the stability rule (§8) and the exit codes.
- [ ] **Step 2:** `TestOutputDocMatchesSchema` parses the field tables in `docs/output.md` and compares them to the JSON field names of `DescribeView` and `VersionView` through reflection. Run it and PASS.
- [ ] **Step 3:** Commit `docs: output schema`.

**1.3 close:** green gates and a sub-phase complete file.

---

## Sub-phase 1.4 — Live check, scale, docs

### Task 1.4.1: Live check on kind

**Files:**
- Create: `hack/live-check.sh` (subcommands `setup`, `verify <kubeconfig>`), `hack/live/fixtures.yaml`, `docs/rbac.yaml` (a ClusterRole with exactly the design §6.3 permissions; Task 1.4.3 documents it)

- [ ] **Step 1:** `setup`, run as admin, does the following:
  1. Creates the kind cluster `kgwctl-live` with `--kubeconfig "$KUBECONFIG_FILE"`.
  2. Installs the Gateway API CRDs matching the kgateway release's go.mod pin. Derive it per AGENTS §14; do not hard-code.
  3. Installs kgateway OSS (latest release) via Helm with `--kube-context`.
  4. Applies `fixtures.yaml`: a healthy Gateway and route, a route to a missing Gateway, a route with a bad backendRef, a TrafficPolicy, a GatewayParameters.
  5. Creates the ServiceAccount `kgwctl-reader` bound to `docs/rbac.yaml`, and writes its kubeconfig to `hack/live/out/reader.kubeconfig` (gitignored) without printing the token.

  `verify <kubeconfig>` runs `version`, `get all -A` in each format, and `describe` on each fixture with the built binary, saving the outputs under `hack/live/out/<name>/`. It fails if any command exits non-zero, or if the orphan route is missing from `get httproutes -A`.
- [ ] **Step 2:** Run `setup`, then `verify` with the admin kubeconfig. Expected: exit 0. Read the outputs and confirm the status cells match `kubectl --context kind-kgwctl-live get -o yaml` for each fixture.
- [ ] **Step 3:** Record the kgateway version, Gateway API version and the observed output excerpts in the 1.4 complete file. Commit `test: live check on kind`.

### Task 1.4.2: Scale benchmark

**Files:**
- Create: `hack/scale/generate.go` (writes 1,000 Namespaces, 500 Gateways, 5,000 HTTPRoutes with parentRefs; no status), `hack/scale/run.sh`

- [ ] **Step 1:** Apply the generated objects to the live cluster.
- [ ] **Step 2:** Run `/usr/bin/time -l kgwctl get httproutes -A > /dev/null` 5 times. Record min, median and max wall time, and peak RSS.
- [ ] **Step 3:** Compare against the §6.4 target (< 5 s). Over target: stop and report numbers to the owner; don't optimise without a decision.
- [ ] **Step 4:** Record the numbers in the 1.4 complete file. Commit `test: scale benchmark`.

### Task 1.4.3: README and RBAC

**Files:**
- Modify: `README.md`

- [ ] **Step 1:** README: install (`go install`), commands with one example each, global flags, the RBAC in `docs/rbac.yaml`, the §6.3 limits (watch required, cluster-wide listing), exit codes, and a link to `docs/output.md`.
- [ ] **Step 2:** `hack/live-check.sh verify hack/live/out/reader.kubeconfig`. Expected: all commands succeed, and the outputs equal the admin run's.
- [ ] **Step 3:** Commit `docs: README and RBAC`.

**1.4 close, then phase close:** run the exit gates in `phase-status.md` and the phase cross-model review. (R1–R4 are filed right after owner approval of the design, design §11.)
