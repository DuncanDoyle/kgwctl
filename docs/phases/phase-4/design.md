# Phase 4 — Support bundle: Design

Status: r2, 2026-11-22. Applies the Codex review triage (`reviews/design-codex.md`). For owner approval.
Roadmap: Phase 4. Decisions: D-009, D-010, D-015, D-017, D-019, D-021, D-024; D-025 and D-026 (proposed, §12).

## 1. Goal and scope
`kgwctl bundle` writes one `tar.gz` that holds what support asks for in a kgateway case. Nobody has to collect it by hand, and the customer can see what leaves the cluster. The archive is useful offline: a person can read it, and later tooling can parse it through a manifest.

In scope:
- Gateway API and kgateway resources with status; Deployments, Pods, Services and Events around them.
- Controller and proxy logs, with previous containers after a restart.
- Per replica: controller metrics, proxy metrics, and the proxy's config dump, stats, server info, certificates and clusters.
- Versions (build, Kubernetes server, image tags, observed Envoy versions) and the `check` report.
- Scoping by namespace, install and time window; one global deadline with partial results and a per-item error record.
- A collector registry for downstream builds.
- Mandatory redaction, a redaction report and a documented threat model.

### Anti-goals
- No API request for Secrets or ConfigMaps (§6.1).
- No controller admin capture. `/snapshots/xds` holds TLS private keys and inline credentials in clear; `/snapshots/krt` can return raw snapshots and ConfigMaps (§6.2).
- No replay system and no analyzer over a saved bundle. The manifest makes both possible later.
- No upload, and no privileges beyond the user's own. kgwctl never creates RBAC objects.

## 2. Evidence
Research notes, not committed: prior art, reuse survey and threat assessment, 2026-11-22. kgateway v2.5.1 read from source, not run.

- **Prior art.** agctl bug-report (agentgateway#3991) is the closest model: kinds by CRD category, 8 workers under one 5 min deadline, partial output with `errors.txt`, 10 MiB per log stream, previous logs only after a restart. It reads full Secret data and writes ConfigMaps unredacted. istioctl bug-report adds a time window and selectors, but dumps every CR unredacted. cilium sysdump keeps its own run log in the archive.
- **kgateway v2.5.1.**
  - Controller: metrics on 9092 (`pods/proxy`), admin on 9095 bound to localhost.
  - Proxy: Envoy admin on 127.0.0.1:19000 (port-forward); 9091 serves `GET /metrics` and, by default, `/stats` (`pods/proxy`). Both routes and their rewrites are chart values.
  - Controller pods carry `kgateway=kgateway`. Proxy pods carry `kgateway=kube-gateway`, `gateway.networking.k8s.io/gateway-name` (shortened with a hash past 63 characters) and the annotation `gateway.kgateway.dev/gateway-full-name`.
  - All kgateway CRDs are namespaced and carry the category `kgateway`.
- **Install metadata without Secrets:** the controller Deployment's `helm.sh/chart` and `app.kubernetes.io/version` labels, image tags, `KGW_*` env vars, the GatewayClass and Envoy's `/server_info`. Helm values live only in `helm.sh/release.v1` Secrets.
- **Sensitive data Envoy does not redact:** header values (including values kgateway resolves from `headerModifiers.secretRef`), `initial_metadata`, Lua code, transformation templates, and inline JWKS, which kgateway copies to `local_jwks.inline_string` without projecting to public keys. Every object's `last-applied-configuration` annotation copies its whole spec.

## 3. Command

```
kgwctl bundle [-n <ns> | -A] [--install <ns>/<name>] [--all-controllers]
              [--since 24h] [--tail 5000] [--timeout 5m] [-o <file>]
              [--skip <collector>,...] [--max-size 512Mi] [--redact-header <name>,...]
              [--no-endpoints] [--list-collectors]
```

- **Scope** uses the root flags as every command does (§3.2).
- **Log window:**
  - One run-start time `T0`. Logs and Events use `since = T0 - --since` for every stream.
  - Log requests send `SinceTime=since`, `Timestamps: true`, `TailLines=--tail` and `LimitBytes` = cap + 64 KiB. kgwctl enforces the 10 MiB cap per stream locally, since `LimitBytes` is approximate.
  - On a cap cut or a cancel, the incomplete last line is dropped. An unterminated PEM block is redacted to the end of the file. A final line `[kgwctl: truncated: <reason>]` follows.
  - An Event's time is the latest of `lastTimestamp`, `eventTime` and `series.lastObservedTime`. Events older than `since` are left out.
  - `.previous` logs only for containers with `restartCount > 0`. Init containers are included.
- **Deadlines:** `--timeout` (default 5m) is the collection deadline for the run (§8.3). `--request-timeout` (default 30s) bounds one item: one list, one log stream, one admin GET. A hung pod costs one item, not the run. Ctrl-C acts as the deadline (D-020).
- **`-o`:**
  - Default file: `kgwctl-bundle-<context>-<UTC timestamp>.tar.gz` in the current directory. The context part is sanitized: characters outside `[A-Za-z0-9._-]` become `_`, a leading `.` or `-` becomes `_`, and it is cut to 64 bytes. Empty becomes `unknown`.
  - The file is created with `O_CREATE|O_EXCL` and mode `0600` before any cluster call. kgwctl never overwrites.
  - `-o -` writes to stdout.
- `--skip` turns off collectors by name. `--list-collectors` prints the names and exits.
- `--max-size` caps the uncompressed total (§8.3).
- `--no-endpoints` sends `/config_dump` without `include_eds` and skips `/clusters`. Pod and Service IPs in workload objects stay.
- `--redact-header` adds names to the sensitive-header list (§7).
- Progress goes to stderr: one line per collector, then counts of files, errors and redactions, and the output path.

### 3.1 Exit codes
- 0: a bundle was written, even a partial one, which is normal on restricted RBAC. The stderr summary names failed collectors.
- 2: no bundle was written: a usage error, an output that exists or cannot be created, no reachable cluster, or a fatal write failure (§8.3).

### 3.2 Scope contract
The root flags resolve once, before any collector runs, into one `Scope` that every collector receives and the manifest records.

| Flag | Meaning in `bundle` |
|---|---|
| `-n <ns>` | One namespace, as today (scalar; repeatable `-n` stays deferred, D-015). Default: the context's namespace. |
| `-A` | All namespaces. |
| `--install <ns>/<name>` | One install. Without it, every detected install. |
| `--all-controllers` | Proxies of Gateways of any controller's class, not only the selected installs'. |

`Scope` holds:
- **Archive namespaces:** the `-n` namespace, or all with `-A`. Namespaced objects outside them are never written. Objects inside them are not filtered by controller.
- **Installs:** each with its namespace, Deployment and how it was chosen (`flag` or `detected`). Install namespaces join the archive scope for the controller's own Deployment, Pods, Services, Events, logs and metrics only.
- **Cluster-scoped kinds:** GatewayClass only. They are written under `_cluster`.
- **Read namespaces:** archive namespaces plus install namespaces. Collectors make namespaced requests only there. The install detection read is the same cluster-wide `load.Load` that `kgwctl version` makes; namespace-scoped model reads stay deferred (kgwctl#8).

Installs come from the Snellius detector through `load.Load`. Several without `--install`: all are taken, each recorded, with a stderr warning. None or unknown ownership: a warning, and controller collectors record `skipped: no install`. `--install` naming no detected install: exit 2.

Metrics name objects in all namespaces. In a namespaced run, Prometheus samples with a label whose name ends in `namespace` and whose value is outside the archive namespaces are dropped and counted under rule `scope`. Controller logs can still name such objects (§7.1).

## 4. Archive layout

```
kgwctl-bundle-<context>-<ts>/
  manifest.json            # BundleManifest envelope (§5), incl. scope and replica inventory
  errors.txt               # one line per failed collector or item
  redactions.json          # per file: rule, location, count; never values (§7)
  run.log                  # kgwctl's progress log for this run
  cluster/version.json     # build, Kubernetes server, image tags, observed Envoy versions
  check/check.json         # Check envelope, as `kgwctl check -o json`
  resources/<group>/<kind>/<namespace|_cluster>/<name>.yaml
  workloads/<namespace>/{deployments,pods,services,events}.yaml
  install/<namespace>/<deployment>.yaml   # labels, images, KGW_* env (redacted)
  controller/<namespace>/<pod>/
    logs/<container>.log, <container>.previous.log
    metrics.txt            # 9092 /metrics
  proxies/<namespace>/<pod>/
    config_dump.json       # redacted (§7)
    stats.txt              # Envoy /stats, incl. text readouts such as version_text
    metrics.txt            # 9091 /metrics
    server_info.json
    certs.json             # SANs, expiry, serials; no keys
    clusters.txt           # health and outlier state; endpoint addresses
    logs/<container>.log, <container>.previous.log
```
The core group is written as `core`. Member names follow §8.4.

## 5. Manifest (`BundleManifest`, `-o json` contract)
`manifest.json` is a versioned envelope like the other kinds in docs/output.md. It is the parse entry point for later tooling. It holds:
- the kgwctl build;
- flags (below);
- the context name, through the text redactor, and the server as `<apiserver>`, as in `check`;
- the resolved `Scope` (§3.2) and the window (`T0`, `since`, `tail`);
- run start, seal and end times, and the finalization outcome;
- the replica inventory (§8.2);
- per collector: name, status (`ok`, `partial`, `failed`, `skipped`, `timeout`), duration and redacted error text;
- per item: path, size, content type, capture time, source (namespace, pod, UID, container, operation), status (`ok`, `denied`, `disabled`, `failed`, `timeout`, `dropped`) and redaction count.

Flags are recorded by name. Values appear only for flags without free text: `-n`, `-A`, `--install`, `--all-controllers`, `--since`, `--tail`, `--timeout`, `--request-timeout`, `--max-size`, `--skip`, `--no-endpoints`. `-o`, `--kubeconfig` and `--context` appear by name only; `--redact-header` as a count.

Per replica, the manifest keeps pod identity, restart counts and capture times next to each file. With `version_text`, listener states and rejection details in the captures, that is the raw evidence a later convergence check reads (roadmap, Deferred).

## 6. Inputs and permissions

| Collector | Reads | RBAC | Default |
|---|---|---|---|
| `resources` | Gateway API kinds, CRDs in category `kgateway`, registry kinds; per read namespace | reader ClusterRole | on |
| `workloads` | Deployments, Pods, Services, Events per read namespace | reader | on |
| `install` | each install's controller Deployment, GatewayClasses | reader | on |
| `version` | discovery server version, install image tags, each proxy replica's `server-info` operation | reader; `pods/portforward` for observed versions | on |
| `check` | its own `kgwctl check` one-shot run | reader + `pods/proxy` (D-017) | on |
| `logs` | inventory replicas, all containers, previous after a restart | **`pods/log` get (new optional Role)** | on |
| `controller-metrics` | each controller replica's 9092 `/metrics` | `pods/proxy` | on |
| `proxy-metrics` | each proxy replica's 9091 `/metrics` | `pods/proxy` | on |
| `proxy-admin` | each proxy replica's allowlisted Envoy admin operations (§6.3) | `pods/portforward` (optional Role, D-021) | on |

- A 403 is recorded once per collector and namespace, with the grant to add, and the run continues. A user with only the reader role gets resources, workloads, install, version without observed Envoy versions, and `check`.
- Metrics use the Snellius `ctrlmetrics.PodProxyScraper`, which sends one GET without redirects. A transport wrapper keeps a 403 typed; response bodies of errors are never read. Item status: `denied` on 403, `disabled` when the Pod declares no container port for the metrics port (unverified that the chart drops the port when stats are off), `failed` otherwise.
- `resources`, `workloads` and `check` list on their own. No model is shared between collectors.

### 6.1 Secrets, ConfigMaps and Helm values
kgwctl makes no API request for Secrets or ConfigMaps in Phase 4.
- Certificate questions are answered without Secrets: Envoy `/certs` gives SANs, expiry and serials; Gateway and listener status give the controller's verdict; `check` reports bad references.
- Install shape comes from Deployment labels, images, `KGW_*` env and GatewayParameters.
- Helm values exist only inside `helm.sh/release.v1` Secrets. RBAC cannot grant metadata-only Secret reads. Deferred (§12, item 1).

Residual risk: derived artifacts carry values the controller resolved from Secrets, such as header values in a config dump, and logs can echo them. Redaction covers known structures and patterns (§7). The README sentence becomes "It never requests Secrets or ConfigMaps from the API", with this residual risk next to it.

### 6.2 Controller admin
No controller admin endpoint is requested. `/snapshots/xds` inlines listener TLS keys, backend client keys and AWS credentials, some as base64 proto bytes (kgateway v2.5.1 `pkg/kgateway/admin/xds_snapshot.go`, `irtranslator/fc.go`, `backend/aws.go`). `/snapshots/krt` can emit raw snapshots and returns every registered collection, including ConfigMaps and objects outside the requested namespaces. A whole-response capture waits for an audited projection.

### 6.3 Envoy admin operations (D-025)
`proxydump` sends only fixed operations. Each binds port, method, path and exact query. Callers pick an operation by constant; no API takes a path.

| Operation | Request | Used by |
|---|---|---|
| `config-dump` | `GET :19000 /config_dump` | `proxy dump`, `proxy routes`; bundle with `--no-endpoints` |
| `config-dump-eds` | `GET :19000 /config_dump?include_eds` | bundle default |
| `stats` | `GET :19000 /stats` | bundle |
| `server-info` | `GET :19000 /server_info` | bundle `version` |
| `certs` | `GET :19000 /certs` | bundle |
| `clusters` | `GET :19000 /clusters` | bundle, not with `--no-endpoints` |

Redirects are refused. An unknown operation fails before a stream opens. The `pods/portforward` grant and its warning are unchanged.

## 7. Redaction (D-026)
One mandatory stage sits in the archive writer. A collector hands the Sink typed content; the writer runs that type's redactor before staging and refuses any other type.

| Content type | Redaction |
|---|---|
| `object` | Drop `metadata.managedFields` and the `last-applied-configuration` annotation. Header-value rule on header modifiers. JWKS rule on `jwks.local.inline`. Per-kind field rules (`basicAuth.users` hashes, `env[].value` except allowlisted names, `DirectResponse` body). Key-name rule, header-pair rule, then the pattern pass over every remaining string. |
| `config-dump` | Keep Envoy's `[redacted]`. Header-value rule. JWKS rule on `local_jwks.inline_string`. Redact Lua `inline_code`/`source_codes`, and every other `inline_string`/`inline_bytes`. Parse the dynamic-module JSON `StringValue` and apply the rules inside. Then key-name rule and pattern pass. |
| `json` | Key-name rule, header-pair rule, pattern pass. Invalid JSON is dropped with an error. |
| `prometheus` | Must parse as Prometheus text exposition, or it is dropped with an error. Scope filter (§3.2). Pattern pass on label values and comments. |
| `text` | Line pattern pass: `Bearer`/`Basic` credentials, JWTs, PEM private-key blocks, AWS keys and signatures, credential query values, known key prefixes, and Envoy debug header lines whose name is sensitive. |

- **Header-value rule:** every header value is redacted, whatever its name. Envoy: `value` and `raw_value` under `request_headers_to_add`, `response_headers_to_add`, header-mutation `mutations`/`request_mutations`/`response_mutations`, and `initial_metadata`. Objects: `set[]`/`add[]` values of HTTPRoute/GRPCRoute `requestHeaderModifier` and `responseHeaderModifier`, TrafficPolicy `headerModifiers` and ListenerPolicy header lists. Header names are kept.
- **JWKS rule:** parse the value as a JWK set (`keys`) or a single JWK. Keep `kty`, `kid`, `alg`, `use`, `key_ops`, `n`, `e`, `crv`, `x`, `y`, `x5c`, `x5t`. Redact every other member, including `k`, `d`, `p`, `q`, `dp`, `dq`, `qi` and `oth`. A value that does not parse is redacted whole.
- **Key-name rule:** a key such as `password`, `secret`, `token`, `apiKey`, `credential`, `privateKey`, `clientSecret`, `sessionToken` or `licenseKey`, with an optional prefix.
- **Header-pair rule:** elsewhere, a `{name|key, value}` pair whose name is on the sensitive-header list has its value redacted: `authorization`, `proxy-authorization`, `cookie`, `set-cookie`, `x-api-key`, `api-key`, `x-auth-token`, `x-amz-security-token`, plus `--redact-header`.
- **Marker:** `[redacted]`. A value stays a string, so files parse with the same keys. A pattern match keeps its prefix (`Bearer [redacted]`). No hashes, no lengths.
- **Report:** `redactions.json` lists per file each rule, location (field path or line number) and count. It never holds values.

### 7.1 Metadata and errors
- Every error string passes `view.Redactor` (URLs, the API server host) and then the text pattern pass, before it reaches `Sink.Error`, the manifest, `errors.txt`, `run.log` or stderr. This covers panic values. Stack traces never enter the archive.
- HTTP error bodies are never read into an error. An error carries the status and the request's operation.
- Archive member names come from validated components only (§8.4), never from free text.

### 7.2 Limitations (README and threat model)
- Pattern redaction of logs and free text is best effort. It misses secrets in custom formats and base64-encoded or compressed content.
- Opaque extension config (unknown `TypedStruct`/`Any`, Wasm configuration) gets only the pattern pass.
- Stats, metrics and names expose topology and tenant names on purpose. Namespace scoping keeps other tenants out of objects, captures and metrics; controller logs can still name them.
- `clusters.txt` and the EDS config dump hold endpoint addresses. `--no-endpoints` drops both.
- The bundle is as sensitive as the cluster's configuration. The README tells users to review `redactions.json` and the files before sharing.

### 7.3 Threat model
`docs/bundle-threat-model.md` is written in 4.1 and kept current. It covers assets per artifact class, who receives the archive, what is never requested, redacted or kept, the RBAC used (including the `pods/portforward` power and the log Role reading every pod's logs in its namespace), archive handling and the limits above.

Trust boundary: downstream collectors are trusted in-process code. The Sink protects against mistakes, such as an unredacted type or a bad path, not against a malicious collector, which can write files or open connections by itself.

## 8. Architecture

### 8.1 Packages
- `pkg/bundle`: the public contract: `Collector`, `Env`, `Sink`, `Path`, `Source`, `Scope`, `Replica`. No Cobra imports (D-019).
- `internal/bundle`: the runner, writer, staging, manifest and replica inventory.
- `internal/bundle/collect`: the built-in collectors.
- `internal/redact`: redactors per content type. Pure functions with their own canary corpus; no dependency on `bundle`.
- `internal/proxydump`: the allowlisted operations (§6.3). `Fetch` stays as the `config-dump` operation, so `proxy dump` and `proxy routes` keep their bare request.
- `internal/app`: the command, scope resolution and the `check` collector, which calls the same one-shot path as `kgwctl check`.
- `pkg/cli.WithBundleCollectors(...bundle.Collector)` adds downstream collectors; `examples/downstream` adds one, built in CI.

A collector is `Name() string` and `Collect(ctx, Env, Sink) error`. `Env` gives clients, the `Scope`, the window, the inventory, the clock and the per-item timeout.

### 8.2 Replica inventory
Built once, after scope resolution and before any collector. Logs, metrics, admin captures and `version` read it; none lists Pods itself.

- Controller replicas: Pods in each install's namespace that match its Deployment's `spec.selector`.
- Proxy replicas: Pods labelled `kgateway=kube-gateway` in an archive namespace. A Pod belongs to the Gateway named by its `gateway.kgateway.dev/gateway-full-name` annotation. Without the annotation, the `gateway-name` label is used and the identity is marked `label`. Only Gateways whose class belongs to a selected install are kept, unless `--all-controllers` is set.
- Each `Replica` holds: namespace, pod, UID, role (`controller` or `proxy`), owner (install `<ns>/<name>` or Gateway `<ns>/<name>`), identity source, ready, terminating, phase, containers with restart counts (init included), and start time.
- Unready, terminating and restarting replicas stay in. Each capture is tried and gets its own item status. A Pod that is not `Running` skips metrics and admin (`skipped: not running`) but still gets logs.
- Directories are keyed by namespace and pod; the manifest maps each to its owner.

### 8.3 Staging, budget, sealing and finalization
- **Staging.** Each Sink call reads and redacts its whole item in memory, up to the item cap for its type: logs 10 MiB, config dump 64 MiB, metrics 8 MiB, others 16 MiB. A text item over its cap is cut at a line boundary (§3). A structured item over its cap is dropped with an error, since a partial document cannot be redacted by structure.
- **Commit order.** One writer goroutine owns the tar and gzip streams. A collector's staged items are committed when it ends, sorted by path, and collectors commit in registration order. Two runs on the same cluster give the same layout.
- **Budget.** `--max-size` counts staged and committed bytes, minus a 1 MiB footer reserve. The first item that does not fit, and every later one, is dropped (`dropped: size budget`). Footers are always written in full; if they exceed the reserve, the manifest says by how much.
- **Sealing.** At the collection deadline the run context is cancelled. Sink calls in flight get up to 2 s to stage what they read. Then the writer seals: later calls return `ErrSealed` and are recorded as `dropped: late`. Collectors still running are marked `timeout`.
- **Finalization.** After the seal, a 30 s budget on its own clock covers committing staged items, the footers (`manifest.json`, `errors.txt`, `redactions.json`, `run.log`), the gzip close and the file close.
- **Failure.** A tar, gzip, write or close error, or an overrun finalization budget, is fatal: exit 2. A file output is closed and removed. On stdout, nothing can be removed; stderr says the stream is incomplete.
- **Panics** in a collector are recovered into `failed` with the redacted panic value.

### 8.4 Paths
- `Path` is a list of components. The writer joins them under the archive root.
- Each component is 1–255 bytes of `[A-Za-z0-9._-]`, is not `.` or `..`, and does not start with `.` or `-`.
- A path has at most 8 components. Duplicates, compared case-insensitively, are refused.
- Cluster-scoped objects use `_cluster` as the namespace component. Kubernetes namespaces cannot contain `_`.
- All entries are regular files with mode `0644` inside the `0600` archive.

## 9. Dependencies
None new. `archive/tar` and `compress/gzip` are standard library. Prometheus validation uses `github.com/prometheus/common/expfmt`, already in the module graph through Snellius; it becomes a direct requirement.

## 10. Testing
Boundaries: redaction is proven on the extracted archive written by the production writer; transport at the HTTP/SPDY request; exit codes and partial results at the command.

- **Canary corpus** (`internal/redact/testdata/canary/`), one fixture per row:
  1. `last-applied-configuration` holding a Secret's `data`/`stringData`;
  2. a header name in `managedFields` (dropped);
  3. HTTPRoute `requestHeaderModifier` Authorization value and an `x-api-key` header-match value;
  4. TrafficPolicy header modifier value, `basicAuth.users` hash, a JWT in a transformation template;
  5. an AWS key in a DirectResponse body;
  6. GatewayParameters `env[].value` and a secret in `deploymentOverlay`;
  7. a Deployment/Pod `env[].value` literal;
  8. a `Bearer` token in an Event message;
  9. config dump `request_headers_to_add` Authorization, ext_authz `initial_metadata`, a JWT in the dynamic-module JSON, an API key in Lua `inline_code`;
  10. a Secret-backed custom header (`x-tenant-key`) with a pattern-free value, in the config dump and in a TrafficPolicy;
  11. a symmetric JWK `k` and a private `d` in `local_jwks.inline_string` and in GatewayExtension `jwks.local.inline`; an unparseable JWKS;
  12. an Envoy-redacted field stays `[redacted]`;
  13. Envoy debug log `'authorization'`/`'cookie'` lines;
  14. access log `?access_token=` and `X-Amz-Signature=`;
  15. a controller log PEM private key block and an `sk-…` key; a PEM block cut by a cancelled stream;
  16. a token in error text, in a panic message, and in a malformed JSON body;
  17. a token in a Prometheus label value and comment; a non-Prometheus body on the metrics path;
  18. a downstream collector writing an undeclared type is refused.

  Each case plants a unique value per location. Corpus tests run each redactor. Archive tests send the same fixtures through the production writer, extract the archive and scan every member and member name, including `manifest.json`, `errors.txt`, `run.log` and `redactions.json`: raw bytes, base64-decoded strings and gzip members. `redactions.json` must name each location. Negative cases keep names, hostnames, header names, JWKS public members and cert SANs, and every redacted file re-parses with the same keys.
- **Writer and runner:** budget exhaustion with footers written; a worker returning after the seal; a failing destination removed with exit 2; deterministic layout; path validation; `O_EXCL` and `0600`.
- **Transport:** at the HTTP level, the log query (`sinceTime`, `timestamps`, `tailLines`, `limitBytes`, `previous`, `container`), the metrics route `pods/http:<pod>:<port>/proxy/metrics`, redirect refusal and a typed 403. At the SPDY level, each operation's GET target and refusal of anything else.
- **Command:** envtest with fixtures and seams. One success, one denied and one timed-out item give exit 0 and matching statuses. A reader-only user gets `errors.txt` lines naming `pods/log` and `pods/portforward`. A request recorder shows no Secret or ConfigMap request and no namespaced request outside the read namespaces.
- **Offline use:** an extracted `config_dump.json` is read by `proxy routes --file`. A fixture with two replicas at different `version_text` and listener states keeps both, attributed to the right pod.
- **Live check** (`hack/live-check.sh bundle`): on kind `kgwctl-live` with planted canaries (including a Secret-backed header), a full bundle passes the canary scan and has, per proxy replica, `config_dump.json`, `stats.txt` with `version_text`, and a valid 9091 `metrics.txt`.

## 11. Proposed sub-phases
| Sub-phase | Delivers |
|---|---|
| 4.1 | `pkg/bundle` contract; `internal/redact` with the canary corpus; writer (staging, budget, seal, finalization, paths); runner and manifest; threat model; D-025, D-026 |
| 4.2 | `kgwctl bundle` command and exit codes; scope resolution; replica inventory; `resources`, `workloads`, `install`, `version`, `check`; docs/output.md `BundleManifest` |
| 4.3 | `logs`; `controller-metrics` and `proxy-metrics`; `proxydump` operations; `proxy-admin`; optional log Role in docs/rbac.yaml |
| 4.4 | `pkg/cli.WithBundleCollectors` and the downstream example; README; live check on kind |

## 12. Open items (owner)
1. **Helm values and Secret metadata.** Recommendation: no Secret reads in Phase 4; Envoy `/certs` covers certificate cases. An opt-in needs full Secret read, so it comes later, if support asks.
2. **The `pods/log` grant.** Recommendation: a new optional Role `kgwctl-bundle-logs` next to the port-forward Role. It reads every pod's logs in its namespace. The reader ClusterRole stays as it is.
3. **Header values in `proxy dump` (Phase 3).** `proxy dump` prints the raw config dump, so every header value appears in clear. Recommendation: run the config-dump redactor there too, with `--no-redact` for the owner's own debugging. That changes a shipped command.
4. **kgateway upstream.** `/snapshots/xds` exposes private keys to anyone with port-forward in the install namespace. Recommendation: raise it privately with the maintainers, not in a public issue.
5. **Partial bundle exit code.** Recommendation: 0, with the stderr summary (§3.1).
6. **D-025 and D-026** are proposed, pending approval.

### Calls made in r2
Where a triage ruling left room, r2 takes the safer reading:
1. Without `-n`, the scope is the context's namespace, as in other commands, not all namespaces (P2-1).
2. Install namespaces join the archive scope only for the controller's own objects, logs and metrics (P2-1).
3. Prometheus samples naming out-of-scope namespaces are dropped in a namespaced run (P2-1).
4. Proxy replicas match on the full-name annotation; the label is a marked fallback (P2-2).
5. In-flight Sink calls get 2 s after the deadline to stage what they read before the seal (P2-4, P2-7).
6. Footers are always written, even past the reserve; the excess is recorded (P2-4).
7. Duplicate paths are compared case-insensitively (P2-5).
8. An Event's time also considers `series.lastObservedTime` (P2-7).
9. `server_info.json` is written by `version`, not `proxy-admin`, so it is fetched once (P2-3).
10. `disabled` means the Pod declares no metrics port (P2-6).
11. `--no-endpoints` is documented narrowly: workload IPs stay (P1-5).
12. The JWKS rule also applies to a single JWK, not only a set (P1-2).
13. Resource YAML is filtered by namespace only, not by controller; `--install` and `--all-controllers` narrow controller and proxy captures (P2-1).
