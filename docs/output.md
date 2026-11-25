# Output schema

`describe`, `version`, `check` and `proxy routes` print a versioned envelope with `-o json` or `-o yaml`. Tables and the default text layout are not a stable interface; the envelope is. `get -o yaml|json` is not wrapped: it prints a plain Kubernetes `v1` List of the matching objects.

## Envelope

```json
{"schemaVersion": "kgwctl.kgateway.dev/v1alpha1", "kind": "Describe", "item": { }}
```

| Field | Type | Meaning |
|---|---|---|
| `schemaVersion` | string | Schema version, currently `kgwctl.kgateway.dev/v1alpha1`. |
| `kind` | string | `Describe`, `Version`, `Check`, `ProxyRoutes` or `BundleManifest`. |
| `item` | object | A `DescribeView`, a `VersionView`, a `Check`, a `ProxyRoutes` or a `BundleManifest`. |

## Conventions

- Field names are camelCase. Times are RFC3339 and nothing is relative to now, so there is no age field.
- A reference is always an object, never a string. `group` is `""` for the core group, except in `proxy routes`, where an empty `group` is omitted. `namespace` is absent only for cluster-scoped objects.
- `install` is `namespace/name` of the owning install, as in `get`.
- Optional fields are omitted when empty, not printed as null. Fields documented as always present are printed even when empty.
- Data goes to stdout and errors to stderr. Warnings never appear in JSON or YAML output, except the parser `warnings` of `proxy routes`.
- Exit codes: `get`, `describe` and `version` use `0` for success and `1` for any error. `check` uses `0` when no shown finding is an error or warning, `1` when one is (or on a usage error), and `2` when it could not check, as with no install found, or the report write returned an error. `proxy routes` uses `0` for success and `2` for any error, including a usage error. Exit codes are the same in every output format.

## Stability

Adding a field is not a breaking change, so readers must ignore unknown fields. Renaming or removing a field, or changing its meaning, bumps `schemaVersion`.

## Describe

### DescribeView

| Field | Type | Meaning |
|---|---|---|
| `object` | ObjectInfo | The described object. |
| `spec` | SpecSummary | Typed spec summary; absent for kinds without one. |
| `conditions` | ConditionBlock[] | Status blocks: the object's own first, then one per listener, parent or ancestor entry. Always present. |
| `relations` | Relations | What the object relates to; absent for kinds that have none. |
| `incomplete` | Input[] | Inputs the install and relations depend on that are not `ok`: ownership inputs for an attributed kind, explore index inputs for a Gateway, ListenerSet or route. Absent when all are `ok`. |

### ObjectInfo

| Field | Type | Meaning |
|---|---|---|
| `kind` | string | Object kind. |
| `group` | string | API group, `""` for the core group. |
| `namespace` | string | Absent for cluster-scoped objects. |
| `name` | string | Object name. |
| `install` | string | Owning install as `namespace/name`; absent when none owns the object or the kind is not attributed. |
| `installUnknown` | string | Why the owner could not be decided, as `namespaces: denied`. |
| `creationTimestamp` | string | RFC3339 creation time. |
| `generation` | integer | `metadata.generation`. |
| `observedGeneration` | integer | Observed generation of the object's status, when it reports one. |

### SpecSummary

| Field | Type | Meaning |
|---|---|---|
| `class` | string | Gateway class name of a Gateway. |
| `addresses` | string[] | Requested Gateway addresses. |
| `parent` | ParentRef | Parent Gateway of a ListenerSet. |
| `hostnames` | string[] | HTTPRoute hostnames. |
| `listeners` | ListenerSpec[] | Listeners of a Gateway or ListenerSet. |
| `rules` | RuleSpec[] | HTTPRoute rules. |

### ListenerSpec

| Field | Type | Meaning |
|---|---|---|
| `name` | string | Listener name. |
| `port` | integer | Listener port. |
| `protocol` | string | Listener protocol. |
| `hostname` | string | Listener hostname. |
| `tlsMode` | string | TLS mode; absent without a TLS block. |
| `allowedRoutes` | AllowedRoutes | Which routes may attach, with defaults applied. |

### AllowedRoutes

| Field | Type | Meaning |
|---|---|---|
| `from` | string | `Same`, `All` or `Selector`. |
| `selector` | string | Namespace selector when `from` is `Selector`. |
| `kinds` | GroupKind[] | Allowed route kinds as written; absent when the spec sets no explicit restriction. The protocol's default kinds still apply. |

### GroupKind

| Field | Type | Meaning |
|---|---|---|
| `group` | string | API group. |
| `kind` | string | Kind. |

### RuleSpec

| Field | Type | Meaning |
|---|---|---|
| `name` | string | Rule name, when set. |
| `matches` | MatchSpec[] | Rule matches. |
| `filters` | FilterSpec[] | Rule filters. |
| `backendRefs` | BackendRefSpec[] | Backends, or delegations to child routes. |

### MatchSpec

| Field | Type | Meaning |
|---|---|---|
| `path` | ValueMatch | Path match. |
| `headers` | KeyMatch[] | Header matches. |
| `queryParams` | KeyMatch[] | Query parameter matches. |
| `method` | string | HTTP method. |

### ValueMatch

| Field | Type | Meaning |
|---|---|---|
| `type` | string | Match type, as `PathPrefix`. |
| `value` | string | Value to match. |

### KeyMatch

| Field | Type | Meaning |
|---|---|---|
| `type` | string | Match type, as `Exact`. |
| `name` | string | Header or parameter name. |
| `value` | string | Value to match. |

### FilterSpec

| Field | Type | Meaning |
|---|---|---|
| `type` | string | Filter type. |
| `target` | Ref | Target of an `ExtensionRef` or `RequestMirror` filter. |

### BackendRefSpec

A backend carries the reference fields inline. A delegation has only `delegation`.

| Field | Type | Meaning |
|---|---|---|
| `group` | string | Backend group, `""` for the core group. |
| `kind` | string | Backend kind. |
| `namespace` | string | Backend namespace. |
| `name` | string | Backend name. |
| `port` | integer | Backend port. |
| `weight` | integer | Weight; absent when the spec leaves the default. |
| `delegation` | DelegationSpec | Set when the backendRef delegates to child routes. |

### DelegationSpec

| Field | Type | Meaning |
|---|---|---|
| `mode` | string | `name`, `wildcard` or `label`. |
| `namespace` | string | Child route namespace. |
| `name` | string | Child route name for mode `name`. |
| `label` | string | Label selector for mode `label`. |

### ConditionBlock

| Field | Type | Meaning |
|---|---|---|
| `scope` | string | `object`, `listener`, `parent` or `ancestor`. |
| `listener` | string | Listener name for scope `listener`. |
| `ref` | ParentRef | Parent or ancestor for those scopes. |
| `controller` | string | Controller that wrote the block. |
| `conditions` | ConditionRow[] | The block's conditions. Always present. |

### ConditionRow

| Field | Type | Meaning |
|---|---|---|
| `type` | string | Condition type. |
| `status` | string | `True`, `False` or `Unknown`. |
| `reason` | string | Condition reason. |
| `message` | string | Condition message. |
| `lastTransition` | string | RFC3339 time of the last transition. |
| `observedGeneration` | integer | Generation the condition was computed for. |
| `stale` | boolean | True when the condition is behind the object's generation. |

### Relations

Which fields are set depends on the kind: Gateways and ListenerSets fill the route fields, routes fill the parent and delegation fields, policies fill `ancestors`.

`resolved` is absent when relations are unknown, and `unknown` names the inputs that kept them from being computed. `resolved: false` means no owning install indexed the object, so only `declared` is filled.

Gateway relations list one row per parent link. `attachedRoutes`, `notAccepted` and `pending` therefore need not add up to the ROUTES count of `get`, but they agree on distinct routes. `notAccepted` is the rejected set that `get` counts.

| Field | Type | Meaning |
|---|---|---|
| `resolved` | boolean | Whether an owning install indexed the object. |
| `unknown` | string | The inputs that kept relations from being computed. |
| `parents` | ParentSummary[] | A route's parents. |
| `duplicateParents` | ParentRef[] | Parents the route lists more than once. |
| `attachedRoutes` | ListenerRoutes[] | Accepted routes, per listener. |
| `notAccepted` | RouteLink[] | Route links the install rejected. |
| `pending` | RouteLink[] | Links without a current status: pending, unwatched or declared. |
| `listenerSets` | Ref[] | ListenerSets attached to a Gateway. |
| `delegationParents` | Ref[] | Routes that delegate to this route. |
| `delegationChildren` | DelegationChild[] | Child routes this route delegates to. |
| `backends` | Backend[] | Backends the route sends or mirrors to. |
| `ancestors` | ParentSummary[] | A policy's ancestors. |
| `declared` | DeclaredRefs | References the spec makes, unresolved. |

### ParentSummary

| Field | Type | Meaning |
|---|---|---|
| `parent` | ParentRef | Parent or ancestor, with defaults applied. |
| `state` | string | `accepted`, `rejected`, `no status`, `stale`, `other`, `unwatched` or `declared`. |
| `reason` | string | Reason of the deciding condition. |
| `controller` | string | Controller of the deciding entry; for `other`, all writers, comma-separated. |

### ListenerRoutes

| Field | Type | Meaning |
|---|---|---|
| `parent` | Ref | The Gateway or ListenerSet. |
| `listener` | string | Listener name; absent when the listener is not known (no `sectionName` and no computed listener). |
| `routes` | Ref[] | Attached routes. Always present. |

### RouteLink

| Field | Type | Meaning |
|---|---|---|
| `route` | Ref | The route. |
| `parentRef` | ParentRef | The route's parentRef to the described object. |
| `attachment` | string | `accepted`, `not-accepted`, `pending`, `unwatched` or `declared`. |
| `condition` | Condition | The deciding condition, when there is one. |
| `pending` | string | Why the status is not current. |

### Condition

| Field | Type | Meaning |
|---|---|---|
| `type` | string | Condition type. |
| `status` | string | `True`, `False` or `Unknown`. |
| `reason` | string | Condition reason. |
| `message` | string | Condition message. |

### DelegationChild

A reference with one extra field.

| Field | Type | Meaning |
|---|---|---|
| `group` | string | Child route group. |
| `kind` | string | Child route kind. |
| `namespace` | string | Child route namespace. |
| `name` | string | Child route name. |
| `declared` | boolean | True for a candidate with no reported delegation edge. |

### Backend

A reference with one extra field.

| Field | Type | Meaning |
|---|---|---|
| `group` | string | Backend group, `""` for the core group. |
| `kind` | string | Backend kind. |
| `namespace` | string | Backend namespace. |
| `name` | string | Backend name. |
| `mirror` | boolean | True when the route mirrors to the backend. |

### DeclaredRefs

| Field | Type | Meaning |
|---|---|---|
| `parentRefs` | ParentRef[] | The spec's parentRefs. |
| `backendRefs` | Backend[] | The spec's backendRefs. |

### Ref

| Field | Type | Meaning |
|---|---|---|
| `group` | string | API group, `""` for the core group. |
| `kind` | string | Kind. |
| `namespace` | string | Absent only for cluster-scoped objects. |
| `name` | string | Name. |

### ParentRef

| Field | Type | Meaning |
|---|---|---|
| `group` | string | API group, `""` for the core group. |
| `kind` | string | Kind. |
| `namespace` | string | Namespace, with defaults applied. |
| `name` | string | Name. |
| `sectionName` | string | Listener name, when the ref targets one. |
| `port` | integer | Listener port, when the ref targets one. |

## Incomplete inputs

`incomplete` lists the cache inputs the output depends on that did not load. It is absent when every input is `ok`. In `version` it is a field of `VersionView`. Relations that depend on an input that is not `ok` are marked unknown, never empty.

### Input

| Field | Type | Meaning |
|---|---|---|
| `input` | string | Input name, as `httproutes.gateway.networking.k8s.io`, `pods` or `proxy pods`. |
| `state` | string | `loading`, `denied` or `failed`. |
| `reason` | string | The access error of a denied or failed input. |

## Version

### VersionView

| Field | Type | Meaning |
|---|---|---|
| `client` | BuildInfo | The kgwctl build. |
| `installs` | InstallVersion[] | Detected installs; absent for `--client` and when none is detected. |
| `installsUnknown` | string | The inputs that kept installs from being detected. |
| `gatewayAPICRDs` | CRDVersion | The Gateway API CRD bundle; absent for `--client`. |
| `incomplete` | Input[] | Inputs the output depends on that are not `ok`. |

### BuildInfo

| Field | Type | Meaning |
|---|---|---|
| `name` | string | Binary name. |
| `version` | string | Client version. |
| `commit` | string | Build commit. |
| `goVersion` | string | Go version of the build. |

### InstallVersion

| Field | Type | Meaning |
|---|---|---|
| `id` | string | Install ID. |
| `display` | string | Install display name. |
| `controller` | string | Controller version without `v`; `""` when unknown. |
| `replicas` | integer | Desired controller replicas; null when there is no Deployment. |
| `ready` | integer | Ready controller Pods; null when `readyUnknown` is set. |
| `readyUnknown` | string | Why `ready` is not known, as `pods: denied`. |
| `proxies` | ProxyVersion[] | One entry per Gateway and version; null when `proxiesUnknown` is set. |
| `proxiesUnknown` | string | The inputs that kept proxies from being read. |

### ProxyVersion

| Field | Type | Meaning |
|---|---|---|
| `gateway` | string | Gateway as `namespace/name`. |
| `version` | string | Proxy image tag without `v`; absent when unknown. |
| `pods` | integer | Proxy Pods on this version, possibly 0; absent when `unknown` is set. |
| `differsFromController` | boolean | True when the version is not the controller's. |
| `unknown` | string | Why the Gateway's proxies were not read. |

### CRDVersion

| Field | Type | Meaning |
|---|---|---|
| `bundleVersion` | string | Gateway API bundle version. |
| `channel` | string | `standard` or `experimental`. |
| `unknown` | string | Why the bundle is not known, as `denied`. |

## Check

`check` prints one report per selected install. With `-n`, findings on objects of other namespaces are dropped; findings without a namespace or in the install's own namespace (such as its controller) stay, since they affect every namespace, and `state` is still the engine's. Times are UTC.

Text from the cluster (finding message and detail, evidence, check error and detail, hold reason, input reason) is redacted: a URL becomes `scheme://<redacted>`, path included, since a `pods/proxy` path names the API server's namespace, Pod and port; the address of a network error (`dial tcp <redacted>:`) and a DNS error's resolver (`lookup x on <redacted>:`) go too. The API server's own host, from the kubeconfig, becomes `<apiserver>` wherever it appears as a whole name (`x509: certificate is valid for other, not <apiserver>`). `version` and `describe` redact `incomplete` with the URL and address rules only.

### Check

| Field | Type | Meaning |
|---|---|---|
| `context` | string | Kube context name. |
| `generatedAt` | string | RFC3339 time of the engine computation behind the report. |
| `installs` | InstallCheck[] | One entry per selected install, ordered by install ID. Always present, `[]` when none. |
| `incomplete` | Input[] | Inputs the report depends on that are not `ok`. Absent when all are `ok`. |
| `observation` | Observation | What `--live` saw during the run; absent without `--live`. |

### InstallCheck

| Field | Type | Meaning |
|---|---|---|
| `install` | InstallRef | The install. |
| `state` | string | `error`, `warning`, `unknown` or `healthy`, as the engine rolled it up. |
| `counts` | Counts | Shown findings per severity. |
| `findings` | CheckFinding[] | Shown findings: errors, then warnings, then info, each in diagnosis order. Always present, `[]` when none. |
| `groups` | CheckGroup[] | Diagnosis groups with at least one shown finding. Always present, `[]` when none. |
| `checks` | CheckStatus[] | Every check of the install, ordered by name. Always present, `[]` when none. |

### InstallRef

| Field | Type | Meaning |
|---|---|---|
| `id` | string | Install ID. |
| `namespace` | string | Namespace of the controller. |
| `name` | string | Name of the controller. |
| `version` | string | Controller version; absent when unknown. |

### Counts

| Field | Type | Meaning |
|---|---|---|
| `error` | integer | Shown findings with severity `error`. |
| `warning` | integer | Shown findings with severity `warning`. |
| `info` | integer | Shown findings with severity `info`. |

### CheckFinding

| Field | Type | Meaning |
|---|---|---|
| `id` | string | Stable finding ID. |
| `code` | string | Finding code. Codes of vendor packs appear as they are. |
| `slug` | string | Short name of the code. |
| `severity` | string | `error`, `warning` or `info`. |
| `resource` | Ref | The object the finding is about; absent for install-level findings. |
| `message` | string | What is wrong. |
| `detail` | string | More context; absent when empty. |
| `since` | string | RFC3339 time the problem started, when known and earlier than the run started observing. A later time only says when kgwctl first looked, so it is left out. |
| `group` | string | ID of the diagnosis group the finding is in; absent when none. |
| `evidence` | object | Typed evidence with a `kind` tag, passed through from the engine; absent when none. |
| `nextSteps` | NextStep[] | Commands to learn more, with the kube context filled in; absent when none. |
| `fix` | Fix | The one change the evidence leaves, when there is one. |

### CheckGroup

| Field | Type | Meaning |
|---|---|---|
| `id` | string | Group ID. |
| `cause` | Ref | The object the group's findings share as cause; absent when the engine names none. |
| `findings` | string[] | IDs of the shown findings in the group. |

### NextStep

| Field | Type | Meaning |
|---|---|---|
| `description` | string | What the command shows. |
| `command` | string | Shell command, with `--context` filled in for kubectl. |
| `note` | string | Caution about the command's output; absent when empty. |

### Fix

| Field | Type | Meaning |
|---|---|---|
| `title` | string | What the change does. |
| `target` | Ref | The object to create, or to change at `path`. |
| `create` | boolean | True when `target` is a new object; absent (false) otherwise. |
| `path` | string | Field path of the fragment in `target`; absent for a new object. |
| `yaml` | string | The object or fragment. |
| `basis` | string | Why this is the fix. |

### CheckStatus

| Field | Type | Meaning |
|---|---|---|
| `name` | string | Check name. |
| `ok` | boolean | The check ran: it collected its input. Not that it found nothing; findings are not linked to checks. Always present. |
| `pending` | boolean | Not `ok` only because objects are still being processed. Always present. |
| `unavailable` | string | Why an optional input is missing: `off`, `denied`, `failing`, `not-on-version` or `not-observed`; always present, `""` when available. |
| `error` | string | Why the check could not collect its input; absent when empty. |
| `detail` | string | Extra context of an `ok` check, such as what it could not look at; absent when empty. |
| `hold` | Hold | Set while the check is still deciding on some items. |

A check is still deciding when `pending` is true or it has a `hold`. Otherwise it was not checked when `unavailable` is set, or when `ok` is false and `error` is set. It ran when `ok` is true and `unavailable` is empty; one that ran with a `detail` other than its hold reason ran with partial coverage, held or not.

### Hold

| Field | Type | Meaning |
|---|---|---|
| `until` | string | RFC3339 time of the earliest decision. |
| `count` | integer | Held items, in the unit of the check's finding. |
| `reason` | string | Why the items are held. |

### Observation

| Field | Type | Meaning |
|---|---|---|
| `startedAt` | string | RFC3339 time tracking starts: the model the first read of the cluster ended with. Changes while inputs were still arriving are not tracked. |
| `endedAt` | string | RFC3339 end of the run. |
| `stopReason` | string | `timeout` or `interrupted`. |
| `observed` | ObservedFinding[] | Findings with a transition during the run: still present, cleared, or no longer observed. |

### ObservedFinding

| Field | Type | Meaning |
|---|---|---|
| `id` | string | Finding ID. |
| `install` | string | ID of the install that raised it; two installs can raise the same finding ID. |
| `code` | string | Finding code. |
| `severity` | string | Latest severity. |
| `resource` | Ref | The object the finding is about. |
| `message` | string | Latest message. |
| `detail` | string | Latest detail, absent when empty. A change in detail under the same ID (such as 013 going from a missing Service to a missing port) is a transition. |
| `firstSeen` | string | RFC3339 time first seen. |
| `clearedAt` | string | RFC3339 time it cleared: every check that ran at any time while it was present (holding or pending included) decided (ran, neither pending nor holding) without it. Absent while present or no longer observed. |
| `lostAt` | string | RFC3339 time it disappeared while such a check did not decide (denied, failing, pending, holding). kgwctl lost sight of it: its state is unknown, not cleared. Absent otherwise; at most one of `clearedAt` and `lostAt` is set. A lost finding that comes back drops `lostAt`; one whose checks run again without it gets `clearedAt` then. |
| `severities` | SeverityAt[] | Severity changes, oldest first. |
| `evidence` | object | The latest evidence while the finding was present, as in Finding. |
| `nextSteps` | NextStep[] | The latest next steps while the finding was present. |
| `fix` | Fix | The latest fix while the finding was present. |

### SeverityAt

| Field | Type | Meaning |
|---|---|---|
| `severity` | string | Severity from `at` on. |
| `at` | string | RFC3339 time. |

## ProxyRoutes

`proxy routes -o json|yaml` prints one document with `kind: ProxyRoutes`. Unlike the other commands, it carries the parser warnings in the document (`warnings`), so a partial result never looks complete. Errors exit `2` and print nothing to stdout. Valid JSON that is not an object with a `configs` array exits `2` with "not an Envoy config dump (no configs array)"; `{"configs":[]}` is an empty proxy.

### ProxyRoutes

| Field | Type | Meaning |
|---|---|---|
| `source` | ProxySource | Where the config dump came from. |
| `snapshot` | EnvoySnapshot | The listeners, virtual hosts and routes in the dump, narrowed by `--route` and `--rule`. Always present; `listeners` is `[]` when nothing is left. |
| `warnings` | string[] | Sections of the dump the parser skipped. Absent when none. |
| `ambiguous` | AmbiguousRoute[] | With `--route`: Envoy routes whose name reads as the selected HTTPRoute and at least one other, or whose name names it but has no reading (`unresolved`). They are not in `snapshot` as matches. Absent without `--route` or when none. |

### ProxySource

Live, `gateway`, `pod` and `capturedAt` are set. With `--file`, `file` is set, and `gateway` only when `--gateway` named it.

| Field | Type | Meaning |
|---|---|---|
| `gateway` | Ref | The Gateway, with `group: gateway.networking.k8s.io`. |
| `pod` | Ref | The proxy Pod the dump was read from, `kind: Pod`. Its `group` is absent, since the core group's empty value is omitted in these refs. Live only. |
| `capturedAt` | string | RFC3339 UTC time of the capture. Live only. |
| `file` | string | The path as given to `--file`. |

### AmbiguousRoute

A route name can repeat across route configs, so `routeConfig` and `name` together identify an entry.

| Field | Type | Meaning |
|---|---|---|
| `routeConfig` | string | Name of the Envoy route config that holds the route. |
| `name` | string | Name of the Envoy route. |
| `candidates` | RouteOrigin[] | Every HTTPRoute, rule and match the name alone could read as, whether or not that HTTPRoute exists in the cluster, each with `route` (Ref), `ruleIndex` and `matchIndex` (`-1` when unknown) and `ruleName` when the rule has one. |
| `narrowedByKnown` | boolean | True when only the HTTPRoutes in the cluster narrowed the candidates to one. Absent otherwise. |
| `unresolved` | boolean | True when the route's name has no reading, so it has no origin; `candidates` is `[]`. Absent otherwise. |

### EnvoySnapshot

`snapshot` is the model of [xdsview](https://github.com/DuncanDoyle/xdsview) (`pkg/envoy/model`, `EnvoySnapshot`) as xdsview writes it, with camelCase names. This page does not repeat its fields. The ones to know:

- `listeners[].state` and `status` carry the listener's active, warming, draining or rejected state and the last error.
- `listeners[].filterChains[].hcm.httpFilters` lists the HTTP filters in order, and `hcm.routeConfig.virtualHosts[].routes[]` the routes.
- Each route has `origin` (the HTTPRoute, rule and match it came from, with a `certainty` of `exact`, `inferred` or `ambiguous`), `policies` and `effectiveChain`.
- `effectiveChain` is attached to each route inside its own filter chain, never keyed by route name, so a route shared by two filter chains has two chains. Each `filters[]` entry has `runs`, `state` (`on`, `off`, `enabled`, `disabled` or `unknown`), `level`, `request` and `response` (`yes`, `no` or `unknown`) and `contributions` (the extension or policy, with `ref` and `field`).
- `typedConfig`, `typedPerFilterConfig` and `metadata` are the dump's own config. Envoy redacts only fields it knows are sensitive, so check the output before you share it (see `proxy dump` in the README).

kgwctl changes two things in the model. First, a route whose origin xdsview resolved only by using the cluster's HTTPRoutes to break a tie appears in `snapshot` with `origin.certainty: "ambiguous"` and `origin.narrowedByKnown: true`. With `--route`, it is not a match: it is left out of `snapshot` and listed in `ambiguous` with `narrowedByKnown: true`. Without `--route`, `ambiguous` is absent and the route appears only in `snapshot`. kgwctl never treats a tie broken that way as a match, because Envoy can still serve a deleted HTTPRoute's route. When the cluster's HTTPRoutes leave two or more readings, the `snapshot` origin lists those, while `ambiguous` lists every reading of the name. Second, an empty `listeners` prints as `[]` instead of null. Everything else in `snapshot` is xdsview's model unchanged.

A breaking change to xdsview's `model` (a renamed or removed field, or a changed meaning) bumps `schemaVersion`, as it does for kgwctl's own fields.

## BundleManifest

Once its output exists, `bundle` always writes an archive, except on an interrupt during install detection or a fatal write failure (D-030). The exit code follows what the archive holds. The archive holds cluster data when a collector other than `version` finished `ok` or `partial`, or when any item other than `cluster/version.json` was written `ok`. `version` is left out because it writes `version.json` from kgwctl's own build data even when the cluster never answers. A collector that finished `ok` on an empty cluster counts, since the empty result is what the cluster said.

- `0`: the archive holds cluster data, also a partial bundle.
- `2`: the archive holds no cluster data, for example with no reachable cluster, a detection timeout, an `--install` that names no detected install, or a `--timeout` that ran out before any collector finished. The archive still exists, and stderr says `bundle holds no cluster data` with the cause.
- `2`: no archive was written, because of a usage error, an output that exists or cannot be created, a fatal write failure or an interrupt during install detection.

Setup that can fail, such as reading the kubeconfig and creating the spool, comes before the output is created, so it leaves no file. The stderr summary names the failed collectors.

`manifest.json` in a support bundle is one envelope with `kind: BundleManifest`. Every list in it is bounded and has an omitted count. When the manifest still would not fit its size cap, it is replaced by a minimal one with `truncated: true`: build, flags, times, finalization, `excluded`, the omitted counts, and the scope's namespace and up to 64 installs. The minimal manifest is under 1 MiB, below the smallest manifest cap.

### BundleManifest

| Field | Type | Meaning |
|---|---|---|
| `build` | ManifestBuild | The kgwctl build that wrote the bundle. |
| `context` | string | Kubeconfig context name, after text redaction. |
| `server` | string | The API server, always `<apiserver>`. |
| `flags` | FlagValue[] | Flags of the run. Absent when none. |
| `scope` | Scope | The resolved scope of the run. |
| `window` | Window | The log window. |
| `start` | string | RFC3339 run start time. |
| `sealed` | string | RFC3339 time the collection deadline sealed the bundle. Absent when it did not. |
| `end` | string | RFC3339 run end time. |
| `finalization` | string | Outcome of writing the archive. |
| `replicas` | Replica[] | Controller and proxy Pods of the run. |
| `collectors` | CollectorEntry[] | One entry per collector. |
| `items` | ManifestItem[] | One entry per file or failed item. |
| `excluded` | Exclusion[] | Counts of objects left out, per kind and reason; at most 1024 entries. |
| `replicasOmitted` | integer | Replicas left out of `replicas`. |
| `collectorsOmitted` | integer | Collectors left out of `collectors`. |
| `itemsOmitted` | integer | Items left out of `items`. |
| `truncated` | boolean | True for the minimal manifest. Absent otherwise. |

### ManifestBuild

Each string is at most 256 bytes.

| Field | Type | Meaning |
|---|---|---|
| `version` | string | kgwctl version. |
| `commit` | string | Source commit. |
| `date` | string | Build date. |

### FlagValue

Values appear only for `-A`, `--all-controllers`, `--since`, `--tail`, `--timeout`, `--request-timeout`, `--max-size` and `--no-endpoints`. All other flags appear by name only.

| Field | Type | Meaning |
|---|---|---|
| `name` | string | Flag name. |
| `value` | string | Flag value. Absent for flags recorded by name only. |

### Scope

| Field | Type | Meaning |
|---|---|---|
| `namespace` | string | The `-n` value. Absent for all namespaces. |
| `installs` | Install[] | The selected installs. |
| `allControllers` | boolean | True with `--all-controllers`. Absent otherwise. |
| `workloadNamespaces` | string[] | Namespaces that workloads came from. Absent for all. |
| `installsOmitted` | integer | Installs left out of `installs`. |
| `workloadNamespacesOmitted` | integer | Names left out of `workloadNamespaces`. |

### Install

| Field | Type | Meaning |
|---|---|---|
| `namespace` | string | Namespace of the controller Deployment. |
| `deployment` | string | Name of the controller Deployment. |
| `chosen` | string | `flag` when `--install` named it, `detected` otherwise. |
| `metricsPort` | integer | Controller metrics port, when known. |
| `discovery` | DiscoveryScope | The namespaces the install watches. |

### DiscoveryScope

| Field | Type | Meaning |
|---|---|---|
| `mode` | string | `all`, `selectors`, `unknown` or `namespaces-unreadable`. |
| `selectors` | string[] | Namespace selectors, at most 64 of at most 256 bytes each. Each one gets the pattern pass before the cut. |
| `selectorsOmitted` | integer | Selectors left out of `selectors`. |
| `source` | string | Where the selectors were read from. |
| `reason` | string | Why the mode is `unknown` or `namespaces-unreadable`. For `unknown`, one of these fixed codes: `invalid selector`, `invalid selector list`, `envFrom`, `valueFrom`, `no controller`, `no controller container` or `unresolved`. The controller's own error text is never recorded. |
| `unwatched` | string[] | Names of namespaces the install does not watch. |
| `unwatchedOmitted` | integer | Names left out of `unwatched`. |

### Window

| Field | Type | Meaning |
|---|---|---|
| `start` | string | RFC3339 run start time `T0`. |
| `since` | string | RFC3339 start of the log and Event window. |
| `tail` | integer | Most log lines per stream. |

### Replica

| Field | Type | Meaning |
|---|---|---|
| `namespace` | string | Pod namespace. |
| `pod` | string | Pod name. |
| `uid` | string | Pod UID. |
| `role` | string | `controller` or `proxy`. |
| `owner` | string | The install or Gateway the Pod belongs to. Empty for a proxy replica whose Gateway is not known, as can happen with `--all-controllers`, and for one with `identity` `unverified`. |
| `identity` | string | How the owner was matched: `annotation`, `selector` or `unverified`. |
| `ready` | boolean | Whether the Pod is ready. |
| `terminating` | boolean | True while the Pod is terminating. Absent otherwise. |
| `phase` | string | Pod phase. |
| `containers` | ReplicaContainer[] | Containers of the Pod. |
| `startTime` | string | RFC3339 Pod start time. |
| `metricsPort` | integer | Metrics port, when known. |
| `metricsPortDeclared` | boolean | True when the Pod declares the metrics port. Absent otherwise. |

### ReplicaContainer

| Field | Type | Meaning |
|---|---|---|
| `name` | string | Container name. |
| `init` | boolean | True for an init container. Absent otherwise. |
| `restarts` | integer | Restart count. |

### CollectorEntry

| Field | Type | Meaning |
|---|---|---|
| `name` | string | Collector name. |
| `status` | string | `ok`, `partial`, `failed`, `skipped` or `timeout`. |
| `reason` | string | Why a collector was skipped: `flag`, or `no install` when no install was detected. `no install` is recorded only when the absence is known, not when detection was cut short or an input was unreadable. |
| `durationMs` | integer | Run time in milliseconds. |
| `error` | string | Redacted error. When install detection produces no model, no collector starts. Each one not skipped by flag has status `timeout` and an error that names the cause: `install detection timed out`, `install detection failed`, `install detection denied` or `no such install detected`. When `--timeout` itself has passed, the error is `collection deadline reached` instead. When detection is cut but leaves a partial model, collectors run only while `--timeout` has time left. When `--timeout` passes, a collector that has not returned has status `timeout` and the error `collection deadline reached`. A collector that returns after `--timeout` has status `timeout`. It keeps its own redacted error only if it reports before the runner records the final outcome. |
| `late` | integer | Results that arrived after the seal. |

### Exclusion

| Field | Type | Meaning |
|---|---|---|
| `kind` | string | `group/kind`, or the kind alone for the core group. `other` sums the entries over the 1024 limit. |
| `reason` | string | `ownership` or `scope`. |
| `count` | integer | Objects left out. |

### ManifestItem

| Field | Type | Meaning |
|---|---|---|
| `path` | string | Path inside the archive. |
| `size` | integer | Size in bytes. |
| `contentType` | string | Content type of the file. |
| `capturedAt` | string | RFC3339 capture time. |
| `source` | ItemSource | Where the item came from. |
| `status` | string | `ok`, `skipped`, `denied`, `disabled`, `failed`, `timeout` or `dropped`. |
| `reason` | string | Why the item has its status, such as `no install`, `timeout` or `missing kind`. |
| `redactions` | integer | Values redacted in the file. |

Some items stand for a file that was not written:

- `inventory/detection` records a failed or cut install detection, with a fixed reason (D-030). The bundle is still written.
  - `timeout`: a deadline or a timeout status, such as HTTP 504, ended detection. With a partial model, collectors run only while `--timeout` has time left, and installs and Gateways may be missing. When detection used up `--timeout`, no collector runs and the archive holds no cluster data. Without a model, the archive holds no cluster data.
  - `denied`: detection was refused with a 403. The archive holds no cluster data.
  - `failed`, reason `install detection failed`: any other error, such as no reachable cluster. The archive holds no cluster data.
  - `failed`, reason `no such install detected`: `--install` names no detected install. The archive holds no cluster data.
- A `skipped` item with reason `no install` marks controller output that was left out because no install was detected. It is recorded only when the absence is known.
- `check/<group>/<resource>` has status `denied` when `check` could not read that input. The group is `core` for the core group, and a `#` in the resource becomes `-`, as in `pods-proxy`.
- A `failed` item with reason `missing kind` marks an object that has no kind. It is refused and its content is not written.

### ItemSource

| Field | Type | Meaning |
|---|---|---|
| `namespace` | string | Namespace of the Pod or object. |
| `pod` | string | Pod name. |
| `uid` | string | Pod UID. |
| `container` | string | Container name. |
| `operation` | string | Operation name, such as `list`, `log`, `config-dump` or `stats`. |
| `identity` | string | For Pod items: `verified`, `changed`, `gone` or `reread-failed`. |
