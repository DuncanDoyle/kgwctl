# Support bundle threat model

This document describes what `kgwctl bundle` puts in a support archive, what it never reads, how it redacts, and what can still leak. It covers the redaction stage, the archive writer and the runner as they exist today. The command, its request guard, the optional log Role and the built-in collectors land in later sub-phases. Where this document describes them, it follows the Phase 4 design.

## Purpose and scope

`kgwctl bundle` writes one `tar.gz` with what support asks for in a kgateway case: resources with status, workloads and Events, install metadata, versions, the `check` report, controller and proxy logs, metrics, and Envoy admin captures. The user runs it with their own credentials and sends the file on.

The goal is that a user can send the archive without leaking credentials, and can see what leaves the cluster. Redaction is mandatory. It runs inside the archive writer on every member, so no collector can skip it. The archive is still as sensitive as the cluster's configuration. It names namespaces, services, routes, hosts and backends on purpose.

## Who receives the archive

The archive leaves the cluster's trust boundary. Assume these readers:

- The user who runs the command, and anyone with access to their disk.
- The support engineers who receive it, and the ticket or file system that stores it.
- Anyone a copy reaches later, through forwarding or a leak.

kgwctl does not upload the archive. It does not create RBAC objects and never uses privileges beyond the user's own.

## Assets per artifact class

| Artifact | Path | Holds | Main risk |
|---|---|---|---|
| Resources | `resources/<group>/<kind>/<ns>/<name>.yaml` | Gateway API and kgateway objects with status | header values, inline JWKS, transformation templates, basic auth hashes, direct response bodies, credentials in `last-applied-configuration` |
| Workloads and Events | `workloads/<ns>/{deployments,pods,services,events}.yaml` | Deployments, Pods, Services, Events | literal `env[].value` secrets, tokens in Event messages, Pod and Service IPs |
| Install metadata | `install/<ns>/<deployment>.yaml` | controller labels, images, `KGW_*` env | env values |
| Versions | `cluster/version.json` | kgwctl build, Kubernetes server version, image tags, observed Envoy versions | low |
| Check report | `check/check.json` | the `kgwctl check -o json` envelope | resource names, error reasons from the API |
| Logs | `controller/.../logs/`, `proxies/.../logs/` | controller and proxy container logs, previous containers after a restart | anything the process logs: tokens, headers, keys, query strings |
| Metrics | `controller/.../metrics.txt`, `proxies/.../metrics.txt` | Prometheus text | label values name namespaces, services and routes |
| Envoy config dump | `proxies/.../config_dump.json` | full Envoy config, with endpoints by default | header values, Lua code, inline sources, JWKS, extension config, endpoint addresses |
| Envoy stats, certs, clusters | `stats.txt`, `certs.json`, `clusters.txt` | counters, text readouts, certificate SANs and expiry, cluster health | topology names, endpoint addresses |
| Footers | `manifest.json`, `errors.txt`, `redactions.json`, `run.log` | run metadata, per-item status, redaction findings, progress log | context name, error text |

## Never requested

kgwctl makes no API request for these:

| Never requested | Why |
|---|---|
| Secrets | They hold credentials and TLS keys. Helm values live only in `helm.sh/release.v1` Secrets, so Helm values are not collected either. RBAC cannot grant metadata-only Secret reads. |
| ConfigMaps | They often hold credentials and application config. |
| Controller admin `/snapshots/xds` | It inlines listener TLS keys, backend client keys and cloud credentials in clear. |
| Controller admin `/snapshots/krt` | It can return raw snapshots and every registered collection, including ConfigMaps. |
| Envoy admin paths outside the allowlist | Only fixed operations are sent: `/config_dump` (with or without `include_eds`), `/stats`, `/server_info`, `/certs` and `/clusters`, all `GET` on port 19000 (D-025). An unknown operation fails before a stream opens, and redirects are refused. |

Enforcement has two layers:

1. **Request guard (when the command lands).** One `RoundTripper` wraps the client configuration behind every client the bundle owns, including install detection, `check` and downstream collectors' clients. It refuses any request for core `secrets` or `configmaps` before the request is sent. The refusal is recorded as an error line.
2. **Object Sink.** `redact.Object` refuses any object of kind `Secret`, `SecretList`, `ConfigMap` or `ConfigMapList`, in any API group, also inside a List. The writer records the item as `failed` with a forbidden-kind reason and writes nothing.

Certificate questions are answered from Envoy `/certs`, Gateway and listener status, and `check`.

## Redaction rules

Each Sink method takes one content type, and the writer runs that type's redactor before it stages the item. A collector cannot hand the writer an untyped body.

| Content type | Used for | Rules, in order |
|---|---|---|
| `object` | resources, workloads, Events, install | Refuse Secret and ConfigMap. Each member of a List gets its own kind's rules. Drop `metadata.managedFields` and the `last-applied-configuration` annotation. Header-value rule, JWKS rule, per-kind field rules, key-name rule, header-pair rule, then the pattern pass on every other string. |
| `config-dump` | Envoy `/config_dump` | Keep Envoy's own `[redacted]`. Header-value rule. Lua `inline_code` and `source_codes` redacted. JWKS rule under `local_jwks`. Every other `inline_string` and `inline_bytes` redacted. A JSON `StringValue`, such as dynamic-module config, is parsed and redacted inside. Then key-name rule, header-pair rule and pattern pass. |
| `json` | `version.json`, `check.json`, `server_info.json`, `certs.json` | Key-name rule, header-pair rule, pattern pass. Invalid JSON is dropped with an error. |
| `prometheus` | metrics | Must parse as Prometheus text exposition, or it is dropped with an error. Pattern pass on label values, `HELP` text and comments, read with the parser's grammar (leading blanks and tabs included). Metric names, label names and samples stay. The output is parsed again; if that fails, it is dropped. |
| `text` | logs, `stats.txt`, `clusters.txt` | Open PEM private-key blocks, the debug header-line rule, then the pattern pass, line by line. A kubelet timestamp prefix is kept. |

The rules:

- **Header-value rule.** These values are redacted whatever the header's name. Names are kept.
  - Envoy: `value` and `raw_value` under `request_headers_to_add`, `response_headers_to_add`, `headers_to_add` (including ext_authz `authorization_request`), `mutations`, `request_mutations`, `response_mutations` and `initial_metadata`.
  - Objects: values under any `*HeaderModifier` field and `headerModifiers` (HTTPRoute, GRPCRoute, TrafficPolicy), `initialMetadata`, `headers` under `tunnel` (BackendConfigPolicy) and in ListenerPolicy, and every value of an ext-auth `headersToAdd` map (GatewayExtension).
- **Debug header-line rule.** In Envoy's `'<name>', '<value>'` debug lines, every value is redacted except for `:authority`, `:method`, `:scheme`, `:status`, `:path`, `content-type`, `content-length`, `user-agent`, `x-request-id` and `x-envoy-*`. Kept values still get the pattern pass. A `--redact-header` name is redacted even when it is on that list.
- **JWKS rule.** Applies to Envoy `local_jwks` inline sources (`inline_bytes` is base64-decoded first) and GatewayExtension `jwks.local.inline`. The value is rebuilt as `{"keys":[...]}` from a public projection per `kty`: RSA `n`, `e`; EC `crv`, `x`, `y`; OKP `crv`, `x`; plus `kid`, `alg`, `use`, `key_ops`, `x5c`, `x5t`, `x5t#S256`. Any other shape, member type (`null` included) or `kty` redacts the whole value.
- **Per-kind field rules.** `basicAuth.users` keeps each user name and redacts the hash. `env[].value` is redacted unless the name starts with `KGW_` or is a fixed runtime name such as `LOG_LEVEL`, `POD_NAME` or `GOMAXPROCS`. A DirectResponse `spec.body` is redacted.
- **Key-name rule.** A value is redacted when its key ends in a credential word after lowercasing and dropping `_`, `-` and `.`: `password`, `passwd`, `secret`, `token`, `apikey`, `credential`, `privatekey`, `accesskey`, `secretkey`, `signature`, `licensekey`, or a plural. Every string, number and boolean below such a key is redacted too, except reference members such as `name`, `namespace`, `kind` and `*Name`. Numbers under the token-bucket fields `max_tokens` and `tokens_per_fill` stay, unless a credential-named key is above them. A header named like them is still redacted.
- **Header-pair rule.** In a `{name|key, value}` object, the value is redacted when the name is `authorization`, `proxy-authorization`, `cookie`, `set-cookie`, `x-api-key`, `api-key`, `x-auth-token`, `x-amz-security-token`, a key-name match, or a `--redact-header` name. Envoy header matcher values (`exact_match`, `prefix_match`, `string_match` and others) count as values.
- **Pattern pass.** `Bearer` and `Basic` credentials (after an `authorization` name, any Basic value), JWTs, PEM private-key blocks (also unterminated), AWS access key IDs, credential query values (`?access_token=`, `X-Amz-Signature=`, `?code=`), credential assignments in free text (`password=…`, `"apiKey":"…"`), and known key prefixes such as `sk-`, `ghp_` and `glpat-`.

**Marker.** A redacted value becomes `[redacted]`. It stays a string, so a redacted file parses with the same keys. A pattern match keeps its prefix, as in `Bearer [redacted]`. There are no hashes or lengths, since low-entropy secrets could be guessed offline.

**Report.** `redactions.json` lists, per file, each rule, location and count. A location is a field path with array elements as `[]`, or `line:<n>` for text. Only a fixed set of schema field names (Kubernetes, Gateway API, kgateway and Envoy) appears literally. Every other key is shown as `[key]`, so a location never holds a map key or a redacted value. Every key below a map field (`selector`, `labels`, `annotations`, `data`, Envoy `node.metadata` and others) is `[key]`, even one that equals a schema field name. A location is capped at 512 bytes. Each file lists at most 4096 distinct findings and counts the rest as omitted.

## Errors and metadata

- **Remote errors** from the Kubernetes API, `pods/proxy`, port-forward and Envoy are recorded as the HTTP status and the Kubernetes reason only, such as `403 Forbidden (Forbidden)`. Only canonical Kubernetes reasons are shown; any other reason becomes `request failed (<code>)`. Messages and response bodies never reach an error record. When the command lands, the guard also replaces every non-2xx body with a code-and-reason status before client-go or `check` builds an error string from it.
- **Other error text**, including panic values, passes the server-address redactor and the pattern pass. It is folded to one line and cut at 512 bytes, after the whole text is redacted (text over 64 KiB becomes `[error text too long]`), before it reaches the manifest, `errors.txt`, `run.log` or stderr. A collector panic is recovered into a `failed` status with that text. Stack traces never enter the archive.
- **Context name.** The manifest records the kubeconfig context name through the same error text redactor. It is never part of a file or member name. The API server address is written as `<apiserver>`.
- **`-o`.** The output path is never recorded. The archive root is `kgwctl-bundle-<UTC timestamp>`.
- **Flags** are recorded by name. Only enum, numeric, duration and boolean flags carry a value: `-A`, `--all-controllers`, `--since`, `--tail`, `--timeout`, `--request-timeout`, `--max-size` and `--no-endpoints`. `-n`, `--install`, `--skip`, `-o`, `--kubeconfig`, `--context` and `--redact-header` appear by name only. The resolved scope carries the validated namespace and install names.
- **Paths and sources.** A path component must be a `pkg/bundle` constant or fit Kubernetes name grammar, with an optional fixed extension. A source name that fails validation is replaced before it reaches the manifest. A capture identity or exclusion reason outside the defined values is rejected and recorded without the value.

## RBAC used

| Grant | Used for | What it exposes |
|---|---|---|
| `kgwctl-reader` ClusterRole | resources, workloads, Events, install, versions, `check` | read access to Gateway API and kgateway objects, Pods, Services, Events, Deployments, Namespaces and CRDs, cluster-wide. No Secrets, ConfigMaps or `pods/log`. |
| `pods/proxy` `get` (in the reader role) | controller and proxy metrics, `check` | HTTP `GET` to any port of any Pod through the API server. kgwctl sends only `GET` requests for metrics and stats, and refuses redirects. |
| Optional `kgwctl-proxy-portforward` Role, `pods/portforward` `create` | Envoy admin captures and observed Envoy versions | any TCP forwarding to Pods in the namespace, including Envoy admin operations that change or stop the proxy. RBAC cannot narrow it to a port or path. kgwctl sends only the allowlisted operations. Bind it only to trusted operators. |
| Optional `kgwctl-bundle-logs` Role, `pods/log` `get` | controller and proxy logs | the logs of every Pod in its namespace, not only kgateway Pods. Bind it in the install and Gateway namespaces only. |

A `403` marks the affected items `denied`, and the run continues. `errors.txt` ends with one line per missing grant and namespace. A reader-only user gets resources, workloads, install, versions without observed Envoy versions, and `check`.

## Archive handling

- The output file is created with `O_CREATE|O_EXCL` and mode `0600`. kgwctl never overwrites a file. Members inside are regular files with mode `0644`.
- Redaction runs before staging, so only redacted bytes are ever spooled. Staged bytes past the RAM budget spill to a `0600` file in a `0700` temporary directory. The spool is removed on success, cancel and failure.
- A fatal write error, a footer encoding error or a panic while writing the archive closes and removes a file output.
- Every footer is built within its cap. A manifest that does not fit even in its minimal form is replaced by a small placeholder, noted in `errors.txt`. At most 4096 failed items per collector are listed; the rest are counted.
- No context name appears in any file or member name.
- `-o -` writes to stdout. The user is then responsible for where the bytes go, and the spool sits in the system temporary directory.

## Trust boundary

Downstream builds can register their own collectors. These are trusted in-process code with the same privileges as kgwctl. The Sink and the request guard protect against their mistakes: an untyped body, a bad path, a Secret or ConfigMap object, or a registry kind that resolves to `secrets`. They do not protect against a malicious collector, which could open its own client or write to disk directly. The path grammar cannot tell a real name from captured data that happens to fit it, so collectors must build paths from names read from the API, never from log or response content.

## Residual risks and limitations

1. **Map keys stay.** Keys are kept so a file keeps its structure. A secret used as a map key stays in the output. It is shown as `[key]` in `redactions.json` but kept in the file.
2. **A single JWK or a PEM public key is redacted whole.** The JWKS rule accepts only a `{"keys":[...]}` set. Other shapes lose their public members too.
3. **Pattern redaction is best effort.** Free text and logs are caught only when a secret has a known shape or a credential-named key. Custom formats, base64-encoded or compressed content, and application log formats can pass. Unknown extension config (`TypedStruct`, `Any`, Wasm configuration) gets only the pattern pass.
4. **Names reveal topology.** Metrics, stats and object names show namespaces, services, routes and hosts. This is on purpose.
5. **`-n` does not bound derived artifacts.** Proxy config dumps, stats and metrics are kept whole. They can name other namespaces' routes and backends, also under `-n`. `check` reads cluster-wide.
6. **Endpoint addresses.** The config dump with endpoints (EDS) and `clusters.txt` hold endpoint IP addresses. `--no-endpoints` drops both. Pod and Service IPs in workload objects stay.
7. **Envoy redacts little.** Envoy `/config_dump` redacts only the fields Envoy marks sensitive. kgwctl's header-value rule covers the structures listed above. A secret in another Envoy field relies on the key-name rule and the pattern pass.
8. **Values resolved from Secrets.** kgwctl never reads Secrets, but the controller copies Secret values into Envoy config, for example a header from a Secret reference. The structural rules catch these in the known header structures. Logs can echo them anywhere.
9. **Over-redaction.** A `BEGIN … PRIVATE KEY` marker with no matching end redacts every later line of that log. Every scalar under a credential-named object is redacted, including values that are not secret.

## Advice for users

- Review the archive before you send it. Read `redactions.json` and look at the logs and the config dump.
- Use `--skip logs` when logs may carry application data or tokens in a custom format.
- Use `--no-endpoints` when endpoint IP addresses are sensitive.
- Use `-n <namespace>` to limit resources, workloads, Events and logs. Proxy captures and metrics can still name other namespaces.
- Use `--redact-header <name>,...` for custom headers that carry credentials, such as a tenant key.
- Grant `pods/portforward` and `pods/log` only in the namespaces you need.
