# Phase 4 — Support bundle: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `kgwctl bundle` writes one redacted `tar.gz` with resources, workloads, install metadata, versions, `check`, logs, metrics and per-replica Envoy captures, under one deadline, with a manifest, `errors.txt` and `redactions.json`.

**Architecture:** `pkg/bundle` is the public contract (`Collector`, `Env`, `Sink`, `Path`). `internal/redact` holds pure redactors per content type. `internal/bundle` stages and redacts every Sink item, commits through one writer goroutine in a fixed order, seals at the deadline and finalizes; it also owns the request guard and the replica inventory. Collectors in `internal/bundle/collect` reuse `load`, the Snellius pods/proxy scraper and `proxydump` operations; the `check` collector lives in `internal/app` and calls the `kgwctl check` one-shot path.

**Tech Stack:** Go 1.26, cobra, client-go v0.36.x, `archive/tar`, `compress/gzip`, `prometheus/common/expfmt` (already in the graph), Snellius v0.8.2, xdsview v0.4.3. No new modules.

**Spec:** [`design.md`](design.md) (r6). Read it first. §n refers to it.

## Global Constraints

- Every bundle-owned client is built from one `rest.Config` wrapped by the request guard: no request for core `secrets` or `configmaps` leaves the process (§6.1), and every non-2xx response body is replaced by a canonical `Status` with code and reason only (a pods/proxy "connection refused" is classified first, from at most 4 KiB), before client-go or Snellius turns it into an error string (§7.1).
- No controller admin request (§6.2). Envoy admin only through `proxydump.Op` (§6.3); no API takes a path.
- Every archive member goes through a `bundle.Sink` method: `Object`, `ConfigDump`, `JSON`, `Prometheus`, `Text`. Path components come from `bundle` constructors (§8.4).
- Header values in the §7 structures redacted whatever their name; debug header lines by allowlist; JWKS rebuilt from a typed projection; Prometheus validated (§7).
- Remote errors: HTTP status and Kubernetes reason/code only, never bodies. Other error text: redacted and cut at 512 bytes (§7.1).
- Marker `[redacted]`; no hashes or lengths; `redactions.json` holds rule, location (map keys as `[key]`) and count only.
- Item caps: logs 10 MiB, config dump 64 MiB, metrics 8 MiB, others 16 MiB. `--max-size` default `512Mi`, minimum `32Mi`, covers items and footers; items get `--max-size` minus the footer reserve, applied in commit order. Footer caps (their sum is the reserve) at the default: manifest 4, redactions 4, errors 1, run.log 1 MiB, scaled by `min(1, (max/4)/10 MiB)`. Staging RAM 64 MiB, then a redacted spool (`0700` dir, `0600` file) bounded by `--max-size` and always removed. Finalization is bounded by size, not by a timer (§8.3).
- Identity: label-only proxy Pods are `unverified` and not captured; every capture re-reads its Pod (`changed`, `gone`, `reread-failed`) (§8.2).
- Grant lines in `errors.txt`: one per grant and namespace; per-item statuses stay (§6).
- Output `kgwctl-bundle-<UTC ts>.tar.gz`: `O_CREATE|O_EXCL`, `0600`, removed on fatal failure; regular entries only, `0644` inside. No context name in any file or member name.
- Deadlines: `--timeout` (default `5m`) for collection; `--request-timeout` (default 30s) per item; Ctrl-C acts as the deadline.
- Scope: default all namespaces; `-n` narrows resources, workloads, Events, logs and proxies (§3.2).
- Ownership and discovery scope on by default (§3.2): `view.Filter` plus each install's Snellius `install.Scope`; bundle visibility of routes follows spec `parentRefs` (Snellius status-only delegation affects `check` findings only); `ScopeUnknown` filters nothing and is recorded `unknown`; cluster-scoped kinds never scope-filtered; `--all-controllers` turns both off. No second implementation of the ownership rules.
- Logs: one `T0`; `SinceTime`, `Timestamps: true`, `TailLines`, `LimitBytes` = cap + 64 KiB; local cap; `.previous` only when `restartCount > 0`; init containers included.
- Workers: 8. Commit order: collector registration order, then path.
- Exit codes: 0 when a bundle is written (also partial), 2 when none is (§3.1).
- RBAC: reader ClusterRole unchanged; `pods/log` in a new optional Role `kgwctl-bundle-logs`; `pods/portforward` in the existing optional Role.
- No vendor or edition names (D-004). No new modules.
- Code comments 1–2 lines; Go doc comments on exported identifiers. Archive bytes to stdout only with `-o -`; progress to stderr.
- Every `kubectl`/`helm` call in scripts passes `--context`/`--kube-context`; kind clusters use their own `--kubeconfig`.

## Review Focus

1. **Pattern-free credentials in header structures** (TrafficPolicy `secretRef` header, ext-auth `headersToAdd`, tunnel headers, `initialMetadata`, Envoy debug lines). Expects `[redacted]` everywhere, names kept. Tasks 4.1.3, 4.1.4.
2. **A downstream kind resolving to `secrets`, and a denial with a pattern-free body.** Expects no Secret request on the wire, an error line, and no body text in `check.json` or `errors.txt`. Tasks 4.2.2, 4.2.5.
3. **A reader-only user.** Expects exit 0 with resources, workloads, install, version without observed Envoy versions, and `check`; per-item `denied` statuses and one grant line per grant and namespace. Task 4.3.4.
4. **The deadline hits mid-log-stream.** Expects the log's complete lines and a truncation line, the collector `timeout`, later results counted as `late`, footers written. Tasks 4.1.4, 4.1.5, 4.3.1.
5. **A Gateway with a 63+ character name and 3 replicas, one CrashLooping, one replaced mid-run.** Expects attribution by annotation, one replica with `.previous` logs and `failed` admin items, and `identity: changed` on the replaced one. Tasks 4.2.2, 4.3.3.
6. **`-n team-a`.** Expects the exact §3.2 request set, with bootstrap's cluster-wide lists and watches allowed; no `team-b` resource or workload member. Tasks 4.2.4, 4.3.4.
7. **The output exists, or the destination fails mid-write.** Expects exit 2; an existing file untouched; a failed file removed. Tasks 4.1.4, 4.2.1.
8. **A second Gateway API controller, and a discovery selector that excludes `team-b`.** Expects no foreign GatewayClass, Gateway or route, and no workloads from namespaces only the other controller uses; `team-b` named in the manifest, its route counted under `scope` and named by `check` (KGWUI-004); an ambiguous Gateway keeps its proxies; all present with `--all-controllers`, never past `-n`. Tasks 4.2.2, 4.2.4.

## Test harness

- `internal/redact`: pure table tests; corpus in `internal/redact/testdata/canary/`, one directory per design §10 row.
- `internal/bundle/bundletest` (new, test-only):
  - `Extract(t *testing.T, r io.Reader) Archive` fails the test on a duplicate member name, a non-regular entry or a path outside the root;
  - `AssertNoCanary(t *testing.T, a Archive, canaries []string)` scans member names, raw bytes, base64-decoded strings and nested gzip members;
  - `Corpus(t *testing.T) []Fixture` loads every corpus row with its Sink method.
- Redaction boundary: production `Writer` into a buffer, then `Extract`. Corpus-only tests do not count as archive proof.
- Transport boundary: client-go and the Snellius scraper against an `httptest` API server (`rest.Config.Host` pointed at it) that records method, path and query. Port-forward against `newSPDYPod` and a new `newSPDYPodWith(t *testing.T, h http.Handler) *spdyPod` for configurable responses in `internal/proxydump/spdy_test.go`.
- Command boundary: envtest (`internal/testenv`). Denials use the production adapters (`KubeLogSource`, `StatusScraper`, `NewPortForwarder`) behind `Proxy.Rule(Match{Resource: "pods", Subresource: "log"|"proxy"|"portforward"}, Action{Status: 403})` and assert `RuleHits.N() > 0`. `testenv.Action` gains `Body []byte` so a rule can return a pattern-free denial body (Task 4.2.5). Success and timeout bodies need a kubelet, so they use `bundleTestHooks` seams. `Proxy.Requests()` is the request recorder.
- Real kubelet paths run only in the live check (Task 4.4.3).

## Sub-phases

| Sub-phase | Delivers | Tasks |
|---|---|---|
| 4.1 | Contract, manifest schema, redactors, writer, runner, threat model | 4.1.1–4.1.6 |
| 4.2 | Command, guard, scope, inventory, Envoy transport, cluster-state collectors | 4.2.1–4.2.5 |
| 4.3 | Logs, metrics, Envoy capture, RBAC and partial runs | 4.3.1–4.3.4 |
| 4.4 | Extension point, README, live check | 4.4.1–4.4.3 |

Each task builds and passes on its own; no task consumes a type a later task introduces. Each sub-phase ends with `make test` (race), `go vet ./...`, gofmt clean, the deps check, and `go mod tidy -diff` empty.

---

## Sub-phase 4.1 — Contract, redaction, writer

### Task 4.1.1: `pkg/bundle` contract
**Files:** create `pkg/bundle/{bundle.go,path.go,status.go}`, `pkg/bundle/path_test.go`.
**Interfaces (produces):**
- `type Path []string`; `func (p Path) String() string`
- Component constructors, the only way to build components:
  - `func Name(kind NameKind, s string) (string, error)`, `NameKind` = `Namespace`, `Object`, `Container`, `Group`, `Kind`, each validated by its Kubernetes rule;
  - `func File(name string, ext Ext) (string, error)`, `Ext` = `YAML`, `JSON`, `Log`, `PreviousLog`, `Text`;
  - constants `Cluster = "_cluster"`, `Core = "core"` and the fixed directory and file names of design §4.
- `func ValidatePath(p Path) error` (§8.4; reserved root names rejected; duplicates are the writer's job).
- `type Op int` (codes for `Source.Operation`: `OpList`, `OpLog`, `OpPreviousLog`, `OpMetrics`, `OpConfigDump`, `OpConfigDumpEDS`, `OpStats`, `OpServerInfo`, `OpCerts`, `OpClusters`, `OpCheck`, `OpCustom`); `func (o Op) String() string`
- `type CaptureIdentity string` (`""` for non-Pod items, `IdentityVerified`, `IdentityChanged`, `IdentityGone`, `IdentityRereadFailed`)
- `type Source struct{ Namespace, Pod string; UID types.UID; Container string; Operation Op; Identity CaptureIdentity }` (every Sink method, `Error` included, takes a `Source`)
- `type Sink interface { Object(p Path, u *unstructured.Unstructured, src Source) error; ConfigDump(p Path, r io.Reader, src Source) error; JSON(p Path, r io.Reader, src Source) error; Prometheus(p Path, r io.Reader, src Source) error; Text(p Path, r io.Reader, src Source) error; Error(item Path, src Source, err error); Exclude(gk schema.GroupKind, reason ExcludeReason) error }` (`Exclude` counts a left-out object and returns `ErrSealed` after the seal, as the other error-returning methods do; an empty group is core and is recorded as `core`; a non-empty group must pass `Name(Group)` and the kind `Name(Kind)`, else `ErrInvalidPath`)
- `type Collector interface { Name() string; Collect(ctx context.Context, env Env, s Sink) error }`
- `type Env struct{ Kube kubernetes.Interface; Dynamic dynamic.Interface; Discovery discovery.DiscoveryInterface; Scope Scope; Window Window; Replicas []Replica; Now func() time.Time; RequestTimeout time.Duration; Keep func(obj *unstructured.Unstructured) ExcludeReason }` (clients are guarded; `Keep` is the §3.2 ownership and scope filter, `ExcludeNone` to keep; always `ExcludeNone` with `AllControllers`)
- `type Scope struct{ Namespace string /* "": all */; Installs []Install; AllControllers bool; WorkloadNamespaces []string /* nil: all */; InstallsOmitted, WorkloadNamespacesOmitted int }`; `func (s Scope) Namespaces() []string` (nil for all; else the namespace plus install namespaces)
- `type Install struct{ Namespace, Deployment, Chosen string /* "flag"|"detected" */; MetricsPort int32; Discovery DiscoveryScope }`
- `type DiscoveryScope struct{ Mode string /* "all"|"selectors"|"unknown"|"namespaces-unreadable" */; Selectors []string /* metav1.FormatLabelSelector of each parsed selector, at most 64 */; SelectorsOmitted int; Source, Reason string; Unwatched []string /* namespace names, bounded */; UnwatchedOmitted int }`
- `type ExcludeReason string` (`ExcludeNone` = "", `ExcludeOwnership`, `ExcludeScope`)
- `type Window struct{ Start, Since time.Time; Tail int64 }`
- `type Role string` (`RoleController`, `RoleProxy`); `type Identity string` (`IdentityAnnotation`, `IdentitySelector`, `IdentityUnverified`); `type Container struct{ Name string; Init bool; Restarts int32 }`
- `type Replica struct{ Namespace, Pod string; UID types.UID; Role Role; Owner string; Identity Identity; Ready, Terminating bool; Phase corev1.PodPhase; Containers []Container; StartTime time.Time; MetricsPort int32; MetricsPortDeclared bool }`
- `type CollectorStatus string` (`ok`, `partial`, `failed`, `skipped`, `timeout`); `type ItemStatus string` (`ok`, `skipped`, `denied`, `disabled`, `failed`, `timeout`, `dropped`)
- `var ErrSealed, ErrBudget, ErrDuplicate, ErrInvalidPath, ErrForbiddenKind` (Sink sentinels)
- `func ValidName(name string) error`: collector names `^[a-z0-9]+(-[a-z0-9]+)*$`.
- [ ] `TestPathConstructors`: `Name(Group, "gateway.networking.k8s.io")`, `Name(Kind, "HTTPRoute")`, `File("gw", YAML)` succeed; `Name(Object, "a/b")`, `Name(Namespace, "Team")`, `Name(Container, "x_y")` and a 254-byte object name fail.
- [ ] `TestValidatePath`: rejects empty path, `.`, `..`, `.hidden`, `-x`, 9 components, and `Path{"manifest.json"}`.
- [ ] `TestValidName`: accepts `logs`, `proxy-admin`; rejects `Logs`, `a_b`, `-x`, empty.
- [ ] Implement; pass; commit `feat(bundle): public collector contract`.

### Task 4.1.2: Manifest schema
**Files:** create `internal/bundle/manifest.go`, `internal/bundle/manifest_doc_test.go`; modify `docs/output.md`.
**Interfaces (produces):**
- `type Manifest struct{ Build BuildInfo; Context, Server string; Flags []FlagValue; Scope bundle.Scope; Window bundle.Window; Start, Sealed, End time.Time; Finalization string; Replicas []bundle.Replica; Collectors []CollectorEntry; Items []Item; Excluded []Exclusion; ReplicasOmitted, CollectorsOmitted, ItemsOmitted int; Truncated bool }`
- JSON fields (camelCase, omit-empty, RFC3339): `build`, `context`, `server`, `flags`, `scope`, `window`, `start`, `sealed`, `end`, `finalization`, `replicas`, `collectors`, `items`, `excluded`, `replicasOmitted`, `collectorsOmitted`, `itemsOmitted`, `truncated`.
- `func Bound(m *Manifest, max int64)` (every list in the manifest, including `Scope.Installs`, `Scope.WorkloadNamespaces`, each install's selectors and unwatched names, and `Collectors`, has an omitted count. Drop order: items, replicas, workload namespaces, unwatched names, selectors, installs, collectors. Each selector string is cut at 256 bytes. `Excluded` holds at most 1024 entries; overflow is summed into one `other` entry per reason, so it is never cut. Build strings are cut at 256 bytes each. If the manifest still does not fit, `Bound` writes a minimal one: build, flags, times, finalization, `excluded`, the omitted counts, `truncated: true`, and the scope's namespace and install IDs (`<ns>/<name>`, at most 64). Every field of the minimal manifest is capped, so it stays under 64 KiB, below the smallest manifest cap (3.2 MiB at `--max-size 32Mi`).)
- `type BuildInfo struct{ Version, Commit, Date string }` (filled from `view.BuildInfo` by the caller; `bundle` does not import `view`)
- `type FlagValue struct{ Name string; Value *string }` (`name`, `value`; nil value for name-only flags)
- `type CollectorEntry struct{ Name string; Status bundle.CollectorStatus; Reason string /* e.g. "flag", "no install" */; DurationMs int64; Error string; Late int }`
- `type Exclusion struct{ Kind string /* group/kind */; Reason bundle.ExcludeReason; Count int }` (`kind`, `reason`, `count`)
- `type Item struct{ Path string; Size int64; ContentType string; CapturedAt time.Time; Source bundle.Source; Status bundle.ItemStatus; Reason string; Redactions int }` (the identity outcome is `source.identity`)
- `const ManifestKind = "BundleManifest"`; `func WriteManifest(w io.Writer, m Manifest) error` (via `print.Envelope`)
- [ ] `TestBundleManifestDocMatchesSchema` (in `internal/bundle`, so it can import the type): the `BundleManifest` section of docs/output.md lists exactly the JSON fields and both status sets.
- [ ] `TestManifestEnvelope`: output has `schemaVersion`, `kind: BundleManifest`, `item`; times RFC3339; empty fields omitted.
- [ ] `TestManifestBound`: 100 000 items, 20 000 replicas and 50 000 unwatched namespace names with a 1 MiB limit → the encoding fits; `itemsOmitted`, `replicasOmitted` and the scope's `unwatchedOmitted` > 0; `excluded` is never cut; an install with 100 selectors records 64 and `selectorsOmitted` 36.
- [ ] Implement; pass; commit `feat(bundle): BundleManifest schema`.

### Task 4.1.3: Redactors and canary corpus
**Files:** create `internal/redact/{redact.go,object.go,configdump.go,headers.go,jwks.go,prometheus.go,text.go,errors.go,rules.go}`, tests, `testdata/canary/`.
**Interfaces (produces):**
- `type Rule string`: `RuleManagedFields`, `RuleLastApplied`, `RuleHeaderValue`, `RuleDebugHeader`, `RuleHeaderPair`, `RuleJWKS`, `RuleKeyName`, `RuleField`, `RuleInline`, `RuleLua`, `RulePattern`, `RulePEMTail`.
- `type Finding struct{ Rule Rule; Location string; Count int }` (field path with map keys as `[key]`, or `line:<n>`)
- `type Options struct{ ExtraHeaders []string }`
- `func Object(u *unstructured.Unstructured, o Options) ([]Finding, error)` (in place; `ErrForbiddenKind` for Secret and ConfigMap)
- `func ConfigDump(body []byte, o Options) ([]byte, []Finding, error)`
- `func JSON(body []byte, o Options) ([]byte, []Finding, error)` (invalid JSON → error, no output)
- `func Prometheus(body []byte, o Options) ([]byte, []Finding, error)` (not text exposition → error)
- `type LineWriter interface{ WriteLine(line []byte) error }`
- `func Text(r io.Reader, w LineWriter, max int64, o Options) (TextResult, error)`; `type TextResult struct{ Findings []Finding; Truncated bool; Reason string }` (emits redacted complete lines as read; the writer adds the truncation marker)
- `func JWKS(s string) (string, []Finding)` (typed projection, design §7)
- `func RemoteError(err error) string` (status and reason/code only); `func ErrorText(err error, server string) string` (`view.Redactor`, pattern pass, cut at 512 bytes)
- [ ] Corpus: design §10 rows 1–21, each value `kgwctl-canary-<row>-<loc>` or a secret-shaped variant; rows 10 and 11 use values with no pattern match.
- [ ] `TestCorpusNoLeaks`: every fixture through its redactor; no canary in output; each planted location in `[]Finding`.
- [ ] `TestHeaderValueAnyName`: values in every design §7 header structure become `[redacted]` for `x-tenant-key`, `x-foo` and `Authorization`; names unchanged; the `headersToAdd` location ends in `headersToAdd[key]`.
- [ ] `TestDebugHeaderLines`: `'x-tenant-key', 'v'` and `'authorization', 'Bearer v'` redacted; `':method', 'GET'` and `'x-envoy-upstream-service-time', '3'` kept; `':path', '/a?token=v'` keeps `/a` and redacts the token.
- [ ] `TestJWKSProjection`: RSA, EC and OKP keys keep only their members, rebuilt as `{"keys":[...]}`; `k`, `d`, an extra member and an extra top-level member are gone; a nested object in `n`, a number in `kid`, `kty: oct`, valid JSON of the wrong shape and invalid JSON give a whole `[redacted]`.
- [ ] `TestPrometheus`: a valid body keeps names and labels; a JWT in a label value and in `# HELP` is redacted; an HTML body returns an error.
- [ ] `TestTextCutAndCancel`: a reader failing mid-line drops the partial line; an open PEM block is redacted to the end; the cap cuts at a line boundary; `Truncated` and `Reason` set.
- [ ] `TestRemoteError`: a `*apierrors.StatusError` whose message holds a canary gives `403 Forbidden (Forbidden)` without it; a client-go log-stream error holding a body gives the status only.
- [ ] `TestErrorText`: URL, server host, Bearer token and `sk-` key removed; 2 KiB input cut to 512 bytes.
- [ ] `TestObjectForbiddenKinds`: Secret and ConfigMap → `ErrForbiddenKind`.
- [ ] `TestKeepsUseful`: names, namespaces, hostnames, header names, JWKS public members, cert SANs and Envoy's own `[redacted]` survive; redacted YAML/JSON re-parses with the same key set.
- [ ] Run, fail, implement design §7, pass. Commit `feat(redact): redactors per content type with a canary corpus`.

### Task 4.1.4: Writer: staging, budget, seal, finalization
**Files:** create `internal/bundle/{writer.go,dest.go,stage.go}`, tests.
**Interfaces (produces):**
- `type Limits struct{ MaxSize, RAM int64; Log, ConfigDump, Metrics, Other int64; Manifest, Redactions, Errors, RunLog int64 }`; `func DefaultLimits(maxSize int64) (Limits, error)` (error below `32Mi`; footer caps scaled per design §8.3)
- `type Spool struct{ ... }`; `func NewSpool(dir string, ram, max int64) (*Spool, error)` (`0700` temp dir under `dir`, `0600` files, redacted bytes only); `func (s *Spool) Remove() error`
- `type Dest interface{ io.Writer; Close() error; Abort() error }` (`Abort` removes a file, no-op on stdout)
- `func CreateFile(path string) (Dest, error)` (`O_CREATE|O_EXCL|O_WRONLY`, `0600`); `func Stdout(w io.Writer) Dest`
- `func DefaultName(t time.Time) string` (`kgwctl-bundle-<YYYYMMDDTHHMMSSZ>`)
- `func NewWriter(d Dest, sp *Spool, root string, l Limits, ro redact.Options, server string, now func() time.Time) *Writer`
- `func (w *Writer) Sink(collector string) bundle.Sink`
- `func (w *Writer) Seal()` (freezes staged state, appends truncation markers to open text items, samples in-flight calls per collector as late; freezes the `Exclude` counts, which the runner reads with `Excluded()` and copies into `Manifest.Excluded` at finalization; later calls return `ErrSealed`)
- `func (w *Writer) Finalize(order []string, m Manifest) error` (commits in `order` then path, applies the budget, writes footers within their caps, closes, removes the spool; on error `Abort`; no timer)
- `func (w *Writer) Items() []Item`; `func (w *Writer) Late() map[string]int`; `func (w *Writer) Excluded() []Exclusion` (snapshot frozen at `Seal`, sorted by kind then reason)
- [ ] `TestWriterCorpusReplay` (archive boundary): every `bundletest.Corpus` row that is a Sink input, through the matching Sink method or `Error`; `Extract` + `AssertNoCanary` over all members and names, footers included. Flag recording, registry resolution and transport denials are gated in Tasks 4.2.5 and 4.3.x.
- [ ] `TestWriterBudgetOrder`: the same items staged in two different orders keep the same set under `MaxSize` minus the footer reserve; the rest `dropped: size budget`; the whole archive's uncompressed tar content is ≤ `MaxSize`.
- [ ] `TestWriterFooterCaps`: 100 000 items → `manifest.json` ≤ 4 MiB with `itemsOmitted` > 0; `errors.txt` ≤ 1 MiB with a marker line; at `--max-size 32Mi` the footers total ≤ 8 MiB.
- [ ] `TestDefaultLimitsMinimum`: `31Mi` → error; `32Mi` → ok.
- [ ] `TestSpool`: past 64 MiB RAM, bytes go to a `0600` file in a `0700` dir and contain no canary; past `MaxSize` → `dropped: staging`; the dir is gone after success, after cancel and after a failed `Finalize`.
- [ ] `TestWriterItemCaps`: a 70 MiB config dump is dropped with an error; an 11 MiB text item is cut with a truncation line.
- [ ] `TestWriterSealTextKeepsLines`: a text item mid-stream at `Seal` keeps its complete lines and one writer-appended truncation line.
- [ ] `TestWriterLateSampledAtSeal`: two calls in flight at `Seal` → `Late()["c"] == 2`; a call starting after `Seal` → `ErrSealed`, count unchanged; neither in the archive.
- [ ] `TestWriterDuplicates`: collector `a` writes `x/f.txt`, collector `b` writes `X/F.txt` → `a`'s wins, `b`'s rejected and recorded; a write to `errors.txt` → `ErrInvalidPath`.
- [ ] `TestWriterRegularEntries`: every header `TypeReg`, mode `0644`, under the root.
- [ ] `TestCreateFileExclusive`: existing path → error, file unchanged; new file mode `0600`.
- [ ] `TestFinalizeFailureRemovesFile`: a `Dest` failing on write → error, file gone; stdout `Dest` → error, `Abort` no-op.
- [ ] `TestFinalizeBoundedBySize`: `Finalize` writes at most `MaxSize` of uncompressed tar content, footers included; there is no finalization timeout to test.
- [ ] Implement; pass; commit `feat(bundle): writer with staging, budget, seal and finalization`.

### Task 4.1.5: Runner
**Files:** create `internal/bundle/{run.go,flags.go}`, tests.
**Interfaces (produces):**
- `type RunOptions struct{ Workers int; Skip map[string]bool; Deadline time.Time }`
- `func Run(ctx context.Context, cs []bundle.Collector, env bundle.Env, w *Writer, m Manifest, o RunOptions) (Manifest, error)` (seals at `Deadline` or ctx end, finalizes in registration order, returns the commit error)
- `func RecordFlags(fs *pflag.FlagSet) []FlagValue` (values only for the design §5 list)
- [ ] `TestRunDeadline`: a collector blocking past the deadline → `timeout`; one finished → `ok`; manifest written.
- [ ] `TestRunPanic`: a collector panicking with a Bearer token → `failed`; the token is in no member.
- [ ] `TestRunSkip`: `Skip{"logs"}` → `skipped`, `Collect` never called.
- [ ] `TestRunCommitOrder`: collectors finishing in reverse order appear in registration order.
- [ ] `TestRunCommitError`: a failing `Dest` → `Run` returns the error.
- [ ] `TestRunErrorCanaries` (archive boundary): canaries in a returned error, a panic, a malformed JSON body and a 2 KiB error; none in any member. The full metadata set (context, flags, `-o`) waits for the command (Task 4.2.5).
- [ ] `TestRecordFlags`: `-n`, `--install`, `--skip`, `-o`, `--kubeconfig`, `--context`, `--redact-header` without values; `--since 1h`, `--tail 10`, `-A` with values.
- [ ] Implement; pass; commit `feat(bundle): runner with one deadline`.

### Task 4.1.6: Threat model and decisions
- [ ] Write `docs/bundle-threat-model.md` (design §7.3): assets per class, never-requested list and the guard, redaction rules, error and metadata handling, downstream trust, residual risk of derived artifacts and of `-n`, RBAC incl. the log Role scope, archive handling, limitations.
- [ ] Mark D-025/D-026 accepted if the owner approved. Commit `docs: bundle threat model`.

## Sub-phase 4.2 — Command, scope, transport, cluster state

### Task 4.2.1: Command, flags, destination, exit codes
**Files:** create `internal/app/bundle.go`, `internal/app/bundle_test.go`; modify `internal/app/root.go`, `internal/app/export_test.go`.
**Interfaces (produces):**
- `type bundleTestHooks struct{ Now func() time.Time; Check checkTestHooks }` (later tasks add fields)
- `func newBundleCmd(reg *extension.Registry, build BuildInfo, extra []bundle.Collector, hooks bundleTestHooks) *cobra.Command`
- `func NewRootWithBundleHooks(cfg Config, h BundleTestHooks) *cobra.Command` (export_test)
- [ ] `TestBundleOutputExists`: existing `-o` → exit 2, file unchanged, `Proxy.Requests()` empty.
- [ ] `TestBundleUnwritableDir`: exit 2 before any request.
- [ ] `TestBundleDefaultName`: file and archive root are `kgwctl-bundle-<ts>`; the context name is in no member name.
- [ ] `TestBundleStdout`: `-o -` → stdout is a valid gzip tar; stderr has the summary.
- [ ] `TestBundleListCollectors`: prints built-in and downstream names, makes no request.
- [ ] `TestBundleUnknownSkip`: exit 2.
- [ ] `TestBundleInterrupt`: cancel mid-run → bundle written, exit 0, running collectors `timeout`; no spool directory left.
- [ ] `TestBundleMinMaxSize`: `--max-size 16Mi` → exit 2 before any request.
- [ ] Implement design §3; pass; commit `feat(bundle): kgwctl bundle command`.

### Task 4.2.2: Request guard, scope and inventory
**Files:** create `internal/bundle/{guard.go,inventory.go}`, `internal/app/bundle_scope.go`, tests.
**Interfaces (produces):**
- `func Guard(cfg *rest.Config) *rest.Config` (copy with a `WrapTransport` that refuses core `secrets`/`configmaps` with `ErrForbiddenKind`, unsent; for a pods/proxy 502/503 it first reads at most 4 KiB of the body for `connect: connection refused` and records the `kgwctl.kgateway.dev/ConnectionRefused` cause; then it replaces each non-2xx body with a canonical `metav1.Status` holding only code, reason and that cause; the body is never stored)
- `type GatewaySet map[types.NamespacedName]string` (kept Gateway → owning install ID, `""` when ambiguous or unowned; the inventory captures proxies of every key)
- `func resolveScope(f *Flags, explicitNamespace bool, res *load.Result) (bundle.Scope, GatewaySet, func(*unstructured.Unstructured) bundle.ExcludeReason, []string /* warnings */, error)`
- The keep function resolves each object's `kinds.Served` from `res.Served` by GVK, and calls `view.Filter.Visible` with a `view.Scope{AllNamespaces: true, Install: f.Install}`, so the filter checks ownership only; the bundle bounds namespaces. Scope comes from `res.Model.Installs[i].Resolved.Scope`; no `install.Resolve` call. A namespaced object passes scope when any selected install's `Scope.Contains` accepts its namespace (design §3.2); `view.Filter` is used unchanged.
- `type ControllerSelector struct{ Install bundle.Install; Selector labels.Selector }`
- `func Inventory(ctx context.Context, kube kubernetes.Interface, s bundle.Scope, gws GatewaySet, ctrls []ControllerSelector) ([]bundle.Replica, []error)`
- `func Reidentify(ctx context.Context, kube kubernetes.Interface, r bundle.Replica) bundle.CaptureIdentity`
- [ ] `TestGuardRefuses` (HTTP boundary): list, get and watch on `secrets` and `configmaps`, namespaced and cluster-wide → error, zero requests at the server; `pods` passes.
- [ ] `TestGuardSanitizesBodies` (HTTP boundary): a 403 and a 500 with pattern-free canary bodies → client-go errors carry code and reason, no canary; a pods/proxy 503 whose body says `connection refused` keeps the refused cause and loses the body; a 5 KiB body with the phrase past 4 KiB is not marked.
- [ ] `TestResolveScope`: no `-n` → all; `-A` → all; `-n team-a` → `team-a` plus install namespaces; two installs → both `detected`, one warning; unknown ownership with installs → all, warning; `--install x/y` undetected → exit 2; none → controller collectors `skipped: no install`.
- [ ] `TestResolveDiscoveryScope`: selector env value `[{"matchLabels":{"team":"a"}}]` → mode `selectors`, `Selectors` = `["team=a"]`, `team-b` in `Unwatched`; keep gives `ExcludeScope` for a `team-b` TrafficPolicy and `ExcludeNone` for GatewayClasses; empty value → `all`; an `envFrom`-only controller → `unknown` with a reason, nothing scope-excluded; a denied Namespace list (`res.Inputs`) → `namespaces-unreadable`, nothing scope-excluded; another controller's GatewayClass and Gateway → `ExcludeOwnership`; `AllControllers` → always `ExcludeNone`, scope still recorded.
- [ ] `TestResolveScopeUnion`: installs A (selector excludes `team-b`) and B (watches `team-b`), no `--install` → a `team-b` HTTPRoute parented to A's Gateway is kept; with `--install` A → `ExcludeScope`. A Gateway both installs claim (ambiguous) is kept and in `GatewaySet`, so its proxies are captured.
- [ ] `TestResolveWorkloadNamespaces`: `WorkloadNamespaces` = install, replica, kept-resource and kept-route `backendRefs` namespaces; with `-n team-a`, a `backendRefs` Service in `team-c` adds nothing, also with `--all-controllers`, and `--all-controllers` yields `team-a` plus install namespaces, not all.
- [ ] `TestInventoryFullName`: a 70-character Gateway with three Pods (shortened label, full-name annotation) → three replicas, `IdentityAnnotation`; a Pod with the same label and another annotation excluded.
- [ ] `TestInventoryUnverified`: a Pod with the gateway label and no annotation → `IdentityUnverified`, an error line, and no `replicasets` request in `Proxy.Requests()`.
- [ ] `TestInventoryStates`: not-ready, terminating, Pending and restarted Pods kept with state and per-container restarts, init included.
- [ ] `TestInventoryMetricsPort`: proxy Pod with and without a 9091 declaration → `MetricsPort` 9091 and `MetricsPortDeclared` true/false; controller port from the install.
- [ ] `TestInventoryAllControllers`: proxies only of `GatewaySet` Gateways: another class's Gateway, and a kgateway Gateway left out by scope, excluded unless `AllControllers`; a Pod without the proxy label is never included.
- [ ] `TestReidentify`: same UID → `verified`; recreated under the same name → `changed`; deleted → `gone`; a 403 on get → `reread-failed`.
- [ ] Implement design §3.2, §6.1, §8.2; pass; commit `feat(bundle): request guard, scope and replica inventory`.

### Task 4.2.3: `proxydump` operations (transport)
**Files:** modify `internal/proxydump/fetch.go`, `portforward.go`, `fetch_test.go`, `spdy_test.go`, `portforward_test.go`; `internal/app/proxy_test.go` (hint check).
**Interfaces (produces):**
- `statusError(resp *http.Response) error` in `portforward.go` returns an `*apierrors.StatusError` with code and reason only and never reads the body, for every command. `proxy dump` keys its RBAC hint on `apierrors.IsForbidden`, so its messages stay the same.
- `type Op int`: `OpConfigDump`, `OpConfigDumpEDS`, `OpStats`, `OpServerInfo`, `OpCerts`, `OpClusters`; `func (o Op) String() string`; `func (o Op) Code() bundle.Op`
- `func FetchOp(ctx context.Context, d Dialer, pod types.NamespacedName, op Op, now func() time.Time) (*Capture, error)`
- `Fetch(ctx, d, pod, now)` = `FetchOp(ctx, d, pod, OpConfigDump, now)`; `Capture` gains `Op Op`.
- [ ] `TestFetchOpTargets` (SPDY boundary): each Op sends exactly its design §6.3 request line on port 19000.
- [ ] `TestFetchOpUnknown`: `Op(99)` and `Op(-1)` fail before `Dial` (dial counter 0).
- [ ] `TestFetchBareConfigDump`: `Fetch` sends `GET /config_dump`; existing `proxy dump` and `proxy routes` tests pass unchanged.
- [ ] `TestFetchOpRedirect` (`newSPDYPodWith` returning 302): refused for every Op; the `Location` value is in no error.
- [ ] `TestPortForwardStatusNoBody` (SPDY boundary): an upgrade refused with 403 and a pattern-free body → error carries code and reason only; `apierrors.IsForbidden` holds.
- [ ] `TestProxyDumpForbiddenHint`: `proxy dump` with a refused upgrade prints the same RBAC hint as before; the existing `proxy` tests pass unchanged.
- [ ] `TestFetchCancelThroughProxy`: an established capture through a CONNECT proxy and through SOCKS5 ends when ctx is cancelled; tunnel and proxy connection closed (Phase 3 carry-over).
- [ ] Commit `feat(proxydump): allowlisted admin operations`.

### Task 4.2.4: Resources, workloads, install
**Files:** create `internal/bundle/collect/{resources.go,workloads.go,install.go}`, tests.
**Interfaces (produces):** `func Resources(reg *extension.Registry) bundle.Collector`; `func Workloads() bundle.Collector`; `func Install() bundle.Collector`.
- [ ] `resources`: Gateway API kinds, every served kind with category `kgateway`, registry kinds; per scope namespace or cluster-wide; only objects `Env.Keep` accepts; each left-out object goes to `Sink.Exclude`; `Path{"resources", group|Core, kind, ns|Cluster, File(name, YAML)}`.
- [ ] `workloads`: Deployments, Pods, Services in `Scope.WorkloadNamespaces` only, which `resolveScope` bounds by `-n` also under `--all-controllers`; Events after `Window.Since` by the latest of `lastTimestamp`, `eventTime`, `series.lastObservedTime`.
- [ ] `install`: each install's controller Deployment; GatewayClasses.
- [ ] `TestClusterStateArchive` (envtest, archive boundary): canaries in `last-applied-configuration`, a TrafficPolicy `x-tenant-key` modifier, a GatewayExtension `headersToAdd` and an Event message; extracted paths as design §4; `AssertNoCanary`.
- [ ] `TestClusterStateScope`: `-n team-a` → no `team-b` member; GatewayClass under `_cluster`.
- [ ] `TestClusterStateOwnership` (envtest, archive boundary): a second controller's GatewayClass (`example.com/other`), its Gateway and HTTPRoute are absent from `resources/` and from the `install` collector's GatewayClasses; a Deployment in a namespace only the other controller uses is absent; one in a namespace shared with a kgateway Gateway is present (per-namespace workloads, §3.2); a selector excluding `team-b` leaves out `team-b` resources, the manifest names `team-b`, and `excluded` counts the left-out HTTPRoute under `scope`; with `--all-controllers` all are present.
- [ ] `TestEventsWindow`: an Event with an old `lastTimestamp` and a recent `series.lastObservedTime` is kept; one older on all three is left out.
- [ ] Commit `feat(bundle): resource, workload and install collectors`.

### Task 4.2.5: Version and `check` collectors
**Files:** create `internal/bundle/collect/version.go`, `internal/app/bundle_check.go`, tests; modify `internal/app/check.go`, `internal/app/bundle.go` (hooks gain `Dialer proxydump.Dialer`), `internal/testenv/proxy.go` (`Action.Body []byte`).
**Interfaces (produces):**
- `func Version(build view.BuildInfo, res *load.Result, d proxydump.Dialer) bundle.Collector` (writes `cluster/version.json` and `proxies/<ns>/<pod>/server_info.json`)
- `type VersionFile struct{ Client view.BuildInfo; Server *version.Info; Installs []view.InstallVersion /* image tags */; Observed []ObservedEnvoy }`; `type ObservedEnvoy struct{ Namespace, Pod, Version string; Status bundle.ItemStatus }`
- `func checkOnce(ctx context.Context, f *Flags, rc *rest.Config, reg *extension.Registry, build BuildInfo, now func() time.Time, hooks checkTestHooks) (view.Check, error)` (extracted; `newCheckCmd` calls it; `rc` is guarded)
- `func newCheckCollector(f *Flags, rc *rest.Config, reg *extension.Registry, build BuildInfo, now func() time.Time, hooks checkTestHooks) bundle.Collector`
- [ ] `TestVersionFile`: server version from discovery; image tags under `installs`; two replicas with different `/server_info` versions → two `observed` entries by pod; a denied replica → `denied`, no version; a replaced Pod → `changed`.
- [ ] `TestCheckCollectorMatchesCheck` (envtest): `check/check.json` equals `kgwctl check -o json` with the same `-n` on the same fixture apart from timestamps and sanitized reasons; existing `check` tests unchanged.
- [ ] `TestCheckCollectorDenialBody` (envtest, production bundle path): a list denied with `Action{Status: 403, Body: <pattern-free canary>}` → `check.json`, `errors.txt` and `run.log` carry `403 Forbidden`, not the canary.
- [ ] `TestCheckCollectorUnwatchedRoute` (envtest): with the `TestClusterStateOwnership` fixture, `check/check.json` has KGWUI-004 naming the left-out `team-b` HTTPRoute.
- [ ] `TestBundleMetadataCanaries` (command, archive boundary): canaries in the context name, `--redact-header`, `-o` path and a map key; none in any member or name.
- [ ] `TestBundleRegistryAlias` (command): a registry kind resolving to core `secrets`, with an admin kubeconfig → no `secrets` request in `Proxy.Requests()`; an error line; exit 0.
- [ ] Commit `feat(bundle): version and check collectors`.

## Sub-phase 4.3 — Logs, metrics, Envoy

### Task 4.3.1: Logs
**Files:** create `internal/bundle/collect/logs.go`, tests; modify `internal/app/bundle.go` (hooks gain `Logs collect.LogSource`).
**Interfaces (produces):**
- `type LogSource interface{ Stream(ctx context.Context, pod types.NamespacedName, opts *corev1.PodLogOptions) (io.ReadCloser, error) }`
- `func KubeLogSource(kube kubernetes.Interface) LogSource` (errors through `redact.RemoteError`); `func Logs(src LogSource) bundle.Collector`
- [ ] `TestKubeLogSourceQuery` (HTTP boundary): `GET /api/v1/namespaces/<ns>/pods/<pod>/log` with `container`, `sinceTime` = `Window.Since` for every stream, `timestamps=true`, `tailLines`, `limitBytes`, and `previous=true` only for restarted containers.
- [ ] `TestKubeLogSourceErrorBody` (HTTP boundary): a 500 with a canary body → the error has the status, not the canary.
- [ ] `TestLogsReread`: a stream from a Pod that stays → no identity error line; a Pod deleted after its stream → an error line whose `Source.Identity` is `gone` (design §12, call 13).
- [ ] `TestLogsPerReplica`: every inventory container incl. init; `.previous.log` only when `Restarts > 0`.
- [ ] `TestBundleSkipLogs` (command): `--skip logs` → no `logs/` member, collector `skipped` with reason `flag`, no `errors.txt` line, no `pods/log` request in `Proxy.Requests()`.
- [ ] `TestLogsCancelMidStream` (archive boundary): a stream cut by the seal keeps complete lines and a truncation line, an open PEM block redacted; collector `timeout`.
- [ ] Commit `feat(bundle): log collector`.

### Task 4.3.2: Controller and proxy metrics
**Files:** create `internal/bundle/collect/{metrics.go,scraper.go}`, tests; modify `internal/app/bundle.go` (hooks gain `Scraper collect.Scraper`).
**Interfaces (produces):**
- `type Scraper interface{ Scrape(ctx context.Context, pod *corev1.Pod, port int32) (io.ReadCloser, error) }`
- `type HTTPStatusError struct{ Code int; Reason string; Refused bool }`
- `func StatusScraper(cfg *rest.Config) (Scraper, error)` (Snellius `ctrlmetrics.PodProxyScraper` over a guarded clientset; a non-200 becomes `*HTTPStatusError` from the guard's canonical `Status`, with `Refused` set from its `ConnectionRefused` cause)
- `func ControllerMetrics(s Scraper) bundle.Collector`; `func ProxyMetrics(s Scraper) bundle.Collector` (always scrape `Replica.MetricsPort`; re-read after each scrape)
- [ ] `TestStatusScraperRoute` (HTTP boundary): GET to `/api/v1/namespaces/<ns>/pods/http:<pod>:9091/proxy/metrics`; a 302 is not followed; a 403 is `*HTTPStatusError{403}`; a 503 with a refused body sets `Refused`; no body text is in any error.
- [ ] `TestMetricsStatuses`: `ok`; `denied` (403); `disabled` only when `Refused` and `!MetricsPortDeclared`; refused with a declared port → `failed`; `skipped: not running`.
- [ ] `TestMetricsStockMarkersNoPort`: a Pod with the proxy label and full-name annotation, no 9091 declaration and a working endpoint → scraped, `ok`.
- [ ] `TestMetricsArchive` (archive boundary): an HTML body dropped with an error; a Bearer label value redacted.
- [ ] Commit `feat(bundle): controller and proxy metrics per replica`.

### Task 4.3.3: Envoy capture
**Files:** create `internal/bundle/collect/proxyadmin.go`, tests.
**Interfaces (produces):** `func ProxyAdmin(d proxydump.Dialer, noEndpoints bool) bundle.Collector` (per proxy replica: `config-dump-eds` or `config-dump`, `stats`, `certs`, `clusters` unless `noEndpoints`; `Reidentify` after each capture).
- [ ] `TestProxyAdminPerReplica`: three replicas, one with a failing dial → two full directories, one with `failed` items; a not-running replica `skipped`.
- [ ] `TestProxyAdminIdentity`: the Pod's UID changes during capture → `changed`; the Pod deleted → `gone`; an unverified replica gets no operation.
- [ ] `TestProxyAdminNoEndpoints`: Ops seen are `config-dump`, `stats`, `certs`.
- [ ] `TestProxyAdminArchive` (archive boundary): a config dump with the design §10 row 9–12 canaries leaves none; Envoy `[redacted]` stays.
- [ ] `TestProxyAdminDivergentReplicas`: two replicas with different `version_text` and listener states keep both under their own pod with matching `Source.UID`; the extracted `config_dump.json` loads in `proxy routes --file`.
- [ ] Commit `feat(bundle): per-replica Envoy captures`.

### Task 4.3.4: RBAC and partial runs at the command
**Files:** modify `docs/rbac.yaml`, README permissions line; add to `internal/app/bundle_test.go`.
- [ ] docs/rbac.yaml: optional Role `kgwctl-bundle-logs` (`get pods/log`), noting it reads every pod's logs in its namespace.
- [ ] `TestBundleReaderOnly` (envtest, real adapters): `pods/log` and `pods/portforward` 403 → exit 0; both rules hit; every affected item `denied`; `errors.txt` ends with one grant line per grant and namespace, although `version` and `proxy-admin` both hit `pods/portforward`; resources, workloads, install, version and `check` present.
- [ ] `TestBundleMixedOutcomes`: one replica ok and one timing out (seams), one denied (real adapter, rule hit) → exit 0; item statuses `ok`, `timeout`, `denied`.
- [ ] `TestBundleNoForbiddenRequests`: `Proxy.Requests()` for a full run has no `secrets` or `configmaps`.
- [ ] `TestNamespacedRequestSet` (envtest, all collectors wired): `-n team-a` → the requests match the design §3.2 table exactly; cluster-wide lists and watches come only from install detection and `check`; collector requests only from `team-a` and the install namespace.
- [ ] Commit `feat(bundle): optional log Role; partial bundles on restricted RBAC`.

## Sub-phase 4.4 — Extension, docs, live check

### Task 4.4.1: `WithBundleCollectors`
**Files:** modify `pkg/cli/cli.go`, `pkg/cli/cli_test.go`, `examples/downstream/main.go`.
**Interfaces (produces):** `func WithBundleCollectors(c ...bundle.Collector) Option`; invalid or duplicate names fail `NewRootCommand` like registry errors.
- [ ] `TestWithBundleCollectors`: the example collector is in `--list-collectors` and in the archive under its path, redacted; a name clash with `logs` is an error.
- [ ] `TestDownstreamCanaries` (archive boundary): the example writes a canary through `JSON` and `Text`, and tries an undeclared type and a Secret object; none in the archive.
- [ ] Commit `feat(bundle): bundle collectors for downstream builds`.

### Task 4.4.2: README
- [ ] README `bundle` section: what is collected, what is never requested and how that is enforced, the residual risk of derived artifacts and of `-n`, redaction limits, "review before sharing", RBAC. Commit `docs: kgwctl bundle`.

### Task 4.4.3: Live check
- [ ] `hack/live-check.sh bundle`: apply canaries (a TrafficPolicy `headerModifiers.secretRef` to a Secret with a pattern-free value, a GatewayExtension ext-auth `headersToAdd`, an Event, a GatewayParameters env, an inline JWKS with `k`); raise one proxy to debug logging with `kubectl --context … port-forward` and send a request that carries the Secret-backed header; run a full bundle as admin and as reader-only; extract and scan (raw, base64, gzip); assert per proxy replica `config_dump.json`, `stats.txt` with `version_text`, and a `metrics.txt` that parses as Prometheus; reader-only exits 0 with one `pods/log` and one `pods/portforward` grant line per affected namespace.
- [ ] Commit `test(live-check): bundle step`.

## Phase close prerequisites (not done by the implementer)
Codex phase review; owner try-out on kind; program file note on the preserved convergence evidence.
