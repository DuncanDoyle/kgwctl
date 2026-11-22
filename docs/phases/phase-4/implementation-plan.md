# Phase 4 — Support bundle: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `kgwctl bundle` writes one redacted `tar.gz` with resources, workloads, install metadata, `check`, logs, controller metrics and per-replica proxy captures, under one deadline, with a manifest, `errors.txt` and `redactions.json`.

**Architecture:** `internal/redact` holds pure redactors per content type. `internal/bundle` schedules collectors under one deadline and writes every file through a `Sink` that redacts by content type and refuses anything else. Collectors in `internal/bundle/collect` reuse `load`, the `check` path, Snellius's pods/proxy scraper and `proxydump`. The collector interface is exported from `pkg/bundle` and registered through `pkg/cli.WithBundleCollectors`.

**Tech Stack:** Go 1.26, cobra, client-go v0.36.x, `archive/tar`, `compress/gzip`, Snellius v0.8.2, xdsview v0.4.3. No new modules.

**Spec:** [`design.md`](design.md) (r1). Read it first. §n refers to it.

## Global Constraints

- No Secret or ConfigMap is read; no `get`/`list` call on them anywhere in the bundle (§6.1).
- The controller's `/snapshots/xds` is never requested (§6.2). Port-forward paths only from the D-025 allowlist: Envoy `/config_dump`, `/stats`, `/server_info`, `/certs`, `/clusters`; controller `/snapshots/krt`.
- Every archive file goes through `bundle.Sink`; content types `Object`, `ConfigDump`, `JSON`, `Prometheus`, `Text` only (D-026).
- Marker `[redacted]`; no hashes or lengths of redacted values; `redactions.json` holds rule, location and count only (§7).
- Archive: regular files only, mode `0600`, never overwrite, path parts validated (no `/`, `..`, leading `.`), `--max-size` default `512Mi` uncompressed (§4).
- Deadlines: `--timeout` (default `5m`) for the run; `--request-timeout` (default 30s) per item (§3).
- Logs: `--since 24h`, `--tail 5000`, 10 MiB per stream; previous container only when `restartCount > 0`; init containers included (§3).
- Workers: 8. Output order deterministic (§8).
- Exit codes: 0 when a bundle is written (also partial), 2 when none is (§3.1).
- RBAC: reader ClusterRole unchanged; `pods/log` in a new optional Role `kgwctl-bundle-logs`; `pods/portforward` in the existing optional Role (§6).
- No vendor or edition names (D-004). No new modules.
- Code comments 1–2 lines; Go doc comments on exported identifiers. Data to stdout only with `-o -`; progress to stderr.
- Every `kubectl`/`helm` call in scripts passes `--context`/`--kube-context`; kind clusters use their own `--kubeconfig`.

## Review Focus

1. **A reader-only user.** Expects a bundle (exit 0) with resources, workloads, install, version and `check`, and `errors.txt` naming `pods/log` and `pods/portforward` once each with the Role to add. Covered in Task 4.3.4.
2. **The deadline hits mid-log-stream.** Expects the partial file kept with a truncation line, the collector marked `timeout`, the manifest written. Covered in Tasks 4.1.3 and 4.3.1.
3. **A proxy with 5 replicas, one CrashLooping.** Expects four complete replica directories and one with previous logs and an `errors.txt` line for its admin GETs. Covered in Task 4.3.3.
4. **A Secret-shaped value in an unexpected place** (an annotation, a DirectResponse body, an Event message). Expects the pattern pass to catch the planted canary. Covered in Task 4.1.1.
5. **The output file already exists, or the directory is read-only.** Expects exit 2 before any cluster call. Covered in Task 4.2.1.

## Test harness

- `internal/redact` and `internal/bundle`: pure unit tests; the canary corpus lives in `internal/redact/testdata/canary/`.
- Command boundary: envtest (`internal/testenv`) for resources, workloads, install and `check`. envtest runs no kubelet, so logs, pods/proxy bodies and port-forward use injected seams in `bundleTestHooks` (log source, scraper, `proxydump.Dialer`), as Phases 2 and 3 do. RBAC denial uses the testenv proxy `Match` on `pods/log` and `pods/portforward`.
- The real kubelet paths run only in the live check on kind (Task 4.4.3).

## Sub-phases

| Sub-phase | Delivers | Tasks |
|---|---|---|
| 4.1 | Redactors, canary corpus, Sink, writer, scheduler, manifest, threat model | 4.1.1–4.1.4 |
| 4.2 | `kgwctl bundle` with cluster-state collectors | 4.2.1–4.2.3 |
| 4.3 | Logs, metrics, proxy admin, controller snapshot, RBAC | 4.3.1–4.3.4 |
| 4.4 | Extension point, live check, README | 4.4.1–4.4.3 |

Each sub-phase ends with `make test` (race), `go vet ./...`, gofmt clean, the deps check, and `go mod tidy -diff` empty.

---

## Sub-phase 4.1 — Redaction and the bundle core

### Task 4.1.1: Redactors and canary corpus
**Files:** create `internal/redact/{redact.go,object.go,configdump.go,text.go,rules.go}`, tests, `testdata/canary/`.
**Interfaces (produces):**
- `type Finding struct{ Rule, Location string; Count int }`
- `type Options struct{ ExtraHeaders []string }`
- `func Object(u *unstructured.Unstructured, o Options) []Finding` (in place)
- `func ConfigDump(body []byte, o Options) ([]byte, []Finding, error)`
- `func JSON(body []byte, o Options) ([]byte, []Finding, error)`
- `func Text(r io.Reader, w io.Writer, o Options) ([]Finding, error)` (line streaming)
- [ ] Write the corpus: one fixture per design §10 row 1–15 (row 16 is Task 4.4.1), each value `kgwctl-canary-<n>-<loc>` or a secret-shaped variant.
- [ ] `TestNoCanaryLeaks`: run every fixture through its redactor; assert no canary in raw output, in base64-decoded strings, or in gzip members; assert each planted location appears in `[]Finding`.
- [ ] `TestKeepsUseful`: names, namespaces, hostnames, header names, `local_jwks`, cert SANs and Envoy's own `[redacted]` survive; redacted YAML/JSON re-parses with the same key set.
- [ ] Run, fail, implement rules per design §7, pass.
- [ ] Commit `feat(redact): redactors per content type with a canary corpus`.

### Task 4.1.2: Sink and archive writer
**Files:** create `internal/bundle/{sink.go,writer.go,manifest.go}`, tests.
**Interfaces (produces):**
- `type Sink interface { Object(path string, u *unstructured.Unstructured) error; ConfigDump(path string, body []byte) error; JSON(path string, body []byte) error; Prometheus(path string, r io.Reader) error; Text(path string, r io.Reader) error; Error(item string, err error) }`
- `type Source struct{ Pod, Container, Endpoint string }` attached via `Sink` options, recorded in the manifest.
- `func NewWriter(w io.Writer, root string, max int64, ro redact.Options, now func() time.Time) *Writer`; `(*Writer).Close(m Manifest) error` writes `manifest.json`, `errors.txt`, `redactions.json`, `run.log` last.
- [ ] Tests: path validation rejects `../x`, `/abs`, `a/b` parts, leading `.`; only regular entries; `--max-size` truncation writes a marker line and an error; every file passes its redactor (a canary through each method leaves no trace); `redactions.json` has no values.
- [ ] Implement; pass; commit `feat(bundle): sink and archive writer with mandatory redaction`.

### Task 4.1.3: Scheduler and manifest
**Files:** create `internal/bundle/{run.go,collector.go}`, tests.
**Interfaces (produces):**
- `type Collector interface { Name() string; Collect(ctx context.Context, env Env, s Sink) error }` (moved to `pkg/bundle` in 4.4.1; alias here)
- `type Env struct{ ... }` (clients, scope, window, loaded model; fields added by later tasks)
- `func Run(ctx context.Context, cs []Collector, env Env, w *Writer, workers int) Manifest`
- [ ] Tests with fake collectors: deadline → partial archive, statuses `timeout`; panic → `failed` with the panic text; `--skip` → `skipped`; deterministic file order across two runs; manifest per-file capture time and source.
- [ ] Implement; pass; commit `feat(bundle): scheduler with one deadline and per-collector status`.

### Task 4.1.4: Threat model and decisions
- [ ] Write `docs/bundle-threat-model.md` (design §7.2): assets per class, never-read list, redaction table, limitations, RBAC, archive handling. Mark D-025/D-026 accepted if the owner approved. Commit `docs: bundle threat model`.

## Sub-phase 4.2 — `kgwctl bundle` and cluster-state collectors

### Task 4.2.1: Command, flags, exit codes
**Files:** create `internal/app/bundle.go`, test; modify `internal/app/root.go` (register).
- [ ] Tests: existing `-o` file → exit 2 before any API call; unwritable directory → exit 2; `-o -` writes the archive to stdout; `--list-collectors` prints names; unknown `--skip` name → exit 2; default file name `kgwctl-bundle-<context>-<UTC ts>.tar.gz`; created file is `0600`.
- [ ] Implement flags of design §3; pass; commit `feat(bundle): kgwctl bundle command`.

### Task 4.2.2: Resources, workloads, install, version
**Files:** create `internal/bundle/collect/{resources.go,workloads.go,install.go,version.go}`, tests.
- [ ] `resources`: Gateway API kinds plus every CRD with category `kgateway` (discovered, not hardcoded), plus registry kinds; one YAML per object at `resources/<group>/<kind>/<ns>/<name>.yaml`.
- [ ] `workloads`: Deployments, Pods, Services in scope and the install namespace; Events filtered by `--since`.
- [ ] `install`: controller Deployment(s) found by `kgateway=kgateway`; GatewayClasses.
- [ ] `version`: `view.Version` output as JSON.
- [ ] envtest test: fixtures incl. a canary in `last-applied-configuration` and an Event message; the extracted archive has the expected paths and no canary; `-n team-a` excludes `team-b` objects but keeps the install namespace.
- [ ] Commit `feat(bundle): resource, workload, install and version collectors`.

### Task 4.2.3: `check` collector and manifest contract
- [ ] `check` collector writes the `Check` envelope from the shared model (no second load); test it equals `kgwctl check -o json` on the same envtest fixture apart from timestamps.
- [ ] docs/output.md: `BundleManifest` section. Commit `feat(bundle): check collector; BundleManifest contract`.

## Sub-phase 4.3 — Logs, metrics, proxy admin

### Task 4.3.1: Logs
- [ ] `logs` collector: controller and proxy pods, every container incl. init; `.previous` only when `restartCount > 0`; `SinceSeconds`, `TailLines`, 10 MiB `LimitBytes`; per stream `--request-timeout`.
- [ ] Seam: `LogSource` interface; test with a fake source: caps, previous rule, deadline mid-stream keeps the partial file with a truncation line, Bearer and PEM canaries redacted.
- [ ] Commit `feat(bundle): log collector`.

### Task 4.3.2: Controller metrics and proxy stats
- [ ] `controller-metrics`: raw `/metrics` per controller replica via Snellius `ctrlmetrics.PodProxyScraper` (or the same pods/proxy GET); `proxy-stats`: raw 9091 `/stats/prometheus` per proxy replica.
- [ ] Tests with a fake scraper: one file per replica, capture time in the manifest, a 403 → one error line with the `pods/proxy` grant.
- [ ] Commit `feat(bundle): controller metrics and proxy stats per replica`.

### Task 4.3.3: Proxy admin and controller snapshot
**Interfaces (produces):** `proxydump.ListReplicas(ctx, c load.Clients, gw types.NamespacedName) ([]types.NamespacedName, error)`; `proxydump.FetchPath(ctx, d Dialer, pod types.NamespacedName, port int, path string, now func() time.Time) (*Capture, error)` refusing paths outside the D-025 allowlist (existing `Fetch` becomes `FetchPath(..., AdminPort, "/config_dump", ...)`).
- [ ] Tests: a path off the allowlist (incl. `/snapshots/xds`, `/quitquitquit`, a query string) is refused before `Dial`; a fake SPDY Envoy serves the five paths; per replica five files; a CrashLooping replica yields error lines, other replicas complete; `--no-endpoints` drops `include_eds`; `controller-snapshot` off by default, on with the flag; the fake admin fails the test on `/snapshots/xds`.
- [ ] Commit `feat(bundle): per-replica proxy admin captures and opt-in controller snapshot`.

### Task 4.3.4: RBAC
- [ ] docs/rbac.yaml: optional Role `kgwctl-bundle-logs` (`get pods/log`); README permissions line.
- [ ] Test: reader-only user against envtest with `pods/log` and `pods/portforward` denied → exit 0, `errors.txt` names each grant once with its Role.
- [ ] Commit `feat(bundle): optional log Role; partial bundles on restricted RBAC`.

## Sub-phase 4.4 — Extension, live check, docs

### Task 4.4.1: `pkg/bundle` and `WithBundleCollectors`
- [ ] Move `Collector`, `Env` (exported subset), `Sink` to `pkg/bundle`; add `cli.WithBundleCollectors(...bundle.Collector)`; `examples/downstream` registers one collector writing a `JSON` file.
- [ ] Test: the downstream example's collector appears in `--list-collectors` and in the archive, redacted; a collector cannot write raw bytes (no such Sink method).
- [ ] Commit `feat(bundle): bundle collectors for downstream builds`.

### Task 4.4.2: README and docs
- [ ] README `bundle` section: what is collected, what is never read, redaction limits, "review before sharing", RBAC. Commit `docs: kgwctl bundle`.

### Task 4.4.3: Live check
- [ ] `hack/live-check.sh bundle`: apply canary fixtures (header modifiers from a secretRef, an Event, a GatewayParameters env), run a full bundle as admin and as reader-only, extract, scan for canaries (raw, base64, gzip), assert a replica directory per proxy pod with `version_text` in `stats.txt`, assert reader-only exit 0 with two grant lines.
- [ ] Commit `test(live-check): bundle step`.

## Phase close prerequisites (not done by the implementer)
Codex phase review; owner try-out on kind; program file note on the convergence inputs.
