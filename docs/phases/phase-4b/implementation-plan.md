# Phase 4b — xdsview v0.7.0 migration and first release: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `proxy routes` runs on the xdsview v0.7.0 contract with a selectable route selection strategy (`--select strict|known`, default `strict`, D-032), a downstream module seam and kgwctl's own `ProxyRoutes` payload; every envelope moves to `kgwctl.kgateway.dev/v1alpha2`; Snellius v0.9.0; a correct client version; `v0.1.0` can be tagged at the close.

**Architecture:** `internal/proxyview` owns the pipeline (parse, effective chains, compose, select or narrow, prune), the text hooks, the warning list and the payload projection. `internal/app/proxy_routes.go` keeps capture, Known, flags and exit codes, and passes the module set from `app.Config`. `pkg/cli.WithProvenanceModules` sets that set for downstream builds. `cmd/kgwctl` resolves the client version. `hack/release.sh` builds the release files.

**Tech Stack:** Go 1.26, cobra, client-go v0.36.x, xdsview v0.7.0 (`pkg/envoy/{parser,chain,model,render,provenance}`, `pkg/adapter/kgateway`), Snellius v0.9.0. No new modules.

**Spec:** [`design.md`](design.md) (r5). Read it first. §n, DD-n and Rn refer to it. Reviews: [`reviews/design-codex.md`](reviews/design-codex.md), [`reviews/design-codex-r2.md`](reviews/design-codex-r2.md), [`reviews/design-codex-r3.md`](reviews/design-codex-r3.md).

## Global Constraints

- Selection follows `--select` (DD-4, D-032). `strict` (default): a match is a route in `kgateway.Select(...).Routes` and nothing else. `known`: also a route in `Select(...).Narrowed` whose kept claims name exactly one reading, merged as `provenance.Resolve` merges, that matches the query; nothing else. `kgateway.Select` is never changed or bypassed.
- `--select known` with `--file` is a usage error (exit 2) before any I/O. Without `--route` the strategy changes neither stdout nor stderr (R23).
- Known is the cluster's HTTPRoutes live, and empty with `--file`. `--route` is never added to Known (DD-3).
- No read of `Claim.Module`, no use of `kgateway.ModuleName`, no parsing of a claim ID or of an index warning message, in any non-test kgwctl code (DD-9, DD-10). Claims are read through `provenance.Resolve`, `ResolveMirror`, `Records`, `Index.About`, `Index.Record`, `kgateway.Select`, `kgateway.Narrow` and the assertion types.
- The payload and the text hold no claim ID, no module name and no raw `typedConfig`, `typedPerFilterConfig` or `metadata` (DD-10, DD-14). `claim-dropped` and `link-dropped` get kgwctl's messages.
- One warning list (DD-12): payload `warnings` and the text block at the end are the same list; `render.Text` gets a snapshot copy with `Warnings` nil and prints none.
- Chains, index, selection and narrowings are computed on the full snapshot; `model.Prune` runs last, only under `--route` (DD-1).
- Live parses with `model.CaptureComplete`; `--file` passes no capture (DD-2).
- Flags other than `--select`, exit codes, RBAC, capture, deadlines and the 64 MiB cap of `proxy routes` are unchanged.
- No vendor or edition names (D-004); say "downstream build". No new Go modules beyond the two bumps.
- Goldens are reviewed, not just regenerated: each `-update` run is followed by a diff read against design §3.3, and the sub-phase record lists what changed and why.
- Every task ends with `make -C /Users/ddoyle/Development/claude/kgwctl-claude-phase4 test` (race, envtest assets set up by the Makefile) green. Narrow `go test -run` commands are for the red/green loop only; a bare `go test` of `internal/app` needs `KUBEBUILDER_ASSETS`.
- Code comments 1–2 lines; Go doc comments on exported identifiers (go.dev/doc/comment).
- Scratch files only in `.agents-tmp/`; no `cd` in compound commands (use `go -C`, `make -C` or absolute paths); every `kubectl`/`helm` call passes `--context`/`--kube-context`.

## Review Focus

1. **A route Known narrowed to one matching reading, with `--route`.** Under `strict`: in `selection.ambiguous` with `narrowed: true` and the note, never in `selection.routes` or the tree. Under `known`: in `selection.routes` with `chosenBy: "known"`, in the tree with `(chosen from known HTTPRoutes)`, also when its kept claims differ only in `IndexCertainty`, `Incomplete` or `Exhaustive`. Tasks 4b.1.1, 4b.1.2.
2. **A wrapping module under another name.** Expects byte-equal text and JSON, also for a dropped claim, and a working `FilterLink` to an inner record. Task 4b.1.3.
3. **Warnings after pruning.** A retained HCM with no routes and an ECDS slot warning; a slot warning under two routes. Expects each warning once in JSON and once in the text block. Task 4b.1.1.
4. **The colliding routes of `policies.json`, live and from a file.** Expects the same view below the header, both listed as ambiguous, neither matched under `strict`. Tasks 4b.1.1, 4b.2.4.
5. **A route config shared by two filter chains, and two sources with one name.** Expects one JSON entry per use with `routeConfig.source`, equal `sourcePath`, and one text line in the ambiguous list. Task 4b.1.1.
6. **`--select` at the command boundary.** Expects R21–R24 stdout and stderr exactly. Task 4b.1.2.
7. **`schemaVersion` in every output and in the bundle's JSON members.** Expects `v1alpha2` everywhere, and no other change in non-`ProxyRoutes` goldens. Task 4b.1.1; Snellius changes only in Task 4b.2.1.
8. **Release order and file set.** Expects a dry run that never writes the tag directory and prints no publish steps, a real run that refuses a dirty tree or a HEAD other than the tag commit, a real run that refuses a non-empty `hack/release/out/<tag>/`, checksums and publish arguments over exactly the four archives, the pushed tag matching `commit.txt`, the `go install` check, then `--verify-tag`. Task 4b.2.3 and the close.

## Test harness

- Recorded dumps: `testdata/dumps/{demo,policies,collide-meta}.json`, unchanged.
- Hand-built dumps in `internal/proxyview/testdata/` (new): `shared-source.json` (one source, two HCMs; two sources with one name), `ecds-network.json` (an unresolved ECDS network filter in a chain without an HCM; an HCM whose routes `--route` prunes, with an ECDS slot warning), `ecds-ambiguous.json` (two HTTP filters with `config_discovery` on one name with two matching ECDS entries in the dump: one the HCM leaves enabled, one the HCM disables and no route enables), `levels.json` (`merge.*` at four levels), `downstream-field.json` (a route with per-filter config for `example/filter` and a `merge.TrafficPolicy` field `exampleField`), and the two-route slot-warning case.
- `internal/proxyview/proxyviewtest` (new in Task 4b.1.1, test-only): `Wrap(name string, inner provenance.Module, extra func(snap *model.Snapshot, inner []provenance.Claim) []provenance.Claim) provenance.Module`. It re-emits the inner claims under its own name, then `extra`'s.
- `internal/app`: command-boundary goldens under `testdata/proxy-routes/`, the fake dialer and envtest as in Phase 3.
- Golden update: `go test ./internal/app -run <Test> -update` inside `make`'s env (`KUBEBUILDER_ASSETS="$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.24.1 use 1.36.x -p path)"`), then `git -C <worktree> diff --stat` and a read of each changed golden.

## Sub-phases

| Sub-phase | Delivers | Tasks |
|---|---|---|
| 4b.1 | Migration commit (pipeline, text, warnings, complete payload, envelope `v1alpha2` for every kind, assertions, goldens, output docs); `--select`; module seam; README | 4b.1.1–4b.1.4 |
| 4b.2 | Snellius v0.9.0, client version, release script, live check | 4b.2.1–4b.2.4 |

Each task builds and passes `make test` on its own; no task consumes a type a later task introduces. Each sub-phase ends with `make test`, `make lint`, the binary dependency test and `go mod tidy -diff` empty.

---

## Sub-phase 4b.1 — Migration, strategy, seam

### Task 4b.1.1: Migration to xdsview v0.7.0 and `v1alpha2` (one commit)
**Files:** modify `go.mod`, `go.sum`, `internal/proxyview/view.go`, `internal/proxyview/view_test.go`, `internal/app/proxy_routes.go`, `internal/app/proxy_routes_test.go`, `internal/app/lifecycle_test.go`, `internal/print/envelope.go`, every test that names the schema literal (`internal/app/{check,describe,version,proxy_routes,bundle_scope,bundle_deadline}_test.go`, `internal/bundle` tests), every golden with `schemaVersion`, `internal/app/testdata/proxy-routes/*.golden`, `docs/output.md` (Envelope, Stability, ProxyRoutes); create `internal/proxyview/{text.go,warnings.go,payload.go}`, `internal/proxyview/proxyviewtest/wrap.go`, `internal/proxyview/{payload_test.go,warnings_test.go,coverage_test.go,doc_test.go}`, `internal/proxyview/testdata/*.json`.
**Interfaces (produces):**
- `type Strategy string`; `const ( StrategyStrict Strategy = "strict"; StrategyKnown Strategy = "known" )`. (Only `strict` is used until Task 4b.1.2; the type exists so `View` and the payload carry it.)
- `type Options struct{ Gateway *types.NamespacedName; Known []model.Ref; Capture model.Capture; Selection Selection; Strategy Strategy; Modules []provenance.Module }` (empty Strategy is `strict`; nil Modules means `DefaultModules()`)
- `func DefaultModules() []provenance.Module` (a fresh `[]provenance.Module{kgateway.New()}` each call)
- `type Selection struct{ Route types.NamespacedName; Rule *int }` (unchanged)
- `type View struct{ Full, Snapshot *model.Snapshot; Chains map[model.ID]model.EffectiveChain; Index *provenance.Index; Narrowed []provenance.Narrowing; Selected *kgateway.Selection; Chosen []provenance.Narrowing; Strategy Strategy; Selection Selection; Gateway *types.NamespacedName; Warnings []Warning }` (Chosen holds normalised narrowings, DD-6; Warnings is the DD-12 list)
- `func Build(body []byte, o Options) (View, error)` (`ErrNotConfigDump` as today; a `Compose` error wrapped as `compose provenance modules: %w`)
- `func (v View) Routes() []model.ID`; `func (v View) Matched() int`
- `type TextOptions struct{ Header string; ResponseOrder bool }`; `func Text(w io.Writer, v View, o TextOptions) error` (hooks per DD-11; Labels gets `v.Chosen` only; renderer input is a copy with `Warnings` nil; selection lists per DD-5; then the warning block)
- `const OneCandidateNote = "one candidate exists in the cluster"`; `const GatewayContextNote = "(from the command line)"`; `func HTTPRouteRef(n types.NamespacedName) model.Ref`
- `warnings.go`: `type Warning struct{ Subject, Code, Message string }`; `func warningList(full *model.Snapshot, ix *provenance.Index) []Warning` (parser warnings, then index warnings; `claim-dropped`/`link-dropped` rewritten per DD-10; deduplicated on subject, code and message); `func describeSubject(full *model.Snapshot, id model.ID) string`
- `func normalise(n provenance.Narrowing) provenance.Narrowing` (one kept claim, `RouteSource` merged: highest `IndexCertainty`, `Incomplete` if any, `Exhaustive` if all)
- `internal/proxyview/proxyviewtest/wrap.go` (test-only package, created here because `TestWarningsDroppedClaimMessage` and Task 4b.1.2 use it): `func Wrap(name string, inner provenance.Module, extra func(snap *model.Snapshot, inner []provenance.Claim) []provenance.Claim) provenance.Module`. It re-emits the inner claims under its own name, then `extra`'s. Imported only by `_test.go` files.
- `payload.go`: the complete payload of design §5: `ProxyRoutes{Source; Capture; Listeners; Sections; Selection *SelectionOut; Warnings []Warning}`, `Ref`, `Resolved[T any]{Certainty; Candidates []T}`, `Listener`, `ListenerState`, `ListenerError`, `ListenerPort`, `FilterChain`, `TLS`, `NetworkFilter{ID, Name, Type, ConfigSource string}`, `HCM`, `RouteConfig{Name, Source, State, Kind string; ScopedRoutes bool; Policies []Policy}`, `HTTPFilter{Slot, Name, Type string; Disabled bool; ConfigSource string; Extension *Resolved[Extension]}`, `Extension`, `VirtualHost`, `Route`, `Match`, `HeaderMatch`, `QueryParamMatch`, `Origin{Certainty string; Complete, Narrowed bool; ChosenBy string; Readings []Reading}`, `Reading`, `Filter`, `Policy`, `Action`, `NamedType`, `Redirect`, `DirectResponse`, `Rewrite`, `Header`, `Backend`, `BackendRef`, `WeightedBackend`, `Mirror`, `SelectionOut{Query; Strategy string; Routes []MatchedRoute; Ambiguous []AmbiguousRoute; Unresolved []RouteKey}`, `Query`, `RouteKey`, `MatchedRoute{RouteKey; ChosenBy string}`, `AmbiguousRoute{RouteKey; Readings []Reading; Narrowed bool}`, `Section`; `func (v View) Payload(src Source) ProxyRoutes`. JSON names per design §5; optional fields `omitempty`; always-present lists non-nil.
- `internal/print`: `const SchemaVersion = "kgwctl.kgateway.dev/v1alpha2"`.
- `internal/app`: `printRoutes` builds `proxyview.Options` (Capture `model.CaptureComplete` live), prints `View.Payload(src)` for `-o json|yaml`, maps a compose error to exit 2.
- `lifecycle_test.go` `forbiddenXdsview`: allow `pkg/envoy/{parser,chain,model,render,provenance}` and `pkg/adapter/kgateway`; forbid everything else, `origin` and `filter` included.
**Steps (red/green loop, one commit at the end):**
- [ ] `go get github.com/DuncanDoyle/xdsview@v0.7.0`; `go mod tidy`.
- [ ] Port each `view_test.go` test (names kept): `TestBuildExactSelection` (collide-meta, `--route kgwctl-proxy/orders`: one ID in vhost `orders.kgwctl.example`, `Matched() == 1`), `TestBuildAmbiguousWithoutMetadata`, `TestBuildSingleSurvivorIsNotAMatch` (Known without `proxy/orders-kgwctl`: in `Ambiguous` and `Narrowed` with one kept reading, not in `Routes()`), `TestBuildWarmingListenerSurvivesNarrowing`, `TestBuildParserWarning`, `TestBuildPolicyChain`, `TestBuildResponseOrderMarksUnknownSide`, `TestBuildRuleSelection`, `TestBuildParseError`, `TestTextMarksNarrowedInFullTree` (row `ambiguous: …`, next line the note, no `chosen from known`), `TestBuildAmbiguousPerRouteConfig`, `TestTextNoListenersAfterNarrowing`, `TestBuildUnresolvedNeverMatches`, `TestBuildUnresolvedIgnoresVhostPrefix`, `TestBuildAmbiguousJudgedOnNameReadings`.
- [ ] `TestBuildNoKnownWithFile`, `TestBuildCapture`, `TestTextGatewayContext` (static rows unlabelled; a `GatewaySource` claim at inferred suppresses the label), `TestBuildDuplicateModules`.
- [ ] `TestNormaliseMergesReading`: kept claims equal but for `IndexCertainty` (inferred, exact), `Incomplete` (false, true) and `Exhaustive` (true, false) → one claim with exact, `Incomplete: true`, `Exhaustive: false`; `provenance.Labels(ix, []Narrowing{normalised})` prints one reading with `(chosen from known HTTPRoutes)`, not `ambiguous:`.
- [ ] `warnings_test.go`: `TestWarningsPrunedHCMSlot` (`ecds-network.json`, `--route` keeps the listener but no route of the second HCM: its ECDS slot warning is once in `Payload().Warnings` and once in the text block; no `warning:` line inside the tree); `TestWarningsSlotUnderTwoRoutes` (one entry, one text line); `TestWarningsPrunedSubject` (pruned-subject warning kept with its `subject`); `TestWarningsDroppedClaimMessage` (a wrapper adds a claim without evidence: one `claim-dropped` entry with kgwctl's message and the subject description; no `/` claim-ID pattern and no module name in message or text); `TestWarningsDedup` (two drops of one code on one subject → one entry).
- [ ] `payload_test.go`: `TestPayloadExactRoute` (as design R4; ext_authz filter `policies` `[{TrafficPolicy kgwctl-proxy/orders-extauth, extAuth, route, inferred}]`; three route-level records `exact`; backend Service `kgwctl-proxy/backend` port 8080), `TestPayloadAmbiguousSelection` (R1), `TestPayloadNarrowed` (R3, R6), `TestPayloadPolicyLevels` (`levels.json`), `TestPayloadNoRawConfig` (no `typedConfig`, `typedPerFilterConfig`, `metadata`, `"module"` key or claim-ID pattern in any recorded dump's payload), `TestPayloadIDsJoin`, `TestPayloadRouteConfigSources` (`shared-source.json`: two uses with equal `routeConfig.source` and `sourcePath`; two sources with one name keep distinct `source`; the payload has no field listing unjoined sources), `TestPayloadECDSNetworkFilter` (R15b, `ecds-network.json`: `configSource: "unresolved"`, no `hcm`; text shows `(ECDS, unresolved)` and `HTTP filters unknown`), `TestPayloadECDSAmbiguousHTTPFilter` (R15c, `ecds-ambiguous.json`: the entry is in the dump, yet both HTTP filters have `configSource: "unresolved"` and `warnings` holds `ecds-ambiguous`; the enabled filter's chain entries are `runs: "unknown"`, `state: "unresolved"`; the disabled one's are `runs: "no"`; both chain values equal xdsview's `chain.Effective` output for the same slot, so the payload copies execution and never derives it from `configSource`), `TestPayloadStrategyField` (`selection.strategy: "strict"` and every route `chosenBy: "select"`).
- [ ] `TestPayloadCoversModel` (`coverage_test.go`): every exported field of the model types in design DD-13 is in a `mapped` or `leftOut` table (`leftOut`: `TypedConfig`, `TypedPerFilterConfig`, `Metadata`, `Discovery` details beyond the `configSource` indicator, `Reported`); a new field fails with its type and name.
- [ ] `TestSchemaVersion` (`internal/print`) pins `kgwctl.kgateway.dev/v1alpha2`; other tests use `print.SchemaVersion`. `TestBundleManifestSchemaVersion`: a bundle run's `manifest.json`, `check.json`, `version.json` carry `v1alpha2`.
- [ ] Update the `proxy_routes_test.go` assertions that decode the old shape: `TestProxyRoutesParserWarning` and `TestProxyRoutesUndecodableHCM` decode `warnings` as `[]struct{Subject, Code, Message string}` and read `item.listeners`; `TestProxyRoutesJSONOneDocument` decodes `item.source` and `item.listeners`; `TestProxyRoutesLiveJSON` checks `origin.narrowed`; text tests expect warnings in the end block only.
- [ ] docs/output.md: Envelope and Stability on `v1alpha2` ("only `ProxyRoutes` changed shape"); ProxyRoutes section per design §5, including DD-12, DD-15, DD-16, DD-23 and `configSource`. `TestProxyRoutesDocMatchesPayload` (`doc_test.go`): every payload JSON field is in the section, and the section names no field the types lack.
- [ ] Regenerate every golden suite with `-update`. Read every diff: non-`ProxyRoutes` goldens may change only the `schemaVersion` line; `proxy-routes` goldens change as design §3.3 lists. Record the reviewed differences in `.agents-tmp/p4b/4b.1.1-goldens.md`.
- [ ] Verify: `make -C /Users/ddoyle/Development/claude/kgwctl-claude-phase4 test`; `make -C … lint`; `grep -rn 'kgwctl.kgateway.dev/v1alpha1' internal pkg cmd examples hack docs/output.md README.md` prints nothing; `go -C … mod tidy -diff` empty.
- [ ] Commit `feat(proxy routes)!: xdsview v0.7.0, kgwctl ProxyRoutes payload, envelope v1alpha2`.

### Task 4b.1.2: `--select strict|known`
**Files:** modify `internal/proxyview/view.go`, `internal/proxyview/view_test.go`, `internal/proxyview/payload_test.go`, `internal/app/proxy_routes.go`, `internal/app/proxy_routes_test.go`, `internal/app/testdata/proxy-routes/*known*.golden` (new), `docs/output.md` (ProxyRoutes: strategy and `chosenBy` wording).
**Interfaces (produces):**
- `func ParseStrategy(s string) (Strategy, error)` (empty is `strict`; else exactly `strict` or `known`)
- `func chooseKnown(q kgateway.Query, sel kgateway.Selection) []provenance.Narrowing` (unexported: narrowings whose kept claims merge into one reading that matches q, each returned through `normalise`)
- `Build` fills `View.Chosen` under `known` with a Selection; Snapshot is pruned to `Selected.Routes` plus the Chosen routes; Payload marks `chosenBy` and `origin.chosenBy`, and leaves chosen routes out of `ambiguous`.
- `internal/app`: `routesOptions.selectStrategy string`; flag `--select` (`strict|known`, default `strict`; help: "How --route picks routes: strict lists a route that only the cluster's HTTPRoutes narrowed as ambiguous; known also selects it"). `routesRequest.strategy`. `parseRoutes`: usage error (exit 2, before any I/O) for an unknown value and for `known` with `--file`, message `--select known needs the cluster's HTTPRoutes; it does not work with --file`. Live, `known` with `--route` and an HTTPRoute list failure the command recovers from (the branch that prints `Warning: could not list HTTPRoutes`; a deadline still exits 2): `routesLive` records the condition as a flag on the request; `printRoutes` writes the stderr line `Warning: --select known: no route can be chosen from known HTTPRoutes` only after stdout was written successfully. Any exit 2 (parse error, not a config dump, compose error, write error) means the line is never written; `TestProxyRoutesSelectFlag` covers the write error with a failing stdout writer. Without `--route`, under `strict`, or on exit 2, no extra line.
**Steps:**
- [ ] `TestChooseKnownRule`: one matching reading → chosen; two distinct readings → not; one reading for another rule index → not; claims differing only in `IndexCertainty`, `Incomplete` or `Exhaustive` → chosen, normalised.
- [ ] `TestBuildStrategySameCapture` (policies.json, one capture, both strategies): Known without `proxy/orders-kgwctl` → `strict`: `Routes()` empty, `Chosen` nil; `known`: both colliding routes in `Routes()` and `Chosen`, rows end `(chosen from known HTTPRoutes)`, no ambiguous list. Known with both → equal `Routes()` (empty) and text.
- [ ] `TestKnownMarkerTextAndJSON` (R26): kept claims differing only in `IndexCertainty`, `Incomplete`, `Exhaustive` (built with `proxyviewtest.Wrap` re-emitting a modified copy of the inner `RouteSource`): text row has one reading and the marker; JSON `chosenBy: "known"`; same output under a second wrapper name.
- [ ] `TestStrategyNoEffectWithoutRoute`: no Selection: `Text` and `Payload` byte-equal under both.
- [ ] `TestPayloadStrategy`: R19 JSON (`selection.strategy`, `chosenBy`, `origin.chosenBy`, `origin.narrowed`, `origin.certainty: "ambiguous"`); collide-meta under `known`: `chosenBy: "select"`.
- [ ] `TestProxyRoutesSelectFlag` (`internal/app`, command boundary):
  - `--file … --select known` (with and without `--route`) and `--select bogus`: exit 2, stdout empty, stderr names `--select`, no file read (a path that does not exist still gives the `--select` error), `Proxy.Requests()` unchanged.
  - R21: live fake dialer serving `collide-meta.json`, `--route kgwctl-proxy/orders`, both strategies: text stdout byte-equal, stderr empty, JSON equal except `selection.strategy`.
  - R19: live, envtest fixture lacking `proxy/orders-kgwctl`, `--select known --route kgwctl-proxy/orders`: both routes printed with the marker; JSON `chosenBy: "known"`.
  - R23: live, no `--route`, list allowed and list denied (`Proxy.Rule` 403 on HTTPRoutes), both strategies: stdout and stderr byte-equal between strategies per outcome; denied stderr is exactly the one existing warning line.
  - R24: live, `--route`, list denied: exit 0; text stdout byte-equal; JSON equal except `selection.strategy`; stderr exactly one line under `strict`, those two lines under `known`.
  - R24a: live, `--route --select known`, list denied, fake dialer serving a malformed dump: exit 2, stdout empty, stderr exactly the list warning and the parse error, no `--select known` line.
- [ ] Goldens `live-policies-orders-known.golden` and `json-live-policies-orders-known.golden`; read them.
- [ ] Verify: `make -C … test`.
- [ ] Commit `feat(proxy routes): --select strict|known (D-032)`.

### Task 4b.1.3: Module seam
**Files:** modify `internal/app/root.go`, `internal/app/proxy.go`, `internal/app/proxy_routes.go`, `pkg/cli/cli.go`, `pkg/cli/cli_test.go`, `examples/downstream/main.go`, `examples/downstream/main_test.go`; create `internal/proxyview/seam_test.go`, `internal/proxyview/hygiene_test.go`, `pkg/cli/testdata/downstream-field.json` (copy of the proxyview fixture).
**Interfaces (produces):**
- `app.Config.Modules []provenance.Module` (nil: `proxyview.DefaultModules()`).
- `func newProxyCmd(now func() time.Time, modules []provenance.Module, hooks proxyTestHooks) *cobra.Command`; `func newProxyRoutesCmd(now func() time.Time, modules []provenance.Module, hooks proxyTestHooks) *cobra.Command`
- `func WithProvenanceModules(m ...provenance.Module) Option` in `pkg/cli` (replaces the default set; no modules keeps it; doc: wrap `kgateway.New()` under your own name; never reuse `kgateway`).
- Uses `proxyviewtest.Wrap` from Task 4b.1.1; `pkg/cli` tests and `examples/downstream` define their own small wrappers, since `internal/` test helpers are not part of the public seam.
**Steps:**
- [ ] `TestWrappingModuleSameSelection`: every golden case, both strategies (`known` only with Known), `Wrap("example", kgateway.New(), nil)`: same `Selected` and `Chosen` IDs, byte-equal `Text` and encoded `Payload`.
- [ ] `TestWrappingModuleAddsRecord` (R12).
- [ ] `TestWrappingModuleDroppedClaimSameOutput` (R12b): the same invalid claim under `Wrap("example", …)` and `Wrap("other", …)` → byte-equal text and JSON.
- [ ] `TestNoModuleNameSelection` (`hygiene_test.go`): parse every non-test `.go` file under `internal/` and `pkg/` with `go/parser`; fail on a selector `X.Module` where `X` is not an imported package name, and on any selector `ModuleName`; print file:line. (Index warning messages are covered by `TestWarningsDroppedClaimMessage` and `TestWrappingModuleDroppedClaimSameOutput`, not by this scan.)
- [ ] `TestWithProvenanceModules` (`pkg/cli`, command boundary): R12a on `downstream-field.json` with `--route`: the wrapper finds the inner `PolicyRecord` with `Field == "exampleField"` among its inner claims and emits `FilterLink{Record: rec.Ref, Slot: <example/filter slot>}` at inferred with metadata evidence naming the policy; JSON filter `policies` has the record (`route`, `inferred`); text row ends `· TrafficPolicy ns/p (exampleField, inferred)`; the default root shows the record only on the `policies:` line. Also: `WithProvenanceModules()` equals the default; two modules with one name exit 2 with `compose provenance modules`.
- [ ] `examples/downstream`: `cli.WithProvenanceModules(exampleModule{})`, a wrapper of `kgateway.New()` named `example` that adds nothing; `main_test.go` runs `proxy routes --file …collide-meta.json --route kgwctl-proxy/orders` and checks the matched route.
- [ ] Verify: `make -C … test` (includes `TestBinaryDeps` for `./examples/downstream`).
- [ ] Commit `feat(cli): provenance module seam for downstream builds`.

### Task 4b.1.4: README
**Files:** modify `README.md` (`proxy routes`, `Extending`), `internal/app/proxy_routes.go` (long help of `proxy routes`).
- [ ] `proxy routes`: the sample tree from the reviewed `collide-meta-orders` golden; warnings at the end; the JSON paragraph, naming the raw-config exclusion and the ID scope.
- [ ] `proxy routes`, a subsection "How `--route` picks routes" (owner request: explain `--select` properly). Plain words for a reader whose first language isn't English; short sentences; no xdsview internals:
  - why an Envoy route's HTTPRoute can be unclear: without route metadata kgateway joins `httproute-<name>-<ns>-…` with dashes, so `team-a/orders` and `a/orders-team` give the same route name; a name and namespace without dashes give one reading;
  - the three values in order, each adding one kind of evidence, with one example command each: `exact` (route metadata only), `strict` (default; also a name with one reading), `known` (also a name the cluster's HTTPRoutes narrow to one reading);
  - a table of cases × strategies (metadata on; one reading; several readings with one in the cluster; several in the cluster) showing match / inferred / ambiguous / "chosen from known HTTPRoutes";
  - the risk of `known`, as the four-step story: an HTTPRoute deleted, another created, the proxy still serving the old route, `known` naming the wrong one; that is why `strict` is the default;
  - `known` needs the live cluster (usage error with `--file`); `exact` and `strict` work with `--file`;
  - turning on route metadata (`enableRouteSourceMetadata`) makes all three agree;
  - how to see the choice in output: text labels `(inferred)`, `(chosen from known HTTPRoutes)`, the "ambiguous"/"inferred … not matched" lists; JSON `selection.strategy`, `chosenBy`, `selection.inferred`;
  - why `known` can differ from krp (krp narrows with `--route` itself).
- [ ] `kgwctl proxy routes --help`: a short paragraph on `--select` (what each value adds, the default, `known` not with `--file`) pointing to the README section. Update help goldens if any.
- [ ] `Extending`: one line on `WithProvenanceModules` and wrapping `kgateway.New()`.
- [ ] Check that docs cite D-032 to D-035 once the controller has added D-033 to D-035.
- [ ] Verify: `make -C … test`.
- [ ] Commit `docs: proxy routes on xdsview v0.7.0`.

## Sub-phase 4b.2 — Snellius, version, release, live check

### Task 4b.2.1: Snellius v0.9.0
**Files:** modify `go.mod`, `go.sum`, `internal/app/bundle_scope.go`, `internal/app/bundle_scope_test.go`, `internal/app/check_test.go`, `internal/app/bundle_check_test.go`; create `internal/app/testdata/check/policy-rejected.yaml`; check goldens if they change.
- [ ] `go get github.com/DuncanDoyle/snellius@v0.9.0`; `go mod tidy`.
- [ ] `discoveryScope`: `names, known := s.Excluded(nss)` replaces the `Contains` loop; `known == false` keeps mode `unknown`, no names, the existing reason. Delete the "local loop" comment.
- [ ] `TestDiscoveryScopeExcluded`: selectors over `team-a`, `team-b` → `Unwatched == ["team-b"]`; `ScopeUnknown` → `unknown`, no names; unreadable Namespaces → `namespaces-unreadable`.
- [ ] `policy-rejected.yaml`: a TrafficPolicy whose ancestor status at its current generation is `Accepted=False/Invalid` and `Attached=False/Pending`. `TestCheckRejectedPolicyIsKGWUI002`: `check -o json` has KGWUI-002 for it and no KGWUI-003. `TestBundleCheckRejectedPolicy`: the bundle's `check.json` has the same finding.
- [ ] Run `make test`. For each changed `check` golden, name the Snellius rule that explains it (D-129 DirectResponse, D-132 policy-ancestor IDs, D-135 freshness, KGWUI-016 ignoring a stale `TargetNotFound`) in `.agents-tmp/p4b/4b.2.1-check.md`; anything else is a defect.
- [ ] Verify: `make -C … test`; `go -C … mod tidy -diff`.
- [ ] Commit `chore(deps): Snellius v0.9.0; Scope.Excluded for unwatched namespaces`.

### Task 4b.2.2: Client version
**Files:** modify `cmd/kgwctl/main.go`; create `cmd/kgwctl/buildinfo.go`, `cmd/kgwctl/buildinfo_test.go`.
**Interfaces (produces):** `func clientBuild(version, commit string, read func() (*debug.BuildInfo, bool)) cli.BuildInfo` (ldflags first; else `Main.Version` unless empty or `(devel)`, and `vcs.revision`; else `dev`/`unknown`; `GoVersion` from `runtime.Version()`). `main` calls `clientBuild(version, commit, debug.ReadBuildInfo)`.
- [ ] `TestClientBuildLdflags`, `TestClientBuildModuleVersion` (`v0.1.0`, `unknown`), `TestClientBuildVCS` (`dev`, `1234abcd`), `TestClientBuildNoInfo` (`dev`, `unknown`).
- [ ] Verify: `make -C … test`; `go -C … build -o .agents-tmp/p4b/kgwctl ./cmd/kgwctl`; `.agents-tmp/p4b/kgwctl version --client` shows the VCS-stamped version.
- [ ] Commit `feat(version): client version from build info when ldflags are unset`.

### Task 4b.2.3: Release script
**Files:** create `hack/release.sh`; modify `.gitignore` (`hack/release/out/`), `README.md` (Install, Development).
- [ ] `hack/release.sh <tag>` per design DD-22:
  - a real build refuses a dirty tree (`git status --porcelain` not empty) and a HEAD that is not the tag commit (`git rev-parse HEAD` differs from `git rev-parse --verify "refs/tags/<tag>^{commit}"`, which also fails when only a branch has that name); only `RELEASE_DRY_RUN=1` skips both;
  - output dir: `hack/release/out/<tag>/` for a real run, refused when it exists and is not empty; `hack/release/out/dry-run/<tag>/` for a dry run, cleared first; a dry run never writes a tag directory;
  - builds the four targets with ldflags and packs `kgwctl`, `LICENSE`, `README.md` into the four named archives;
  - writes `checksums.txt` by naming the four archives one by one (`sha256sum`, else `shasum -a 256`), never a glob, and `commit.txt`; a real build also writes `publish-files.txt` (the four archive paths and `checksums.txt`, one per line);
  - checks the host binary's `version --client` prints `<tag>`;
  - a real build prints the DD-22 close sequence with `<tag>` and the five paths from its own `publish-files.txt`; a dry run writes no `publish-files.txt` and prints only `dry run: not for release`. Never pushes or publishes.
- [ ] README Install: `go install github.com/DuncanDoyle/kgwctl/cmd/kgwctl@v0.1.0`, and the release archives. Development: one line on `hack/release.sh`.
- [ ] Verify: `bash -n hack/release.sh`; `shellcheck hack/release.sh` when installed; `RELEASE_DRY_RUN=1 hack/release.sh v0.0.0-dryrun` writes only under `hack/release/out/dry-run/v0.0.0-dryrun/`: four archives, `commit.txt` equal to `git rev-parse HEAD`, a `checksums.txt` with exactly four lines that `shasum -a 256 -c` accepts, no `publish-files.txt`; it passes its version check, prints `dry run: not for release` and no `gh release`, `git push` or `--verify-tag` text; a second dry run succeeds (cleared dir); a planted file in `hack/release/out/v0.0.0-dryrun/` stays untouched; `tar -tzf` lists the three files. Commit the script and `.gitignore` first; the real-build checks then run on that candidate commit in a scratch clone under `.agents-tmp/p4b/release-clone/` (`git clone` of the worktree, never the worktree itself): without the env var on an untagged HEAD → refused, nothing written; with a local tag `v0.0.0-test` on HEAD and an untracked file → refused (dirty); clean and tagged → `publish-files.txt` with exactly five lines, and the printed steps name those five paths with `--verify-tag` and no glob; the clone's tag is never pushed; a branch named `v0.0.0-test` without a tag → refused. A failed check is fixed in a new commit and the clone checks rerun. Then `make -C … test`.
- [ ] Commit `build: release script for tagged builds` (before the clone checks, see above).

### Task 4b.2.4: Live check and goldens check
**Files:** modify `hack/live-check.sh`, `testdata/dumps/README.md` (result lines only).
- [ ] `expect_matched`: strings taken from the reviewed `collide-meta-orders` golden (route line, `(extAuth, inferred)`, `(rateLimit.local, inferred)`, `(transformation, inferred)`, `policies:` line).
- [ ] `expect_ambiguous`: unchanged lines; also fails on `chosen from known`.
- [ ] New `expect_json <file> <matched|ambiguous>`: `schemaVersion == "kgwctl.kgateway.dev/v1alpha2"`, `kind == "ProxyRoutes"`; matched: one `selection.routes` entry with `chosenBy == "select"` and the orders route's running filters equal `$ORDERS_FILTERS`; ambiguous: `routes == []`, `ambiguous | length == 2`.
- [ ] `routes`, metadata on: after the text run, run live with `-o json` to `meta-live.json` and call `expect_json meta-live.json matched`; a failure sets `failed=1`. It checks `selection.strategy == "strict"`, one `selection.routes` entry with `chosenBy == "select"`, and the orders route's running filters in order equal `$ORDERS_FILTERS`.
- [ ] `routes`, metadata off: run live with `-o json` to `off-live.json` and call `expect_json off-live.json ambiguous`; a failure sets `failed=1`. It checks `routes == []`, two `ambiguous` entries, and no route in `listeners` (the unmatched case).
- [ ] `routes`, metadata off: also run live with `--select known` (both HTTPRoutes exist: `expect_ambiguous` holds, `selection.strategy == "known"`, R20), and `--file … --select known` (exit 2, `--select known` on stderr, R22).
- [ ] `same_view` still compares live and file text below the header.
- [ ] `goldens_check`: also run `kgwctl proxy routes --file <dump> -o json` on each recorded dump: `v1alpha2`, non-empty `listeners`, a route with `filters`. `goldens` (capture) is not re-run.
- [ ] Run on kind `kgwctl-live` (kgateway v2.5.1, context `kind-kgwctl-live`, kubeconfig `hack/live/out/admin.kubeconfig`): `goldens-check`, `routes`, `bundle`. Record the summary lines.
- [ ] Verify: `make -C … test`.
- [ ] Commit `test(live-check): proxy routes on the v1alpha2 schema`.

## Phase close prerequisites (not done by the implementer)
1. Codex phase review and triage (queue Q5).
2. Owner try-out on kind.
3. Release, in this order, after the owner's go-ahead (design DD-22):
   1. Merge the phase branch; tag `v0.1.0` on the merged main commit; run `hack/release.sh v0.1.0` (not a dry run). It writes `hack/release/out/v0.1.0/` and refuses if that directory is not empty.
   2. `git push origin refs/tags/v0.1.0`.
   3. `git ls-remote origin "refs/tags/v0.1.0^{}" refs/tags/v0.1.0`: the commit equals `hack/release/out/v0.1.0/commit.txt`. Stop on a mismatch.
   4. `checksums.txt` names exactly the four archives of `publish-files.txt`, and `shasum -a 256 -c checksums.txt` passes in `hack/release/out/v0.1.0/`.
   5. Clean `GOPATH`/`GOMODCACHE` under `.agents-tmp/`: `go install github.com/DuncanDoyle/kgwctl/cmd/kgwctl@v0.1.0`; `version --client` prints `v0.1.0` (R16). Retry with `GOPROXY=direct` if the proxy has not seen the tag.
   6. `gh release create v0.1.0 --verify-tag --title v0.1.0 --notes-file <notes>` followed by the five paths of `hack/release/out/v0.1.0/publish-files.txt`, as listed, never a glob.
4. Close kgwctl#15 and kgwctl#16; tell xdsview its Phase 9 contract is confirmed by a consumer; update the program file (E15 consumed).
