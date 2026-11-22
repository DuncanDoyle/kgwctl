# Phase 4 — Support bundle: Design

Status: r3, 2026-11-22. Applies the Codex review triages (`reviews/design-codex.md`, `reviews/design-codex-r2.md`). For owner approval.
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
- No API request for Secrets or ConfigMaps, enforced on every client (§6.1).
- No controller admin capture. `/snapshots/xds` holds TLS private keys and inline credentials in clear; `/snapshots/krt` can return raw snapshots and ConfigMaps (§6.2).
- No replay system and no analyzer over a saved bundle. The manifest makes both possible later.
- No upload, and no privileges beyond the user's own. kgwctl never creates RBAC objects.

## 2. Evidence
Research notes, not committed: prior art, reuse survey and threat assessment, 2026-11-22. kgateway v2.5.1 read from source, not run.

- **Prior art.** agctl bug-report (agentgateway#3991) is the closest model: kinds by CRD category, 8 workers under one 5 min deadline, partial output with `errors.txt`, 10 MiB per log stream, previous logs only after a restart. It reads full Secret data and writes ConfigMaps unredacted. istioctl bug-report adds a time window and selectors, but dumps every CR unredacted. cilium sysdump keeps its own run log in the archive.
- **kgateway v2.5.1.**
  - Controller: metrics on 9092 (`pods/proxy`), admin on 9095 bound to localhost.
  - Proxy: Envoy admin on 127.0.0.1:19000 (port-forward); 9091 serves `GET /metrics` and, by default, `/stats` (`pods/proxy`). The chart declares container port 9091 only when stats are enabled.
  - Controller pods carry `kgateway=kgateway`. Proxy pods carry `kgateway=kube-gateway`, `gateway.networking.k8s.io/gateway-name` (shortened with a hash past 63 characters) and the annotation `gateway.kgateway.dev/gateway-full-name`.
  - All kgateway CRDs are namespaced and carry the category `kgateway`.
- **Install metadata without Secrets:** the controller Deployment's `helm.sh/chart` and `app.kubernetes.io/version` labels, image tags, `KGW_*` env vars, the GatewayClass and Envoy's `/server_info`. Helm values live only in `helm.sh/release.v1` Secrets.
- **Sensitive data Envoy does not redact:** header values (including values kgateway resolves from `headerModifiers.secretRef`), ext-auth `headers_to_add`, `initial_metadata`, Lua code, transformation templates, and inline JWKS, which kgateway copies to `local_jwks.inline_string` without projecting to public keys. Every object's `last-applied-configuration` annotation copies its whole spec.

## 3. Command

```
kgwctl bundle [-n <ns> | -A] [--install <ns>/<name>] [--all-controllers]
              [--since 24h] [--tail 5000] [--timeout 5m] [-o <file>]
              [--skip <collector>,...] [--max-size 512Mi] [--redact-header <name>,...]
              [--no-endpoints] [--list-collectors]
```

- **Scope:** §3.2.
- **Log window:**
  - One run-start time `T0`. Logs and Events use `since = T0 - --since` for every stream.
  - Log requests send `SinceTime=since`, `Timestamps: true`, `TailLines=--tail` and `LimitBytes` = cap + 64 KiB. kgwctl enforces the 10 MiB cap per stream locally, since `LimitBytes` is approximate.
  - On a cap cut, a cancel or the seal, the incomplete last line is dropped. An unterminated PEM block is redacted to the end. A final line `[kgwctl: truncated: <reason>]` follows.
  - An Event's time is the latest of `lastTimestamp`, `eventTime` and `series.lastObservedTime`. Older Events are left out.
  - `.previous` logs only for containers with `restartCount > 0`. Init containers are included.
- **Deadlines:** `--timeout` (default 5m) is the collection deadline (§8.3). `--request-timeout` (default 30s) bounds one item. A hung pod costs one item, not the run. Ctrl-C acts as the deadline (D-020).
- **`-o`:** default `kgwctl-bundle-<UTC timestamp>.tar.gz` in the current directory, which is also the archive root. The context name is not part of any file or member name. The file is created with `O_CREATE|O_EXCL` and mode `0600` before any cluster call; kgwctl never overwrites. `-o -` writes to stdout.
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

- **Default: all namespaces.** A support bundle is cluster-wide unless narrowed. `-A` is accepted and changes nothing.
- **`-n <ns>`** (scalar; repeatable `-n` stays deferred, D-015): resources, workloads, Events and logs come from that namespace plus each install namespace. Proxy replicas come from that namespace.
- **`--install <ns>/<name>`** selects one install. Without it, every detected install is taken, each recorded, with a stderr warning when there are several. When installs are detected but ownership is unknown, the same applies. No install: a warning, and controller collectors record `skipped: no install`. `--install` naming no detected install: exit 2.
- **`--all-controllers`** keeps proxies of Gateways of any class, limited to Pods with the kgateway proxy label (§8.2).
- **Cluster-scoped kinds:** GatewayClass only, under `_cluster`.

What a namespaced run reads and writes:

| Source | Reads | Written |
|---|---|---|
| Install detection (`load.Load`, as `kgwctl version`) | cluster-wide lists | installs and Gateways in scope only |
| `check` | as `kgwctl check -n <ns>` makes them: cluster-wide | the namespaced report |
| `resources`, `workloads`, `logs` | the namespace and install namespaces | the same |
| Proxy captures and metrics of in-scope proxies and controllers | per replica | whole; they can name other namespaces' routes and backends |

Resource YAML is filtered by namespace only, not by controller. Tests assert the exact request set of a namespaced run (§10).

## 4. Archive layout

```
kgwctl-bundle-<ts>/
  manifest.json            # BundleManifest envelope (§5)
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
    metrics.txt            # /metrics on the install's metrics port
  proxies/<namespace>/<pod>/
    config_dump.json       # redacted (§7)
    stats.txt              # Envoy /stats, incl. text readouts such as version_text
    metrics.txt            # 9091 /metrics
    server_info.json
    certs.json             # SANs, expiry, serials; no keys
    clusters.txt           # health and outlier state; endpoint addresses
    logs/<container>.log, <container>.previous.log
```
The core group is written as `core`. Member names follow §8.4. The four root files are reserved names.

## 5. Manifest (`BundleManifest`, `-o json` contract)
`manifest.json` is a versioned envelope (`schemaVersion`, `kind: BundleManifest`, `item`) like the other kinds in docs/output.md. The plan fixes its JSON fields. It holds:
- the kgwctl build, the context name through the text redactor, and the server as `<apiserver>`;
- flags (below), the resolved `Scope` and the window (`T0`, `since`, `tail`);
- run start, seal and end times, and the finalization outcome;
- the replica inventory (§8.2);
- per collector: name, status (`ok`, `partial`, `failed`, `skipped`, `timeout`), duration, redacted error and a count of `late` results;
- per item: path, size, content type, capture time, source (namespace, pod, UID, container, operation code), status (`ok`, `skipped`, `denied`, `disabled`, `failed`, `timeout`, `dropped`) and redaction count.

Flags are recorded by name. Values appear only for enum, numeric, duration and boolean flags: `-A`, `--all-controllers`, `--since`, `--tail`, `--timeout`, `--request-timeout`, `--max-size`, `--no-endpoints`. All others, including `-n`, `--install`, `--skip`, `-o`, `--kubeconfig`, `--context` and `--redact-header`, appear by name only. The scope carries the validated namespace and install names.

Per replica, the manifest keeps pod identity, restart counts and capture times next to each file. With `version_text`, listener states and rejection details in the captures, that is the raw evidence a later convergence check reads (roadmap, Deferred).

## 6. Inputs and permissions

| Collector | Reads | RBAC | Default |
|---|---|---|---|
| `resources` | Gateway API kinds, CRDs in category `kgateway`, registry kinds | reader ClusterRole | on |
| `workloads` | Deployments, Pods, Services, Events | reader | on |
| `install` | each install's controller Deployment, GatewayClasses | reader | on |
| `version` | discovery server version, install image tags, each proxy replica's `server-info` operation | reader; `pods/portforward` for observed versions | on |
| `check` | its own `kgwctl check` one-shot run with the same `-n` | reader + `pods/proxy` (D-017) | on |
| `logs` | inventory replicas, all containers, previous after a restart | **`pods/log` get (new optional Role)** | on |
| `controller-metrics` | each controller replica's `/metrics` on the install's port | `pods/proxy` | on |
| `proxy-metrics` | each proxy replica's 9091 `/metrics` | `pods/proxy` | on |
| `proxy-admin` | each proxy replica's allowlisted Envoy admin operations (§6.3) | `pods/portforward` (optional Role, D-021) | on |

- A 403 is recorded once per collector and namespace, with the grant to add, and the run continues. A reader-only user gets resources, workloads, install, version without observed Envoy versions, and `check`.
- Metrics use the Snellius `ctrlmetrics.PodProxyScraper` (one GET, no redirects). A transport wrapper keeps a 403 typed and never reads error bodies. Item status: `denied` on 403; `disabled` only for a kgateway-chart proxy Pod without the chart's 9091 port declaration; otherwise the default port is tried and a failure is `failed`.
- No model is shared between collectors.

### 6.1 Secrets, ConfigMaps and Helm values
kgwctl makes no API request for Secrets or ConfigMaps in Phase 4.
- **Enforcement:** one guard `RoundTripper` wraps the `rest.Config` behind every bundle-owned client, including install detection, `check` and downstream collectors' clients from `Env`. It refuses any request for core `secrets` or `configmaps` before it is sent, and the refusal is an error line. The `Object` Sink refuses kinds Secret and ConfigMap. A downstream registry kind resolving to either is thereby blocked.
- Certificate questions are answered without Secrets: Envoy `/certs`, Gateway and listener status, and `check`.
- Helm values exist only inside `helm.sh/release.v1` Secrets. RBAC cannot grant metadata-only Secret reads. Deferred (§12, item 1).

Residual risk: derived artifacts carry values the controller resolved from Secrets, and logs can echo them. Redaction covers known structures and patterns (§7). The README says "It never requests Secrets or ConfigMaps from the API", with this residual risk next to it.

### 6.2 Controller admin
No controller admin endpoint is requested. `/snapshots/xds` inlines listener TLS keys, backend client keys and AWS credentials, some as base64 proto bytes (kgateway v2.5.1 `pkg/kgateway/admin/xds_snapshot.go`, `irtranslator/fc.go`, `backend/aws.go`). `/snapshots/krt` can emit raw snapshots and returns every registered collection, including ConfigMaps. A whole-response capture waits for an audited projection.

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
| `object` | Refuse Secret and ConfigMap. Drop `metadata.managedFields` and the `last-applied-configuration` annotation. Header-value and JWKS rules. Per-kind field rules (`basicAuth.users` hashes, `env[].value` except allowlisted names, `DirectResponse` body). Key-name rule, header-pair rule, then the pattern pass over every remaining string. |
| `config-dump` | Keep Envoy's `[redacted]`. Header-value and JWKS rules. Redact Lua `inline_code`/`source_codes`, and every other `inline_string`/`inline_bytes`. Parse the dynamic-module JSON `StringValue` and apply the rules inside. Then key-name rule and pattern pass. |
| `json` | Key-name rule, header-pair rule, pattern pass. Invalid JSON is dropped with an error. |
| `prometheus` | Must parse as Prometheus text exposition, or it is dropped with an error. Pattern pass on label values and comments. |
| `text` | Debug header-line rule, then the line pattern pass: `Bearer`/`Basic` credentials, JWTs, PEM private-key blocks, AWS keys and signatures, credential query values, known key prefixes. |

- **Header-value rule:** these values are redacted whatever the header's name; names are kept.
  - Envoy: `value` and `raw_value` under `request_headers_to_add`, `response_headers_to_add`, `authorization_request.headers_to_add`, header-mutation `mutations`/`request_mutations`/`response_mutations`, and `initial_metadata`.
  - Objects: `set[]`/`add[]` values of HTTPRoute/GRPCRoute header modifiers, TrafficPolicy `headerModifiers`, every value of the GatewayExtension ext-auth `headersToAdd` map, BackendConfigPolicy `tunnel.headers[].value`, ListenerPolicy header lists and `initialMetadata[].value`.
- **Debug header-line rule:** in Envoy's `'<name>', '<value>'` lines, every value is redacted except for `:authority`, `:method`, `:scheme`, `:status`, `content-type`, `content-length`, `user-agent`, `x-request-id` and `x-envoy-*`. `:path` gets the pattern pass.
- **JWKS rule:** applies to `local_jwks.inline_string` and GatewayExtension `jwks.local.inline`. The value is parsed into a typed public projection per `kty`: RSA `n`, `e`; EC `crv`, `x`, `y`; OKP `crv`, `x`; plus `kid`, `alg`, `use`, `key_ops`, `x5c`, `x5t`, `x5t#S256`. Values must be strings or string arrays. The output is rebuilt as `{"keys":[...]}` from the projection. Any other shape, type or `kty` redacts the whole value.
- **Key-name rule:** a key such as `password`, `secret`, `token`, `apiKey`, `credential`, `privateKey`, `clientSecret`, `sessionToken` or `licenseKey`, with an optional prefix.
- **Header-pair rule:** elsewhere, a `{name|key, value}` pair whose name is on the sensitive-header list has its value redacted: `authorization`, `proxy-authorization`, `cookie`, `set-cookie`, `x-api-key`, `api-key`, `x-auth-token`, `x-amz-security-token`, plus `--redact-header`.
- **Marker:** `[redacted]`. A value stays a string, so files parse with the same keys. A pattern match keeps its prefix (`Bearer [redacted]`). No hashes, no lengths.
- **Report:** `redactions.json` lists per file each rule, location and count. A location is a schema field path with every map key shown as `[key]`, or a line number. It never holds values or map keys.

### 7.1 Metadata and errors
- **Remote errors** (Kubernetes API, pods/proxy, port-forward, Envoy) are recorded as the HTTP status and the Kubernetes reason and code only. Response bodies are never read into an error, including client-go's log-stream errors.
- **Other error text**, including panic values, passes `view.Redactor` and the text pattern pass, then is cut at 512 bytes, before it reaches the manifest, `errors.txt`, `run.log` or stderr. Stack traces never enter the archive.
- **Names:** the archive root has no context name. Path components come only from validated Kubernetes names or constants (§8.4). `Source.Operation` is an operation code. Flag values follow §5.

### 7.2 Limitations (README and threat model)
- Pattern redaction of logs and free text is best effort. It misses secrets in custom formats, application log formats, and base64-encoded or compressed content.
- Opaque extension config (unknown `TypedStruct`/`Any`, Wasm configuration) gets only the pattern pass.
- Stats, metrics and names expose topology and tenant names on purpose. With `-n`, proxy captures, metrics and controller logs can still name other namespaces' routes and backends, and `check` reads cluster-wide.
- `clusters.txt` and the EDS config dump hold endpoint addresses. `--no-endpoints` drops both.
- The bundle is as sensitive as the cluster's configuration. The README tells users to review `redactions.json` and the files before sharing.

### 7.3 Threat model
`docs/bundle-threat-model.md` is written in 4.1 and kept current. It covers assets per artifact class, who receives the archive, what is never requested, redacted or kept, the RBAC used (including the `pods/portforward` power and the log Role reading every pod's logs in its namespace), archive handling and the limits above.

Trust boundary: downstream collectors are trusted in-process code. The Sink and the request guard protect against mistakes, such as an unredacted type, a bad path or a registry kind resolving to Secrets, not against a malicious collector.

## 8. Architecture

### 8.1 Packages
- `pkg/bundle`: the public contract: `Collector`, `Env`, `Sink`, `Path`, `Source`, `Scope`, `Replica`. No Cobra imports (D-019).
- `internal/bundle`: the runner, writer, staging, manifest, request guard and replica inventory.
- `internal/bundle/collect`: the built-in collectors.
- `internal/redact`: redactors per content type. Pure functions with their own canary corpus; no dependency on `bundle`.
- `internal/proxydump`: the allowlisted operations (§6.3). `Fetch` stays the `config-dump` operation.
- `internal/app`: the command, scope resolution and the `check` collector, which calls the same one-shot path as `kgwctl check`.
- `pkg/cli.WithBundleCollectors(...bundle.Collector)` adds downstream collectors; `examples/downstream` adds one, built in CI.

A collector is `Name() string` and `Collect(ctx, Env, Sink) error`. `Env` gives guarded clients, the `Scope`, the window, the inventory, the clock and the per-item timeout.

### 8.2 Replica inventory
Built once, after scope resolution and before any collector. Logs, metrics, admin captures and `version` read it; none lists Pods itself.

- **Controller replicas:** Pods in each install's namespace that match its Deployment's `spec.selector`. Each carries the install's resolved metrics port (default 9092).
- **Proxy replicas:** Pods labelled `kgateway=kube-gateway` in scope.
  - A Pod belongs to the Gateway its `gateway.kgateway.dev/gateway-full-name` annotation names.
  - Without the annotation, the `gateway-name` label is used only when the Pod's owner chain leads to the Gateway's Deployment (§12, call 2). Otherwise the Pod is skipped with an error line.
  - Only Gateways whose class belongs to a selected install are kept, unless `--all-controllers` is set.
  - Each carries its metrics port and the evidence: `chart-port`, `no-chart-port` or `default`.
- **Each `Replica`** holds namespace, pod, UID, role, owner (install or Gateway `<ns>/<name>`), identity source (`annotation`, `owner-chain`, `selector`), ready, terminating, phase, containers with restart counts (init included), start time and metrics port.
- Unready, terminating and restarting replicas stay in. A Pod that is not `Running` skips metrics and admin (`skipped: not running`) but still gets logs.
- **Identity check:** after each capture the Pod is read again. A changed UID marks that item `identity: changed`.

### 8.3 Staging, budget, sealing and finalization
- **Item caps:** logs 10 MiB, config dump 64 MiB, metrics 8 MiB, others 16 MiB. A text item over its cap is cut at a line boundary (§3). A structured item over its cap is dropped with an error.
- **Staging:** a structured item is staged once complete and redacted. A text item is staged line by line, so a cut or sealed stream keeps its complete lines.
- **Commit and budget:** one writer goroutine owns the tar and gzip streams. Items commit in a fixed order: collector registration order, then path. `--max-size` is applied in that order, so the kept set does not depend on timing. Items past it are `dropped: size budget`. Staged memory is bounded at 2 × `--max-size`; an item past that bound is `dropped: staging`, the one timing-dependent drop.
- **Footers** have fixed caps inside a 10 MiB reserve: `manifest.json` 4 MiB, `redactions.json` 4 MiB, `errors.txt` 1 MiB, `run.log` 1 MiB. Past its cap, a JSON footer omits per-item entries and records how many; a text footer is cut with a marker.
- **Seal:** at the collection deadline the run context is cancelled and all staged state is frozen. Results that arrive later are not written; each collector's manifest entry counts them as `late`. Collectors still running are `timeout`.
- **Finalization:** after the seal, commits and footers run on a 30 s budget of their own. For a file output this bound holds. A blocked stdout write (a stalled reader) can hold the process; this is documented, and Ctrl-C ends it with exit 2.
- **Failure:** a tar, gzip, write or close error, or an overrun budget, is fatal: exit 2. A file output is closed and removed. `Run` returns the commit error.
- **Panics** in a collector are recovered into `failed` with the redacted panic value.

### 8.4 Paths
- `Path` is a list of components joined under the archive root.
- A component is a validated Kubernetes name (namespace, object, container, API group or kind), optionally with a fixed file extension, or a constant from `pkg/bundle`. Each is 1–255 bytes, not `.` or `..`, and does not start with `.` or `-`. A path has at most 8 components.
- The first path to commit wins. A later path equal to it, compared case-insensitively, is rejected and recorded. The four root footer names are reserved.
- Cluster-scoped objects use `_cluster` as the namespace component. Kubernetes namespaces cannot contain `_`.
- All entries are regular files with mode `0644` inside the `0600` archive.

## 9. Dependencies
None new. `archive/tar` and `compress/gzip` are standard library. Prometheus validation uses `github.com/prometheus/common/expfmt`, already in the module graph through Snellius; it becomes a direct requirement.

## 10. Testing
Boundaries: redaction is proven on the extracted archive written by the production writer; transport at the HTTP/SPDY request; exit codes and partial results at the command, with the real request adapters.

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
  10. a Secret-backed custom header (`x-tenant-key`) with a pattern-free value, in the config dump, a TrafficPolicy and an Envoy debug header line;
  11. pattern-free values in GatewayExtension ext-auth `headersToAdd`, Envoy `authorization_request.headers_to_add`, BackendConfigPolicy `tunnel.headers` and ListenerPolicy `initialMetadata`;
  12. JWKS: a symmetric `k`, a private `d`, a nested `d` inside `n`, an extra member on the set, valid JSON of the wrong shape, and invalid JSON, in both locations;
  13. an Envoy-redacted field stays `[redacted]`;
  14. Envoy debug log `'authorization'`/`'cookie'` lines;
  15. access log `?access_token=` and `X-Amz-Signature=`;
  16. a controller log PEM private key block and an `sk-…` key; a PEM block cut by a cancelled stream;
  17. a token in error text, a panic message, a malformed JSON body and a non-200 log-stream body;
  18. a token in a Prometheus label value and comment; a non-Prometheus body on the metrics path;
  19. metadata: a token in the context name, a `--redact-header` value, an `-o` path and a map key;
  20. a Secret and a ConfigMap handed to `Object`, and a downstream kind resolving to `secrets`;
  21. a downstream collector writing an undeclared type.

  Corpus tests run each redactor. The full corpus is also replayed through the production writer; the extracted archive is scanned in every member and member name, including the footers: raw bytes, base64-decoded strings and gzip members. Extraction fails on a duplicate header. `redactions.json` must name each location. Negative cases keep names, hostnames, header names, JWKS public members and cert SANs, and every redacted file re-parses with the same keys.
- **Writer and runner:** budget order with footers; footer caps; a result after the seal counted as `late`; duplicate paths across collectors and a reserved name; a failing destination removed with exit 2; `O_EXCL` and `0600`.
- **Transport:** at the HTTP level, the log query, the metrics route `pods/http:<pod>:<port>/proxy/metrics`, redirect refusal, a typed 403 and the guard refusing `secrets` and `configmaps`. At the SPDY level, each operation's GET target, redirect refusal with a response-configurable fixture, and cancelling an established capture through a CONNECT or SOCKS5 proxy (Phase 3 carry-over).
- **Command:** envtest with fixtures and the real log, scraper and port-forward adapters behind testenv proxy rules. One success, one denied and one timed-out item give exit 0 and matching statuses, and each denial rule is hit. The request recorder shows the exact request set of a namespaced run and no Secret or ConfigMap request.
- **Offline use:** an extracted `config_dump.json` is read by `proxy routes --file`. Two replicas at different `version_text` and listener states keep both, attributed to the right pod.
- **Live check** (`hack/live-check.sh bundle`): on kind `kgwctl-live` with planted canaries (including a Secret-backed header), a full bundle passes the canary scan and has, per proxy replica, `config_dump.json`, `stats.txt` with `version_text`, and a valid 9091 `metrics.txt`.

## 11. Proposed sub-phases
| Sub-phase | Delivers |
|---|---|
| 4.1 | `pkg/bundle` contract; manifest schema; `internal/redact` with the canary corpus; writer; runner; threat model; D-025, D-026 |
| 4.2 | `kgwctl bundle` command and exit codes; request guard, scope and inventory; `proxydump` operations; `resources`, `workloads`, `install`, `version`, `check` |
| 4.3 | `logs`; `controller-metrics` and `proxy-metrics`; `proxy-admin`; optional log Role; command-level partial runs |
| 4.4 | `pkg/cli.WithBundleCollectors` and the downstream example; README; live check on kind |

## 12. Open items (owner)
1. **Helm values and Secret metadata.** Recommendation: no Secret reads in Phase 4; Envoy `/certs` covers certificate cases. An opt-in needs full Secret read, so it comes later, if support asks.
2. **The `pods/log` grant.** Recommendation: a new optional Role `kgwctl-bundle-logs` next to the port-forward Role. It reads every pod's logs in its namespace. The reader ClusterRole stays as it is.
3. **Header values in `proxy dump` (Phase 3).** `proxy dump` prints the raw config dump, so every header value appears in clear. Recommendation: run the config-dump redactor there too, with `--no-redact` for the owner's own debugging. That changes a shipped command.
4. **kgateway upstream.** `/snapshots/xds` exposes private keys to anyone with port-forward in the install namespace. Recommendation: raise it privately with the maintainers, not in a public issue.
5. **Partial bundle exit code.** Recommendation: 0, with the stderr summary (§3.1).
6. **D-025 and D-026** are proposed, pending approval.

### Calls made
Where a triage ruling left room, the design takes the safer reading:
1. Duplicate paths are compared case-insensitively (r2).
2. The owner chain is checked without reading ReplicaSets, which the reader role cannot: the Pod's controller is a ReplicaSet named `<deployment>-<hash>`, the Pod matches that Deployment's selector, and the Deployment has a controller ownerReference to the Gateway (unverified that the deployer sets it) (r3, P2-1).
3. An Event's time also considers `series.lastObservedTime` (r2).
4. `server_info.json` is written by `version`, not `proxy-admin`, so it is fetched once (r2).
5. `--no-endpoints` is documented narrowly: workload IPs stay (r2).
6. Resource YAML is filtered by namespace only, not by controller (r2).
7. Installs detected with unknown ownership are all taken, as with several installs (r3, P1-5).
8. A kgateway-chart proxy Pod is one carrying `kgateway=kube-gateway` and the full-name annotation (r3, P2-5).
9. Map keys appear in redaction locations as the literal `[key]`, never the key itself (r3, P1-3).
10. `--skip` values are recorded by name only; skipped collectors show in their status (r3, P1-3).
11. A text item stages line by line, so a stream cut by the seal keeps its complete lines (r3, P2-2).
12. Staged memory is bounded at 2 × `--max-size`; past it, items are `dropped: staging`, the one timing-dependent drop (r3, P2-2).
13. The footer reserve is 10 MiB, the sum of the footer caps. An oversized JSON footer omits per-item entries and records the count (r3, P2-2).
