# Phase 4b — xdsview v0.7.0 migration and first release: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `proxy routes` runs on the xdsview v0.7.0 contract with a selectable route selection strategy (`--select strict|known`, default `strict`, D-032), a downstream module seam and kgwctl's own `ProxyRoutes` payload; every envelope moves to `kgwctl.kgateway.dev/v1alpha2`; Snellius v0.9.0; a correct client version; `v0.1.0` can be tagged at the close.

**Architecture:** `internal/proxyview` owns the pipeline (parse, effective chains, compose, select or narrow, prune), the text hooks and the payload projection. `internal/app/proxy_routes.go` keeps capture, Known and exit codes, and passes the module set from `app.Config`. `pkg/cli.WithProvenanceModules` sets that set for downstream builds. `cmd/kgwctl` resolves the client version. `hack/release.sh` builds the release files.

**Tech Stack:** Go 1.26, cobra, client-go v0.36.x, xdsview v0.7.0 (`pkg/envoy/{parser,chain,model,render,provenance}`, `pkg/adapter/kgateway`), Snellius v0.9.0. No new modules.

**Spec:** [`design.md`](design.md) (r1). Read it first. §n and DD-n refer to it.

## Global Constraints

- Selection follows `--select` (DD-4, D-032). `strict` (default): a match is a route in `kgateway.Select(...).Routes` and nothing else. `known`: also a route in `Select(...).Narrowed` whose kept claims name exactly one reading that matches the query; nothing else. `kgateway.Select` is never changed or bypassed.
- `--select known` with `--file` is a usage error (exit 2) before any I/O. Without `--route` the strategy changes no output.
- Known is the cluster's HTTPRoutes live, and empty with `--file`. `--route` is never added to Known (DD-3).
- No read of `Claim.Module`, no use of `kgateway.ModuleName`, no parsing of a claim ID, in any non-test kgwctl code (DD-9). Claims are read through `provenance.Resolve`, `ResolveMirror`, `Records`, `Index.About`, `Index.Record`, `kgateway.Select`, `kgateway.Narrow` and the assertion types.
- The payload holds no claim ID, no module name and no raw `typedConfig`, `typedPerFilterConfig` or `metadata` (DD-10, DD-14).
- Chains, index, selection and narrowings are computed on the full snapshot; `model.Prune` runs last, only under `--route` (DD-1).
- Live parses with `model.CaptureComplete`; `--file` passes no capture (DD-2).
- Flags, exit codes, RBAC, capture, deadlines and the 64 MiB cap of `proxy routes` are unchanged.
- No vendor or edition names (D-004); say "downstream build". No new Go modules beyond the two bumps.
- Goldens are reviewed, not just regenerated: each `-update` run is followed by a diff read against design §3.3, and the sub-phase record lists what changed and why.
- Code comments 1–2 lines; Go doc comments on exported identifiers (go.dev/doc/comment).
- Scratch files only in `.agents-tmp/`; no `cd` in compound commands (use `go -C` or absolute paths); every `kubectl`/`helm` call passes `--context`/`--kube-context`.

## Review Focus

1. **A route Known narrowed to one matching reading, with `--route`.** Under `strict`: in `selection.ambiguous` with `narrowed: true` and the note, never in `selection.routes` or the tree. Under `known`: in `selection.routes` with `chosenBy: "known"`, in the tree with `(chosen from known HTTPRoutes)`, not in `ambiguous`. A narrowing that kept two readings, or one that does not match, is never chosen. Tasks 4b.1.1, 4b.1.2.
2. **A wrapping module under another name.** Expects byte-equal text and JSON to the default build, and no path in kgwctl that reads the module name. Task 4b.1.3.
3. **The colliding routes of `policies.json`, live and from a file.** Expects the same view below the header, both listed as ambiguous, neither matched. Tasks 4b.1.1, 4b.2.5.
4. **A route config shared by two filter chains.** Expects one JSON entry per use, with equal `sourcePath`, and one text line in the ambiguous list. Tasks 4b.1.1, 4b.1.2.
5. **A warning whose subject `--route` pruned.** Expects it in JSON `warnings` and at the end of the text. Tasks 4b.1.1, 4b.1.2.
6. **Data dropped by the projection.** Expects the model coverage test to name every xdsview field kgwctl does not map. Task 4b.1.2.
7. **`schemaVersion` in every output and in the bundle's JSON members.** Expects `v1alpha2` everywhere, and no other change in non-`ProxyRoutes` goldens beyond Snellius v0.9.0 findings. Tasks 4b.2.1, 4b.2.2.
8. **`go install …@v0.1.0` and a release binary.** Expects `v0.1.0` from `version --client`. Tasks 4b.2.3, 4b.2.4.

## Test harness

- Recorded dumps: `testdata/dumps/{demo,policies,collide-meta}.json`, unchanged.
- `internal/proxyview`: unit tests build views from the recorded dumps and from small hand-built dumps (as in today's `view_test.go`).
- `internal/proxyview/proxyviewtest` (new, test-only): `Wrap(name string, inner provenance.Module, extra func(*model.Snapshot) []provenance.Claim) provenance.Module`, a wrapping module for the seam tests.
- `internal/app`: command-boundary goldens under `testdata/proxy-routes/`, the fake dialer and envtest as in Phase 3.
- Golden update: `go test ./internal/app -run <Test> -update`, then `git -C <worktree> diff --stat` and a read of each changed golden.

## Sub-phases

| Sub-phase | Delivers | Tasks |
|---|---|---|
| 4b.1 | xdsview v0.7.0 pipeline and text, `ProxyRoutes` payload, module seam, `proxy routes` docs | 4b.1.1–4b.1.4 |
| 4b.2 | Envelope `v1alpha2`, Snellius v0.9.0, client version, release script, live check | 4b.2.1–4b.2.5 |

Each task builds and passes on its own; no task consumes a type a later task introduces. Each sub-phase ends with `make test` (race, envtest), `make lint`, the binary dependency test and `go mod tidy -diff` empty.

---

## Sub-phase 4b.1 — Pipeline, payload, seam

### Task 4b.1.1: xdsview v0.7.0 pipeline and text
**Files:** modify `go.mod`, `go.sum`, `internal/proxyview/view.go`, `internal/proxyview/view_test.go`, `internal/app/proxy_routes.go`, `internal/app/proxy_routes_test.go`, `internal/app/lifecycle_test.go`, `internal/app/testdata/proxy-routes/*.golden`; create `internal/proxyview/text.go`, `internal/proxyview/payload.go` (skeleton, completed in 4b.1.2).
**Interfaces (produces):**
- `type Strategy string`; `const ( StrategyStrict Strategy = "strict"; StrategyKnown Strategy = "known" )`; `func ParseStrategy(s string) (Strategy, error)` (empty is `strict`; anything else but the two is an error)
- `type Options struct{ Gateway *types.NamespacedName; Known []model.Ref; Capture model.Capture; Selection Selection; Strategy Strategy; Modules []provenance.Module }` (Gateway is kgwctl's context, never a claim; nil Modules means `DefaultModules()`; empty Strategy is `strict`)
- `func chooseKnown(q kgateway.Query, sel kgateway.Selection) []provenance.Narrowing` (unexported: the narrowings whose `Kept` claims share one reading, compared on HTTPRoute, rule index, rule name and match index, that matches q; the D-022 choice 1 rule)
- `func DefaultModules() []provenance.Module` (returns `[]provenance.Module{kgateway.New()}`, a fresh slice each call)
- `type Selection struct{ Route types.NamespacedName; Rule *int }` (unchanged)
- `type View struct{ Full, Snapshot *model.Snapshot; Chains map[model.ID]model.EffectiveChain; Index *provenance.Index; Narrowed []provenance.Narrowing; Selected *kgateway.Selection; Chosen []provenance.Narrowing; Strategy Strategy; Selection Selection; Gateway *types.NamespacedName }` (Selected is `Select`'s result unchanged; Chosen is `chooseKnown`'s result under `known`, nil under `strict`; Snapshot is Full pruned to `Selected.Routes` plus the Chosen routes under a Selection, else Full; Narrowed is `Selected.Narrowed` under a Selection, `kgateway.Narrow(ix, Known)` live without one, nil otherwise)
- `func (v View) Routes() []model.ID` (the matched routes in snapshot order: `Selected.Routes` plus the Chosen routes)
- `Text` passes `v.Chosen` as `provenance.Labels`' narrowings, and nothing else (DD-6).
- `func Build(body []byte, o Options) (View, error)` (`ErrNotConfigDump` as today; a `Compose` error wrapped as `compose provenance modules: %w`)
- `func (v View) Matched() int` (routes in `v.Snapshot`, once per use)
- `type TextOptions struct{ Header string; ResponseOrder bool }`
- `func Text(w io.Writer, v View, o TextOptions) error` (hooks per DD-11; selection lists per DD-5; trailing warnings per DD-12)
- `const OneCandidateNote = "one candidate exists in the cluster"` (unchanged)
- `const GatewayContextNote = "(from the command line)"`
- `func HTTPRouteRef(n types.NamespacedName) model.Ref` (unchanged)
- `internal/app`: `routesOptions` gains `selectStrategy string`; flag `--select` (`strict|known`, default `strict`, help: "How --route picks routes: strict lists a route only the cluster's HTTPRoutes narrowed as ambiguous; known also selects it"). `routesRequest` gains `strategy proxyview.Strategy`. `parseRoutes` returns a usage error (exit 2, before any I/O) for an unknown value and for `known` with `--file`. Live with `known`, a failed HTTPRoute list adds the stderr line `Warning: --select known: no route can be chosen from known HTTPRoutes`. `printRoutes` builds `proxyview.Options` (Capture `model.CaptureComplete` live, Strategy), maps a compose error to exit 2. `-o json|yaml` prints `proxyview.View.Payload(src)` with the 4b.1.1 skeleton: `source`, `capture`, `listeners` (id, name, address, static, state, filterChains with id and name), `selection` keys, `warnings`.
- `lifecycle_test.go` `forbiddenXdsview`: allow `pkg/envoy/parser`, `pkg/envoy/chain`, `pkg/envoy/model`, `pkg/envoy/render`, `pkg/envoy/provenance`, `pkg/adapter/kgateway`; forbid everything else, `origin` and `filter` included.
**Steps:**
- [ ] `go get github.com/DuncanDoyle/xdsview@v0.7.0` and `go mod tidy`.
- [ ] Port each `view_test.go` test to the new API (names kept): `TestBuildExactSelection` (collide-meta, `--route kgwctl-proxy/orders`: `Selected.Routes` has one ID, in vhost `orders.kgwctl.example`; `Matched() == 1`; the `orders-kgwctl.proxy.example` route is not selected), `TestBuildAmbiguousWithoutMetadata` (policies: `Selected.Routes` empty, `Selected.Ambiguous` has both colliding routes), `TestBuildSingleSurvivorIsNotAMatch` (Known without `proxy/orders-kgwctl`: route in `Ambiguous` and in `Narrowed` with one kept reading, not in `Routes`), `TestBuildWarmingListenerSurvivesNarrowing`, `TestBuildParserWarning`, `TestBuildPolicyChain` (orders route chain: ext_authz, local rate limit, dynamic module, router, each `Runs: yes`), `TestBuildResponseOrderMarksUnknownSide`, `TestBuildRuleSelection`, `TestBuildParseError`, `TestTextMarksNarrowedInFullTree` (live, no Selection: route row `ambiguous: …`, next line `one candidate exists in the cluster`, no `chosen from known`), `TestBuildAmbiguousPerRouteConfig` (shared source: two IDs with one `SourcePath`; text lists one line), `TestTextNoListenersAfterNarrowing`, `TestBuildUnresolvedNeverMatches`, `TestBuildUnresolvedIgnoresVhostPrefix`, `TestBuildAmbiguousJudgedOnNameReadings`.
- [ ] Add `TestBuildStrategySameCapture` (policies.json, `--route kgwctl-proxy/orders`, one table, both strategies): Known without `proxy/orders-kgwctl` → `strict`: `Routes()` empty, both routes in `Selected.Ambiguous`, `Chosen` nil; `known`: `Routes()` has both colliding routes, `Chosen` has both, text rows end `(chosen from known HTTPRoutes)` and print no ambiguous list. Known with both HTTPRoutes → identical `Routes()` (empty) and text under both. collide-meta.json → identical `Routes()` under both, `Chosen` nil.
- [ ] Add `TestChooseKnownRule`: hand-built `kgateway.Selection` values: a narrowing whose kept claims name one matching reading → chosen; two distinct kept readings → not; one kept reading for another rule index than `--rule` → not; two kept claims that differ only in `IndexCertainty` → one reading, chosen.
- [ ] Add `TestStrategyNoEffectWithoutRoute`: live Known, no Selection: `Text` and `Payload` byte-equal under `strict` and `known`.
- [ ] Add `TestBuildNoKnownWithFile`: Options without Known never yields `Narrowed`, with or without a Selection.
- [ ] Add `TestBuildCapture`: `Full.Capture` is `complete` when Options says so, `unverified` otherwise.
- [ ] Add `TestTextGatewayContext`: with Gateway set, each non-static listener row ends `· Gateway ns/gw (from the command line)`; static rows don't; a module claiming a `GatewaySource` at inferred suppresses it.
- [ ] Add `TestTextTrailingWarnings`: a warning on a pruned subject and a dump-wide warning print after the selection lists as `warning: …`; a warning on a kept subject prints once, under its row.
- [ ] Add `TestBuildDuplicateModules`: two modules named `x` → error containing `compose provenance modules`.
- [ ] Run, fail, implement `Build` and `Text`, pass.
- [ ] `go test ./internal/app -run 'TestProxyRoutes' -update`; read every changed text golden against design §3.3 (listener and vhost port labels, `(inferred)` suffixes, context label, static listeners, `ignored dump sections`, warnings under rows). Record the reviewed differences in `.agents-tmp/p4b/4b.1.1-goldens.md` for the sub-phase record.
- [ ] `TestProxyRoutesSelectFlag` (`internal/app`): `--file … --select known` (with and without `--route`) and `--select bogus` exit 2, stdout empty, no file read and no request (`Proxy.Requests()` unchanged); the usage line names `--select`. Live `--select known --route kgwctl-proxy/orders` with the fake dialer and an envtest fixture lacking `proxy/orders-kgwctl`: exit 0, both routes printed with the mark. Live `--select known` with the HTTPRoute list denied: exit 0, the warning on stderr, output equal to `strict`.
- [ ] Add text goldens `live-policies-orders-known` and `json-live-policies-orders-known` next to the `strict` ones.
- [ ] `TestProxyRoutesLive`, `TestProxyRoutesLiveJSON`, `TestProxyRoutesRouteLoadDeadline`, `TestProxyRoutesListsOnlyHTTPRoutes`, `TestProxyRoutesHTTPRouteListDenied`: same assertions on the new output (narrowed note, Known only live, warnings).
- [ ] Verify: `go -C /Users/ddoyle/Development/claude/kgwctl-claude-phase4 test ./internal/proxyview ./internal/app -run 'Proxy|Build|Text|BinaryDeps'`; `go -C … vet ./...`.
- [ ] Commit `feat(proxy routes): xdsview v0.7.0 pipeline and --select strategy`.

### Task 4b.1.2: `ProxyRoutes` payload
**Files:** modify `internal/proxyview/payload.go`; create `internal/proxyview/payload_test.go`, `internal/proxyview/coverage_test.go`; modify `internal/app/testdata/proxy-routes/{json,yaml}-*.golden`, `internal/app/proxy_routes_test.go`.
**Interfaces (produces):** (JSON names in design §5; every optional field `omitempty`; lists documented as always present are non-nil)
- `type ProxyRoutes struct{ Source Source; Capture string; Listeners []Listener; Sections []Section; Selection *SelectionOut; Warnings []Warning }`
- `type Source struct{ Gateway, Pod *Ref; CapturedAt *time.Time; File string }` (Ref replaces `*model.Ref`, same JSON)
- `type Ref struct{ Group, Kind, Namespace, Name string }` (`group` and `namespace` omitempty)
- `type Resolved[T any] struct{ Certainty string; Candidates []T }`
- `type Listener struct{ ID, Name, Address string; Static bool; State ListenerState; Port *Resolved[ListenerPort]; Gateway *Resolved[Ref]; Policies []Policy; FilterChains []FilterChain }`
- `type ListenerState struct{ Status string; Updating, Draining bool; LastUpdated string; Error *ListenerError }`; `type ListenerError struct{ Details, LastAttempt string }`; `type ListenerPort struct{ Port int; Section string }`
- `type FilterChain struct{ ID, Name string; Default bool; TLS *TLS; NetworkFilters []NetworkFilter; HCM *HCM }`; `type TLS struct{ SNIHosts []string }`; `type NetworkFilter struct{ ID, Name, Type string }`
- `type HCM struct{ ID string; RouteConfig RouteConfig; HTTPFilters, UpstreamFilters []HTTPFilter; DefaultChain []Filter; VirtualHosts []VirtualHost }`
- `type RouteConfig struct{ Name, State, Kind string; ScopedRoutes bool; Policies []Policy }`
- `type HTTPFilter struct{ Slot, Name, Type string; Disabled bool; ConfigSource string; Extension *Resolved[Extension] }`; `type Extension struct{ Ref *Ref; FilterKind string; Helper bool }`
- `type VirtualHost struct{ ID, SourcePath, Name string; Domains []string; Host *Resolved[string]; Port *Resolved[ListenerPort]; Policies []Policy; Routes []Route }`
- `type Route struct{ ID, SourcePath, Name string; Match Match; Origin Origin; Filters []Filter; ChainConditional bool; Policies []Policy; Action Action; Backend *Backend; WeightedBackends []WeightedBackend; Mirrors []Mirror; Rewrite *Rewrite; RequestHeadersToAdd, ResponseHeadersToAdd []Header; ResponseHeadersToRemove []string }`
- `type Match struct{ Prefix, PathSeparatedPrefix, Path, Regex string; Connect bool; CaseSensitive *bool; Headers []HeaderMatch; QueryParams []QueryParamMatch; Unparsed []string }`; `type HeaderMatch struct{ Name, Value, Kind string; IgnoreCase, Invert, MissingAsEmpty bool }`; `type QueryParamMatch struct{ Name, Value, Kind string; IgnoreCase bool }`
- `type Origin struct{ Certainty string; Complete, Narrowed bool; ChosenBy string; Readings []Reading }` (`ChosenBy` is `known` or empty)
- `type Reading struct{ Route Ref; RuleIndex int; RuleName string; MatchIndex int; Certainty, IndexCertainty string; Incomplete, NotInCluster bool }`
- `type Filter struct{ Slot, Name, Type, Runs, State, Level, PerFilterType, Request, Response, Wrapper string; Conditional bool; ConfigSource string; Extension *Resolved[Extension]; Policies []Policy }`
- `type Policy struct{ Policy Ref; Field, Level, Certainty string }`
- `type Action struct{ Kind, ClusterHeader, ClusterSpecifierPlugin string; InlineClusterSpecifierPlugin *NamedType; Redirect *Redirect; DirectResponse *DirectResponse; FilterActionType string }`; `NamedType`, `Redirect`, `DirectResponse`, `Rewrite`, `Header` mirror the xdsview fields of design §5.3.
- `type Backend struct{ Cluster, Certainty string; Candidates []BackendRef }`; `type BackendRef struct{ Backend Ref; Port int }`; `type WeightedBackend struct{ ID, Name string; Weight int; OverridesFilters bool; Backend *Backend }`; `type Mirror struct{ Cluster string; Backend *Backend }`
- `type SelectionOut struct{ Query Query; Strategy string; Routes []MatchedRoute; Ambiguous []AmbiguousRoute; Unresolved []RouteKey }`; `type Query struct{ Route Ref; RuleIndex *int }`; `type RouteKey struct{ ID, RouteConfig, VirtualHost, Name string }`; `type MatchedRoute struct{ RouteKey; ChosenBy string }` (`select` or `known`); `type AmbiguousRoute struct{ RouteKey; Readings []Reading; Narrowed bool }` (a chosen route is never in `Ambiguous`)
- `type Warning struct{ Subject, Code, Message string }`; `type Section struct{ Type, Status string }`
- `func (v View) Payload(src Source) ProxyRoutes`
**Steps:**
- [ ] `TestPayloadExactRoute` (collide-meta, `--route kgwctl-proxy/orders`): one route; `origin.certainty` `exact`, one reading `{kgwctl-proxy/orders, 0, match 0}`; `filters` names in HCM order with `runs`; ext_authz entry has `extension` GatewayExtension `kgwctl-proxy/authz` and `policies` `[{TrafficPolicy kgwctl-proxy/orders-extauth, extAuth, route, inferred}]`; `route.policies` has the three records at level `route`, certainty `exact`; `backend.cluster` `kube_kgwctl-proxy_backend_8080` with a Service candidate `kgwctl-proxy/backend` port 8080.
- [ ] `TestPayloadAmbiguousSelection` (policies, `--route kgwctl-proxy/orders`): `listeners == []`; `selection.routes == []`; 2 `ambiguous` entries, each with 2 readings and `narrowed: false`; `unresolved == []`.
- [ ] `TestPayloadNarrowed` (policies, Known without `proxy/orders-kgwctl`, `--route`): both entries `narrowed: true`, the `proxy/orders-kgwctl` reading `notInCluster: true`; without `--route`: no `selection`, the routes in `listeners` have `origin.narrowed: true`.
- [ ] `TestPayloadStrategy` (policies, Known without `proxy/orders-kgwctl`, `--route`): `strict` → `selection.strategy` `strict`, `routes == []`; `known` → `strategy` `known`, both routes in `routes` with `chosenBy: "known"`, `ambiguous == []`, each route in `listeners` has `origin.chosenBy: "known"`, `origin.narrowed: true`, `origin.certainty: "ambiguous"`. collide-meta under `known`: `chosenBy: "select"`, no `origin.chosenBy`.
- [ ] `TestPayloadPolicyLevels`: a hand-built dump with `merge.*` records at listener, route config, virtual host and route level → each record on its own occurrence with its `level`; none copied to the route.
- [ ] `TestPayloadNoRawConfig`: the encoded payload of every recorded dump has no `typedConfig`, `typedPerFilterConfig`, `metadata`, `"module"` key or claim ID (`kgateway/<n>` pattern).
- [ ] `TestPayloadIDsJoin`: every `selection.routes` ID and every `filters[].slot` is an `id` in `listeners`; every `ambiguous`/`unresolved` ID is in `View.Full`.
- [ ] `TestPayloadSharedRouteConfig`: a route config used by two filter chains gives two route entries with distinct `id` and equal `sourcePath`.
- [ ] `TestPayloadWarningsWholeDump`: under `--route`, a warning whose subject was pruned is still in `warnings` with its `subject`.
- [ ] `TestPayloadCoversModel` (`coverage_test.go`): for `model.Listener`, `ListenerState`, `ListenerError`, `NetworkFilterChain`, `NetworkFilter`, `TLSContext`, `HCMConfig`, `HTTPFilter`, `RouteConfigUse`, `RouteConfigSource`, `VirtualHost`, `Route`, `RouteMatch`, `HeaderMatch`, `QueryParamMatch`, `RouteAction`, `Redirect`, `DirectResponse`, `Extension`, `RouteRewrite`, `WeightedCluster`, `HeaderOperation`, `ChainEntry`, `EffectiveChain`, `Warning`, `SectionCoverage`: every exported field is in a `mapped` or `leftOut` table in the test (leftOut: `TypedConfig`, `TypedPerFilterConfig`, `Metadata`, `Discovery` beyond `Source`, `Reported`); a new field fails the test with its type and name.
- [ ] Run, fail, implement `Payload`, pass.
- [ ] `go test ./internal/app -run 'TestProxyRoutes(JSON|YAML|LiveJSON)' -update`; read every JSON and YAML golden; check each design §5 field appears where the fixture has data. `TestProxyRoutesJSONOneDocument` decodes `item.source` and `item.listeners`.
- [ ] Verify: `go -C … test ./internal/proxyview ./internal/app -run 'Payload|Proxy'`.
- [ ] Commit `feat(proxy routes): kgwctl ProxyRoutes payload`.

### Task 4b.1.3: Module seam
**Files:** modify `internal/app/root.go`, `internal/app/proxy.go`, `internal/app/proxy_routes.go`, `pkg/cli/cli.go`, `pkg/cli/cli_test.go`, `examples/downstream/main.go`, `examples/downstream/main_test.go`; create `internal/proxyview/proxyviewtest/wrap.go`, `internal/proxyview/seam_test.go`, `internal/proxyview/hygiene_test.go`.
**Interfaces (produces):**
- `app.Config` gains `Modules []provenance.Module` (nil: `proxyview.DefaultModules()`).
- `func newProxyCmd(now func() time.Time, modules []provenance.Module, hooks proxyTestHooks) *cobra.Command`; `func newProxyRoutesCmd(now func() time.Time, modules []provenance.Module, hooks proxyTestHooks) *cobra.Command`
- `func WithProvenanceModules(m ...provenance.Module) Option` in `pkg/cli`: replaces the default module set of `proxy routes`; a call with no modules keeps the default. Doc comment: wrap `kgateway.New()` under your own name to extend it; never reuse the name `kgateway`.
- `func Wrap(name string, inner provenance.Module, extra func(*model.Snapshot) []provenance.Claim) provenance.Module` in `proxyviewtest`.
**Steps:**
- [ ] `TestWrappingModuleSameSelection` (`seam_test.go`): for every golden case of Task 4b.1.1 and 4b.1.2 (file and Known variants), under both `strict` and `known` (`known` only with Known set), `Wrap("example", kgateway.New(), nil)` gives the same `Selected` and `Chosen` (IDs), byte-equal `Text` output and byte-equal encoded `Payload`.
- [ ] `TestWrappingModuleAddsRecord`: `extra` adds one `PolicyRecord` (level route, evidence object the policy) on the orders route → it is in `route.policies` and on the `policies:` line; `Selected` unchanged.
- [ ] `TestNoModuleNameSelection` (`hygiene_test.go`): parse every non-test `.go` file under `internal/` and `pkg/` with `go/parser`; fail on a selector `X.Module` where `X` is not an imported package name, and on any selector `ModuleName`; print file:line.
- [ ] `TestWithProvenanceModules` (`pkg/cli`): a root with a wrapper that adds a record shows it in `proxy routes --file …collide-meta.json --route kgwctl-proxy/orders -o json`; the default root does not; `WithProvenanceModules()` with no modules equals the default; two modules with one name exit 2 with `compose provenance modules`.
- [ ] `examples/downstream`: pass `cli.WithProvenanceModules(exampleModule{})`, a wrapper of `kgateway.New()` named `example` that adds nothing; `main_test.go` runs `proxy routes --file …collide-meta.json --route kgwctl-proxy/orders` and checks the matched route.
- [ ] Run, fail, implement, pass. `TestBinaryDeps` still passes for `./examples/downstream`.
- [ ] Verify: `go -C … test ./internal/proxyview/... ./pkg/cli ./examples/... ./internal/app -run 'Seam|Wrapping|Module|Provenance|Downstream|BinaryDeps'`.
- [ ] Commit `feat(cli): provenance module seam for downstream builds`.

### Task 4b.1.4: Docs for `proxy routes`
**Files:** modify `docs/output.md` (ProxyRoutes section only), `README.md` (`proxy routes` section and `Extending`), `docs/decisions.md`.
- [ ] docs/output.md `ProxyRoutes`: replace `ProxySource`/`AmbiguousRoute`/`EnvoySnapshot` with the design §5 tables; the ID scope (DD-15), the selection strategy and `chosenBy` (DD-4), raw config left out (DD-14), policy levels (DD-16).
- [ ] Add `TestProxyRoutesDocMatchesPayload` (in `internal/proxyview`, like `TestBundleManifestDocMatchesSchema`): every JSON field name of the payload types is in the docs section, and the docs name no field the types lack.
- [ ] README `proxy routes`: the new sample tree from the reviewed `collide-meta-orders` golden; one paragraph on `--select strict|known`: why `strict` is the default, what `known` marks, that it needs a live cluster, and why it can still differ from krp (Known is the cluster's HTTPRoutes, not `--route`) (DD-4); the JSON paragraph. `Extending`: one line on `WithProvenanceModules`.
- [ ] decisions.md: the controller adds D-033 (module seam: `WithProvenanceModules`, no module-name selection, no claim IDs in output), D-034 (own `ProxyRoutes` payload, raw config out, occurrence IDs scoped to one document) and D-035 (release mechanics: version from build info, `hack/release.sh`, controller publishes). D-032 (selection strategy) is already in. The implementer only checks that the docs cite them.
- [ ] Verify: `go -C … test ./internal/proxyview -run Doc`.
- [ ] Commit `docs: ProxyRoutes v1alpha2 payload and module seam`.

## Sub-phase 4b.2 — Envelope, Snellius, release

### Task 4b.2.1: Envelope `v1alpha2`
**Files:** modify `internal/print/envelope.go`, `docs/output.md` (Envelope, Stability), every test that names the literal (`internal/app/{check,describe,version,proxy_routes,bundle_scope,bundle_deadline}_test.go`, `internal/bundle` tests), every golden with `schemaVersion` (`internal/app/testdata/{describe,version,proxy-routes,…}`).
**Interfaces (produces):** `const SchemaVersion = "kgwctl.kgateway.dev/v1alpha2"`.
- [ ] `TestSchemaVersion` (`internal/print`): pins the literal `kgwctl.kgateway.dev/v1alpha2`. Other tests compare with `print.SchemaVersion`.
- [ ] `TestBundleManifestSchemaVersion`: a bundle run's `manifest.json`, `check.json` and `version.json` carry `v1alpha2`.
- [ ] `-update` every golden suite; `git diff` must show only the `schemaVersion` line in non-`ProxyRoutes` goldens. Any other line is a defect to explain before commit.
- [ ] docs/output.md: Envelope example and field table on `v1alpha2`; Stability: "`v1alpha2` changed the shape of `ProxyRoutes` only; the other kinds kept their fields."
- [ ] Verify: `make -C /Users/ddoyle/Development/claude/kgwctl-claude-phase4 test`; `grep -rn 'kgwctl.kgateway.dev/v1alpha1' internal pkg cmd examples hack docs/output.md README.md` prints nothing.
- [ ] Commit `feat(output): envelope schemaVersion v1alpha2`.

### Task 4b.2.2: Snellius v0.9.0
**Files:** modify `go.mod`, `go.sum`, `internal/app/bundle_scope.go`, `internal/app/bundle_scope_test.go`, check goldens if Snellius findings change them.
- [ ] `go get github.com/DuncanDoyle/snellius@v0.9.0`; `go mod tidy`; `go build ./...`.
- [ ] `discoveryScope`: replace the `Contains` loop with `names, known := s.Excluded(nss)`; `known == false` keeps mode `unknown`, no names, the existing reason. Delete the "local loop" comment.
- [ ] `TestDiscoveryScopeExcluded`: selectors scope over `team-a`, `team-b` (unwatched) → `Unwatched == ["team-b"]`, sorted; `ScopeUnknown` → mode `unknown`, no names; Namespaces unreadable → `namespaces-unreadable` as today.
- [ ] Run the full suite. For each changed `check` golden, name the Snellius decision (D-129, D-132, D-135) that explains it in `.agents-tmp/p4b/4b.2.2-check.md`; anything else is a defect.
- [ ] Verify: `go -C … test ./internal/app -run 'Bundle|Check|Scope'`; `go -C … mod tidy -diff`.
- [ ] Commit `chore(deps): Snellius v0.9.0; Scope.Excluded for unwatched namespaces`.

### Task 4b.2.3: Client version
**Files:** modify `cmd/kgwctl/main.go`; create `cmd/kgwctl/buildinfo.go`, `cmd/kgwctl/buildinfo_test.go`.
**Interfaces (produces):**
- `func clientBuild(version, commit string, read func() (*debug.BuildInfo, bool)) cli.BuildInfo` (ldflags first; else `Main.Version` unless empty or `(devel)`, and the `vcs.revision` setting; else `dev`/`unknown`; `GoVersion` from `runtime.Version()`)
- `main` calls `clientBuild(version, commit, debug.ReadBuildInfo)`.
- [ ] `TestClientBuildLdflags`: `version="v0.1.0"`, `commit="abc"` → both kept, build info ignored.
- [ ] `TestClientBuildModuleVersion`: ldflags unset, `Main.Version="v0.1.0"`, no VCS settings → `v0.1.0`, `unknown`.
- [ ] `TestClientBuildVCS`: `Main.Version="(devel)"`, `vcs.revision=1234abcd` → `dev`, `1234abcd`.
- [ ] `TestClientBuildNoInfo`: `read` returns `nil, false` → `dev`, `unknown`.
- [ ] Verify: `go -C … test ./cmd/kgwctl`; `go -C … build -o .agents-tmp/p4b/kgwctl ./cmd/kgwctl` and `.agents-tmp/p4b/kgwctl version --client` shows the VCS-stamped version.
- [ ] Commit `feat(version): client version from build info when ldflags are unset`.

### Task 4b.2.4: Release script
**Files:** create `hack/release.sh`; modify `.gitignore` (`hack/release/out/`), `README.md` (Install, Development).
- [ ] `hack/release.sh <tag>` per design DD-22: refuses a dirty tree or a HEAD that is not exactly `<tag>` (`git describe --exact-match --tags`), unless `RELEASE_DRY_RUN=1`, which allows any HEAD and marks the output dir `dry-run`; builds the four targets; packs `kgwctl`, `LICENSE`, `README.md`; writes `checksums.txt` with `sha256sum`, or `shasum -a 256` where that is missing; runs the host binary's `version --client` and fails unless it prints `<tag>`; prints the `gh release create <tag> hack/release/out/*.tar.gz hack/release/out/checksums.txt --title <tag> --notes-file <file>` command. Never publishes.
- [ ] README Install: `go install github.com/DuncanDoyle/kgwctl/cmd/kgwctl@v0.1.0`, and the release archives. Development: one line on `hack/release.sh`.
- [ ] Verify: `bash -n hack/release.sh`; `shellcheck hack/release.sh` when installed; `RELEASE_DRY_RUN=1 hack/release.sh v0.0.0-dryrun` builds four archives and its version check passes; `tar -tzf` lists the three files.
- [ ] Commit `build: release script for tagged builds`.

### Task 4b.2.5: Live check and goldens check
**Files:** modify `hack/live-check.sh`, `testdata/dumps/README.md` (result lines only).
- [ ] `expect_matched`: route line `· HTTPRoute $ORDERS · rule 0 · match 0`; each filter line names its policy as `(extAuth, inferred)`, `(rateLimit.local, inferred)`, `(transformation, inferred)`; the `policies:` line as today. Take every string from the reviewed `collide-meta-orders` golden, not from memory.
- [ ] `expect_ambiguous`: unchanged lines; also fails on `chosen from known`.
- [ ] `routes`, metadata off: also run live with `--select known`. Both HTTPRoutes exist, so `expect_ambiguous` holds and the JSON has `selection.strategy == "known"` (design R20). Run `--file … --select known` and require exit 2 with `--select known` on stderr (R22).
- [ ] New `expect_json <file> <matched|ambiguous>`: `jq` checks `schemaVersion == "kgwctl.kgateway.dev/v1alpha2"`, `kind == "ProxyRoutes"`; matched: `.item.selection.routes | length == 1` and the orders route's filter names in HCM order equal `$ORDERS_FILTERS` restricted to `runs == "yes"`; ambiguous: `routes == []`, `ambiguous | length == 2`. `routes` runs it on a `-o json` live run per metadata setting.
- [ ] `same_view` still compares the live and file text below the header (the context label is equal by DD-11).
- [ ] `goldens_check`: after the dump checks, run `kgwctl proxy routes --file <dump> -o json` on each recorded dump and require `schemaVersion` `v1alpha2`, a non-empty `listeners`, and at least one route with `filters`. `goldens` (capture) is not re-run; the dumps stay.
- [ ] Run on kind `kgwctl-live` (kgateway v2.5.1, context `kind-kgwctl-live`, kubeconfig `hack/live/out/admin.kubeconfig`): `hack/live-check.sh goldens-check`, `hack/live-check.sh routes`, `hack/live-check.sh bundle`. Record the summary lines.
- [ ] Commit `test(live-check): proxy routes on the v1alpha2 schema`.

## Phase close prerequisites (not done by the implementer)
1. Codex phase review and triage (queue Q5).
2. Owner try-out on kind.
3. Merge the phase branch, tag `v0.1.0` on main, run `hack/release.sh v0.1.0`, and, after the owner's go-ahead, the printed `gh release create`. Then `go install github.com/DuncanDoyle/kgwctl/cmd/kgwctl@v0.1.0` in a clean `GOPATH` and check `version --client` (design R16).
4. Close kgwctl#15 and kgwctl#16; tell xdsview its Phase 9 contract is confirmed by a consumer; update the program file (E15 consumed).
