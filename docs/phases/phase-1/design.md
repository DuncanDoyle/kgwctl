# Phase 1 — Foundation and resource inspection: Design

**Status:** approved 2026-11-05 (D-010). r4; Codex rounds 1–4 in reviews/
**Base:** `779c421`
**Roadmap:** [Phase 1](../../roadmap.md#phase-1--foundation-and-resource-inspection)
**Program edges:** E2 (kgateway-ui `pkg/cache` + `pkg/engine`, one-shot)

Calls marked *(our call)* were made by the author without owner input. Check those first.

## 1. Goal and scope

Phase 1 delivers a CLI that answers one question well: **what do the Gateway API and kgateway resources say, and did a controller accept them?** It also lays the foundation every later phase builds on: the command tree, cluster access, output formats and the extension API for vendor builds.

In scope:
1. Cobra scaffold, cluster access flags, `version`.
2. `get` and `describe` for every kind the kgateway-ui library models (§5), with status conditions.
3. Output: table, `-o wide`, `-o yaml`, `-o json`.
4. The extension API: a downstream `main` builds a vendor binary from this module (§7).
5. A short design proposal for kgateway maintainers (program P-005), drafted in [`maintainer-proposal.md`](maintainer-proposal.md). Filing it needs the owner's go-ahead.

Out of scope: `check` (Phase 2), Envoy views (Phase 3), analysis and `analyze -f` (Phase 4), attached and effective policy (Phase 5), support bundle (§12), any write to the cluster, port-forwarding.

### Anti-goals

- **A status view that looks healthy when it isn't.** If no controller wrote status, or status is stale, the output says so. It never shows a blank or a default.
- **A second source of truth.** Attachment, ownership and conditions come from the library (D-003). The CLI formats; it does not decide.
- **A kubectl clone.** `get -o yaml` exists for scripting, but the value is the summary columns and the relations in `describe`.
- **Vendor names in this repo** (D-004).

## 2. Evidence

Raw notes with case references are kept out of git (the repo is public). The summary below cites public sources only.

**What users get stuck on.** In kgateway issues since 2025, the recurring troubleshooting themes are, roughly by frequency:
1. Status missing, empty or misleading. Routes stuck at "Waiting for controller", orphan routes and ListenerSets, and empty messages ([#12224](https://github.com/kgateway-dev/kgateway/issues/12224), [#14012](https://github.com/kgateway-dev/kgateway/issues/14012)). A controller cannot write status on a route whose parent it doesn't own, so only a client-side tool can flag that case.
2. A policy is not applied, or it is unclear which one wins ([#12846](https://github.com/kgateway-dev/kgateway/issues/12846), [#11160](https://github.com/kgateway-dev/kgateway/issues/11160)).
3. Status reports Accepted and Programmed, but Envoy rejected the config ([#14453](https://github.com/kgateway-dev/kgateway/issues/14453), [#11983](https://github.com/kgateway-dev/kgateway/issues/11983)).
4. ListenerSet attachment ([#12804](https://github.com/kgateway-dev/kgateway/issues/12804)), bad references ([#11998](https://github.com/kgateway-dev/kgateway/issues/11998)), delegation ([#11551](https://github.com/kgateway-dev/kgateway/issues/11551)), ReferenceGrant ([#11439](https://github.com/kgateway-dev/kgateway/issues/11439)).
5. Version skew between controller, proxies and the Gateway API CRDs ([#13735](https://github.com/kgateway-dev/kgateway/issues/13735)).

Support history from production users matches this order. Theme 3 is the most expensive one there: outages where every status was green and Envoy was rejecting config. Users also ask for:
- one command that says "is my install healthy", usable as a CI gate after apply or upgrade;
- a debug bundle with secrets redacted by default;
- a `version` that shows controller, proxy and CRD versions side by side.

Support engineers collect all of this by hand today: port-forwards, curl to admin ports, loops over API groups.

**Phase 1's share.** Themes 1 and 4 are answered by `get` and `describe` when they show per-parent conditions, staleness and "no status" explicitly. `version` covers theme 5's visibility. Themes 2 and 3 belong to Phases 5 and 2.

**Other CLIs.** istioctl, egctl, gwctl, linkerd, cilium-cli, glooctl and cmctl were surveyed. Conventions worth following:
- kubectl-style flags;
- `-o table|wide|json|yaml` on every command, with a versioned JSON schema;
- stable finding codes;
- exit codes that are the same in every output format (istioctl's `analyze` sets them only for its default output);
- read-only by default.

Complaints to design against:
- **Port-forwarding:** it is fragile and needs `pods/portforward`.
- **Large clusters:** hangs at hundreds of namespaces.
- **Goroutine leaks** when the CLI is embedded as a library.
- **Kube context ignored:** `--context` not honoured everywhere.
- **Vendor builds drifting** from the OSS binary.

**Prior attempts.** Two earlier kgwctl prototypes forked gwctl, and both stalled on ownership and upstreaming, not on design (D-001, D-008). Ideas reused: relation graph, policy label discovery, `check`, `analyze -f`.

## 3. Commands

```
kgwctl [global flags] <command>

  get <kind>[,<kind>...] [name] [-n ns | -A] [-l selector] [-o table|wide|yaml|json]
  describe <kind> <name> [-n ns] [-o text|yaml|json]
  version [--client] [-o text|yaml|json]
  completion bash|zsh|fish|powershell
```

**Global flags** (§6): `--kubeconfig`, `--context`, `-n/--namespace`, `--request-timeout` (default `30s`), `--install <ns>/<name>`, `--all-controllers`.

**Kind names** resolve like kubectl: plural, singular, kind and short name, case-insensitive (`gateways`, `gateway`, `gw`). ListenerSet is two kinds in the library: `ListenerSet` (`gateway.networking.k8s.io`) and `XListenerSet` (`gateway.networking.x-k8s.io`). The alias `listenersets`/`listenerset`/`ls` lists both, in one table with a GROUP column. `xlistenersets` and `listenersets.gateway.networking.k8s.io` select one. Any `resource.group` form picks exactly that kind. `all` means every modelled kind except GatewayClass. *(our call: short names `gwc`, `gw`, `ls`, `hr`, `gr`, `tr`, `tlsr`, `tp`, `lp`, `bcp`, `be`, `gwp`, `btlsp`)*

### 3.1 `get`

One table per kind, kubectl-style. Columns show status summaries the library already computes:

| Kind | Columns (`wide` adds) |
|---|---|
| GatewayClass | NAME, CONTROLLER, ACCEPTED, AGE (+ PARAMETERS) |
| Gateway | NAMESPACE, NAME, CLASS, ADDRESS, PROGRAMMED, LISTENERS, ROUTES, AGE (+ LISTENERSETS, INSTALL) |
| ListenerSet | NAMESPACE, NAME, PARENT, ACCEPTED, PROGRAMMED, LISTENERS, AGE |
| HTTPRoute, GRPCRoute, TCPRoute, TLSRoute | NAMESPACE, NAME, HOSTNAMES, PARENTS, ACCEPTED, RESOLVED, AGE (+ BACKENDS, DELEGATION) |
| Policy kinds | NAMESPACE, NAME, ANCESTORS, ACCEPTED, AGE |
| Other kinds | NAMESPACE, NAME, CONDITIONS, AGE |

**Object conditions** (GatewayClass, Gateway, ListenerSet, other kinds) render as follows:
- `True`, `False (<Reason>)` or `Unknown (<Reason>)`;
- `<no status>` when the condition is absent;
- `<stale>` when the condition's `observedGeneration` is behind `metadata.generation` (`objects.IsStale`, evaluated per condition).

**Per-parent and per-ancestor conditions** (routes: ACCEPTED, RESOLVED; policies: ACCEPTED) summarise over a fixed denominator:

| Input | Rule |
|---|---|
| Denominator (routes) | the route's `spec.parentRefs` (`objects.RouteParentRefs`), deduplicated on the full defaulted parentRef, including `sectionName` and `port`. Two listeners of one Gateway are two parents. `describe` notes exact duplicates. |
| Denominator (policies) | the `status.ancestors` entries (`objects.PolicyAncestors`); with none, the cell is `<no status>` |
| Status entries of one parent | the `status.parents` entries whose full parentRef matches it (`objects.RouteParents`), as the library matches at `explore/rows.go:283-285` |
| State: `accepted` / `rejected` / `stale` | an entry from one of our controllers (§6.2) whose condition is True / False (with its reason) / stale (`objects.IsStale`). With entries from several of ours, the one from the install owning the parent wins; failing that, the worst. |
| State: `other` | entries only from other controllers |
| State: `unwatched` / `declared` | no entry from ours, and the owning install's `explore` index places the route in that state (`Attachment` unwatched or declared) |
| State: `no status` | anything else |
| Cell, all parents accepted | `True` |
| Cell, otherwise | `<accepted>/<total>` plus each non-zero count, in the order rejected, stale, no status, unwatched, declared, other. Examples: `1/3 (1 stale, 1 no status)`, `0/1 (1 rejected: NotAllowedByListeners)`. |

The state comes from decoded status, not from the explorer's attachment. The explorer drops stale and foreign entries before attachment (`explore/rows.go:283-297`), so it can't distinguish them. The CLI only formats status. It decides nothing the controller didn't write (D-003). A route that is accepted on one parent and has no status on another never shows `True`. `describe` lists each parent's state. *(our call)*

`-o yaml` and `-o json` print the objects as stored: a `v1` `List`, `managedFields` stripped. Scripts get the shape they already know from kubectl. *(our call)*

### 3.2 `describe`

Text output in kubectl-describe style. Sections:
1. **Object:** kind, ref, install, age, generation and observedGeneration.
2. **Spec summary:** for a Gateway, its listeners (name, port, protocol, hostname, TLS mode, allowed routes); for an HTTPRoute, its rules (matches, filters, backendRefs); for a ListenerSet, its parent and listeners. These come from the typed views in `pkg/objects` (`DecodeGateway`, `DecodeListenerSet`, `HTTPRouteView`). Kinds without a typed view show no spec summary.
3. **Conditions:** one table per status block (object, per listener, per parent, per ancestor), with TYPE, STATUS, REASON, MESSAGE, LAST TRANSITION and OBSERVED GEN. Per-parent blocks name the `controllerName` that wrote them.
4. **Relations.** The source depends on whether a detected install owns the object:
   - **Owned (indexed):** from that install's `explore.Index`.
     - A Gateway gets its attached routes per listener, plus the routes that name it but are not accepted, each with the deciding condition, and its ListenerSets.
     - A route gets its parents with attachment state, its delegation parents and children, and its backends.
   - **Not owned (unindexed):** for example a route whose parent doesn't exist, a route of another controller under `--all-controllers`, or any object when no install was detected. The CLI prints only what the object itself declares: parentRefs and backendRefs decoded from spec, and status entries decoded from status. The section is labelled "Declared references (not resolved)".
   - **Policies, either way:** their ancestors come from `status.ancestors` (`objects.PolicyAncestors`), labelled as status. The library does not resolve targets yet (E7).
5. **Incomplete inputs:** the inputs the CLI could not read (§6.3) that affect this object's relations.

`-o json` and `-o yaml` emit the same content as a versioned document (§8).

Phase 1 adds no attached or effective policy view. The library reads policy only through `status.ancestors` today (E7, Phase 5).

### 3.3 `version`

```
Client:  kgwctl v0.1.0 (a1b2c3d, go1.26.0)
Install: kgateway-system/kgateway
  Controller: v2.4.5 (2/2 replicas)
  Proxies:
    default/http       v2.4.5  3 pods
    team-a/edge        v2.2.4  2 pods   differs from controller
Gateway API CRDs: v1.6.1 (standard)
```

- **Controller:** detected by the library's install detector (`install.UpstreamDetector` or a vendor detector), from the `app.kubernetes.io/version` label with the image tag as fallback.
- **Proxy versions:** from proxy Pods, grouped by Gateway, through the engine's process source.
- **CRD bundle version and channel:** from the CRD annotations.
- **Readiness:** proxy Pods and CRDs come from optional watches that don't hold back settle. So `version` waits for those inputs as well (§4.1). An input still unavailable at the deadline prints as `unknown (<reason>)`, never as absent.
- **Skew:** a difference is labelled, not judged. Supported-skew rules are a `check` finding in Phase 2.
- **Envoy version:** not shown. It needs the admin API, and that comes in Phase 3.
- **`--client`:** never contacts a cluster.

## 4. Architecture

```
cmd/kgwctl/main.go         ~10 lines: cli.NewRootCommand(...) and Execute
pkg/cli/                   public extension API: NewRootCommand, Options (§7)
internal/cmd/              get, describe, version, completion (cobra wiring only)
internal/load/             one-shot load: cluster config → cache → settled snapshot → engine.Compute
internal/resolve/          kind-name resolution against the served kinds
internal/view/             model → view structs, one per command (pure, no I/O)
internal/print/            table, describe text, yaml/json encoders
examples/downstream/       vendor-style main using only pkg/cli; built in CI
```

**Flow for every cluster command:**
1. Build `rest.Config` (§6) and one deadline for the command (§4.4).
2. `load.Model(ctx, cfg, registry, needs)`:
   - **Discovery:** `kinds.Resolve`.
   - **Cache:** build `cache.Config` and `engine.Config` the way kgateway-ui's `pkg/app` does, then `cache.New` and `Start`. Wait until the command's inputs are ready (§4.1) or the deadline passes.
   - **Engine:** `Snapshot`, then `engine.New(...).Compute()`.
   - **Teardown:** cancel the context (§4.3).
3. Build the view, then print it.

`Compute` runs once and in sync. No `Run` loop, scrape loop or monitor is ever started.

### 4.1 Readiness per command

`Settled()` covers only the core inputs: Namespaces, served kinds, Pods and Deployments. Each command declares which further inputs it needs, and `load` waits until each of them is OK, denied or failed in `Snapshot.Access`:

| Command | Waits for |
|---|---|
| `get`, `describe` | core settle |
| `version` | core settle, plus proxy Pods and CRDs |

An input still loading at the deadline counts as unavailable (§6.3).

### 4.2 What Phase 1 reads from the model

The engine is configured the way the UI configures it:
- the registry's checks;
- `process.Kube{}` for processes;
- a static metrics source with no data;
- the managed-count verifier.

Checks run inside `Compute` and may shape explorer rows. Phase 1 reads only these model fields:
- `Snapshot` (objects, Access, Namespaces, Pods, Deployments, ProxyPods, CRDs);
- `Installs[].Resolved` and `.Owned`;
- `Installs[].Index` (relations and attachment);
- the process sets used for versions.

It never prints `Health`, `Overview`, `Diagnosis` or findings. Their values are partial without metrics and a second observation, and they belong to Phase 2.

Once kgateway-ui exports its config builder (its D-053, requested in DuncanDoyle/kgateway-ui#13), `load` swaps the hand-built configs for it. Until then, the wiring copy stays in one file.

### 4.3 Shutdown

The library cache has no stop or join call. Cancelling the context signals informers and readiness loops to exit, but `Execute` cannot wait for them.

The CLI process exits right after, so this is harmless there. Embedders such as tests and downstream builds can see short-lived goroutines after `Execute` returns. Phase 1 promises the following:
- cancellation is always signalled before `Execute` returns, including on error and timeout;
- no command starts a loop that ignores the context;
- repeated in-process invocations do not accumulate goroutines once their requests finish.

A stop/wait contract on the cache is request R4.

### 4.4 Deadline

`--request-timeout` sets one deadline for the command's **I/O**:
- **HTTP requests:** every request kgwctl makes goes through a `rest.Config.WrapTransport` round tripper bound to the command context. When the context ends, in-flight requests are cancelled and new ones fail at once.
- **Discovery:** client-go's `ServerResourcesForGroupVersion` runs `Do(context.TODO())`, so its retry waits (`Retry-After`) and client-side throttling sit above the transport. kgwctl therefore passes `kinds.Resolve` its own adapter for the one-method `kinds.DiscoveryClient` interface (`kinds.go:62`). The adapter issues the same GET with `Do(ctx)`, which bounds retries and throttling by the deadline as well.
- **Readiness wait:** stops at the deadline. Precedence:
  - **requested kinds pending:** the command fails with a timeout;
  - **any other pending input:** it becomes `loading` (§6.3) and output continues.
- **`Compute`:** takes no context. In the CLI's wiring it does no I/O: metrics are static, and processes are read from the snapshot. It always runs once on what loaded, even after the deadline has passed, because its cost is CPU over data already in memory.

On timeout, the error names the stage (discovery or loading) and, for loading, the requested kinds that were not ready.

**Why not plain dynamic-client LISTs?** The library's install detection, ownership and relation index need the full snapshot, not one kind. Rebuilding them breaks D-003.

## 5. Kinds

Phase 1 covers exactly what `pkg/kinds` models and the cluster serves:
- **Gateway API:** GatewayClass, Gateway, ListenerSet (v1 and the experimental group), HTTPRoute, GRPCRoute, TCPRoute, TLSRoute, BackendTLSPolicy.
- **kgateway:** TrafficPolicy, ListenerPolicy, HTTPListenerPolicy, BackendConfigPolicy, GatewayParameters, Backend.

Kinds the cluster doesn't serve are dropped silently. `get <kind>` for one of them says so and exits 1.

**Gaps:** DirectResponse, GatewayExtension and ReferenceGrant are not modelled. ReferenceGrant matters for theme 4. Adding them is a library change, so it goes upstream (§11, request R1) rather than into CLI-local descriptors (D-003). Phase 1 ships without them if R1 isn't delivered. *(our call)*

Secrets and ConfigMaps are never read. The library excludes them on purpose.

## 6. Cluster access, scope and permissions

### 6.1 Flags

- **Client library:** `client-go` `clientcmd` with the standard loading rules. That covers `KUBECONFIG`, `--kubeconfig`, `--context` and exec credential plugins.
- **Namespace:** `-n` defaults to the context's namespace. `-A` selects all namespaces.
- **Why not `cli-runtime`:** it would add the full kubectl flag set for six flags we need. *(our call, D-005)*
- **Context handling:** every command honours `--context`. The global context is never read or written beyond the standard loading rules.

### 6.2 Installs and ownership

The engine detects kgateway installs and assigns objects to them (`InstallModel.Owned`). Ownership is only meaningful for the kinds the library attributes:
- GatewayClass, Gateway and ListenerSet;
- routes;
- policies.

Parameter kinds (GatewayParameters) and Backends are never attributed (`kinds.go:30-35`).

**Rule: hide only what is positively someone else's.** By default, `get` shows every object in the selected namespaces except attributed-kind objects that **another** controller owns:

| Object | Hidden by default when |
|---|---|
| GatewayClass | its `spec.controllerName` is not a detected install's controller |
| Gateway | its class is hidden |
| ListenerSet, route | it has at least one parentRef, and every current parentRef resolves to an existing, hidden Gateway, ListenerSet or parent route. Status is not consulted: an old entry from another controller never hides a route that now points at ours or at nothing. |
| Policy | it has status ancestors, and all of them come from other controllers. This is a known limit: targets aren't decoded until E7, so a policy retargeted to ours keeps the old controller's ancestors until it is reconciled. |
| Parameters, Backends, other unattributed kinds | never |

So the following stay visible by default:
- routes whose parent doesn't exist (`Owned.Unattached`);
- routes with no parentRefs (`Owned.NoParent`);
- declared delegation candidates;
- routes in namespaces the controller doesn't watch;
- policies no controller accepted.

They show `<no status>` or `0/n`, which is theme 1. Absence from `Owned` alone never hides an object or labels it `<not ours>`.

**Flags and special cases:**
- **`--all-controllers`:** shows hidden objects too, with `other` in their status cells.
- **`--install ns/name`:** narrows to one install when several exist. Hidden then also covers objects owned by the other detected installs. An `INSTALL` column appears when more than one install is detected.
- **No install detected,** or an input ownership depends on is unavailable (§6.3): nothing is hidden, and stderr says why.

*(our call. It honours the UI owner's feedback that other controllers' routes confuse multi-controller clusters, without hiding the orphans this tool exists to show. Routes in unwatched namespaces are shown on purpose: they name our Gateway and will never get status.)*

### 6.3 Permissions

kgwctl is read-only. It needs `get`, `list` and `watch` on:
- the served kinds;
- Namespaces, Pods and Deployments;
- CustomResourceDefinitions (for `version`).

It needs no `pods/portforward`, no `create` and no Secrets.

**Unavailable inputs.** Each input is in one of these states, read from `Snapshot.Access` after the readiness wait:
- `ok`;
- `loading` (still pending at the deadline);
- `denied` (403);
- `failed` (any other error: auth, transport, server).

The library settles failed inputs as well, and their stores may hold partial data. Every derived output therefore names its inputs, and an output whose inputs aren't all `ok` is never computed from what loaded:

| Output | Depends on | When an input is not `ok` |
|---|---|---|
| Rows of a requested kind | that kind | error naming the kind, state and reason; exit 1. Partial store contents are never printed. |
| Hiding rule (§6.2), INSTALL column | GatewayClasses, Gateways, ListenerSets, Namespaces, Deployments, controller Pods (scope reads a running Pod's env before the Deployment template, `install/scope.go:95-99`) | hiding switched off; INSTALL shows `unknown`; stderr warning |
| Status cells | the object itself; `unwatched`/`declared` also need the index inputs below | cells still render; an index-only state degrades to `no status` with a warning |
| Index-derived fields: ROUTES and LISTENERSETS columns, `describe` relations | Namespaces, Deployments, controller Pods, GatewayClasses, Gateways, ListenerSets, all route kinds | `unknown (<input>: <state>)`; `describe` prints "Relations unknown" and lists the inputs |
| Proxy versions | the ownership inputs above (they enumerate the owned Gateways), plus proxy Pods | `unknown (<input>: <state>)`, for the whole Proxies block when ownership inputs are down |
| CRD version | CRDs | `unknown (<state>)` |

Versioned JSON output carries the same information in an `incomplete` list of `{input, state, reason}` entries. Warnings never go to stdout.

**List allowed, watch denied.** This is outside the supported permissions until R2 lands. The library may settle a kind from its LIST before the initial WATCH fails, so the result is either correct data with exit 0 or a `denied` error with exit 1. It is never an empty or partial table with exit 0, and that invariant is tested.

**Known limits, upstream:**
- *List-only access:* the library needs `watch` as well as `list`. kubectl needs only `list`. Request R2.
- *Namespace-restricted users:* the library lists cluster-wide, so they get denied kinds. Request R3. Phase 1 documents both limits.

### 6.4 Timeouts and scale

- **Timeout:** see §4.4.
- **Scale target:** load and print `get httproutes -A` in under 5 s, on a cluster with 1,000 namespaces, 5,000 HTTPRoutes and 500 Gateways, measured (plan task). The UI already holds this model in memory, so the cost is one LIST and one WATCH open per kind.
- **Embedding:** lifecycle guarantees are in §4.3.

## 7. Extension API for vendor builds

**Model:** the OSS module is a library plus a thin `main`. A vendor ships its own `main` that imports `pkg/cli`, registers its kinds, detectors and commands, and builds its own binary. Nothing vendor-specific lives here. The same model is used by cilium-cli (`Hooks`) and by kgateway-ui (its D-003, D-038). The survey ranked it above PATH plugins and forks for an "OSS core plus extended build". (D-009)

```go
// A vendor's main.go in its own repo. Names are illustrative.
func main() {
	reg := extension.New( // kgateway-ui's registry: one place for kinds, detectors, checks
		extension.WithKinds(vendorkinds.All()...),
		extension.WithDetectors(vendordetect.Detector{}),
	)
	root := cli.NewRootCommand(
		cli.WithName("vendorctl"),  // binary name in help and completions
		cli.WithRegistry(reg),      // kinds and detectors reach get/describe/version
		cli.WithCommands(vendorcmd.License()), // extra top-level commands
		cli.WithBuildInfo(version.Info()),
	)
	os.Exit(cli.Execute(root))
}
```

Phase 1 surface:
- `NewRootCommand(opts ...Option) *cobra.Command`;
- `Execute(*cobra.Command) int`;
- options `WithName`, `WithRegistry`, `WithCommands`, `WithBuildInfo`.

**Registry capabilities Phase 1 supports:** kinds, detectors, scope provider, scope verifier, process source, checks (run, not printed), and computed rules. The rest are long-running collectors (proxy stats, metrics sources) or UI-only (commands for the UI's API, authorizer, capabilities). For those, `NewRootCommand` logs one debug line per ignored capability and otherwise ignores them.

**Later phases:**
- **Phase 2:** checks that need collector data require a one-shot acquisition contract for collector inputs. Phase 2 designs it together with E8 (kgateway-ui's `ctrlmetrics.Static`). Forwarding the registry alone is not enough.
- **Phase 5:** policy semantics.
- **Bundle phase:** collectors.

Vendor kinds show up in `get` and `describe` with the generic columns (§3.1). Custom columns per kind are deferred until a vendor needs them.

**Stability:** `pkg/cli` is the only exported package. Everything else is `internal/`. `pkg/cli` follows semver from `v0.1.0`. Breaking changes need a `decisions.md` entry.

**`examples/downstream`** is built and tested in CI, so the seam can't rot. It registers:
- a fake policy kind, which `get` lists and the hiding rule keeps;
- a fake detector with a different controller name, whose install `version` reports;
- a fake top-level command.

## 8. Output contract

- **Tables:** `text/tabwriter`, no colour, no truncation of names. Each column has a header. The table outputs are not a stable interface; JSON is.
- **Versioned JSON/YAML** (`describe`, `version`) uses an envelope like krp's:
  ```json
  {"schemaVersion": "kgwctl.kgateway.dev/v1alpha1", "kind": "Describe", "item": { ... }}
  ```
  Field names are camelCase and the schema is documented in `docs/output.md`. Adding fields is non-breaking. Renaming or removing a field bumps `schemaVersion`. *(our call: the group is a placeholder until maintainers pick a name, D-006)*
- **Streams:** errors go to stderr and data to stdout. Warnings never mix into `-o json` output.
- **Exit codes:** for Phase 1 they are `0` success and `1` error, the same in every output format. D-007's `0/1/2` scheme belongs to `check`. *(our call)*

## 9. Dependencies

| Dependency | Why | Notes |
|---|---|---|
| `github.com/DuncanDoyle/kgateway-ui` | the model (E2) | tag requested, DuncanDoyle/kgateway-ui#16; only `pkg/cache`, `engine`, `kinds`, `objects`, `explore`, `install`, `extension` |
| `k8s.io/client-go` v0.36.x | cluster access | the version the UI and krp use |
| `github.com/spf13/cobra` v1.10.x | commands, completion | the version krp uses |
| `sigs.k8s.io/yaml` | `-o yaml` | already a UI dependency |

No `cli-runtime`, controller-runtime, gateway-api types or colour library in the binary. The UI works on `unstructured` and so does kgwctl. `pkg/extension` imports the UI's `pkg/api` and with it `net/http`. That package is already linked through client-go, so the binary-size cost is small. Checked in the plan.

**Test-only:** `sigs.k8s.io/controller-runtime/pkg/envtest`, which provides a real kube-apiserver and etcd for command tests (§10). The binaries are fetched with `setup-envtest`, pinned to the client-go minor. It is never imported outside `_test.go` files and `internal/testenv`, and a test asserts that the `kgwctl` binary's dependency list doesn't contain it. *(our call)*

## 10. Testing

- **Unit tests** cover pure functions on decoded objects, with no cluster: kind resolution, status cells, the hiding rule over a hand-built `load.Result`.
- **Command tests** run the real root command, through `pkg/cli` or the internal root, with production clients and a real kubeconfig. They hit a real API server and go through discovery, the cache and the engine, with golden files for every output format.
  - **Server:** envtest (kube-apiserver and etcd) with the Gateway API and kgateway CRDs installed. Fixtures are created from YAML in `testdata/`, and status is written through the status subresource.
  - **Fault proxy:** an `httptest` reverse proxy in front of envtest injects faults by resource, label selector and request kind. The request kinds are:
    - **acquisition:** a classic LIST, or an initial WatchList request (`watch=true&sendInitialEvents=true`). client-go 0.36 defaults to WatchList, so faults must apply to both.
    - **watch:** a plain WATCH after a LIST.
    - **discovery.**

    Faults are delays (outside any lock), HTTP errors with optional `Retry-After`, and watches held open, then failed. Every rule counts its hits, and a test fails if one of its rules never matched. The `pods#proxy` input is a Pods request carrying the registered detector's `ProxyPodSelector()`, so it can be faulted on its own.
  - **Isolation:** one envtest per package, with command tests run serially. Each test deletes every object it created, cluster-scoped ones included, and waits until they are gone. Namespaces are created once and reused, because envtest has no namespace controller.
  - **Contexts:** two proxies make two contexts in one kubeconfig. Each proxy counts its requests, so the test sees which server answered.
  - **Fixtures cover:**
    - a healthy install;
    - a route whose parent doesn't exist;
    - a route with no status;
    - stale status;
    - mixed parent states (accepted, no status, stale, other controller);
    - two listeners of one Gateway as two parents, one accepted and one rejected;
    - delegation;
    - two installs;
    - another controller's GatewayClass and Gateway (hidden, then shown with `--all-controllers`);
    - GatewayParameters and Backends shown with an install present;
    - an unserved kind;
    - an XListenerSet next to a ListenerSet.
  - **Retargeted route:** a route whose only status comes from another controller while its spec now points at ours (visible).
  - **Failure injection:**
    - Pods forbidden;
    - a non-403 failure on a requested kind;
    - Namespaces, Deployments and controller Pods each forbidden (hiding off, relations unknown);
    - Gateways forbidden during `version` (Proxies unknown, not empty);
    - list allowed but watch denied, both immediate and held past LIST sync (the invariant in §6.3). The held case runs in classic LIST/WATCH mode, with the WatchList client feature gate off for that test.
  - **Readiness:** proxy Pods and CRDs delayed and denied, each on its own. `version` shows them once they arrive, and shows `unknown` when the delay outlasts the deadline.
  - **Deadline:**
    - slow discovery, cumulative across group versions;
    - a discovery `429` with `Retry-After: 10` under a 1 s deadline.

    Both fail at the deadline with stage `discovery`.
  - **Contexts:** `--context` selects the second server for every request, discovery included.
- **Exit-code tests:** every command × output format × {success, not found, denied, failed, timeout}.
- **Downstream test:** the example's real `main` path (`pkg/cli`) runs with `--kubeconfig` pointing at the test server. Its fake policy kind appears in `get`, its detector's install appears in `version`, and its command runs.
- **Lifecycle tests:**
  - when a command ends on success, error or timeout, the fault proxy sees every watch connection closed within 2 s;
  - with a request held open by the proxy, `Execute` returns within 1 s of the deadline;
  - 20 in-process invocations leave no more open connections than one does.

  A goroutine count runs as supplementary evidence only.
- **Live check:** a script in two parts:
  1. `setup`, as admin: a kind cluster with its own `--kubeconfig`, kgateway OSS via Helm, the fixtures, and a read-only ServiceAccount bound to `docs/rbac.yaml`;
  2. `verify`: runs every command with any given kubeconfig, both admin and the ServiceAccount.

  It is run before phase close, observed, not assumed.
- **Scale benchmark:** the §6.4 target on a synthetic cluster, numbers recorded in the phase-complete file.

## 11. Requests to other repos

| ID | Repo | Ask | Blocks |
|---|---|---|---|
| R0 | kgateway-ui | tag the module (DuncanDoyle/kgateway-ui#16) | Phase 1 implementation start |
| R1 | kgateway-ui | model DirectResponse, GatewayExtension, ReferenceGrant | nothing; Phase 1 ships without them |
| R2 | kgateway-ui | a list-only one-shot snapshot (no watch) | nothing; documented limit |
| R3 | kgateway-ui | namespace-scoped listing for restricted users | nothing; documented limit |
| R4 | kgateway-ui | a cache stop/wait contract, so embedders can join shutdown | nothing; §4.3 promises signal, not join |
| — | kgateway-ui | config builder (#13, its D-053) | nothing; hand-wired until it lands |

R1–R4 are filed as `cross-repo` issues once this design is approved.

## 12. Roadmap implications (proposals for the owner)

The research points at gaps in the roadmap. None of them changes Phase 1.

1. **Support bundle.** It is the second most frequent ask, and support teams spend real hours on it. Secrets (license keys, Helm values) leak into hand-collected bundles today.
   - **Proposal:** a new phase after Phase 3, `bundle`. It needs Phase 3's admin-API access and must redact secrets by default, with an opt-in.
2. **"Accepted but rejected by Envoy"** is the costliest theme. Phase 2's xDS-rejection check covers it. It should be the first check built and the most tested.
3. **Version skew** belongs in Phase 2 `check` (controller vs proxies vs CRDs). The library already has the check (`health/skew.go`).
4. **Served-vs-running diff** (controller snapshot vs proxy config) solved real outages by hand. It stays deferred with krp Phase 6, as the roadmap has it.

## 13. Left to the builder

- Table column widths, ordering of rows (namespace, then name), age formatting (kubectl's).
- Spec-summary layout per kind in `describe`.
- Exact error and warning wording (short, plain, actionable).
- The fixture set beyond the cases in §10.

## 14. Open items

1. The owner reviews the *(our call)* items, especially the hiding rule (§6.2), the parent-status summary (§3.1) and `get -o yaml` as raw objects (§3.1).
2. Approval of D-009 (extension API defined in Phase 1, not Phase 4).
3. Approval of the test-only envtest dependency (§9, §10). It replaces client-go fakes, which can't model the faults this design depends on.
4. The roadmap proposals in §12.
5. Go-ahead to file the maintainer proposal (P-005).
