# Output schema

`describe` and `version` print a versioned envelope with `-o json` or `-o yaml`. Tables and the default text layout are not a stable interface; the envelope is. `get -o yaml|json` is not wrapped: it prints a plain Kubernetes `v1` List of the matching objects.

## Envelope

```json
{"schemaVersion": "kgwctl.kgateway.dev/v1alpha1", "kind": "Describe", "item": { }}
```

| Field | Type | Meaning |
|---|---|---|
| `schemaVersion` | string | Schema version, currently `kgwctl.kgateway.dev/v1alpha1`. |
| `kind` | string | `Describe` or `Version`. |
| `item` | object | A `DescribeView` or a `VersionView`. |

## Conventions

- Field names are camelCase. Times are RFC3339 and nothing is relative to now, so there is no age field.
- A reference is always an object, never a string. `group` is `""` for the core group. `namespace` is absent only for cluster-scoped objects.
- `install` is `namespace/name` of the owning install, as in `get`.
- Optional fields are omitted when empty, not printed as null. Fields documented as always present are printed even when empty.
- Data goes to stdout and errors to stderr. Warnings never appear in JSON or YAML output.
- Exit codes: `0` for success and `1` for any error, in every output format.

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
