# kgwctl

A read-only command-line tool for inspecting kgateway installs and the Gateway API objects they serve. It shows each object's status per parent and calls out stale or missing status, so a route that never attached is easy to spot. `kgwctl` is a working name.

See [`docs/roadmap.md`](docs/roadmap.md) for what comes next.

## Install

```bash
go install github.com/DuncanDoyle/kgwctl/cmd/kgwctl@latest
```

## Commands

These examples come from a kind cluster running kgateway v2.5.0 and Gateway API v1.7.0.

### `version`

Prints the client, controller, proxy and Gateway API CRD versions.

```console
$ kgwctl version
Client:  kgwctl dev (unknown, go1.27.3)
Install: kgateway-system/kgateway
  Controller: v2.5.0 (1/1 replicas)
  Proxies:
    live/web  v2.5.0  1 pod
Gateway API CRDs: v1.7.0 (standard)
```

### `get <kind>[,<kind>...] [name]`

Lists objects with their status. Use `all` for every served kind except GatewayClass (`gwc,all` lists both), and `-o wide|json|yaml` for the other formats. Kinds take plural, singular, `kind.group` or short names: `gwc`, `gw`, `hr`, `gr`, `tr`, `tlsr`, `btlsp`, `tp`, `lp`, `bcp`, `gwp`, `be`, and `ls` for ListenerSets.

```console
$ kgwctl get httproutes -A
NAMESPACE   NAME          HOSTNAMES             PARENTS   ACCEPTED            RESOLVED                            AGE
live        bad-backend   bad.example.com       web       True                0/1 (1 rejected: BackendNotFound)   8m
live        healthy       healthy.example.com   web       True                True                                8m
live        orphan        orphan.example.com    missing   0/1 (1 no status)   0/1 (1 no status)                   8m
```

`orphan` names a Gateway that doesn't exist, so no controller has written its status.

### `describe <kind> <name>`

Shows one object in full: spec, status per parent and relations.

```console
$ kgwctl describe httproute bad-backend -n live
Name:                 bad-backend
Namespace:            live
Kind:                 HTTPRoute.gateway.networking.k8s.io
Install:              kgateway-system/kgateway
...
Conditions:
  parent Gateway/web (kgateway.dev/kgateway):
    TYPE                     STATUS  REASON           MESSAGE                                LAST TRANSITION       OBSERVED GEN
    ResolvedRefs             False   BackendNotFound  Service live/does-not-exist not found  2026-11-06T08:01:30Z  1
    Accepted                 True    Accepted         Successfully accepted Route            2026-11-06T08:01:30Z  1
    kgateway.dev/Programmed  True    Programmed       Successfully programmed Route          2026-11-06T08:01:30Z  1
Relations:
  Parents:
    Gateway/web  accepted  kgateway.dev/kgateway
  ...
  Backends:             does-not-exist
```

### `check`

Runs the health checks over each kgateway install and prints a report you can paste into a ticket. Sections appear only when they have content:

1. **Findings:** what is wrong, with the object and the reason.
2. **Still deciding:** checks that need more time, with the time of the next decision.
3. **Not checked:** checks that could not run, with the cause and the fix.
4. **Partial coverage:** checks that ran but could not look at everything, with what they missed.
5. **Checks run:** how many checks ran. A check that ran is not a check that passed: findings are not tied to checks.

Error text from the cluster has its URLs and addresses redacted.

```bash
kgwctl check -n live
```

Exit codes: `0` when clean, `1` when there are error or warning findings, `2` when something could not be checked. `get`, `describe` and `version` keep `0` and `1`. Use `-o json|yaml` for a versioned envelope ([`docs/output.md`](docs/output.md)), `-n` or `-A` to pick the namespaces (install-level findings, such as a controller that is down, always show), and `--install <ns>/<name>` to pick one install.

`check` reads controller metrics and Envoy stats through the API server's `pods/proxy`. If that is denied, those checks show under Not checked with the RBAC line to add. The run does not fail. See [Permissions](#permissions).

### Live mode

`check --live` keeps watching the cluster for a bounded time, then prints one report. Use it when:

- a check is still deciding and needs a grace period or a hold to pass,
- you want to see a fix take effect while you apply it,
- you are watching a rollout, or
- a problem comes and goes.

```bash
kgwctl check --live --timeout 10m
```

On a terminal, progress lines go to stderr. The report goes to stdout once, at the end. It adds an **Observed during this run** section: findings that appeared, cleared or changed severity, with their times. A failure that came and went stays in the report.

```
OBSERVED DURING THIS RUN (2) · 18:09:44 to 18:11:18 UTC, stopped at --timeout
  KGWUI-003  HTTPRoute live/orphan  Not attached: parent not found
             First seen 18:09:48, still present.
             Severity: info from 18:09:48, warning from 18:10:45.
  KGWUI-023  Service live/echo  Requests to this backend get 503
             First seen 18:10:15, still present.
             Severity: warning from 18:10:15.
```

`--timeout` sets how long the run lasts (default `6m`). It is only valid with `--live`. `--request-timeout` still bounds the first read of the cluster.

The first Ctrl-C stops the run and prints the report, then `Stopped by Ctrl-C.` on stderr. The exit code follows the findings, as with `--timeout`. A Ctrl-C before an install was found gives exit `2` and no report. A second Ctrl-C exits at once, without a report.

Exit codes follow the same rules as `check`, applied to the final state. With `-o json|yaml` you get one document at the end, with an `observation` block ([`docs/output.md`](docs/output.md)).

### `proxy dump <gateway>`

Streams the Envoy config dump of one replica of the Gateway to stdout, unchanged. It picks the oldest Ready replica; `--pod <pod>` names another replica of the same Gateway, Ready or not.

```bash
kgwctl proxy dump http -n kgateway-system > dump.json
```

Envoy redacts only the fields it knows are sensitive, and only in strongly typed config. The dump can still contain credentials, tokens or keys in other config. Check it before you attach it to a ticket.

It reads the dump over a port-forward to Envoy's admin port. `--request-timeout` bounds the whole command; there is no size limit. Exit codes: `0` when printed, `2` when the dump could not be read (no Ready replica, `--pod` not a replica of the Gateway, RBAC, timeout).

Live `proxy routes` and `proxy dump` require `create pods/portforward`. kgwctl sends only `GET /config_dump`, but the grant allows any TCP forwarding to Pods in the namespace, including Envoy admin operations that change or stop the proxy. RBAC cannot narrow it to a port or path. Bind it only to trusted operators. The optional `kgwctl-proxy-portforward` Role in [`docs/rbac.yaml`](docs/rbac.yaml) grants it.

### `proxy routes [<gateway>]`

Shows one Gateway's Envoy listeners, virtual hosts and routes, and for each route the HTTP filters it runs in request order, with the Gateway API and kgateway objects they come from. Live, it reads the config dump of one replica (the oldest Ready one, or `--pod`) and lists the cluster's HTTPRoutes to attribute routes. With `--file` it reads a saved dump and calls no Kubernetes API. It needs the same `create pods/portforward` grant as `proxy dump` when live.

`--route <ns>/<name>` shows only that HTTPRoute's routes, and `--rule <index>` one of its rules. `--response` also shows the response order. A route whose name reads as two HTTPRoutes is listed as ambiguous, and one whose origin is unresolved is listed as such; neither is ever matched. The dump is capped by `--max-size` (default `64Mi`). To read a larger one, raise `--max-size`, or save it with `proxy dump` and read it with `--file <path> --max-size <n>`.

Does the transformation of HTTPRoute `orders` run before ext_authz? Ask its Gateway's proxy:

```bash
kgwctl proxy routes proxy -n kgwctl-proxy --route kgwctl-proxy/orders
```

```
Gateway kgwctl-proxy/proxy · pod kgwctl-proxy/proxy-575f694596-lk8s6 · 2026-11-21T12:07:08Z
listener~8080 [::]:8080 · active · Gateway kgwctl-proxy/proxy port 8080
└─ vhost listener~8080~orders_kgwctl_example [orders.kgwctl.example] · port 8080
   └─ route / (prefix) · HTTPRoute kgwctl-proxy/orders · rule 0 · match 0
      filters (request order):
        1 ext_auth/kgwctl-proxy/authz       enabled by route · GatewayExtension kgwctl-proxy/authz · TrafficPolicy kgwctl-proxy/orders-extauth (extAuth)
        2 ratelimit/local                   enabled by route · TrafficPolicy kgwctl-proxy/orders-ratelimit (rateLimit.local)
        3 dynamic_modules/simple_mutations  enabled by route · TrafficPolicy kgwctl-proxy/orders-transformation (transformation)
        4 envoy.filters.http.router         on
      not run: global_disable/ext_auth (off)
      policies: TrafficPolicy kgwctl-proxy/orders-extauth (extAuth), TrafficPolicy kgwctl-proxy/orders-ratelimit (rateLimit.local), TrafficPolicy kgwctl-proxy/orders-transformation (transformation)
      backend: kube_kgwctl-proxy_backend_8080
```

No. The route runs external auth first, then the local rate limit, then the transformation (`dynamic_modules/simple_mutations`), and last the router. Every filter but the router is off on the Gateway and switched on by this route. Each filter entry names the TrafficPolicy that configures it, and the `policies` line lists them all.

A saved dump gives the same view; only the header names the file instead of the pod. `--gateway` labels the origins:

```bash
kgwctl proxy dump proxy -n kgwctl-proxy > dump.json
kgwctl proxy routes --file dump.json --gateway kgwctl-proxy/proxy --route kgwctl-proxy/orders
```

This Gateway also serves HTTPRoute `proxy/orders-kgwctl`, and the Envoy route names of both HTTPRoutes read as either one. The output above was taken with kgateway's Helm value `enableRouteSourceMetadata: true`, which tells the two apart. Without it, live and file views both list the two routes as ambiguous and match neither.

`-o json|yaml` prints one `ProxyRoutes` document ([`docs/output.md`](docs/output.md)) with the source, the snapshot, parser warnings and the ambiguous routes. Exit codes: `0` when printed, `2` on any error.

### `bundle`

Writes one `.tar.gz` with what a maintainer asks for in a kgateway case. It covers all namespaces and every detected install, unlike the other commands. Use `-n <ns>` or `--install <ns>/<name>` to narrow it. It runs with your own credentials and uploads nothing.

```bash
kgwctl bundle -o bundle.tar.gz
```

The archive root holds `manifest.json` (per-item status), `errors.txt`, `redactions.json` and `run.log`. Each collector writes its own part:

| Collector | Collects | Needs | Path in the archive |
|---|---|---|---|
| `resources` | Gateway API and kgateway objects with status | reader | `resources/<group>/<kind>/<ns>/<name>.yaml` |
| `workloads` | Deployments, Pods, Services and Events | reader | `workloads/<ns>/` |
| `install` | controller Deployment, GatewayClasses | reader | `install/` |
| `version` | Kubernetes, image and Envoy versions | reader; `pods/portforward` for the Envoy versions | `cluster/version.json`, `proxies/<ns>/<pod>/server_info.json` |
| `check` | the `check` report | reader | `check/check.json` |
| `logs` | controller and proxy logs, and the previous logs after a restart | `pods/log` | `controller/<ns>/<pod>/logs/`, `proxies/<ns>/<pod>/logs/` |
| `controller-metrics` | controller `/metrics` | `pods/proxy` (reader) | `controller/<ns>/<pod>/metrics.txt` |
| `proxy-metrics` | proxy `/metrics` | `pods/proxy` (reader) | `proxies/<ns>/<pod>/metrics.txt` |
| `proxy-admin` | Envoy config dump, stats, certs and clusters | `pods/portforward` | `proxies/<ns>/<pod>/` |

`--skip <collector>,...` leaves collectors out, and `--list-collectors` prints their names. `--since` (default `24h`) bounds logs and Events, and `--tail` (default `5000`) bounds the log lines per container. `--skip proxy-admin` does not stop port-forwarding, because `version` also port-forwards to each proxy. Skip both to avoid it. `--timeout` (default `5m`) ends collection, and `--max-size` (default `512Mi`) caps the uncompressed size. `-o` names the file, `-o -` writes to stdout. The file is created with mode `0600`, and kgwctl never overwrites one. Exit codes: `0` when a bundle was written, even a partial one, and `2` when none was.

**Never requested.** kgwctl makes no API request for Secrets or ConfigMaps. Helm values are not collected either, because they live only in Secrets. It requests no controller admin endpoint (`/snapshots/xds` and `/snapshots/krt` can hold keys and credentials), and sends Envoy only fixed `GET` requests: config dump, stats, server info, certs and clusters. For Secrets and ConfigMaps, two layers enforce this. A request guard wraps every client the command owns, including install detection, `check` and downstream collectors. It refuses any request for `secrets` or `configmaps` before it is sent. The archive writer also refuses a Secret or ConfigMap object of any API group, even inside a list. The Envoy requests are limited to a fixed list of operations, and no code path requests a controller admin endpoint.

**Residual risk.** Although kgwctl never reads Secrets, the controller copies Secret values into Envoy config, such as a header value from a Secret reference. These derived values land in the config dump, and logs can echo them. The config dump, stats and metrics are kept whole, so with `-n` they can still name routes and backends in other namespaces. `-n` does not bound the controller either: its logs and metrics are collected, and the install namespace is always included. `check` reads cluster-wide. The config dump and `clusters.txt` hold endpoint addresses. `--no-endpoints` drops the endpoint data and `clusters.txt`, but Pod and Service IPs in `workloads/` and static or DNS addresses in the config dump stay. The bundle names namespaces, hosts and backends on purpose, so treat it as sensitive as your cluster's configuration.

**Redaction.** Every file is redacted by its content type before it is written. Redacted values become `[redacted]`, so files keep their keys. It covers header values, inline JWKS private members, literal env values, credential-named keys and Envoy inline sources, and matches patterns such as `Bearer` tokens, JWTs, PEM private keys and AWS access key IDs. Pass `--redact-header <name>,...` for custom credential headers. Redaction is best effort:

- Logs and free text are caught only when a secret has a known shape. Custom formats, base64 and compressed content can pass.
- Opaque extension config, such as an unknown `TypedStruct`, `Any` or Wasm configuration, gets only the pattern pass.
- A Secret-derived value in an Envoy field outside the known header structures gets only the key-name rule and the pattern pass.
- Map keys stay in the output.

**Review before sharing.** Do not send the file unread:

1. Read `redactions.json`. It lists the rule, location and count for each file, never a value.
2. Search the logs and the config dump for tokens, internal hostnames and anything you don't want to share.
3. Rerun with `--skip logs`, `--no-endpoints` or `--redact-header` if something is there. `--no-endpoints` does not remove Pod and Service IPs.

[`docs/bundle-threat-model.md`](docs/bundle-threat-model.md) lists every artifact, rule and limit.

**RBAC.** The `kgwctl-reader` ClusterRole in [`docs/rbac.yaml`](docs/rbac.yaml) covers everything except logs and Envoy admin captures. Two optional Roles add them: `kgwctl-bundle-logs` (`get pods/log`) and `kgwctl-proxy-portforward` (`create pods/portforward`). Apply the logs Role in every namespace with controller or proxy Pods. Apply the port-forward Role only in Gateway (proxy) namespaces, since in a controller namespace it would reach the controller admin port. Both grants reach every Pod in the namespace, so bind them only to trusted operators. With the reader role alone, the command exits `0`. The affected logs and Envoy captures are marked `denied` in `manifest.json`, and `errors.txt` ends with one grant line per grant and namespace:

```
grant: get pods/log in namespace kgateway-system: see the kgwctl-bundle-logs Role in docs/rbac.yaml
```

## Global flags

| Flag | Meaning |
|---|---|
| `--kubeconfig` | Path to the kubeconfig file |
| `--context` | Kubeconfig context to use |
| `-n`, `--namespace` | Namespace scope; defaults to the context's namespace |
| `-A`, `--all-namespaces` | List across all namespaces |
| `--request-timeout` | Deadline for all cluster I/O of the command (default `30s`) |
| `--install <ns>/<name>` | Select one install when several are detected |
| `--all-controllers` | Also show objects that another controller owns (hidden by default) |

## Output and exit codes

`get`, `describe` and `version` exit `0` on success and `1` on any error. `check` exits `0` when clean, `1` for problems and `2` when it could not check something. `proxy dump`, `proxy routes` and `bundle` exit `0` or `2`. Data goes to stdout; errors and warnings go to stderr. With `-o json|yaml`, `describe`, `version`, `check` and `proxy routes` print a versioned envelope, documented in [`docs/output.md`](docs/output.md); `get` prints a plain Kubernetes `List`. Tables and text layouts are not a stable interface.

## Permissions

kgwctl only reads. It needs `get`, `list` and `watch` on the Gateway API and kgateway kinds, Namespaces, Pods, Events, Services, EndpointSlices, Deployments and CustomResourceDefinitions, and `get` on `pods/proxy` for `check` and the bundle's metrics. It never reads Secrets or ConfigMaps. [`docs/rbac.yaml`](docs/rbac.yaml) holds that ClusterRole, plus a sample ServiceAccount and binding. The reader role grants no `pods/portforward` or `pods/log`. Two optional Roles at the end of the file add them: `kgwctl-proxy-portforward` for the `proxy` commands and the bundle's Envoy captures (see [`proxy dump`](#proxy-dump-gateway)), and `kgwctl-bundle-logs` for the bundle's logs. `get pods/log` reads every Pod's logs in its namespace. When a grant is missing, `kgwctl bundle` still writes a bundle, and `errors.txt` ends with one line per missing grant and namespace. See [`bundle`](#bundle).

Apply the file with:

```bash
kubectl apply -f docs/rbac.yaml
```

Known limits:
- `watch` is required as well as `list` ([DuncanDoyle/kgateway-ui#18](https://github.com/DuncanDoyle/kgateway-ui/issues/18)).
- Objects are listed cluster-wide, so users limited to some namespaces get errors ([DuncanDoyle/kgateway-ui#19](https://github.com/DuncanDoyle/kgateway-ui/issues/19)).

When the kind `get` or `describe` was asked for can't be read, the command fails: exit `1`, empty stdout. Any other input that can't be read is named in a stderr warning, and the output marks what depends on it as unknown. `check` lists it under Not checked instead. With `-o json|yaml`, `describe` and `version` also list those inputs in the `incomplete` field.

## Extending

Downstream builds embed the CLI through [`pkg/cli`](pkg/cli). `cli.NewRootCommand` takes options for the binary name, a registry of extra kinds and install detectors, extra commands and build info. `cli.WithBundleCollectors` adds collectors to `bundle`. They are registered after the built-in ones, appear in `--list-collectors`, and honor `--skip`. Redaction and the Secret and ConfigMap refusal apply to what a collector writes through the `bundle.Sink`.

Pass Kubernetes resources and lists to `Object` with their kind metadata intact. The Sink rejects Secrets and ConfigMaps, including their list kinds and either kind inside a List. Use `Text` for plain text and `Prometheus` for metrics. Emit resources separately from reports and logs. The Sink has no generic JSON method. New structured report formats require a reviewed schema and a writer-owned redactor.

Collectors are trusted code, and the Sink does not stop one that opens its own client. A name that is invalid, or repeats a built-in or another added collector, makes `bundle` exit `2`. [`examples/downstream`](examples/downstream) is a complete example, including a bundle collector.

## Development

```bash
make test           # unit and envtest suite
make vet lint build
```

The live check runs the commands against a real kgateway install on a local kind cluster. It writes its own kubeconfig files under `hack/live/out/` and needs `kind`, `helm`, `kubectl`, `gh` and `jq`.

```bash
hack/live-check.sh setup                                   # cluster, Gateway API CRDs, kgateway, fixtures, reader ServiceAccount
hack/live-check.sh verify hack/live/out/admin.kubeconfig   # outputs in hack/live/out/admin/
hack/live-check.sh verify hack/live/out/reader.kubeconfig  # the same commands with docs/rbac.yaml permissions
                                                           # (verify also runs check, scaling the controller to 0 and adding a broken route, then restores both)
hack/live-check.sh routes                                  # proxy routes live and from a dump; sets enableRouteSourceMetadata, then back to false
hack/scale/run.sh                                          # scale benchmark: 1,000 namespaces, 500 Gateways, 5,000 HTTPRoutes
```

## License

Apache License 2.0. See [LICENSE](LICENSE).
