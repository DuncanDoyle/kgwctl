# Phase 4 — Support bundle: Design

Status: r1, 2026-11-22. For Codex review, then owner approval.
Roadmap: Phase 4. Decisions: D-009, D-010, D-017, D-019, D-021, D-024; new D-025 and D-026 (proposed, §13).

## 1. Goal and scope
`kgwctl bundle` writes one `tar.gz` that holds what support asks for in a kgateway case. Nobody has to collect it by hand, and the customer can see what leaves the cluster. The archive is useful offline: a person can read it, and later tooling can parse it through a manifest.

In scope:
- Gateway API and kgateway resources, with status.
- Workloads around them: Deployments, Pods, Services, Events.
- Controller and proxy logs, previous containers after a restart.
- Controller metrics per replica.
- Proxy config dump, stats, server info and certificates per replica.
- Versions and install metadata.
- The `check` report.
- Scoping by namespace, install and time window.
- One global deadline with partial results and a per-collector error record.
- A collector registry for downstream builds.
- Mandatory redaction, a redaction report and a documented threat model.

### Anti-goals
- No Secret values, ever. In this phase no Secret is read at all (§6).
- No controller `/snapshots/xds`. It holds TLS private keys and inline credentials in clear (§6.2).
- No replay system and no analyzer over a saved bundle. The manifest makes both possible later.
- No upload. The file stays on the user's disk.
- No privileges beyond the user's own. kgwctl never creates RBAC objects.

## 2. Evidence
Research notes, not committed: prior art, reuse survey and threat assessment, 2026-11-22.

- **Prior art.**
  - agctl bug-report (agentgateway#3991) is the closest model. It finds kinds by CRD category, runs 8 workers under one 5 min deadline, keeps partial output with `errors.txt`, caps logs at 10 MiB per stream (`--since 24h`, `--tail 5000`), and takes previous logs only after a restart.
    - It reads full Secret data, then redacts it.
    - It writes ConfigMaps unredacted.
  - istioctl bug-report adds a time window and include/exclude selectors. It lists Secrets without values by default.
    - It dumps every CR in the cluster unredacted.
    - Its timeout does not cover cluster resources.
  - cilium sysdump keeps its own run log inside the archive and names every failed task.
- **kgateway v2.5.1, read from source.**
  - Controller ports: metrics 9092 (`pods/proxy`), health 9093, and admin 9095 on localhost (port-forward only).
  - Proxy ports: Envoy admin 19000 on 127.0.0.1 (port-forward), stats 9091 (`pods/proxy`), readiness 8082.
  - Controller pods carry `kgateway=kgateway`. Proxy pods carry `kgateway=kube-gateway` plus the Gateway labels.
  - All seven kgateway CRDs are namespaced and carry the category `kgateway`.
- **Install metadata.** Without reading Secrets we can get:
  - the controller Deployment's `helm.sh/chart` and `app.kubernetes.io/version` labels;
  - image tags;
  - the `KGW_*` env vars, which are the rendered settings;
  - the GatewayClass;
  - Envoy's `/server_info`.

  Helm values live only in `helm.sh/release.v1` Secrets.
- **Sensitive data.** Envoy redacts fields it marks sensitive (TLS keys, basic-auth users, API-key credentials, AWS inline keys, generic secrets). It does not redact:
  - header values, including the values kgateway resolves from `headerModifiers.secretRef`;
  - `initial_metadata`;
  - Lua code;
  - transformation templates.

  Seven kgateway CRD fields hold sensitive data inline. Every object's `last-applied-configuration` annotation copies its whole spec, and a Secret's data when that Secret was created with `kubectl apply`.

## 3. Command

```
kgwctl bundle [-n <ns> ... | -A] [--install-namespace <ns>] [--since 24h] [--tail 5000]
              [--timeout 5m] [-o <file>] [--skip <collector>,...] [--max-size 512Mi]
              [--redact-header <name>,...] [--no-endpoints] [--controller-snapshot]
```

- Scope:
  - `-n` selects namespaces for Gateway API and kgateway objects and their proxies. It can be repeated.
  - `-A` selects all namespaces, and is the default when no `-n` is given.
  - The install namespace always comes along. It is found from the controller pods' `kgateway=kgateway` label, or set with `--install-namespace`.
- Time window:
  - `--since` limits logs and Events.
  - `--tail` caps lines per log stream.
  - Each stream is also capped at 10 MiB.
- `--timeout` is one deadline for the whole run (default 5m). At the deadline, unfinished collectors are cancelled and recorded. What finished is written. The global `--request-timeout` (default 30s) bounds each single item: one list, one log stream, one Envoy GET. A hung pod costs one item, not the run.
- `-o`:
  - The default file is `kgwctl-bundle-<context>-<UTC timestamp>.tar.gz` in the current directory.
  - kgwctl never overwrites a file. It creates the file with mode `0600`.
  - `-o -` writes to stdout.
- `--skip` turns off collectors by name (`kgwctl bundle --list-collectors` prints the names).
- `--max-size` caps the uncompressed total. Past it, collectors stop writing, and the cut is recorded.
- `--no-endpoints` drops `include_eds` from the config dump.
- `--controller-snapshot` adds the controller's `/snapshots/krt` (§6.2). It is opt-in.
- `--redact-header` adds header names to the redaction list (§7).
- Progress goes to stderr: one line per collector, then a summary with counts of files, errors and redactions, and the output path.

### 3.1 Exit codes
- 0: a bundle was written, even a partial one. The stderr summary names failed collectors.
- 2: no bundle was written, because of a usage error, an unwritable output, or no reachable cluster.
- Partial bundles are normal on restricted RBAC, so they are not an error code. The manifest and `errors.txt` say what is missing.

## 4. Archive layout

```
kgwctl-bundle-<context>-<ts>/
  manifest.json            # BundleManifest envelope (§5)
  errors.txt               # one line per failed collector or item, human-readable
  redactions.json          # per file: rule, location, count; never values (§7)
  run.log                  # kgwctl's own progress log for this run
  cluster/version.json     # kgwctl build, server version, controller and Envoy versions
  check/check.json         # `check` Check envelope, as `kgwctl check -o json`
  resources/<group>/<kind>/<namespace>/<name>.yaml
  workloads/<namespace>/{deployments,pods,services,events}.yaml
  install/<namespace>/<controller-deployment>.yaml   # labels, images, KGW_* env (redacted)
  controller/<pod>/
    logs/<container>.log[.previous]
    metrics.txt            # raw /metrics, one scrape, timestamped in the manifest
    snapshot-krt.json      # only with --controller-snapshot
  proxies/<namespace>/<gateway>/<pod>/
    config_dump.json       # redacted (§7)
    stats.txt              # Envoy /stats (text readouts incl. version_text)
    stats-prometheus.txt   # 9091 /stats/prometheus via pods/proxy
    server_info.json
    certs.json             # SANs, expiry, serials; no keys
    clusters.txt           # health and outlier state
    logs/<container>.log[.previous]
```

All entries are regular files. Names are built from validated Kubernetes names, never from free text, and a name part with `/`, `..` or a leading `.` is rejected.

## 5. Manifest (`BundleManifest`, `-o json` contract)
`manifest.json` is a versioned envelope like the other kinds in docs/output.md. It is the parse entry point for later tooling. It contains:
- kgwctl build and the flags used, with values;
- the context name, the server address replaced as in `check`;
- the scope (namespaces, install namespace), and the start and end time of the run;
- per collector: name, status (`ok`, `partial`, `failed`, `skipped`, `timeout`), duration, error text, and the files it wrote;
- per file: path, size, content type, capture time, source (pod, container, endpoint), and its redaction count.

Capture time is per file. Per replica, the proxy stats, config dump and controller metrics then carry timestamps, so a later convergence check can compare a replica's `version_text`, `control_plane.*` and update counters with the controller's metrics from the same run (program coordination input, 2026-11-22).

## 6. Inputs and permissions

| Collector | Reads | RBAC | Default |
|---|---|---|---|
| `resources` | Gateway API kinds and the CRDs in category `kgateway`, plus vendor kinds from the registry | existing reader ClusterRole | on |
| `workloads` | Deployments, Pods, Services, Events in scope | reader | on |
| `install` | controller Deployment, GatewayClass | reader | on |
| `version` | as `kgwctl version` | reader | on |
| `check` | as `kgwctl check` | reader + `pods/proxy` (D-017) | on |
| `logs` | controller and proxy containers, init containers, previous after a restart | **`pods/log` get (new, optional Role)** | on |
| `controller-metrics` | each controller replica's `/metrics` on 9092 | `pods/proxy` | on |
| `proxy-stats` | each proxy replica's 9091 `/stats/prometheus` | `pods/proxy` | on |
| `proxy-admin` | each proxy replica's Envoy admin: `/config_dump`, `/stats`, `/server_info`, `/certs`, `/clusters` | `pods/portforward` (optional Role, D-021) | on |
| `controller-snapshot` | `/snapshots/krt` on 9095 | `pods/portforward` in the install namespace | **off** |

A collector that hits a 403 records it once, with the grant to add, and the run continues. A user with only the reader role gets a bundle of resources, workloads, install, version and `check`.

### 6.1 Secrets, ConfigMaps and Helm values
No Secret and no ConfigMap is read in Phase 4.
- Certificate questions are answered without Secrets: Envoy `/certs` gives SANs, expiry and serials for what the proxy serves, Gateway and listener status give the controller's verdict, and `check` reports bad references.
- Install shape comes from Deployment labels, images, `KGW_*` env and GatewayParameters.
- Helm values exist only inside `helm.sh/release.v1` Secrets. RBAC cannot grant metadata-only Secret reads, so any opt-in would mean full Secret read for the user. Deferred (§12, item 1).

The README sentence "It never reads Secrets or ConfigMaps" stays true.

### 6.2 Controller debug endpoints
- `/snapshots/xds` is never requested. kgateway blanks only SDS Secret resources there, but it inlines listener TLS keys, backend client keys and AWS credentials, some as base64 proto bytes that pattern redaction misses (kgateway v2.5.1 `pkg/kgateway/admin/xds_snapshot.go`, `irtranslator/fc.go`, `backend/aws.go`; read from source, not run). A test fails if the fake admin server ever sees that path.
- `/snapshots/krt` is safer: kgateway keeps raw Secrets out of its debug collections, and `ir.Secret` prints `[REDACTED]`. It is still large and not audited type by type. It is opt-in with `--controller-snapshot`, runs through the structured redactor, and needs port-forward into the install namespace.

### 6.3 Port-forward allowlist (D-025)
D-021 limits port-forward to one `GET /config_dump`. The bundle needs four more read-only Envoy admin GETs: `/stats`, `/server_info`, `/certs`, `/clusters`. D-025 replaces the one-path rule with an allowlist of read-only GETs, enforced in `proxydump`: any other path is refused before the stream opens. The controller's `/snapshots/krt` is the only controller path. The grant's power (Envoy admin writes) is unchanged and already documented in README and docs/rbac.yaml.

## 7. Redaction (D-026)
One mandatory stage sits in the archive writer. A collector, built-in or from a downstream registry, hands the writer `(path, content type, bytes or objects)`. The writer runs the redactor for that type before writing, and refuses an undeclared type, so no collector can bypass it.

| Content type | Redaction |
|---|---|
| `k8s-object` | Drop `metadata.managedFields` and the `kubectl.kubernetes.io/last-applied-configuration` annotation. Apply per-kind field rules (the seven kgateway fields, `env[].value` except on an allowlist of non-secret names, `DirectResponse` body pattern pass), the key-name rule, the header-pair rule, then the text pattern pass over every remaining string. |
| `envoy-config-dump` | Keep Envoy's own `[redacted]`. Header-pair rule on `request_headers_to_add`, `response_headers_to_add`, header mutations and `initial_metadata`. Redact Lua `inline_code`/`source_codes`, and every `inline_string`/`inline_bytes` except under `local_jwks`. Parse the dynamic-module JSON `StringValue` and apply the same rules inside. Then the text pattern pass over every remaining string. |
| `json` | Key-name rule, header-pair rule, pattern pass. |
| `prometheus` | None. Names, namespaces and hostnames are what support needs. |
| `text` (logs, `errors.txt`, `run.log`, `clusters.txt`) | Line patterns: `Bearer`/`Basic` credentials, JWTs, PEM private-key blocks, AWS access keys and signatures, `(?i)(access_token\|api_key\|token\|password\|secret)=` query values, and Envoy debug header lines whose name is on the sensitive-header list. |

- **Key-name rule:** a key like `password`, `secret`, `token`, `apiKey`, `credential`, `privateKey`, `clientSecret`, `sessionToken` or `licenseKey`, with an optional prefix.
- **Header-pair rule:** in a `{name|key, value}` pair whose name is on the sensitive-header list, redact the value. The list holds `authorization`, `proxy-authorization`, `cookie`, `set-cookie`, `x-api-key`, `api-key`, `x-auth-token`, `x-amz-security-token`, plus `--redact-header`.
- **Marker:** `[redacted]`, the same marker Envoy uses. A value stays a string, so files still parse with the same keys. A pattern match keeps its prefix (`Bearer [redacted]`). No hashes and no lengths.
- **Report:** `redactions.json` lists, per file, each rule, its location (field path or line number) and a count. It never holds values. The stderr summary gives the total.

### 7.1 Limitations (documented in README and the threat model)
- Pattern redaction of logs and free text is best effort. It misses secrets in custom formats, custom header names not on the list, and base64-encoded or compressed content.
- Opaque extension config, meaning unknown `TypedStruct`/`Any` payloads and Wasm configuration, is redacted only by the pattern pass.
- Stats, metrics and resource names expose topology and tenant names. They are kept on purpose; scoping by namespace is the way to leave tenants out.
- Endpoint IPs are internal topology. `--no-endpoints` drops them.
- The bundle is as sensitive as the cluster's configuration. The README tells users to review `redactions.json` and the files before sharing.

### 7.2 Threat model
`docs/bundle-threat-model.md` is written in 4.1 and kept current. It covers:
- assets per artifact class;
- who receives the archive;
- what is never read, what is redacted and what is kept;
- the RBAC the bundle uses, including the `pods/portforward` power;
- archive handling (`0600`, no overwrite, no links, validated names, size caps);
- the limitations above.

## 8. Architecture
- `internal/bundle`: the collector interface, the scheduler, the archive writer and the manifest. It has no Cobra imports, so it can move to a library later (D-019).
  - **Collector:** `Name() string`, `Collect(ctx, Env, Sink) error`. `Env` gives clients, scope, the time window and the loaded model.
  - **`Sink`** accepts typed content only (`Object`, `ConfigDump`, `JSON`, `Prometheus`, `Text`). Each call is one file, written through the redactor. A collector can report per-item errors on the Sink and keep going.
  - **Scheduler:** a bounded worker pool (8), one context with the global deadline, panics recovered into a failed status. Results are written in a fixed order, so two runs on the same cluster produce the same layout.
  - **Writer:** streams to the gzip tar, enforces `--max-size`, and writes `manifest.json`, `errors.txt`, `redactions.json` and `run.log` last. It also writes them when the deadline hits.
- `internal/redact`: the redactors per content type. Pure functions, with their own canary test corpus. No dependency on `bundle`.
- `internal/proxydump`:
  - `ListReplicas(ctx, clients, gw)` returns every proxy pod of a Gateway.
  - `Fetch` takes a path from the D-025 allowlist.
  - The existing deadline behaviour is reused unchanged.
- The `check` collector runs the same load and engine as `kgwctl check` and writes its JSON envelope. `bundle` loads the model once and shares it with `check`, `version` and `resources`.
- Extension:
  - `pkg/cli.WithBundleCollectors(...bundle.Collector)` lets a downstream build add collectors, such as for its own controllers. They go through the same Sink and redactor.
  - `examples/downstream` adds one collector, built in CI.
  - The collector interface is a `pkg/` contract, so it lives in `pkg/bundle`; the scheduler and writer stay internal.

## 9. Dependencies
None new. `archive/tar` and `compress/gzip` come from the standard library, and the rest is client-go and existing packages.

## 10. Testing
- `internal/redact`: the canary corpus, one fixture per row:
  1. any object's `last-applied-configuration` holding a Secret's `data`/`stringData`;
  2. a header name in `managedFields` (dropped);
  3. HTTPRoute `requestHeaderModifier` Authorization value and an `x-api-key` header-match value;
  4. TrafficPolicy header modifier value, `basicAuth.users` hash, a JWT in a transformation template;
  5. an AWS key in a DirectResponse body;
  6. GatewayParameters `env[].value` and a secret in `deploymentOverlay`;
  7. a Deployment/Pod `env[].value` literal;
  8. a `Bearer` token in an Event message;
  9. config dump `request_headers_to_add` Authorization, ext_authz `initial_metadata`, a JWT in the dynamic-module JSON, an API key in Lua `inline_code`;
  10. an Envoy-redacted field stays `[redacted]`;
  11. Envoy debug log `'authorization'`/`'cookie'` lines;
  12. access log `?access_token=` and `X-Amz-Signature=`;
  13. a controller log PEM private key block and an `sk-…` key;
  14. a Secret-shaped value in a `/snapshots/krt` embedded spec;
  15. a token in `errors.txt` error text;
  16. a downstream collector cannot write undeclared content.

  Each case plants a unique value per location; the test runs the redactor and asserts zero hits in raw bytes, base64-decoded strings and gzip members, and asserts `redactions.json` names each location. Negative cases keep names, hostnames, header names, JWKS and cert SANs, and every redacted file still parses with the same keys.
- `internal/bundle`, with fake collectors:
  - the global deadline returns a partial archive with `timeout` statuses;
  - a panic is recorded;
  - `--max-size` truncation is recorded;
  - an undeclared content type is refused;
  - archive safety: no links, validated names, `0600`, no overwrite;
  - the layout is deterministic.
- Command boundary (envtest plus seams, as in Phases 2 and 3): `kgwctl bundle` against envtest with fixture objects, a fake log source, a fake pods/proxy scraper and a fake SPDY Envoy. The tests check:
  - the extracted archive has the expected files and the manifest;
  - a reader-only user gets a partial bundle with `errors.txt` lines naming `pods/log` and `pods/portforward`, and exits 0;
  - `/snapshots/xds` is never requested;
  - the canary fixtures leave no trace in the archive.
- Live check (`hack/live-check.sh bundle`): on kind `kgwctl-live` with planted canary objects, a full bundle passes the canary scan, holds per-replica proxy files for every replica, and has Envoy `version_text` in `stats.txt`.

## 11. Proposed sub-phases
| Sub-phase | Delivers |
|---|---|
| 4.1 | `internal/redact` with the canary corpus; `internal/bundle` (Sink, writer, manifest, scheduler, archive safety); threat model doc; D-025, D-026 |
| 4.2 | `kgwctl bundle` with the `resources`, `workloads`, `install`, `version` and `check` collectors; scoping flags; exit codes; docs/output.md `BundleManifest` |
| 4.3 | `logs`, `controller-metrics`, `proxy-stats`, `proxy-admin` (all replicas, allowlist), `controller-snapshot`; optional Role in docs/rbac.yaml |
| 4.4 | `pkg/bundle` + `pkg/cli.WithBundleCollectors` + downstream example; live check on kind; README |

## 12. Open items (owner)
1. **Helm values and Secret metadata.** Recommendation: no Secret reads in Phase 4, with Envoy `/certs` covering certificate cases. An opt-in `--secret-metadata` / `--helm-values` needs full Secret read, so it would come later, if support asks.
2. **The `pods/log` grant.** Recommendation: a new optional Role `kgwctl-bundle-logs` next to the port-forward Role; the reader ClusterRole stays as it is.
3. **Header values in `proxy dump` (Phase 3).** `proxy dump` prints the raw config dump, so header values resolved from `secretRef` appear in clear. Recommendation: run the 4.1 config-dump redactor in `proxy dump` too, with `--no-redact` for the owner's own debugging. That is a behaviour change to a shipped command.
4. **kgateway upstream.** `/snapshots/xds` exposes private keys to anyone with port-forward in the install namespace. It is admin-only and loopback-bound, but it is a security-relevant finding. Recommendation: raise it privately with the maintainers, not in a public issue.
5. **Partial bundle exit code.** Recommendation: 0, with the stderr summary (§3.1).
