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

Exit code `0` means success and `1` means any error, in every output format. Data goes to stdout; errors and warnings go to stderr. With `-o json|yaml`, `describe` and `version` print a versioned envelope, documented in [`docs/output.md`](docs/output.md); `get` prints a plain Kubernetes `List`. Tables and text layouts are not a stable interface.

## Permissions

kgwctl only reads. It needs `get`, `list` and `watch` on the Gateway API and kgateway kinds, Namespaces, Pods, Deployments and CustomResourceDefinitions. It never reads Secrets or ConfigMaps. [`docs/rbac.yaml`](docs/rbac.yaml) holds that ClusterRole, plus a sample ServiceAccount and binding:

```bash
kubectl apply -f docs/rbac.yaml
```

Known limits:
- `watch` is required as well as `list` ([DuncanDoyle/kgateway-ui#18](https://github.com/DuncanDoyle/kgateway-ui/issues/18)).
- Objects are listed cluster-wide, so users limited to some namespaces get errors ([DuncanDoyle/kgateway-ui#19](https://github.com/DuncanDoyle/kgateway-ui/issues/19)).

When the kind `get` or `describe` was asked for can't be read, the command fails: exit `1`, empty stdout. Any other input that can't be read is named in a stderr warning, and the output marks what depends on it as unknown. With `-o json|yaml`, `describe` and `version` also list those inputs in the `incomplete` field.

## Extending

Downstream builds embed the CLI through [`pkg/cli`](pkg/cli). `cli.NewRootCommand` takes options for the binary name, a registry of extra kinds and install detectors, extra commands and build info. [`examples/downstream`](examples/downstream) is a complete example.

## Development

```bash
make test           # unit and envtest suite
make vet lint build
```

The live check runs the commands against a real kgateway install on a local kind cluster. It writes its own kubeconfig files under `hack/live/out/` and needs `kind`, `helm`, `kubectl` and `gh`.

```bash
hack/live-check.sh setup                                   # cluster, Gateway API CRDs, kgateway, fixtures, reader ServiceAccount
hack/live-check.sh verify hack/live/out/admin.kubeconfig   # outputs in hack/live/out/admin/
hack/live-check.sh verify hack/live/out/reader.kubeconfig  # the same commands with docs/rbac.yaml permissions
hack/scale/run.sh                                          # scale benchmark: 1,000 namespaces, 500 Gateways, 5,000 HTTPRoutes
```
