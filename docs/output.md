# Output schema

`describe`, `version` and `check` print a versioned envelope with `-o json` or `-o yaml`. Tables and the default text layout are not a stable interface; the envelope is. `get -o yaml|json` is not wrapped: it prints a plain Kubernetes `v1` List of the matching objects.

## Envelope

```json
{"schemaVersion": "kgwctl.kgateway.dev/v1alpha1", "kind": "Describe", "item": { }}
```

| Field | Type | Meaning |
|---|---|---|
| `schemaVersion` | string | Schema version, currently `kgwctl.kgateway.dev/v1alpha1`. |
| `kind` | string | `Describe`, `Version` or `Check`. |
| `item` | object | A `DescribeView`, a `VersionView` or a `Check`. |

## Conventions

- Field names are camelCase. Times are RFC3339 and nothing is relative to now, so there is no age field.
- A reference is always an object, never a string. `group` is `""` for the core group. `namespace` is absent only for cluster-scoped objects.
- `install` is `namespace/name` of the owning install, as in `get`.
- Optional fields are omitted when empty, not printed as null. Fields documented as always present are printed even when empty.
- Data goes to stdout and errors to stderr. Warnings never appear in JSON or YAML output.
- Exit codes: `get`, `describe` and `version` use `0` for success and `1` for any error. `check` uses `0` when no shown finding is an error or warning, `1` when one is (or on a usage error), and `2` when it could not check, as with no install found. Exit codes are the same in every output format.

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

`check` prints one report per selected install. With `-n`, findings on objects of other namespaces are dropped; findings without a namespace stay, and `state` is still the engine's. Times are UTC.

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
| `since` | string | RFC3339 time the problem started, when known. |
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
| `ok` | boolean | The check collected its input and found nothing to report. Always present. |
| `pending` | boolean | Not `ok` only because objects are still being processed. Always present. |
| `unavailable` | string | Why an optional input is missing: `off`, `denied`, `failing`, `not-on-version` or `not-observed`; always present, `""` when available. |
| `error` | string | Why the check could not collect its input; absent when empty. |
| `detail` | string | Extra context of an `ok` check; absent when empty. |
| `hold` | Hold | Set while the check is still deciding on some items. |

A check was not checked when `unavailable` is set, or when `ok` is false and `error` is set. It passed when `ok` is true, it has no `hold` and it was checked.

### Hold

| Field | Type | Meaning |
|---|---|---|
| `until` | string | RFC3339 time of the earliest decision. |
| `count` | integer | Held items, in the unit of the check's finding. |
| `reason` | string | Why the items are held. |

### Observation

| Field | Type | Meaning |
|---|---|---|
| `startedAt` | string | RFC3339 start of the run. |
| `endedAt` | string | RFC3339 end of the run. |
| `stopReason` | string | `timeout` or `interrupted`. |
| `observed` | ObservedFinding[] | Findings seen during the run, cleared or not. |

### ObservedFinding

| Field | Type | Meaning |
|---|---|---|
| `id` | string | Finding ID. |
| `code` | string | Finding code. |
| `severity` | string | Latest severity. |
| `resource` | Ref | The object the finding is about. |
| `firstSeen` | string | RFC3339 time first seen. |
| `clearedAt` | string | RFC3339 time it cleared; absent while it was still present at the end. |
| `severities` | SeverityAt[] | Severity changes, oldest first. |

### SeverityAt

| Field | Type | Meaning |
|---|---|---|
| `severity` | string | Severity from `at` on. |
| `at` | string | RFC3339 time. |
