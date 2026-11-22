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

`get`, `describe` and `version` exit `0` on success and `1` on any error. `check` exits `0` when clean, `1` for problems and `2` when it could not check something. `proxy dump` and `proxy routes` exit `0` or `2`. Data goes to stdout; errors and warnings go to stderr. With `-o json|yaml`, `describe`, `version`, `check` and `proxy routes` print a versioned envelope, documented in [`docs/output.md`](docs/output.md); `get` prints a plain Kubernetes `List`. Tables and text layouts are not a stable interface.

## Permissions

kgwctl only reads. It needs `get`, `list` and `watch` on the Gateway API and kgateway kinds, Namespaces, Pods, Events, Services, EndpointSlices, Deployments and CustomResourceDefinitions, and `get` on `pods/proxy` for `check`. It never reads Secrets or ConfigMaps. [`docs/rbac.yaml`](docs/rbac.yaml) holds that ClusterRole, plus a sample ServiceAccount and binding. The reader role grants no `pods/portforward`; the optional Role at the end of the file adds it for the `proxy` commands (see [`proxy dump`](#proxy-dump-gateway)):

```bash
kubectl apply -f docs/rbac.yaml
```

Known limits:
- `watch` is required as well as `list` ([DuncanDoyle/kgateway-ui#18](https://github.com/DuncanDoyle/kgateway-ui/issues/18)).
- Objects are listed cluster-wide, so users limited to some namespaces get errors ([DuncanDoyle/kgateway-ui#19](https://github.com/DuncanDoyle/kgateway-ui/issues/19)).

When the kind `get` or `describe` was asked for can't be read, the command fails: exit `1`, empty stdout. Any other input that can't be read is named in a stderr warning, and the output marks what depends on it as unknown. `check` lists it under Not checked instead. With `-o json|yaml`, `describe` and `version` also list those inputs in the `incomplete` field.

## Extending

Downstream builds embed the CLI through [`pkg/cli`](pkg/cli). `cli.NewRootCommand` takes options for the binary name, a registry of extra kinds and install detectors, extra commands and build info. [`examples/downstream`](examples/downstream) is a complete example.

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
