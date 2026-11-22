# Phase 4 — Support bundle: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `kgwctl bundle` writes one redacted `tar.gz` with resources, workloads, install metadata, versions, `check`, logs, metrics and per-replica Envoy captures, under one deadline, with a manifest, `errors.txt` and `redactions.json`.

**Architecture:** `pkg/bundle` is the public contract (`Collector`, `Env`, `Sink`, `Path`). `internal/redact` holds pure redactors per content type. `internal/bundle` stages and redacts every Sink item, commits through one writer goroutine, seals at the deadline and finalizes. Scope and the replica inventory are built once before collectors run. Collectors in `internal/bundle/collect` reuse `load`, the Snellius pods/proxy scraper and `proxydump` operations; the `check` collector lives in `internal/app` and calls the `kgwctl check` one-shot path.

**Tech Stack:** Go 1.26, cobra, client-go v0.36.x, `archive/tar`, `compress/gzip`, `prometheus/common/expfmt` (already in the graph), Snellius v0.8.2, xdsview v0.4.3. No new modules.

**Spec:** [`design.md`](design.md) (r2). Read it first. §n refers to it.

## Global Constraints

- No API request for Secrets or ConfigMaps anywhere in the bundle (§6.1). No namespaced request outside `Scope.ReadNamespaces` (§3.2).
- No controller admin request (§6.2). Envoy admin only through `proxydump.Op` (§6.3); no API takes a path.
- Every archive member goes through a `bundle.Sink` method: `Object`, `ConfigDump`, `JSON`, `Prometheus`, `Text`. Paths are `bundle.Path` components (§8.4).
- Header values redacted whatever their name; JWKS reduced to public members; Prometheus validated; every error string redacted (§7, §7.1).
- Marker `[redacted]`; no hashes or lengths; `redactions.json` holds rule, location and count only.
- Item caps: logs 10 MiB, config dump 64 MiB, metrics 8 MiB, others 16 MiB. `--max-size` default `512Mi` with a 1 MiB footer reserve. Seal drain 2 s; finalization 30 s (§8.3).
- Output: `O_CREATE|O_EXCL`, `0600`, removed on fatal failure; regular entries only, `0644` inside.
- Deadlines: `--timeout` (default `5m`) for collection; `--request-timeout` (default 30s) per item; Ctrl-C acts as the deadline.
- Logs: one `T0`; `SinceTime`, `Timestamps: true`, `TailLines`, `LimitBytes` = cap + 64 KiB; local cap; `.previous` only when `restartCount > 0`; init containers included (§3).
- Workers: 8. Commit order: registration order, then path order within a collector.
- Exit codes: 0 when a bundle is written (also partial), 2 when none is (§3.1).
- RBAC: reader ClusterRole unchanged; `pods/log` in a new optional Role `kgwctl-bundle-logs`; `pods/portforward` in the existing optional Role.
- No vendor or edition names (D-004). No new modules.
- Code comments 1–2 lines; Go doc comments on exported identifiers. Archive bytes to stdout only with `-o -`; progress to stderr.
- Every `kubectl`/`helm` call in scripts passes `--context`/`--kube-context`; kind clusters use their own `--kubeconfig`.

## Review Focus

1. **A Secret-backed custom header** (`x-tenant-key: <pattern-free value>`) in a TrafficPolicy and in the config dump. Expects `[redacted]` in both, the name kept. Tasks 4.1.2, 4.1.3.
2. **A reader-only user.** Expects exit 0 with resources, workloads, install, version without observed Envoy versions, and `check`; `errors.txt` names `pods/log` and `pods/portforward` with their Roles. Task 4.3.5.
3. **The deadline hits mid-log-stream.** Expects the partial log with its last full line and a truncation line, the collector `timeout`, a late Sink call `dropped: late`, footers written. Tasks 4.1.3, 4.1.4, 4.3.1.
4. **A Gateway with a 63+ character name and 3 replicas, one CrashLooping.** Expects three inventory entries by full-name annotation, two complete replica directories, and one with `.previous` logs and admin items `failed`. Tasks 4.2.2, 4.3.4.
5. **`-n team-a`.** Expects no `team-b` object, capture, metrics sample or namespaced request; install namespace only for controller artefacts. Tasks 4.2.2, 4.2.3, 4.3.2.
6. **The output exists, or the destination fails mid-write.** Expects exit 2; an existing file untouched; a failed file removed. Tasks 4.1.3, 4.2.1.

## Test harness

- `internal/redact`: pure table tests; corpus in `internal/redact/testdata/canary/`, one directory per design §10 row.
- `internal/bundle/bundletest` (new, test-only): `Extract(t *testing.T, r io.Reader) Archive` (member name → bytes, mode, type flag) and `AssertNoCanary(t *testing.T, a Archive, canaries []string)`, which scans member names, raw bytes, base64-decoded strings and nested gzip members.
- Redaction boundary: production `Writer` into a buffer, then `Extract`. Corpus-only tests do not count as archive proof.
- Transport boundary: client-go and the Snellius scraper against an `httptest` API server (`rest.Config.Host` pointed at it) that records method, path and query; port-forward against `newSPDYPod` in `internal/proxydump/spdy_test.go`.
- Command boundary: envtest (`internal/testenv`) with fixtures; `bundleTestHooks` seams for the log source, scraper and `proxydump.Dialer` (envtest runs no kubelet). RBAC denial through `Proxy.Rule(Match{Resource: "pods", Subresource: "log"|"portforward"|"proxy"}, Action{Status: 403})`. `Proxy.Requests()` is the request recorder.
- Real kubelet paths run only in the live check (Task 4.4.3).

## Sub-phases

| Sub-phase | Delivers | Tasks |
|---|---|---|
| 4.1 | Public contract, redactors, writer, runner and manifest, threat model | 4.1.1–4.1.5 |
| 4.2 | Command, scope, inventory, cluster-state collectors | 4.2.1–4.2.4 |
| 4.3 | Logs, metrics, Envoy transport and capture, RBAC | 4.3.1–4.3.5 |
| 4.4 | Extension point, README, live check | 4.4.1–4.4.3 |

Each sub-phase ends with `make test` (race), `go vet ./...`, gofmt clean, the deps check, and `go mod tidy -diff` empty.

---

## Sub-phase 4.1 — Contract, redaction, writer

### Task 4.1.1: `pkg/bundle` contract
**Files:** create `pkg/bundle/{bundle.go,path.go}`, `pkg/bundle/path_test.go`.
**Interfaces (produces):**
- `type Path []string`; `func (p Path) String() string`; `func ValidatePath(p Path) error` (§8.4; no duplicate check, that needs the writer).
- `type Source struct{ Namespace, Pod string; UID types.UID; Container, Operation string }`
- `type Sink interface { Object(p Path, u *unstructured.Unstructured, src Source) error; ConfigDump(p Path, r io.Reader, src Source) error; JSON(p Path, r io.Reader, src Source) error; Prometheus(p Path, r io.Reader, src Source) error; Text(p Path, r io.Reader, src Source) error; Error(item Path, src Source, err error) }`
- `type Collector interface { Name() string; Collect(ctx context.Context, env Env, s Sink) error }`
- `type Env struct{ Kube kubernetes.Interface; Dynamic dynamic.Interface; Discovery discovery.DiscoveryInterface; Scope Scope; Window Window; Replicas []Replica; Now func() time.Time; RequestTimeout time.Duration }`
- `type Scope struct{ Namespaces []string /* nil: all */; ReadNamespaces []string /* nil: all */; Installs []Install; ClusterKinds []schema.GroupKind; AllControllers bool }`
- `type Install struct{ Namespace, Deployment, Chosen string /* "flag"|"detected" */ }`
- `type Window struct{ Start, Since time.Time; Tail int64 }`
- `type Role string` with `RoleController`, `RoleProxy`; `type Container struct{ Name string; Init bool; Restarts int32 }`
- `type Replica struct{ Namespace, Pod string; UID types.UID; Role Role; Owner, Identity string; Ready, Terminating bool; Phase corev1.PodPhase; Containers []Container; StartTime time.Time }`
- `var ErrSealed`, `ErrBudget`, `ErrDuplicate`, `ErrInvalidPath` (sentinels the Sink returns).
- `func ValidName(name string) error`: collector names `^[a-z0-9]+(-[a-z0-9]+)*$`.
- [ ] `TestValidatePath`: accepts `{"resources","gateway.networking.k8s.io","Gateway","_cluster","gw.yaml"}`; rejects empty path, empty component, `.`, `..`, `.hidden`, `-x`, `a/b`, `a\b`, a NUL, a 256-byte component, 9 components.
- [ ] `TestValidName`: accepts `logs`, `proxy-admin`; rejects `Logs`, `a_b`, `-x`, empty.
- [ ] Implement; pass; commit `feat(bundle): public collector contract`.

### Task 4.1.2: Redactors and canary corpus
**Files:** create `internal/redact/{redact.go,object.go,configdump.go,jwks.go,prometheus.go,text.go,errors.go,rules.go}`, tests, `testdata/canary/`.
**Interfaces (produces):**
- `type Rule string` with constants `RuleManagedFields`, `RuleLastApplied`, `RuleHeaderValue`, `RuleHeaderPair`, `RuleJWKS`, `RuleKeyName`, `RuleField`, `RuleInline`, `RuleLua`, `RulePattern`, `RuleScope`, `RulePEMTail`.
- `type Finding struct{ Rule Rule; Location string; Count int }`
- `type Options struct{ ExtraHeaders []string; Namespaces map[string]bool /* nil: all */ }`
- `func Object(u *unstructured.Unstructured, o Options) []Finding` (in place)
- `func ConfigDump(body []byte, o Options) ([]byte, []Finding, error)`
- `func JSON(body []byte, o Options) ([]byte, []Finding, error)` (invalid JSON → error, no output)
- `func Prometheus(body []byte, o Options) ([]byte, []Finding, error)` (not text exposition → error)
- `type TextResult struct{ Findings []Finding; Truncated bool; Reason string }`
- `func Text(r io.Reader, w io.Writer, max int64, o Options) (TextResult, error)` (line streaming; cut and cancel rules of §3)
- `func JWKS(s string) (string, []Finding)`
- `func Error(err error, server string) string` (`view.Redactor` then the pattern pass)
- [ ] Corpus: design §10 rows 1–17, each value `kgwctl-canary-<row>-<loc>` or a secret-shaped variant; row 10 uses a value with no pattern match.
- [ ] `TestCorpusNoLeaks`: every fixture through its redactor; no canary in output; each planted location in `[]Finding`.
- [ ] `TestHeaderValueAnyName`: `x-tenant-key`, `x-foo` and `Authorization` values in `request_headers_to_add`, `response_headers_to_add`, header-mutation `mutations`, `initial_metadata` (`value` and `raw_value`) and in HTTPRoute, TrafficPolicy and ListenerPolicy modifiers become `[redacted]`; names unchanged.
- [ ] `TestJWKSPublicOnly`: RSA and EC keys keep `kty`, `kid`, `n`, `e`, `crv`, `x`, `y`, `x5c`; `k`, `d`, `p`, `q`, `dp`, `dq`, `qi`, `oth` and an unknown member become `[redacted]`; a single JWK works; invalid JSON and a PEM string are redacted whole.
- [ ] `TestPrometheus`: valid body keeps metric names and non-secret labels; a JWT in a label value and in a `# HELP` comment is redacted; an HTML body returns an error; with `Namespaces={team-a}` a sample with `namespace="team-b"` or `gateway_namespace="team-b"` is dropped (`RuleScope`).
- [ ] `TestTextCutAndCancel`: a reader that fails mid-line drops the partial line; an open `BEGIN … PRIVATE KEY` block without `END` is redacted to the end (`RulePEMTail`); the cap cuts at a line boundary; `Truncated` and `Reason` set.
- [ ] `TestErrorRedaction`: an error holding a URL, the server host, a Bearer token and an `sk-` key keeps none of them.
- [ ] `TestKeepsUseful`: names, namespaces, hostnames, header names, JWKS public members, cert SANs and Envoy's own `[redacted]` survive; redacted YAML/JSON re-parses with the same key set.
- [ ] Run, fail, implement §7, pass. Commit `feat(redact): redactors per content type with a canary corpus`.

### Task 4.1.3: Writer: staging, budget, seal, finalization
**Files:** create `internal/bundle/{writer.go,dest.go,stage.go}`, tests.
**Interfaces (produces):**
- `type Limits struct{ MaxSize, FooterReserve int64; Log, ConfigDump, Metrics, Other int64; Drain, Finalize time.Duration }`; `func DefaultLimits(maxSize int64) Limits`
- `type Dest interface{ io.Writer; Close() error; Abort() error }` (`Abort` removes a file, no-op on stdout)
- `func CreateFile(path string) (Dest, error)` (`O_CREATE|O_EXCL|O_WRONLY`, `0600`); `func Stdout(w io.Writer) Dest`
- `func NewWriter(d Dest, root string, l Limits, ro redact.Options, server string, now func() time.Time) *Writer`
- `func (w *Writer) Sink(collector string) bundle.Sink` (stages per collector)
- `func (w *Writer) Commit(collector string) error` (staged items, sorted by path)
- `func (w *Writer) Seal()` (waits up to `Drain` for in-flight calls)
- `func (w *Writer) Finalize(m Manifest) error` (footers, gzip and file close, within `Finalize`; on error `Abort`)
- `func (w *Writer) Items() []Item`, `Errors() []ItemError`, `Findings() map[string][]redact.Finding`
- [ ] `TestWriterRedactsEveryMethod` (archive boundary): one canary through each Sink method and `Error`; `Extract` + `AssertNoCanary` over all members incl. footers.
- [ ] `TestWriterRefusesBadPaths`: invalid path → `ErrInvalidPath`; `A/x.txt` after `a/x.txt` → `ErrDuplicate`; both recorded; archive has neither.
- [ ] `TestWriterBudget`: items past `MaxSize - FooterReserve` → `ErrBudget`, `dropped: size budget`; footers present; manifest records overrun when footers exceed the reserve.
- [ ] `TestWriterItemCaps`: a 70 MiB config dump is dropped with an error; an 11 MiB text item is cut with a truncation line.
- [ ] `TestWriterLateAfterSeal`: a call blocked past `Seal` + `Drain` returns `ErrSealed`; the item is `dropped: late`; nothing of it is in the archive.
- [ ] `TestWriterDeterministic`: two writers fed the same items in different orders give identical member lists.
- [ ] `TestWriterRegularEntries`: every header is `TypeReg`, mode `0644`, under the root.
- [ ] `TestCreateFileExclusive`: existing path → error, file unchanged; new file mode `0600`.
- [ ] `TestFinalizeFailureRemovesFile`: a `Dest` failing on write → `Finalize` error, file gone; stdout `Dest` → error, `Abort` no-op.
- [ ] `TestFinalizeBudget`: a `Dest` that blocks → `Finalize` returns within `Finalize` + slack with an error.
- [ ] Implement; pass; commit `feat(bundle): writer with staging, budget, seal and finalization`.

### Task 4.1.4: Runner and manifest
**Files:** create `internal/bundle/{run.go,manifest.go}`, tests.
**Interfaces (produces):**
- `type RunOptions struct{ Workers int; Skip map[string]bool; Deadline time.Time }`
- `func Run(ctx context.Context, cs []bundle.Collector, env bundle.Env, w *Writer, o RunOptions) Manifest` (runs, commits in registration order, seals at `Deadline` or ctx end)
- `type Manifest struct{ Build view.BuildInfo; Flags []FlagValue; Context, Server string; Scope bundle.Scope; Window bundle.Window; Start, Sealed, End time.Time; Finalization string; FooterOverrun int64; Replicas []bundle.Replica; Collectors []CollectorStatus; Items []Item }`
- `type FlagValue struct{ Name string; Value *string }` (nil for name-only flags)
- `type CollectorStatus struct{ Name, Status string; Duration time.Duration; Error string; Files []string }`
- `type Item struct{ Path string; Size int64; ContentType string; CapturedAt time.Time; Source bundle.Source; Status string; Redactions int }`
- `func RecordFlags(fs *pflag.FlagSet) []FlagValue` (value only for the §5 list)
- [ ] `TestRunDeadline`: a collector blocking past the deadline → `timeout`; one finished → `ok`; manifest written.
- [ ] `TestRunPanic`: a collector panicking with a Bearer token → `failed`; the token is in no member.
- [ ] `TestRunSkip`: `Skip{"logs"}` → `skipped`, `Collect` never called.
- [ ] `TestRunCommitOrder`: collectors finishing in reverse order commit in registration order.
- [ ] `TestRunErrorPaths` (archive boundary): canaries in `Sink.Error`, a returned error, a panic and a malformed JSON body; none in `manifest.json`, `errors.txt`, `run.log`.
- [ ] `TestRecordFlags`: `-o /home/u/x.tgz`, `--kubeconfig`, `--context`, `--redact-header a,b` recorded without values (header as count 2); `--since 1h` with value.
- [ ] Implement; pass; commit `feat(bundle): runner with one deadline and a redacted manifest`.

### Task 4.1.5: Threat model and decisions
- [ ] Write `docs/bundle-threat-model.md` (§7.3): assets per class, never-requested list, redaction table, header and JWKS rules, error redaction, downstream trust statement, residual risk of derived artifacts, RBAC incl. the log Role scope, archive handling, limitations.
- [ ] Mark D-025/D-026 accepted if the owner approved. Commit `docs: bundle threat model`.

## Sub-phase 4.2 — Command, scope, inventory, cluster state

### Task 4.2.1: Command, flags, destination, exit codes
**Files:** create `internal/app/bundle.go`, `internal/app/bundle_test.go`; modify `internal/app/root.go`, `internal/app/export_test.go`.
**Interfaces (produces):**
- `type bundleTestHooks struct{ Dialer proxydump.Dialer; Scraper collect.Scraper; Logs collect.LogSource; Now func() time.Time; Check checkTestHooks }`
- `func newBundleCmd(reg *extension.Registry, build BuildInfo, extra []bundle.Collector, hooks bundleTestHooks) *cobra.Command`
- `func defaultBundleName(context string, t time.Time) string`; `func sanitizeContext(s string) string`
- `func NewRootWithBundleHooks(cfg Config, h BundleTestHooks) *cobra.Command` (export_test)
- [ ] `TestBundleOutputExists`: existing `-o` → exit 2, file unchanged, `Proxy.Requests()` empty.
- [ ] `TestBundleUnwritableDir`: exit 2 before any request.
- [ ] `TestBundleStdout`: `-o -` → stdout is a valid gzip tar; stderr has the summary.
- [ ] `TestBundleListCollectors`: prints built-in and downstream names, makes no request.
- [ ] `TestBundleUnknownSkip`: exit 2.
- [ ] `TestSanitizeContext`: `admin@prod:eu/x` → `admin_prod_eu_x`; `.x` → `_x`; `../..` → `_._..` with no `/`; 100 bytes → 64; empty → `unknown`.
- [ ] `TestBundleInterrupt`: cancel mid-run → bundle written, exit 0, collectors `timeout`.
- [ ] Implement §3; pass; commit `feat(bundle): kgwctl bundle command`.

### Task 4.2.2: Scope resolution and replica inventory
**Files:** create `internal/app/bundle_scope.go`, `internal/bundle/inventory.go`, tests.
**Interfaces (produces):**
- `type GatewaySet map[types.NamespacedName]string` (Gateway → owning install ID)
- `func resolveScope(f *Flags, ctxNamespace string, res *load.Result) (bundle.Scope, GatewaySet, []string /* warnings */, error)`
- `type ControllerSelector struct{ Install bundle.Install; Selector labels.Selector }`
- `func Inventory(ctx context.Context, kube kubernetes.Interface, s bundle.Scope, gws GatewaySet, ctrls []ControllerSelector) ([]bundle.Replica, error)`
- [ ] `TestResolveScopeDefaults` (envtest): no `-n` → context namespace; `-A` → nil; install namespace in `ReadNamespaces`, not in `Namespaces`.
- [ ] `TestResolveScopeInstalls`: two installs, no `--install` → both `detected`, one warning; `--install a/kgateway` → one `flag`; `--install x/y` undetected → exit 2; none → controller collectors `skipped: no install`.
- [ ] `TestInventoryFullName`: a 70-character Gateway with three Pods (shortened label, full-name annotation) → three proxy replicas owned by it; a Pod with the same shortened label but another annotation is excluded; a Pod without annotation matching by label → `Identity: "label"`.
- [ ] `TestInventoryStates`: not-ready, terminating, Pending and restarted Pods are kept with their state and per-container restarts, init containers included.
- [ ] `TestInventoryControllerSelector`: controller Pods match the Deployment selector; a stray `kgateway=kgateway` Pod outside the selector is excluded.
- [ ] `TestInventoryAllControllers`: a Gateway of another controller's class is excluded unless `AllControllers`.
- [ ] `TestInventoryRequests`: `Proxy.Requests()` has Pod lists only in `ReadNamespaces`.
- [ ] Implement §3.2, §8.2; pass; commit `feat(bundle): scope and replica inventory`.

### Task 4.2.3: Resources, workloads, install
**Files:** create `internal/bundle/collect/{resources.go,workloads.go,install.go}`, tests.
**Interfaces (produces):** `func Resources(reg *extension.Registry) bundle.Collector`; `func Workloads() bundle.Collector`; `func Install() bundle.Collector`.
- [ ] `resources`: Gateway API kinds, every served kind with category `kgateway` (discovered), registry kinds; per read namespace, or cluster-wide with `-A`; `Path{"resources", group|"core", kind, ns|"_cluster", name+".yaml"}`.
- [ ] `workloads`: Deployments, Pods, Services per archive namespace, and the install's controller objects; Events after `Window.Since` by the latest of `lastTimestamp`, `eventTime`, `series.lastObservedTime`.
- [ ] `install`: each install's controller Deployment; GatewayClasses.
- [ ] `TestClusterStateArchive` (envtest, archive boundary): fixtures with canaries in `last-applied-configuration`, a TrafficPolicy `x-tenant-key` modifier and an Event message; extracted paths as §4; `AssertNoCanary`.
- [ ] `TestClusterStateScope`: `-n team-a` → no `team-b` member; GatewayClass under `_cluster`; `Proxy.Requests()` has no `team-b` request and no `secrets` or `configmaps` request.
- [ ] `TestEventsWindow`: an Event with old `lastTimestamp` but recent `series.lastObservedTime` is kept; one older than `since` on all three is left out.
- [ ] Commit `feat(bundle): resource, workload and install collectors`.

### Task 4.2.4: Version and `check` collectors; manifest contract
**Files:** create `internal/bundle/collect/version.go`, `internal/app/bundle_check.go`, tests; modify `internal/app/check.go`, `docs/output.md`.
**Interfaces (produces):**
- `func Version(build view.BuildInfo, res *load.Result, d proxydump.Dialer) bundle.Collector` (writes `cluster/version.json` and `proxies/<ns>/<pod>/server_info.json`)
- `type VersionFile struct{ Client view.BuildInfo; Server *version.Info; Installs []view.InstallVersion /* image tags */; Observed []ObservedEnvoy }`; `type ObservedEnvoy struct{ Namespace, Pod, Version, Status string }`
- `func checkOnce(ctx context.Context, f *Flags, rc *rest.Config, reg *extension.Registry, build BuildInfo, now func() time.Time, hooks checkTestHooks) (view.Check, error)` (extracted; `newCheckCmd` calls it)
- `func newCheckCollector(f *Flags, rc *rest.Config, reg *extension.Registry, build BuildInfo, now func() time.Time, hooks checkTestHooks) bundle.Collector`
- [ ] `TestVersionFile`: server version from discovery; image tags under `installs`; two replicas with different `/server_info` versions → two `observed` entries by pod; a denied replica → `status: denied`, no version.
- [ ] `TestCheckCollectorMatchesCheck` (envtest): `check/check.json` equals `kgwctl check -o json` on the same fixture apart from timestamps; existing `check` tests unchanged.
- [ ] docs/output.md: `BundleManifest` section; `TestOutputDocMatchesSchema` covers it.
- [ ] Commit `feat(bundle): version and check collectors; BundleManifest contract`.

## Sub-phase 4.3 — Logs, metrics, Envoy

### Task 4.3.1: Logs
**Files:** create `internal/bundle/collect/logs.go`, tests.
**Interfaces (produces):**
- `type LogSource interface{ Stream(ctx context.Context, pod types.NamespacedName, opts *corev1.PodLogOptions) (io.ReadCloser, error) }`
- `func KubeLogSource(kube kubernetes.Interface) LogSource`; `func Logs(src LogSource) bundle.Collector`
- [ ] `TestKubeLogSourceQuery` (HTTP boundary): the request is `GET /api/v1/namespaces/<ns>/pods/<pod>/log` with `container`, `sinceTime` = `Window.Since` (same for every stream), `timestamps=true`, `tailLines`, `limitBytes`, and `previous=true` only for restarted containers.
- [ ] `TestLogsPerReplica`: every inventory container incl. init; `.previous.log` only when `Restarts > 0`.
- [ ] `TestLogsCancelMidStream` (archive boundary): a stream cut by the deadline keeps full lines, a truncation line, no partial line, and an open PEM block redacted; collector `timeout`.
- [ ] `TestLogsDenied`: 403 → one error line per namespace naming `pods/log` and `kgwctl-bundle-logs`.
- [ ] Commit `feat(bundle): log collector`.

### Task 4.3.2: Controller and proxy metrics
**Files:** create `internal/bundle/collect/{metrics.go,scraper.go}`, tests.
**Interfaces (produces):**
- `type Scraper interface{ Scrape(ctx context.Context, pod *corev1.Pod, port int32) (io.ReadCloser, error) }`
- `type HTTPStatusError struct{ Code int }`
- `func StatusScraper(cfg *rest.Config) (Scraper, error)` (Snellius `ctrlmetrics.PodProxyScraper` over a clientset whose transport turns a non-200 into `*HTTPStatusError` without reading the body)
- `func ControllerMetrics(s Scraper) bundle.Collector` (port from the install, default 9092); `func ProxyMetrics(s Scraper) bundle.Collector` (port 9091)
- [ ] `TestStatusScraperRoute` (HTTP boundary): GET to `/api/v1/namespaces/<ns>/pods/http:<pod>:9091/proxy/metrics`; a 302 is not followed; a 403 is `*HTTPStatusError{403}`; the 403 body text is in no error.
- [ ] `TestMetricsStatuses`: per replica `ok`, `denied` (403), `disabled` (no 9091 container port), `failed` (500), `skipped: not running`.
- [ ] `TestMetricsArchive` (archive boundary): an HTML body is dropped with an error; a Bearer label value is redacted; `-n team-a` drops `team-b` samples.
- [ ] Commit `feat(bundle): controller and proxy metrics per replica`.

### Task 4.3.3: `proxydump` operations (transport)
**Files:** modify `internal/proxydump/fetch.go`, `fetch_test.go`, `spdy_test.go`.
**Interfaces (produces):**
- `type Op int` with `OpConfigDump`, `OpConfigDumpEDS`, `OpStats`, `OpServerInfo`, `OpCerts`, `OpClusters`; `func (o Op) String() string`
- `func FetchOp(ctx context.Context, d Dialer, pod types.NamespacedName, op Op, now func() time.Time) (*Capture, error)`
- `Fetch(ctx, d, pod, now)` = `FetchOp(ctx, d, pod, OpConfigDump, now)`; `Capture` gains `Op Op`.
- [ ] `TestFetchOpTargets` (SPDY boundary, `newSPDYPod`): each Op sends exactly its §6.3 request line on port 19000; `OpConfigDumpEDS` sends `/config_dump?include_eds`.
- [ ] `TestFetchOpUnknown`: `Op(99)` and `Op(-1)` fail before `Dial` (dial counter 0).
- [ ] `TestFetchBareConfigDump`: `Fetch` still sends `GET /config_dump`; existing `proxy dump` and `proxy routes` tests pass unchanged.
- [ ] `TestFetchOpRedirect`: a 302 is refused for every Op.
- [ ] Commit `feat(proxydump): allowlisted admin operations`.

### Task 4.3.4: Envoy capture
**Files:** create `internal/bundle/collect/proxyadmin.go`, tests.
**Interfaces (produces):** `func ProxyAdmin(d proxydump.Dialer, noEndpoints bool) bundle.Collector` (per proxy replica: `config-dump-eds` or `config-dump`, `stats`, `certs`, `clusters` unless `noEndpoints`).
- [ ] `TestProxyAdminPerReplica`: three replicas, one with a failing dial → two full directories, one with `failed` items; a not-running replica `skipped`.
- [ ] `TestProxyAdminNoEndpoints`: Ops seen are `config-dump`, `stats`, `certs`; no `clusters`.
- [ ] `TestProxyAdminArchive` (archive boundary): a config dump with the row 9–11 canaries leaves none in the archive; Envoy `[redacted]` stays.
- [ ] `TestProxyAdminDivergentReplicas`: two replicas with different `version_text` and listener states keep both, each under its own pod with matching `Source.UID`; the extracted `config_dump.json` loads in `proxy routes --file`.
- [ ] Commit `feat(bundle): per-replica Envoy captures`.

### Task 4.3.5: RBAC and the command-level partial run
**Files:** modify `docs/rbac.yaml`, README permissions line; add to `internal/app/bundle_test.go`.
- [ ] docs/rbac.yaml: optional Role `kgwctl-bundle-logs` (`get pods/log`), noting it reads every pod's logs in its namespace.
- [ ] `TestBundleReaderOnly` (envtest): `pods/log` and `pods/portforward` 403 → exit 0; `errors.txt` names each grant once with its Role; resources, workloads, install, version and `check` present.
- [ ] `TestBundleMixedOutcomes`: one replica ok, one denied, one timing out → exit 0; manifest item statuses `ok`, `denied`, `timeout`.
- [ ] `TestBundleNoForbiddenRequests`: `Proxy.Requests()` for a full run has no `secrets`, `configmaps` or out-of-scope namespace.
- [ ] Commit `feat(bundle): optional log Role; partial bundles on restricted RBAC`.

## Sub-phase 4.4 — Extension, docs, live check

### Task 4.4.1: `WithBundleCollectors`
**Files:** modify `pkg/cli/cli.go`, `pkg/cli/cli_test.go`, `examples/downstream/main.go`.
**Interfaces (produces):** `func WithBundleCollectors(c ...bundle.Collector) Option`; invalid or duplicate names fail `NewRootCommand` like registry errors.
- [ ] `TestWithBundleCollectors`: the example collector is in `--list-collectors` and in the archive under its path, redacted; a name clash with `logs` is an error.
- [ ] `TestDownstreamCanaries` (archive boundary): the example writes a canary through `JSON` and `Text`; none in the archive.
- [ ] Commit `feat(bundle): bundle collectors for downstream builds`.

### Task 4.4.2: README
- [ ] README `bundle` section: what is collected, what is never requested, the residual risk of derived artifacts, redaction limits, "review before sharing", RBAC. Commit `docs: kgwctl bundle`.

### Task 4.4.3: Live check
- [ ] `hack/live-check.sh bundle`: apply canaries (a TrafficPolicy `headerModifiers.secretRef` to a Secret with a pattern-free value, an Event, a GatewayParameters env, an inline JWKS with `k`), run a full bundle as admin and as reader-only, extract, scan (raw, base64, gzip); assert per proxy replica `config_dump.json`, `stats.txt` with `version_text`, and a `metrics.txt` that parses as Prometheus; reader-only exits 0 with two grant lines.
- [ ] Commit `test(live-check): bundle step`.

## Phase close prerequisites (not done by the implementer)
Codex phase review; owner try-out on kind; program file note on the preserved convergence evidence.
