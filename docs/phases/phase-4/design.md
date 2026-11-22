# Phase 4 — Support bundle: Design

Status: r6, 2026-11-22. Applies the Codex review triages (`reviews/design-codex.md`, `-r2.md`, `-r3.md`, `-r4.md`). r6 adds the owner's ownership and discovery-scope filters and `--skip logs` (§3, §3.2); §12 approved.
Roadmap: Phase 4. Decisions: D-009, D-010, D-015, D-017, D-019, D-021, D-024; D-025, D-026.

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
  - Controller: metrics on 9092 (`pods/proxy`), admin on 9095, bound to localhost by default (`AdminBindAddress` makes it configurable; no auth).
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
- `--skip` turns off collectors by name. `--skip logs` leaves out every controller and proxy log; the collector is recorded `skipped: flag`, with no line in `errors.txt` and no `pods/log` request. `--list-collectors` prints the names and exits.
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
- **Ownership (default on).** Resources follow the rule `get` uses (Phase 1 design §6.2: `view.Filter` over the ownership Snellius `install.Assign` computes), with the filter's own namespace check off; the bundle bounds namespaces itself. A GatewayClass or Gateway of another controller is left out, as is a route whose spec parentRefs all name left-out parents, and a policy whose ancestors name only other controllers. With `--install`, objects owned only by other installs are left out too. When ownership is unknown, nothing is left out and a stderr warning says so.
- **Discovery scope (default on).** Each install's scope is the Snellius `install.Scope` on the loaded model (`Model.Installs[i].Resolved.Scope`), read from the controller's `KGW_DISCOVERY_NAMESPACE_SELECTORS` entry on a Pod or, without one, the Deployment template; no Helm values. Reading the env entry mirrors the controller's logic (program mirror M-4); it retires when kgateway serves the discovery scope read-only (U12, [kgateway#15070](https://github.com/kgateway-dev/kgateway/issues/15070)). A namespaced object is kept when any selected install watches its namespace. The rule is per namespace, never per owner, so status, delegation and ambiguous ownership cannot change it. With several installs and no `--install`, scope is their union: a route in a namespace only install B watches stays in, even if its parent is install A's; `--install` narrows it. Cluster-scoped kinds are never scope-filtered. Nothing is scope-filtered when an install's scope is `ScopeUnknown` (no controller Pod or Deployment, `envFrom` only, `valueFrom`, invalid JSON), or when Namespace objects could not be read (`load.Result.Inputs`); the reason is recorded.
- **Recorded scope.** Per install, the manifest records the mode (`all`, `selectors`, `unknown`, `namespaces-unreadable`; never written as all), the selectors rendered from the parsed `LabelSelector`s, their source, and the names of namespaces it does not watch (bounded, with an omitted count). Routes left out by scope are counted, not listed. `check` reports scope per install (KGWUI-004): related evidence for "my route is ignored", not a list of what the bundle left out. With the union rule, a route can be bundled and still be named by KGWUI-004 for another install. Selectors are capped at 64 per install and 256 bytes each, with an omitted count. `excluded` holds at most 1024 kind and reason entries, the rest summed as one `other` entry per reason, and is never cut.
- **Exclusion counts.** Each left-out object counts once per kind and reason (`ownership` or `scope`); no names.
- **Workload namespaces.** Deployments, Pods, Services and Events come from the install namespaces, the namespaces of in-scope proxy replicas, the namespaces of kept namespaced resources, and the namespaces of Services that kept routes name in `backendRefs`. Not every namespace in the cluster. Workloads are filtered per namespace, not per object: another controller's Deployment in a shared namespace is collected.
- **Proxies** are those of kept Gateways (§8.2).
- **`--all-controllers`** turns ownership and discovery scope off: resources and workloads from every namespace in the run's namespace bound, and proxies of Gateways of any class, limited to Pods with the kgateway proxy label (§8.2). Scope is still recorded.
- **`-n` always bounds.** With `-n`, no flag widens resources, workloads, Events or logs past that namespace plus install namespaces; a `backendRefs` Service elsewhere is not collected.
- **Cluster-scoped kinds:** GatewayClass only, under `_cluster`.

What a namespaced run reads and writes:

| Source | Reads | Written |
|---|---|---|
| Install detection (`load.Load`, as `kgwctl version`) | cluster-wide lists and watches (permitted) | installs and Gateways in scope only |
| `check` | as `kgwctl check -n <ns>` makes them: cluster-wide | the namespaced report |
| `resources`, `workloads`, `logs` | the namespace and install namespaces | the same, after the ownership and scope filters |
| Proxy captures and metrics of in-scope proxies and controllers | per replica | whole; they can name other namespaces' routes and backends |

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
- flags (below), the resolved `Scope` with each install's discovery scope (§3.2), and the window (`T0`, `since`, `tail`);
- `excluded`: counts of objects left out, per kind and reason (`ownership`, `scope`); no names;
- per collector, a reason for `skipped` (`flag`, `no install`);
- run start, seal and end times, and the finalization outcome;
- the replica inventory (§8.2);
- per collector: name, status (`ok`, `partial`, `failed`, `skipped`, `timeout`), duration, redacted error and a count of `late` results;
- per item: path, size, content type, capture time, source (namespace, pod, UID, container, operation code, identity outcome per §8.2), status (`ok`, `skipped`, `denied`, `disabled`, `failed`, `timeout`, `dropped`) and redaction count.

Flags are recorded by name. Values appear only for enum, numeric, duration and boolean flags: `-A`, `--all-controllers`, `--since`, `--tail`, `--timeout`, `--request-timeout`, `--max-size`, `--no-endpoints`. All others, including `-n`, `--install`, `--skip`, `-o`, `--kubeconfig`, `--context` and `--redact-header`, appear by name only. The scope carries the validated namespace and install names.

Per replica, the manifest keeps pod identity, restart counts and capture times next to each file. With `version_text`, listener states and rejection details in the captures, that is the raw evidence a later convergence check reads (roadmap, Deferred).

## 6. Inputs and permissions

| Collector | Reads | RBAC | Default |
|---|---|---|---|
| `resources` | Gateway API kinds, CRDs in category `kgateway`, registry kinds; ownership and scope filtered (§3.2) | reader ClusterRole | on |
| `workloads` | Deployments, Pods, Services, Events in the workload namespaces (§3.2) | reader | on |
| `install` | each install's controller Deployment, GatewayClasses | reader | on |
| `version` | discovery server version, install image tags, each proxy replica's `server-info` operation | reader; `pods/portforward` for observed versions | on |
| `check` | its own `kgwctl check` one-shot run with the same `-n` | reader + `pods/proxy` (D-017) | on |
| `logs` | inventory replicas, all containers, previous after a restart | **`pods/log` get (new optional Role)** | on |
| `controller-metrics` | each controller replica's `/metrics` on the install's port | `pods/proxy` | on |
| `proxy-metrics` | each proxy replica's 9091 `/metrics` | `pods/proxy` | on |
| `proxy-admin` | each proxy replica's allowlisted Envoy admin operations (§6.3) | `pods/portforward` (optional Role, D-021) | on |

- A 403 gives each affected item the status `denied`, and the run continues. `errors.txt` ends with grant lines, one per grant and namespace, whichever collectors hit it. A reader-only user gets resources, workloads, install, version without observed Envoy versions, and `check`.
- Metrics use the Snellius `ctrlmetrics.PodProxyScraper` (one GET, no redirects). The guard classifies a pods/proxy 502/503 first: it reads at most 4 KiB of the body for "connection refused" and sets a typed `Refused` marker, then replaces the body (§7.1). The body is never stored. A 403 stays typed. Proxy metrics always try 9091. Item status: `denied` on 403; `disabled` only when the connection is refused and the Pod declares no 9091 port; `failed` otherwise.
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
| `object` | Refuse Secret, ConfigMap, SecretList and ConfigMapList in any API group, also inside a List; each List member gets its own kind's rules. Drop `metadata.managedFields` and the `last-applied-configuration` annotation. Header-value and JWKS rules. Per-kind field rules (`basicAuth.users` hashes, `env[].value` except allowlisted names, `DirectResponse` body). Key-name rule, header-pair rule, then the pattern pass over every remaining string. |
| `config-dump` | Keep Envoy's `[redacted]`. Header-value and JWKS rules (also on base64 `inline_bytes` under `local_jwks`). Redact Lua `inline_code`/`source_codes`, and every other `inline_string`/`inline_bytes`. Parse the dynamic-module JSON `StringValue` and apply the rules inside. Then key-name rule and pattern pass. |
| `json` | Key-name rule, header-pair rule, pattern pass. Invalid JSON is dropped with an error. |
| `prometheus` | Must parse as Prometheus text exposition, or it is dropped with an error. Pattern pass on label values, `HELP` text and other comments, read with the parser's grammar (leading blanks, tabs); `TYPE` lines stay. The output is parsed again and dropped with an error if it fails. |
| `text` | Debug header-line rule, then the line pattern pass: `Bearer`/`Basic` credentials (after an `authorization` name, any Basic value), JWTs, PEM private-key blocks, AWS keys and signatures, credential query values, known key prefixes. |

- **Header-value rule:** these values are redacted whatever the header's name; names are kept.
  - Envoy: `value` and `raw_value` under `request_headers_to_add`, `response_headers_to_add`, `headers_to_add` under any parent, header-mutation `mutations`/`request_mutations`/`response_mutations`, and `initial_metadata`.
  - Objects, by field name in any kind: values under any `*HeaderModifier` field, `headerModifiers` and `initialMetadata`, every value of a `headersToAdd` map, and `headers` under `tunnel` or in any `*ListenerPolicy` kind.
- **Debug header-line rule:** in Envoy's `'<name>', '<value>'` lines, every value is redacted except for `:authority`, `:method`, `:scheme`, `:status`, `:path`, `content-type`, `content-length`, `user-agent`, `x-request-id` and `x-envoy-*`. Kept values get the pattern pass. A `--redact-header` name is redacted even when it is on that list.
- **JWKS rule:** applies to `local_jwks.inline_string` and GatewayExtension `jwks.local.inline`. The value is parsed into a typed public projection per `kty`: RSA `n`, `e`; EC `crv`, `x`, `y`; OKP `crv`, `x`; plus `kid`, `alg`, `use`, `key_ops`, `x5c`, `x5t`, `x5t#S256`. Values must be strings or arrays of strings; `null` is refused. The output is rebuilt as `{"keys":[...]}` from the projection. Any other shape, type or `kty` redacts the whole value.
- **Key-name rule:** a key that ends, after lowercasing and dropping `_`, `-` and `.`, in `password`, `passwd`, `secret`, `token`, `apikey`, `credential`, `privatekey`, `accesskey`, `secretkey`, `signature` or `licensekey`, or a plural. Strings, numbers and booleans below it are redacted, except reference members such as `name` and `kind`. Numbers under the token-bucket fields `max_tokens` and `tokens_per_fill` stay unless a credential-named key is above them; a header name never gets this exception.
- **Header-pair rule:** elsewhere, a `{name|key, value}` pair whose name is on the sensitive-header list has its value redacted: `authorization`, `proxy-authorization`, `cookie`, `set-cookie`, `x-api-key`, `api-key`, `x-auth-token`, `x-amz-security-token`, any key-name rule match, plus `--redact-header`. Envoy header matcher members (`exact_match`, `prefix_match` and others) count as values.
- **Per-kind field rules:** `env[].value` is kept only for names starting with `KGW_` and a fixed list of runtime names (`LOG_LEVEL`, `POD_NAME`, `GOMAXPROCS` and others).
- **Marker:** `[redacted]`. A value stays a string, so files parse with the same keys. A pattern match keeps its prefix (`Bearer [redacted]`). No hashes, no lengths.
- **Report:** `redactions.json` lists per file each rule, location and count. A location is a field path or a line number. Only a fixed set of schema field names appears literally; every other key is `[key]` and array elements are `[]`. Every key below a map field (`selector`, `labels`, `annotations`, `data`, Envoy `node.metadata` and others) is `[key]`, even one that equals a schema field name. A location is capped at 512 bytes, cut in the middle with `[...]`. Rules match the real key path, never the displayed one. It never holds values or map keys.
- **Types:** the Sink has one typed method per content type and no raw write, so an undeclared type cannot reach the writer.

### 7.1 Metadata and errors
- **Remote errors** (Kubernetes API, pods/proxy, port-forward, Envoy) are recorded as the HTTP status and the Kubernetes reason and code only, with the operation for non-Kubernetes errors. Only canonical `metav1.StatusReason` values are shown; any other reason gives `request failed (<code>)`. Response bodies never reach an error. This holds for client-go's log-stream errors, `proxydump`'s port-forward status errors in every command (`proxy dump` keys its RBAC hint on the typed Forbidden, so its messages stay the same), and the `check` collector, which sanitizes errors before the reused check path flattens them into `Input.Reason`.
- **Other error text**, including panic values, passes `view.Redactor` and the text pattern pass, then is cut at 512 bytes; the whole text is redacted before the cut, and text over 64 KiB is replaced by `[error text too long]`, before it reaches the manifest, `errors.txt`, `run.log` or stderr. Stack traces never enter the archive.
- **Names:** the archive root has no context name. Path components come only from validated Kubernetes names or constants (§8.4). `Source.Operation` is an operation code. The Sink rejects a `Source.Identity` or exclusion reason outside its constants and records the failure without the value. Flag values follow §5.

### 7.2 Limitations (README and threat model)
- Pattern redaction of logs and free text is best effort. It misses secrets in custom formats, application log formats, and base64-encoded or compressed content.
- Opaque extension config (unknown `TypedStruct`/`Any`, Wasm configuration) gets only the pattern pass.
- Stats, metrics and names expose topology and tenant names on purpose. With `-n`, proxy captures, metrics and controller logs can still name other namespaces' routes and backends, and `check` reads cluster-wide.
- `clusters.txt` and the EDS config dump hold endpoint addresses. `--no-endpoints` drops both.
- The bundle is as sensitive as the cluster's configuration. The README tells users to review `redactions.json` and the files before sharing.

### 7.3 Threat model
`docs/bundle-threat-model.md` is written in 4.1 and kept current. It covers assets per artifact class, who receives the archive, what is never requested, redacted or kept, the RBAC used (including the `pods/portforward` power and the log Role reading every pod's logs in its namespace), archive handling and the limits above. Trust boundary: downstream collectors are trusted in-process code. The Sink and the request guard protect against mistakes, such as an unredacted type, a bad path or a registry kind resolving to Secrets, not against a malicious collector.

## 8. Architecture

### 8.1 Packages
- `pkg/bundle`: the public contract: `Collector`, `Env`, `Sink`, `Path`, `Source`, `Scope`, `Replica`. No Cobra imports (D-019).
- `internal/bundle`: the runner, writer, staging, manifest, request guard and replica inventory.
- `internal/bundle/collect`: the built-in collectors.
- `internal/redact`: redactors per content type. Pure functions with their own canary corpus; no dependency on `bundle`.
- `internal/proxydump`: the allowlisted operations (§6.3). `Fetch` stays the `config-dump` operation.
- `internal/app`: the command, scope resolution and the `check` collector, which calls the same one-shot path as `kgwctl check`.
- `pkg/cli.WithBundleCollectors(...bundle.Collector)` adds downstream collectors; `examples/downstream` adds one, built in CI.

A collector is `Name() string` and `Collect(ctx, Env, Sink) error`. `Env` gives guarded clients, the `Scope`, the window, the inventory, the clock, the per-item timeout and `Keep(obj) ExcludeReason`, the §3.2 ownership and scope filter, so downstream collectors filter the same way. A collector reports each left-out object with `Sink.Exclude(groupKind, reason)`; counts are frozen at the seal like other Sink calls.

### 8.2 Replica inventory
Built once, after scope resolution and before any collector. Logs, metrics, admin captures and `version` read it; none lists Pods itself.

- **Controller replicas:** Pods in each install's namespace that match its Deployment's `spec.selector`. Each carries the install's resolved metrics port (default 9092).
- **Proxy replicas:** Pods labelled `kgateway=kube-gateway` in scope.
  - A Pod belongs to the Gateway its `gateway.kgateway.dev/gateway-full-name` annotation names.
  - A Pod without the annotation is recorded with `identity: unverified` and gets no capture. The reader role cannot read ReplicaSets, so the owner chain cannot be verified.
  - Only Gateways the §3.2 ownership and scope filters keep get proxies captured, unless `--all-controllers` is set.
- **Each `Replica`** holds namespace, pod, UID, role, owner (install or Gateway `<ns>/<name>`), identity (`annotation`, `selector`, `unverified`), ready, terminating, phase, containers with restart counts (init included), start time, metrics port and whether the Pod declares it.
- Unready, terminating and restarting replicas stay in. A Pod that is not `Running` skips metrics and admin (`skipped: not running`) but still gets logs.
- **Identity check:** after each log, metrics, version or admin capture the Pod is read again. The outcome goes in the item's `Source.Identity`: `verified` (same UID), `changed`, `gone` or `reread-failed`.

### 8.3 Staging, budget, sealing and finalization
- **Item caps:** logs 10 MiB, config dump 64 MiB, metrics 8 MiB, others 16 MiB. A text item over its cap is cut at a line boundary (§3). A structured item over its cap is dropped with an error.
- **Staging:** items are redacted before staging. A structured item is staged once complete; a text item line by line, so a cut or sealed stream keeps its complete lines. The writer, not the collector, appends truncation markers.
- **Spool:** staged bytes use a fixed 64 MiB RAM budget. Past it, the already-redacted bytes spill to a `0600` file in a `0700` temp directory. Spool and index are bounded by `--max-size`; an item past that bound is `dropped: staging`, the one timing-dependent drop. The spool is removed on success, cancel and failure.
- **Commit and budget:** one writer goroutine owns the tar and gzip streams. Items commit in a fixed order: collector registration order, then path. `--max-size` (minimum `32Mi`) covers items and footers together: items get `--max-size` minus the footer reserve, applied in that order, so the kept set does not depend on timing. Items past it are `dropped: size budget`. Each item keeps at most 4096 distinct findings, summed per rule and location as they arrive; the rest are counted as omitted in `redactions.json`.
- **Footers** have a reserve inside `--max-size`, the sum of their caps, at most a quarter of `--max-size`: at the default, `manifest.json` 4 MiB, `redactions.json` 4 MiB, `errors.txt` 1 MiB, `run.log` 1 MiB, scaled down for smaller sizes. Every manifest list (items, replicas, collectors) is bounded and records an omitted count. Footers are built within their caps. A manifest still over its cap after the minimal fallback is replaced by a fixed placeholder, noted in `errors.txt`. A text footer is cut with a marker. At most 4096 items per collector that are not `ok` are recorded; the rest are counted as omitted.
- **Bounded encoding:** JSON and config dumps whose indented form would pass the item cap are written compact. An object whose YAML indentation alone would pass the cap is `dropped: size cap` before it is encoded. A text item reserves spool room for its truncation line.
- **Seal:** at the collection deadline the run context is cancelled and all staged state is frozen. Sink calls still in flight at that moment are counted per collector as `late`; their results are not written, except that a text item keeps its staged lines with a marker. Collectors still running are `timeout`. A collector not yet started is never started; it is `timeout` too.
- **Result:** `Run` returns the manifest as written to `manifest.json`: post-budget item statuses, collector status after commit (`partial` when an item did not commit), redacted and bounded.
- **Finalization:** the deadline guarantee covers collection. Finalization is not cancellable; it is bounded by sizes, since it writes at most `--max-size` in total. A stalled stdout reader can still hold the process; this is documented.
- **Failure:** a tar, gzip, write, close or footer encoding error, or a panic while sealing or finalizing, is fatal: exit 2. A file output is closed and removed. `Run` returns the commit error.
- **Panics** in a collector are recovered into `failed` with the redacted panic value.

### 8.4 Paths
- `Path` is a list of components joined under the archive root.
- A component is a constant from `pkg/bundle` or a name in Kubernetes grammar (DNS-1123 subdomain or label, or kind), optionally with one fixed file extension; `ValidatePath` enforces this. Each is 1–255 bytes. `_` appears only in constants. A path has at most 8 components.
- The grammar cannot prove a name came from the API. Collectors must pass real Kubernetes names, never captured data.
- Paths are resolved at commit, in commit order: the first to commit wins. A later path equal to it ignoring case, or that is a file or directory prefix of it (`a/b` and `a/b/c`), is dropped and recorded. An item dropped by the budget claims no path. An exact repeat within one collector is rejected when it begins. The four root footer names are reserved.
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
  17. a token in error text, a panic message and a malformed JSON body; a pattern-free body on a non-200 log stream, a port-forward denial and a denied list seen by the bundle `check` path;
  18. a token in a Prometheus label value and comment; a non-Prometheus body on the metrics path;
  19. metadata: a token in the context name, a `--redact-header` value, an `-o` path and a map key;
  20. a Secret and a ConfigMap handed to `Object`, and a downstream kind resolving to `secrets`;
  21. a downstream collector writing an undeclared type.

  Corpus tests run each redactor. Every row that is a Sink input is also replayed through the production writer (4.1.4); flag recording, registry resolution and transport denials are gated where the command and adapters exist (4.2.5, 4.3.x). In both, the extracted archive is scanned in every member and member name, including the footers: raw bytes, base64-decoded strings and gzip members. Extraction fails on a duplicate header. `redactions.json` must name each location. Negative cases keep names, hostnames, header names, JWKS public members and cert SANs, and every redacted file re-parses with the same keys.
- **Writer and runner:** budget order; footer caps at `32Mi` and the default; bounded manifest lists; spool permissions and removal on success, cancel and failure; in-flight calls at the seal counted as `late`; writer-appended truncation markers; duplicate paths across collectors and a reserved name; a failing destination removed with exit 2; `O_EXCL` and `0600`. No test asserts a finalization timeout, since finalization is bounded by size only.
- **Transport:** at the HTTP level, the log query, the metrics route `pods/http:<pod>:<port>/proxy/metrics`, redirect refusal, a typed 403 and the guard refusing `secrets` and `configmaps`. At the SPDY level, each operation's GET target, redirect refusal with a response-configurable fixture, and cancelling an established capture through a CONNECT or SOCKS5 proxy (Phase 3 carry-over).
- **Command:** envtest with fixtures and the real log, scraper and port-forward adapters behind testenv proxy rules. One success, one denied and one timed-out item give exit 0 and matching statuses, and each denial rule is hit. Two collectors denied `pods/portforward` in one namespace give two `denied` items and one grant line. Once the command is wired, the full metadata canaries, a registry kind resolving to `secrets`, and the exact request set of a namespaced run are asserted there.
- **Identity and metrics:** a Pod replaced, deleted or unreadable after capture gives `changed`, `gone` or `reread-failed`; a label-only Pod is `unverified` with no capture; a Pod with the stock markers, no 9091 declaration and a working endpoint is scraped `ok`.
- **Ownership and scope:** a second controller's GatewayClass, Gateway and route are absent, and present with `--all-controllers`; workloads follow namespaces, so a shared namespace keeps both controllers' Deployments. A selector excluding `team-b` leaves out its resources, records `team-b` by name and counts its route under `scope`; `check.json` has KGWUI-004 for it. An ambiguous Gateway (two installs claim it) keeps its proxy captures. An `envFrom` controller records `unknown`, and an unreadable Namespace list records `namespaces-unreadable`; neither filters by scope. `-n` is never widened. `--skip logs` gives no `logs/` member, no `pods/log` request and no `errors.txt` line.
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
All six approved as recommended by the owner, 2026-11-22.

1. **Helm values and Secret metadata.** Recommendation: no Secret reads in Phase 4; Envoy `/certs` covers certificate cases. An opt-in needs full Secret read, so it comes later, if support asks.
2. **The `pods/log` grant.** Recommendation: a new optional Role `kgwctl-bundle-logs` next to the port-forward Role. It reads every pod's logs in its namespace. The reader ClusterRole stays as it is.
3. **Header values in `proxy dump` (Phase 3).** `proxy dump` prints the raw config dump, so every header value appears in clear. Recommendation: run the config-dump redactor there too, with `--no-redact` for the owner's own debugging. That changes a shipped command.
4. **kgateway upstream.** `/snapshots/xds` exposes private keys to anyone with port-forward in the install namespace. Recommendation: raise it privately with the maintainers, not in a public issue.
5. **Partial bundle exit code.** Recommendation: 0, with the stderr summary (§3.1).
6. **D-025 and D-026** are proposed, pending approval.

### Calls made
Where a triage ruling left room, the design takes the safer reading:
1. Duplicate paths are compared case-insensitively. An Event's time also considers `series.lastObservedTime` (r2).
2. `server_info.json` is written by `version` only. `--no-endpoints` keeps workload IPs. Resource YAML was filtered by namespace only (r2); r6 adds the ownership and discovery-scope filters at the owner's request (§3.2).
14. Ownership reuses `view.Filter`, and `--all-controllers` turns off both filters; there is no separate scope flag. Bundle visibility follows spec `parentRefs` (`view.Filter`). Snellius may also assign a delegated child route through status; that affects only `check` findings, not bundle membership. Discovery scope is per namespace over the union of selected installs, not per owning install: the per-owner rule let delegation status and ambiguity change membership (r6, review r6).
15. Objects in unwatched namespaces are left out, not kept as evidence. Their routes are counted, not listed: `engine.Model` does not expose Snellius's assignment; `check` reports per-install scope separately (KGWUI-004) (r6, review r5).
16. Workloads are filtered per namespace, not per object; ownership of a Deployment is not derived (r6, review r5).
3. Installs detected with unknown ownership are all taken. Map keys appear in locations as the literal `[key]`. `--skip` is recorded by name only (r3).
4. A text item stages line by line, so a stream cut by the seal keeps its complete lines (r3).
5. Label-only proxy Pods are always `unverified`. kgwctl never reads ReplicaSets, even when RBAC would allow it (r4, row 2).
6. The spool lives in a temp directory beside the output file, or in the system temp directory for `-o -` (r4, row 3).
7. Footer caps scale by `min(1, (--max-size / 4) / 10 MiB)`, and their sum is the reserve taken out of `--max-size`. A `--max-size` below `32Mi` is a usage error, exit 2 (r4, r5).
8. "Late counters sampled at seal" means Sink calls in flight at the seal, per collector; nothing later changes the count (r4, row 3).
9. Grant lines name the grant, namespace and Role to add, and follow the item lines in `errors.txt` (r4, row 6).
10. The guard on every client replaces non-2xx bodies with a code-and-reason `Status` before client-go or Snellius flattens them. That is how `check` and load errors are sanitized (r4, row 1).
11. The guard's `Refused` marker is a fixed cause type in the canonical `Status` it writes (`kgwctl.kgateway.dev/ConnectionRefused`), which the scraper reads back as `HTTPStatusError.Refused` (r5).
12. The full metadata canaries and the registry-alias test run in the task that wires `check` (4.2.5). The exact request set runs once all collectors exist (4.3.4) (r4, row 4).
13. For a streamed log, the re-read runs after `Sink.Text` returns. An outcome other than `verified` is recorded through `Sink.Error` with that `Source` (r5).
