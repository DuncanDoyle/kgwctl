# kgwctl

A read-only command-line tool for inspecting kgateway installs and the Gateway API objects they serve. It shows each object's status per parent and calls out stale or missing status, so a route that never attached is easy to spot. `kgwctl` is a working name.

See [`docs/roadmap.md`](docs/roadmap.md) for what comes next.

## Install

```bash
go install github.com/DuncanDoyle/kgwctl/cmd/kgwctl@v0.1.0
```

Each GitHub release also has `kgwctl_<tag>_<os>_<arch>.tar.gz` archives for linux and darwin on amd64 and arm64, with a `checksums.txt`.

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

`--route <ns>/<name>` shows only that HTTPRoute's routes, and `--rule <index>` one of its rules. `--select` sets how sure kgwctl must be that a route comes from that HTTPRoute; see [How `--route` picks routes](#how---route-picks-routes). `--response` also shows the response order. The dump is capped by `--max-size` (default `64Mi`). To read a larger one, raise `--max-size`, or save it with `proxy dump` and read it with `--file <path> --max-size <n>`.

Does the transformation of HTTPRoute `orders` run before ext_authz? Save a dump of its Gateway's proxy and ask:

```bash
kgwctl proxy dump proxy -n kgwctl-proxy > dump.json
kgwctl proxy routes --file dump.json --gateway kgwctl-proxy/proxy --route kgwctl-proxy/orders
```

```
File dump.json · Gateway kgwctl-proxy/proxy
listener~8080 [::]:8080 · active · port 8080 (inferred) · Gateway kgwctl-proxy/proxy (from the command line)
└─ vhost listener~8080~orders_kgwctl_example [orders.kgwctl.example] · port 8080 (inferred)
   └─ route / (prefix) · HTTPRoute kgwctl-proxy/orders · rule 0 · match 0
      filters (request order):
        1 ext_auth/kgwctl-proxy/authz       enabled by route · GatewayExtension kgwctl-proxy/authz (inferred) · TrafficPolicy kgwctl-proxy/orders-extauth (extAuth, inferred)
        2 ratelimit/local                   enabled by route · TrafficPolicy kgwctl-proxy/orders-ratelimit (rateLimit.local, inferred)
        3 dynamic_modules/simple_mutations  enabled by route · TrafficPolicy kgwctl-proxy/orders-transformation (transformation, inferred)
        4 envoy.filters.http.router         on
      not run: global_disable/ext_auth (off)
      policies: TrafficPolicy kgwctl-proxy/orders-extauth (extAuth), TrafficPolicy kgwctl-proxy/orders-ratelimit (rateLimit.local), TrafficPolicy kgwctl-proxy/orders-transformation (transformation)
      backend: kube_kgwctl-proxy_backend_8080
ignored dump sections: BootstrapConfigDump, ClustersConfigDump, ScopedRoutesConfigDump, SecretsConfigDump
```

No. The route runs external auth first, then the local rate limit, then the transformation (`dynamic_modules/simple_mutations`), and last the router. Every filter but the router is off on the Gateway and switched on by this route. Each filter entry names the TrafficPolicy that configures it, and the `policies` line lists them all.

`(inferred)` marks a value kgwctl read from a name or a table, not from metadata. `(from the command line)` marks the Gateway that `--gateway` named. The last line of the tree lists the dump sections kgwctl does not read. Live (`kgwctl proxy routes proxy -n kgwctl-proxy --route kgwctl-proxy/orders`), the first line names the Gateway, the pod and the capture time instead of the file.

After the tree, `--route` lists the routes it did not match (see below). Every warning comes last, once, as a `warning:` line.

`-o json|yaml` prints one `ProxyRoutes` document ([`docs/output.md`](docs/output.md)): the source, the listener tree with each route's origin and filters, the selection and the warnings. It leaves out the dump's raw config (`typedConfig`, `typedPerFilterConfig` and `metadata`); `proxy dump` gives the dump unchanged. Each `id` is valid only within one document, so don't store it or compare it across dumps. Exit codes: `0` when printed, `2` on any error.

The text and the document are smaller than the dump, but they can still hold secrets: header and query match values, header values that routes add, and the error details of a listener Envoy rejected. These can contain credentials or tokens. Check the output before you attach it to a ticket, as with `proxy dump`.

#### How `--route` picks routes

kgwctl must find out which HTTPRoute each Envoy route comes from. Sometimes it can't be sure. `--select` sets how sure it must be before `--route` shows a route.

**Why it can be unclear.** kgateway can add *route metadata* to each Envoy route: data that names the HTTPRoute the route comes from. It does this when the Helm value `enableRouteSourceMetadata` is `true`. Without route metadata, kgwctl reads the HTTPRoute from the Envoy route name. kgateway builds that name by joining parts with dashes: `httproute-<name>-<namespace>-<rule>-<match>`. HTTPRoute `team-a/orders` gives `httproute-orders-team-a-0-0`. HTTPRoute `a/orders-team` gives the same string. So the name alone can't tell which HTTPRoute it is. Each way to split the name is a *reading*. Every dash in the HTTPRoute's name or namespace is another place to split, so another possible reading. With no dash in either, the name has one reading. The sample above has the same problem: `kgwctl-proxy/orders` and `proxy/orders-kgwctl` both give `httproute-orders-kgwctl-proxy-0-0`. Route metadata was on there, so kgwctl knew the answer.

**The three values.** Each value accepts what the one before it accepts, plus one more case.

1. `exact`: only routes whose origin is certain, because they carry route metadata. With `--rule`, the rule index and the match index must both be known and come from route metadata too.
   ```bash
   kgwctl proxy routes proxy -n kgwctl-proxy --route kgwctl-proxy/orders --select exact
   ```
2. `strict`, the default: also a route whose name has exactly one reading.
   ```bash
   kgwctl proxy routes proxy -n kgwctl-proxy --route kgwctl-proxy/orders
   ```
3. `known`: also a route whose name has several readings when only one of those HTTPRoutes exists in the cluster now. The output marks it "chosen from known HTTPRoutes".
   ```bash
   kgwctl proxy routes proxy -n kgwctl-proxy --route kgwctl-proxy/orders --select known
   ```

| Envoy route | `exact` | `strict` | `known` |
|---|---|---|---|
| Has route metadata | match | match | match |
| No route metadata, one reading | listed as inferred | match | match |
| Several readings, one of them in the cluster | ambiguous | ambiguous | match, chosen from known HTTPRoutes |
| Several readings, more than one in the cluster | ambiguous | ambiguous | ambiguous |

"Match" means the route is shown in the tree. "Listed as inferred" and "ambiguous" mean it is only named in a list after the tree, never shown as a match. In the third row, `known` matches only when that one HTTPRoute is the one you gave to `--route`.

**Why `strict` is the default.** `known` trusts the HTTPRoutes in the cluster now. But the proxy can still serve older config:

1. HTTPRoute `a/orders-team` exists. The proxy serves its route `httproute-orders-team-a-0-0`.
2. Someone deletes `a/orders-team` and creates `team-a/orders`.
3. The proxy has not received the new config yet. It still serves the old route.
4. `known` sees only `team-a/orders` in the cluster, so it says the route comes from `team-a/orders`. That is wrong.

`strict` never makes this guess. Use `known` only when you know the proxy has the latest config.

**`--file`.** `known` needs the cluster's HTTPRoutes, so `--select known` with `--file` is a usage error (exit `2`). `exact` and `strict` work with `--file`.

**A failed list.** Live, if kgwctl can't list the HTTPRoutes, for example because RBAC denies it, `known` chooses nothing and prints `Warning: --select known: no route can be chosen from known HTTPRoutes` on stderr.

**Route metadata.** For routes that carry route metadata, all three values give the same answer.

**How to see the choice.** In the text:

- A matched route read from its name ends in `(inferred)`, for example `HTTPRoute shop/orders · rule 0 · match 0 (inferred)`.
- A route `known` chose ends in `(chosen from known HTTPRoutes)`.
- `inferred routes, not matched:` lists the routes `exact` did not accept (only under `exact`).
- `ambiguous routes, not matched:` lists the routes with several readings. When only one of those HTTPRoutes is in the cluster, the line ends in `one candidate exists in the cluster`. `known` matches such a route when that one is the HTTPRoute you asked for.

In JSON, `selection.strategy` is the value used, each entry of `selection.routes` has `chosenBy` (`select`, or `known` for a route `known` chose), and `selection.inferred` lists the routes `exact` did not accept.

**`known` and krp.** krp, the command-line tool of [xdsview](https://github.com/DuncanDoyle/xdsview), uses the same rule as `known`. But krp picks between the readings with the `--route` you give it. kgwctl uses the cluster's HTTPRoutes. So the two can differ. When both colliding HTTPRoutes exist, krp picks the one you asked for, and `kgwctl --select known` picks neither.

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

`--skip <collector>,...` leaves collectors out, and `--list-collectors` prints their names. `--since` (default `24h`) bounds logs and Events, and `--tail` (default `5000`) bounds the log lines per container. `--skip proxy-admin` does not stop port-forwarding, because `version` also port-forwards to each proxy. Skip both to avoid it. `--timeout` (default `5m`) ends collection, and `--max-size` (default `512Mi`) caps the uncompressed size. `-o` names the file, `-o -` writes to stdout. The file is created with mode `0600`, and kgwctl never overwrites one. Exit codes: `0` when the bundle holds cluster data, even a partial bundle. `2` when no bundle was written, or when the bundle holds no cluster data, for example because the cluster was unreachable. Once the output file exists, kgwctl writes a bundle even then, so a script that checks for the file must also check the exit code.

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

Pass Kubernetes resources and lists to `Object` with their kind metadata intact. The Sink rejects Secrets and ConfigMaps, including their list kinds and either kind inside a List. It also rejects an object or List item without a kind, such as one from a typed clientset list. Use `Text` for plain text and `Prometheus` for metrics. Emit resources separately from reports and logs. The Sink has no JSON or config-dump method. New structured report formats require a reviewed schema and a writer-owned redactor.

Collectors are trusted code, and the Sink does not stop one that opens its own client. A name that is invalid, or repeats a built-in or another added collector, makes `bundle` exit `2`.

`cli.WithProvenanceModules` replaces the xdsview modules `proxy routes` uses to find where routes and filters come from; the default is xdsview's `kgateway.New()`. To add your own findings, wrap `kgateway.New()` in a module under your own name. Two modules with one name make `proxy routes` exit `2`.

[`examples/downstream`](examples/downstream) is a complete example, including a bundle collector and a provenance module.

## Development

```bash
make test           # unit and envtest suite
make vet lint build
```

`hack/release.sh <tag>` builds the release archives from a clean tree at the tag commit into `hack/release/out/<tag>/`; `RELEASE_DRY_RUN=1` skips those checks and writes to `hack/release/out/dry-run/<tag>/`.

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
