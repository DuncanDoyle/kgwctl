# Phase 4b — xdsview v0.7.0 migration and first release: Design

Status: r5, 2026-11-27. r5 applies Codex round 3 (`reviews/design-codex-r3.md`, all accepted): R15c keeps xdsview's execution state apart from `configSource`, a dry run prints no publish steps, and a later exit 2 suppresses the `known` stderr line. r4 applies Codex round 2 (`reviews/design-codex-r2.md`, all accepted): isolated release output with an explicit file list, `unresolved` defined as "xdsview could not establish the config", `unusedRouteConfigs` dropped, DD-11 aligned with DD-6, the extra `known` stderr line limited to `--route` with a recoverable list failure. r2 added the owner's selection strategy (`--select strict|known`, D-032). r3 applies the Codex design review (`reviews/design-codex.md`, 10 findings, all accepted): normalised `known` marker, kgwctl-owned warning messages, one warning list, the migration commit carries the envelope bump, `known` cases at the command boundary, ECDS network filters, release order, a Snellius acceptance case, a FilterLink seam test, and the Phase 6 boundary.
Roadmap: Phase 4b. Decisions: D-021, D-022, D-023, D-031, D-032. xdsview: Phase 9 design (E15), xdsview D-022 and D-023.
Base: cd2fbaa (branch `duncandoyle/phase4b`).

## 1. Goal and scope
`proxy routes` moves to the xdsview v0.7.0 contract (program edge E15), the JSON and YAML envelope moves to `kgwctl.kgateway.dev/v1alpha2`, and kgwctl gets its first release, `v0.1.0`. After this phase, the format people script against does not change in the next release (D-031).

Terms used below, from the xdsview v0.7.0 contract:
- **Occurrence ID**: the ID xdsview gives every listener, filter chain, filter slot, virtual host and route in one parsed dump, such as `L0/C1/N0/RC/V2/R5`. It is valid only within that dump.
- **Module**: code that reads a parsed dump and makes **claims** about where each piece came from. xdsview ships one, the kgateway module (`kgateway.New()`).
- **Claim**: one statement by a module, such as "route `L0/…/R5` comes from HTTPRoute `team-a/orders`, rule 0", with a certainty (`unknown`, `ambiguous`, `inferred` or `exact`) and the evidence it rests on.
- **Reading**: one HTTPRoute, rule and match a route can come from. A route name without `route_source` metadata can have several readings.
- **Known**: the HTTPRoutes that exist in the cluster. Known can set aside readings whose HTTPRoute does not exist. That is a **narrowing**.

In scope:
1. `proxy routes` on xdsview v0.7.0: parse, `chain.Effective`, `provenance.Compose`, `kgateway.Select` or `kgateway.Narrow`, `model.Prune`, `render.Text`.
2. A route selection strategy, `--select exact|strict|known` (D-032). `strict`, the default, is today's rule (D-022).
3. The module set as a `pkg/cli` seam for downstream builds (xdsview D-023).
4. kgwctl's own `ProxyRoutes` payload, built from the snapshot, the chains and the claim index.
5. Envelope `v1alpha2` for every kind.
6. Snellius v0.9.0 and `install.Scope.Excluded`.
7. A correct client version for `go install …@v0.1.0` and for release binaries, and a minimal release script.
8. Live check and goldens on the new schema.

### Anti-goals
- No change to capture, RBAC or exit codes, and no flag change beyond `--select`. `proxy dump` and `bundle` capture are unchanged.
- No `--route` in Known, unlike krp (DD-3). `--select known` narrows with the cluster's HTTPRoutes only.
- No claim selected by module name: no use of `Claim.Module` or `kgateway.ModuleName` (xdsview D-023).
- No policy view, no `pkg/policy` (Phase 6, E7), no convergence (E14), and none of the Phase 4 deferred items.
- No release tooling beyond one script (no goreleaser, no CI release job).

## 2. Evidence
- xdsview v0.7.0 Phase 9 design §1 (layers), §5 (claims, `Compose`, `Resolve`), §6 (kgateway module, `Select`, `Narrow`), §7 (render hooks, krp flow) and Compatibility (kgwctl migrates in one step, goldens change). README "Using xdsview as a library".
- `kgateway.Select` (`pkg/adapter/kgateway/select.go`) reads readings with `provenance.Resolve[RouteSource]` over every claim in the index. It does not filter by module name. `Narrow` walks `ix.Claims()`, also without a module filter. Claim IDs are `"<module>/<n>"` (`provenance/compose.go:59`), so they change with the module name.
- Measured with krp v0.7.0 on `testdata/dumps/` (2026-11-27, `.agents-tmp/p4b/`):
  - `collide-meta.json --route kgwctl-proxy/orders`: same filter order and policies as today. New: `port 8080 (inferred)` on listener and vhost rows, `(inferred)` on the GatewayExtension, `(extAuth, inferred)` on each filter link (links are capped at inferred), and an `ignored dump sections: …` line.
  - `policies.json`, full tree: the colliding routes print `ambiguous: HTTPRoute kgwctl-proxy/orders, HTTPRoute proxy/orders-kgwctl` as today. The two bootstrap listeners (`readiness_listener`, `prometheus_listener`) now appear, marked `· static`. The JSON has 31 claims with IDs `kgateway/1` … and no warnings.
- Snellius v0.8.2 → v0.9.0 for the packages kgwctl imports: additive Go API only (`install.Scope.Excluded`, `PolicyInterpreter` fields in `engine`, `extension` and `health`, `health.Basis.Strict`, `health.Collected`). Same Go, client-go and cobra versions. Behaviour changes that reach `check`: a rejected policy raises KGWUI-002 instead of KGWUI-003 (Snellius D-135); DirectResponse status gets findings (D-129); a policy-ancestor KGWUI-002 finding ID now hashes its ancestor (D-132).
- `cmd/kgwctl/main.go` sets `version = "dev"` and `commit = "unknown"`, changed only by `-ldflags`. `go install …@v0.1.0` passes no ldflags, so it prints `dev` today.
- kgwctl's `go.mod` has no `replace` directive, so `go install …@v0.1.0` works.

## 3. Behaviour

### 3.1 Commands and flags
One new flag, `proxy routes --select exact|strict|known` (default `strict`, §4.2). No other flag, argument or exit code changes. `proxy routes` exits `0` when it prints and `2` on any error. New exit-2 causes: `--select known` with `--file`, an unknown `--select` value (both usage errors, before any I/O), and a downstream build that passes two modules with the same name (§4.3).

### 3.2 Requirements (input → output)
Fixtures are the recorded dumps in `testdata/dumps/` and the live fixtures of `hack/live/proxy-fixtures.yaml`. "Colliding routes" are the two Envoy routes whose names read as both `kgwctl-proxy/orders` and `proxy/orders-kgwctl`.

| # | Input | Output |
|---|---|---|
| R1 | `--file policies.json --route kgwctl-proxy/orders` (no metadata) | Exit 0. `no route matches HTTPRoute kgwctl-proxy/orders`, then both colliding routes under `ambiguous routes, not matched:`, each with both readings. JSON: `selection.routes` is `[]`, `selection.ambiguous` has 2 entries with 2 readings each, `listeners` is `[]`. |
| R2 | Live, same fixtures, both HTTPRoutes exist | Same as R1 below the header. Neither route is narrowed. |
| R3 | Live, `--route kgwctl-proxy/orders`, `proxy/orders-kgwctl` deleted while Envoy still serves its route | Both routes listed as ambiguous, each line ending `· one candidate exists in the cluster`. Not matched. JSON: each entry has `narrowed: true`, and the reading for `proxy/orders-kgwctl` has `notInCluster: true`. |
| R4 | `--file collide-meta.json --route kgwctl-proxy/orders` | Exit 0. One route, `HTTPRoute kgwctl-proxy/orders · rule 0 · match 0`, its 4 filters in request order with their policies, and the `policies:` line. JSON: `selection.routes` has 1 entry; the route's `origin.certainty` is `exact`. |
| R5 | `--file collide-meta.json --route kgwctl-proxy/orders --rule 1` | Exit 0, `no route matches HTTPRoute kgwctl-proxy/orders rule 1`. |
| R6 | Live, no `--route`, a route that Known narrowed to one reading | The route row reads `ambiguous: A, B`, with `one candidate exists in the cluster` on the next line. JSON: `origin.narrowed: true`, the set-aside reading `notInCluster: true`. No `selection`. |
| R7 | `--file` with `--gateway ns/gw` | Every non-static listener row ends `· Gateway ns/gw (from the command line)`. JSON: `source.gateway` is set; no listener has a `gateway` attribution. |
| R8 | A route name with a `httproute-orders-kgwctl-proxy-` marker and no reading | Listed under `unresolved routes, not matched:` with `origin unresolved`. Never matched. JSON: in `selection.unresolved`. |
| R9 | A warming or rejected listener under `--route` | The listener stays, with its state and last error, even with no matched route. |
| R10 | A parser warning whose subject `--route` pruned; an ECDS warning on a downstream slot of a retained HCM that has no routes after pruning; one slot warning in an HCM with two routes | JSON: each in `warnings` once, with its `subject`. Text: each printed once, in the warning block at the end (DD-12). None inline. |
| R11 | A downstream build: `cli.WithProvenanceModules(m)`, where `m` wraps `kgateway.New()` under another name and adds no claim | Text and JSON byte-equal to the default build for every golden case. |
| R12 | As R11, but `m` adds one `PolicyRecord` on a route | That record is in the route's `policies` and on the `policies:` line. Selection unchanged. |
| R12a | As R11, on a dump where the kgateway module records a TrafficPolicy field it cannot link (`merge.*` field `exampleField`) and the route has per-filter config for slot `example/filter`. `m` adds a `FilterLink` from that inner record (by its `Ref`) to the slot | Through `pkg/cli`: the filter's JSON `policies` has the record at level `route`, certainty `inferred`; its text row ends `· TrafficPolicy ns/p (exampleField, inferred)`. The default build shows the record only on the `policies:` line. |
| R12b | As R11, but `m` adds an invalid claim (no evidence) on a route; run once with `m` named `example` and once named `other` | `claim-dropped` in `warnings` with kgwctl's message (DD-10). Text and JSON are byte-equal between the two names; no claim ID and no module name in either. |
| R13 | `WithProvenanceModules(a, b)` with `a.Name() == b.Name()` | `proxy routes` exits 2: `compose provenance modules: …`. Other commands work. |
| R14 | `-o json` or `-o yaml` of `describe`, `version`, `check`, `proxy routes`; a bundle's `manifest.json` and `check.json` | `schemaVersion: kgwctl.kgateway.dev/v1alpha2`. Only `ProxyRoutes` changes shape. The bundle's `cluster/version.json` has no envelope and stays unchanged. |
| R15 | `bundle` with a discovery selector that excludes `team-b` | The manifest records `team-b` under `unwatched`, as today. Scope `unknown` records no names. |
| R15a | A TrafficPolicy whose ancestor status at the current generation is `Accepted=False/Invalid` and `Attached=False/Pending` | `check` reports KGWUI-002 for it, not KGWUI-003. The bundle's `check.json` has the same finding. |
| R15b | A network filter whose config comes from ECDS that xdsview could not establish, in a chain without an HCM | JSON: the network filter has `configSource: "unresolved"`; the chain has no `hcm`. Text: `(ECDS, unresolved)` and `HTTP filters unknown: …`, as xdsview renders it. No raw config. |
| R15c | HTTP filters with `config_discovery` whose matching ECDS entry is present in the dump but ambiguous (two entries match, or several sources subscribe to its name) | Two filters on the same ambiguous config. Both have `configSource: "unresolved"`, and an `ecds-ambiguous` warning is in `warnings`. Execution comes from xdsview unchanged and does not follow from `configSource`: the filter the HCM leaves enabled has chain entries `runs: "unknown"`, `state: "unresolved"`; the filter the HCM disables and no route enables has `runs: "no"`. The entry's presence in the dump does not make it resolved. |
| R16 | `go install github.com/DuncanDoyle/kgwctl/cmd/kgwctl@v0.1.0`, then `kgwctl version --client` | `Client:  kgwctl v0.1.0 (unknown, go1.x)`. |
| R17 | A binary from `hack/release.sh v0.1.0` | `Client:  kgwctl v0.1.0 (<commit sha>, go1.x)`. |
| R18 | `go build ./cmd/kgwctl` in a checkout | The version Go stamps from VCS, such as `v0.1.1-0.20261201…+dirty`, and the VCS revision; `dev` and `unknown` only when Go stamps nothing. |
| R19 | As R3, with `--select known` | Both colliding routes keep one reading, `kgwctl-proxy/orders`, which matches, so both are matched. Each row reads `HTTPRoute kgwctl-proxy/orders · rule 0 · match 0 (chosen from known HTTPRoutes)`. Nothing is listed as ambiguous. One of the two is the deleted `proxy/orders-kgwctl` route that Envoy still serves: the case `strict` guards against. JSON: both in `selection.routes` with `chosenBy: "known"`, `selection.strategy: "known"`, and `origin.chosenBy: "known"` on both routes. |
| R20 | As R2 (both HTTPRoutes exist), with `--select known` | Same as R2: Known narrows nothing, so nothing is chosen. `selection.strategy: "known"`. |
| R21 | Live (fake dialer serving `collide-meta.json`), `--route kgwctl-proxy/orders`, `--select strict` and `--select known` | Text stdout byte-equal under both; stderr empty under both. JSON equal except `selection.strategy`. The route's `chosenBy` is `select` under both. |
| R22 | `--file … --select known`, with or without `--route` | Exit 2 before reading the file: `--select known needs the cluster's HTTPRoutes; it does not work with --file`. |
| R23 | Live, no `--route`, `--select strict` vs `--select known`, HTTPRoute list allowed, then denied | Under each listing outcome, stdout (text and JSON) and stderr are byte-equal between the two strategies. Allowed: stderr empty. Denied: stderr is the one existing line `Warning: could not list HTTPRoutes: …`. Exit 0. |
| R24a | Live, `--route kgwctl-proxy/orders --select known`, HTTPRoute list denied, and the fake dialer serves a malformed dump | Exit 2. stdout empty. stderr: `Warning: could not list HTTPRoutes: …` and the parse error, nothing else; no `--select known` line. |
| R24 | Live, `--route kgwctl-proxy/orders`, HTTPRoute list denied | Exit 0 under both. Text stdout byte-equal. JSON equal except `selection.strategy`. stderr under `strict`: `Warning: could not list HTTPRoutes: …`. stderr under `known`: that line, then `Warning: --select known: no route can be chosen from known HTTPRoutes`. |
| R25 | R1, R3 and R19 with a wrapping module (R11) | Byte-equal to the default module under each strategy. |
| R26 | As R19, where the one kept reading comes from two claims that differ only in `IndexCertainty`, `Incomplete` or `Exhaustive` | The route is chosen. Its text row shows the one reading with `(chosen from known HTTPRoutes)`, never `ambiguous:`. JSON `chosenBy: "known"`. Same with a wrapping module. |
| R27 | `--file collide-meta.json --route kgwctl-proxy/orders --select exact`; live the same under `exact`, `strict` and `known` | Exit 0. Same text as `strict`: the exact route is matched. JSON equal to `strict` except `selection.strategy: "exact"`; `chosenBy: "select"`, `inferred: []`. |
| R28 | `--file policies.json --route kgwctl-proxy/orders --select exact`; live the same under all three | Exit 0. Nothing is exact, so nothing matches: same text and JSON as `strict` (R1, R3) except `selection.strategy`. `known` still chooses as in R19. |
| R29 | A route whose one reading is `inferred` (name only, such as `shop/orders` in `levels.json`), `--route` to it, `--select exact`; and a route with exact metadata but rule and match from the name, with `--rule 0` | Not matched: `no route matches …`, then `inferred routes, not matched:` and the route with its reading (`(inferred)`, or `(partly from name)`). JSON: in `selection.inferred` with its readings; `routes: []`. `strict` matches both; `exact` without `--rule` matches the second. |

### 3.3 What changes for users
| Area | Change |
|---|---|
| `proxy routes` text | Listener and vhost rows show their port or section, with `(inferred)` when read from a name. The Gateway kgwctl was given is labelled `(from the command line)`. Name-derived readings, extension labels and filter links say `(inferred)`; exact ones print bare. Static bootstrap listeners appear in a full tree, marked `· static`. All warnings print once, in one block at the end (DD-12); none inline. One `ignored dump sections:` line ends the tree. Policies above route level print as `inherited policies:`. Ambiguous and unresolved listings keep their form. With `--select known`, a route chosen from known HTTPRoutes is shown and marked `(chosen from known HTTPRoutes)`. |
| `proxy routes` JSON/YAML | New `ProxyRoutes` shape (§5). Raw `typedConfig`, `typedPerFilterConfig` and `metadata` are gone. |
| All JSON/YAML | `schemaVersion` is `kgwctl.kgateway.dev/v1alpha2`. Other kinds keep their fields. |
| `check` | Snellius v0.9.0 findings: KGWUI-002 for rejected policies (was KGWUI-003), DirectResponse status findings, and new IDs for policy-ancestor KGWUI-002 findings. |
| `bundle` | `manifest.json` and `check.json` move to `v1alpha2`. Content unchanged. `cluster/version.json` has no envelope and is unchanged. |
| `version` | The client version is right for `go install …@<tag>` and release binaries. |
| Flags | New `proxy routes --select strict\|known`, default `strict`. |
| Exit codes, RBAC | Unchanged; `--select known` with `--file` is a usage error (exit 2). |
| Install | `v0.1.0` tag; release binaries for darwin and linux, amd64 and arm64. |

## 4. `proxy routes` on xdsview v0.7.0

### 4.1 Flow
```
body ─► parser.Parse(Capture) ─► chain.Effective ─────────────────────────────┐
                            └─► provenance.Compose(snap, modules...) ─► index ─┤
   --route:          kgateway.Select(ix, snap, Query{Route, RuleIndex, Known}) ─► model.Prune(Routes)
   live, no --route: kgateway.Narrow(ix, Known)
   --file, no --route: nothing                                                 ─► render.Text / payload
```

**DD-1. One pipeline, computed on the whole dump.** Chains, index, selection and narrowings are computed on the full snapshot; pruning happens last and only under `--route`. IDs survive pruning, so every result joins on the pruned snapshot (xdsview §2).

**DD-2. Capture.** Live, kgwctl fetches `GET /config_dump` with no filter, so it parses with `model.CaptureComplete`. With `--file` it passes nothing: unverified. A complete capture lets xdsview report a missing route config (`rds-missing`) and an ECDS default; a file never gets those verdicts. (inferred: the owner did not decide this, but xdsview §2 defines it for this case.)

**DD-3. Known.** Live, Known is the cluster's HTTPRoutes, listed as today (all namespaces, paged, only HTTPRoutes, by `--request-timeout`). With `--file`, Known is empty. `--route` is never added to Known, unlike krp.

### 4.2 Selection strategy (D-032)
**DD-4. `--select exact|strict|known` picks the rule; `strict` is the default (D-032).** Each value adds one kind of evidence to the one before it.
- `exact` (owner, 2026-11-27): a match is a route in `Select(...).Routes` whose resolved reading (`provenance.Resolve`) has certainty `exact`. With `--rule`, the reading's `IndexCertainty` must also be exact; unset means the reading's own certainty applies. kgwctl reads certainty only, never `Claim.Module` or evidence kinds. With kgateway, exact readings come from route metadata. A route of `Select(...).Routes` that fails this is not matched: it is listed under `inferred routes, not matched:` with its reading as the tree prints it, and in `selection.inferred` with its key fields and `readings`. Ambiguous and unresolved routes are listed as under `strict`. Selected routes are `chosenBy: "select"`. `exact` needs no Known, so it works with `--file`. (inferred: the list's text and shape)
- `strict`: a match is a route in `Select(...).Routes`, and nothing else. A route that `Select` leaves ambiguous is never selected, also when Known narrowed it to one matching reading. Reason for the default: Envoy can still serve the route of a deleted HTTPRoute whose name collides with a live one.
- `known`: the matches are `Select(...).Routes`, plus each route in `Select(...).Narrowed` whose `Kept` claims name exactly one reading that matches the query. Readings are compared as `provenance.Resolve` merges them: HTTPRoute, rule index, rule name and match index. This is the rule of xdsview D-022 choice 1, as krp's `newSelection` and `oneReading` implement it (`cmd/krp/output.go`). Such a route leaves the ambiguous list and is marked `chosenBy: "known"`. All other routes are as in `strict`.
- The rule lives in kgwctl (`internal/proxyview`), over `kgateway.Select`'s result. `Select` is not changed and kgwctl does not import krp.
- `--select known` with `--file` is a usage error: Known is empty with `--file` (DD-3), so `known` would give exactly `strict` and only suggest a choice that was never made. Using `--route` as Known, as krp does, is the only way to give it effect there, and DD-3 rules that out. I found no sound reason to allow it.
- Without `--route`, the strategy has no effect. Checked against the flow: the strategy only filters `Select`'s result, and without `--route` there is no `Select` call. The tree, the narrowed note (DD-6) and the JSON are the same for both values. `--select known` without `--route` is accepted live, and still a usage error with `--file`. (inferred: the owner's expectation, confirmed against the design)
- Live, with `--route`, when the HTTPRoute list fails in a way the command recovers from (denied, or any error other than the deadline, which still exits 2), Known is empty and `known` chooses nothing. The line `Warning: --select known: no route can be chosen from known HTTPRoutes` belongs to that recoverable branch only, and kgwctl writes it only after stdout was written successfully. Any later error that exits 2 (a parse error, a dump that is not a config dump, a compose error, a write error) suppresses it: stderr then holds only the list warning and the error. Without `--route`, or under `strict`, there is no extra line (R23, R24, R24a). (inferred)
- Known differs from krp's. krp uses `--route` as Known (`cmd/krp/main.go`, live and file); kgwctl uses the cluster's HTTPRoutes. So for the colliding routes with both HTTPRoutes present (R20), krp picks `kgwctl-proxy/orders` and kgwctl with `known` picks nothing. The rule is the same; the input is not.

**DD-5. Ambiguous and unresolved routes are listed as today.**
- Text, under `--route`: a `no route matches …` line when nothing matched, then `ambiguous routes, not matched:` (or `unresolved …`, or both) with one line per route: its name, then `ambiguous:` and every reading's HTTPRoute (kept and set aside), or `origin unresolved`. A route whose narrowing kept exactly one reading adds `· one candidate exists in the cluster`.
- The text lists a route of a shared route config once, by its source path (xdsview `SourcePath`), and adds `· route config <name>` when two listed routes share a name. JSON lists every use, by occurrence ID (§5.6). (inferred)
- A narrowing that keeps two or more readings adds no note in text; JSON marks the set-aside readings. (inferred)

**DD-6. Narrowed routes in the tree.**
- A route chosen by `--select known` is marked by `provenance.Labels` itself, with a normalised narrowing. Labels' narrowed path compares whole `RouteSource` values, so two kept claims that differ only in `IndexCertainty`, `Incomplete` or `Exhaustive` would print `ambiguous:` for a route DD-4 counts as one reading. kgwctl therefore passes, for each chosen route and only those, a `provenance.Narrowing{Route, Kept: []Claim{c}}` where `c` is the first kept claim with its `RouteSource` merged the way `provenance.Resolve` merges readings: the highest `IndexCertainty`, `Incomplete` if any is, `Exhaustive` only if all are. Labels then prints the one reading with `(chosen from known HTTPRoutes)`, the text krp prints. The normalised claim is built for the label hook only; it never enters the index or the payload. The same merged reading feeds the JSON, so text and JSON cannot disagree (R26).
- Every other narrowed route is passed to Labels as not narrowed, so its row shows `ambiguous: …` with every reading. Live without `--route`, kgwctl's own `Notes` hook adds `one candidate exists in the cluster` under a route whose narrowing kept one reading. This keeps today's text, under both strategies (DD-4). krp marks those routes `(chosen from known HTTPRoutes)` in its full tree; kgwctl does not, because nothing was chosen. (inferred: the wording was decided; the mechanism is ours.)

**DD-7. Incomplete readings follow `Select`.** A reading the module marks `Incomplete` (a route name outside kgateway's format) can still be the only reading, and `Select` then selects it. kgwctl follows `Select` and shows `incomplete: true` on the reading in JSON. kgateway route names always carry the `<vhost>-route-<N>-` prefix, so this is rare. Open item 1 asks the owner. (inferred)

### 4.3 Downstream seam (xdsview D-023)
**DD-8. The module set is a `pkg/cli` option that replaces the default.** `cli.WithProvenanceModules(m ...provenance.Module)` sets the modules `proxy routes` passes to `Compose`. The default is `[]provenance.Module{kgateway.New()}`. A downstream build wraps the kgateway module in its own module, under its own name, and passes only the wrapper. A call with no modules keeps the default. (inferred: owner approved the seam; the name and the empty-call rule are ours.) Duplicate names make `Compose` fail; `proxy routes` exits 2 (R13). Other commands do not compose and are not affected.

**DD-9. kgwctl reads claims only through `Resolve`, `ResolveMirror`, `Select`, `Narrow`, the index lookups and the assertion types.** It never reads `Claim.Module`, never compares with `kgateway.ModuleName`, and never parses a claim ID. A test parses kgwctl's non-test Go files and fails on either (§9).

**DD-10. The payload holds no claim ID and no module name.** Claim IDs mean something only within one `Compose` result, and they embed the module name. Leaving both out keeps the payload of a wrapping module equal to the default one when it adds no claim (R11). (inferred: the owner ruled out claim IDs as stable IDs; leaving out the module name is ours.) A downstream build whose module adds claims has different goldens from kgwctl's; its own tests own them.

`Compose` writes the claim ID, and so the module name, into the message of its `claim-dropped` and `link-dropped` warnings. kgwctl never copies those messages. It keeps the code and the subject and writes its own message, keyed by code and subject:
- `claim-dropped`: `a provenance claim about <subject> broke a composition rule and was dropped`.
- `link-dropped`: `a policy-to-filter link on <subject> could not be resolved and was dropped`.

`<subject>` is kgwctl's description of the occurrence from the full snapshot (`listener <name>`, `route <name> in virtual host <name>`, `filter <name>`, …; `occurrence <id>` when the lookup fails). Two drops of one code on one subject give one warning. A dropped claim under two wrapper names gives byte-equal output (R12b).

### 4.4 Text
**DD-11. Hooks.** `render.Text` gets:
- `Labels`: `provenance.Labels(ix, chosen)`, where `chosen` holds the normalised narrowings of the routes `--select known` chose and nothing else (DD-6), plus kgwctl's context label on non-static listener rows: `Gateway <ns>/<name> (from the command line)`. The label is added only when no `gatewaySource` claim resolves at inferred or better, so a downstream module that claims the Gateway is not repeated. The same words serve live and `--file`, so the live check can compare the two views (§8). (inferred)
- `Notes`: the narrowed note of DD-6.
- `Trailer`: `provenance.Notes(ix)` (`policies:` and `inherited policies:`).
- `ResponseOrder` from `--response`.

**DD-12. One complete warning list, printed once.** kgwctl builds one list: every parser warning of the full snapshot, then every index warning with kgwctl's message (DD-10), deduplicated on (subject, code, message), in that order. The payload's `warnings` is that list. The text prints the same list once, after the tree and the selection lists, one `warning: <message>` line per entry, with ` (<subject description>)` added for a subject warning.

The renderer prints no warning. `render.Text` takes subject warnings only from `snap.Warnings` (`render.go` `Text`), so kgwctl passes it a shallow copy of the pruned snapshot with `Warnings` set to nil. Reason: what the renderer prints inline cannot be tracked from outside. A retained HCM with no routes after pruning keeps its slot warnings but prints no slot row, and the renderer resets slot tracking per route, so one slot warning can print under two routes. One list at the end cannot lose or repeat a warning. The cost is that warnings move away from their rows; the subject description says where they belong. (inferred: the owner asked for the simplest safe contract)

## 5. `ProxyRoutes` payload
**DD-13. kgwctl defines its own types.** The payload is built from the snapshot, the chains and the index, and embeds no xdsview type. It keeps the tree shape (listener, filter chain, virtual host, route) because listener state and filter chains belong to it, and it attaches everything about a route to the route: readings, runtime filter chain, filter attribution, policies, backend. A test lists every exported field of the xdsview model types kgwctl reads and fails when one is neither mapped nor listed as left out, so an xdsview bump cannot drop data in silence. (inferred: the test is ours.)

**DD-14. Raw config stays out.** `typedConfig`, `typedPerFilterConfig` and `metadata` are not in the payload. Reasons: they are the dump's own config, which `proxy dump` already gives unchanged; they are the main place where credentials Envoy does not redact can sit (Phase 4 design §2); xdsview says raw metadata is not an attribution input; and leaving them out makes the payload small enough to read. What they decide is in the payload in typed form: each filter's `state`, `level`, `perFilterType` and `configSource`, and the policy records decoded from `merge.*` metadata. Header operations a route adds stay, because the text shows them too. The README keeps the note to check output before sharing it.

**DD-15. Occurrence IDs are exposed as `id`.** Selection lists and filter slots refer to them, and a route of a shared route config appears once per use. The docs say: an `id` is valid only within one document; the same dump read by the same xdsview version gives the same IDs; a listener or route added beside another can shift them; never store them across dumps. `sourcePath` tells the uses of one shared route config apart from different routes.

**DD-16. Policy records are per level, never merged.** Records sit on the occurrence they were recorded at: `listener.policies` (level `listener`), `hcm.routeConfig.policies` (`routeConfig`), `virtualHost.policies` (`virtualHost`), `route.policies` (`route`). Each record names its level. kgwctl does not copy them down, combine them or say which level wins (D-024, P-008). A filter's `policies` lists the records a filter link places on that filter for that route, each with its own level.

**DD-17. Alternatives are shown as resolutions.** Where xdsview has competing claims (listener port or section, Gateway, hostname, extension, backend), the payload shows `{certainty, candidates[]}`: the distinct candidates at the highest certainty, as `provenance.Resolve` gives them. `ambiguous` with two or more candidates means the claims disagree.

### 5.1 `ProxyRoutes` (`item`)
| Field | Type | Meaning |
|---|---|---|
| `source` | ProxySource | Unchanged from v1alpha1: `gateway`, `pod`, `capturedAt` live; `file` and optional `gateway` with `--file`. |
| `capture` | string | `complete` (live) or `unverified` (`--file`). |
| `listeners` | Listener[] | Always present; `[]` when `--route` left nothing. Pruned to the selected routes under `--route`; non-active listeners stay. |
| `sections` | Section[] | Each config dump section with `type` and `status` (`parsed`, `partial`, `ignored`), in dump order. |
| `selection` | Selection | With `--route` only. Describes the whole dump. |
| `warnings` | Warning[] | The one warning list of DD-12: every parser and index warning of the whole dump, once, with kgwctl's message for `claim-dropped` and `link-dropped`. Absent when none. |

### 5.2 Listener, filter chain, HCM
| Type | Fields |
|---|---|
| Listener | `id`, `name`, `address`, `static`, `state` {`status`, `updating`, `draining`, `lastUpdated`, `error` {`details`, `lastAttempt`}}, `port` (resolution of {`port`, `section`}), `gateway` (resolution of Ref; absent without a claim), `policies`, `filterChains` |
| FilterChain | `id`, `name`, `default`, `tls` {`sniHosts`}, `networkFilters` [{`id`, `name`, `type`, `configSource`}], `hcm` |
| HCM | `id`, `routeConfig` {`name`, `source` (the route config source ID, such as `RD3`, when joined), `state` (`found`, `missing`, `ambiguous`, `unknown`), `kind` (`dynamic`, `static`, `inline`), `scopedRoutes`, `policies`}, `httpFilters` [HTTPFilter], `upstreamFilters` [HTTPFilter], `defaultChain` (Filter[], only when no route config joined), `virtualHosts` |
| HTTPFilter | `slot`, `name`, `type`, `disabled`, `configSource`, `extension` (resolution of {`ref`, `filterKind`, `helper`}) |

`configSource` on a network filter or HTTP filter: absent for inline config; `ecds` or `ecdsDefault` when xdsview resolved the ECDS config; `unresolved` when the filter uses discovery and xdsview could not establish the config for this occurrence. That covers a config missing from the dump, but also one present but ambiguous (category, subscription or two matching entries), and every network filter with discovery, which xdsview v0.7.0 never resolves (R15b, R15c). An unresolved network filter may itself be an HCM, so its chain's HTTP filters are unknown. This is a typed indicator only; the discovery details and the config stay out.

### 5.3 Virtual host and route
| Type | Fields |
|---|---|
| VirtualHost | `id`, `sourcePath`, `name`, `domains`, `host` (resolution of hostname), `port` (resolution, when claimed), `policies`, `routes` |
| Route | `id`, `sourcePath`, `name`, `match`, `origin`, `filters`, `chainConditional`, `policies`, `action`, `backend`, `weightedBackends`, `mirrors`, `rewrite`, `requestHeadersToAdd`, `responseHeadersToAdd`, `responseHeadersToRemove` |
| Match | `prefix`, `pathSeparatedPrefix`, `path`, `regex`, `connect`, `caseSensitive`, `headers` [{`name`, `value`, `kind`, `ignoreCase`, `invert`, `missingAsEmpty`}], `queryParams` [{`name`, `value`, `kind`, `ignoreCase`}], `unparsed` |
| Origin | `certainty` (the route's resolved certainty; a chosen route stays `ambiguous`, since Known never raises certainty), `complete` (the readings name every possible source), `narrowed` (Known set a reading aside), `chosenBy` (`known` for a route `--select known` chose; absent otherwise), `readings` [Reading] |
| Reading | `route` (Ref), `ruleIndex`, `ruleName`, `matchIndex` (`-1` unknown), `certainty`, `indexCertainty` (only when it differs), `incomplete`, `notInCluster` |
| Action | `kind` (`route`, `redirect`, `directResponse`, `filterAction`, `nonForwarding`), `clusterHeader`, `clusterSpecifierPlugin`, `inlineClusterSpecifierPlugin` {`name`, `type`}, `redirect` {…}, `directResponse` {`status`, `bodySource`, `bodySize`, `bodyFormat`}, `filterActionType` |
| Backend | `cluster`, `certainty`, `candidates` [{`backend` (Ref), `port`}] |
| WeightedBackend | `id`, `name`, `weight`, `overridesFilters` (has its own per-filter config), `backend` |
| Mirror | `cluster`, `backend` |

`readings` lists every `routeSource` reading at the highest certainty, set-aside ones included, so a narrowed route still shows what its name could mean.

### 5.4 Runtime filter chain
`filters` is the route's effective chain from `chain.Effective`, in HCM order, every HCM filter included. Request order is the entries whose `runs` and `request` are not `no`; response order is the reverse of those whose `runs` and `response` are not `no`. `chainConditional` says a weighted backend's own per-filter config may change the chain.

| Field | Meaning |
|---|---|
| `slot` | The HCM filter's `id`. |
| `name`, `type` | The filter's name and `@type`. |
| `runs` | `yes`, `no` or `unknown`. |
| `state` | `on`, `off`, `enabled`, `disabled`, `unknown` or `unresolved`. |
| `level` | Where the deciding per-filter config sits: `route`, `virtualHost`, `routeConfig`; absent for the HCM default. |
| `perFilterType` | `@type` of the deciding per-filter config. |
| `request`, `response` | `yes`, `no` or `unknown`. |
| `wrapper`, `conditional`, `configSource` | Composite or matcher wrapper; whether a matcher decides; where the config came from. |
| `extension` | Resolution of what defines the filter. |
| `policies` | Policy records linked to this filter for this route: {`policy`, `field`, `level`, `certainty`}. The certainty is the lower of the record's and the link's. |

### 5.5 Policy
`{policy: Ref, field: string, level: "listener"|"routeConfig"|"virtualHost"|"route", certainty}`.

### 5.6 Selection (`--route` only)
| Field | Meaning |
|---|---|
| `query` | `route` (Ref) and `ruleIndex` when `--rule` is set. |
| `strategy` | `exact`, `strict` or `known`: the `--select` value used. Always present. |
| `routes` | The matched routes: `id`, `routeConfig`, `virtualHost`, `name`, `chosenBy` (`select` for a route in `Select(...).Routes`, `known` for one `--select known` added). Every one is in `listeners`. |
| `inferred` | Under `exact`, routes of `Select(...).Routes` whose reading, or with `--rule` whose indices, are below exact: the key fields plus `readings`. Not in `listeners`. `[]` otherwise. |
| `ambiguous` | Routes with several readings, at least one matching, or one left only by Known and not chosen: the same key fields plus `readings` and `narrowed`. Not in `listeners`. |
| `unresolved` | Routes with no reading whose name carries the HTTPRoute's marker: the key fields. Not in `listeners`. |

There is no separate `narrowed` list. `unresolved` routes have no readings, so a narrowed route is either an `ambiguous` entry with `narrowed: true` or, under `known`, a `routes` entry with `chosenBy: "known"`. The route's `origin` in `listeners` carries its readings with `notInCluster`. (inferred)

### 5.7 Warning and Section
Warning: `subject` (an occurrence ID; absent for a dump-wide warning), `code` (xdsview's code, such as `section-skipped`, `rds-missing`, `claim-dropped`), `message`. Section: `type`, `status`.

### 5.8 Phase 6 boundary
**DD-23. The payload is a presentation projection, not the join model.** Phase 6 joins runtime policy evidence with declared policies (roadmap Phase 6). Two HCMs can use one route config source, and two sources can share a name, so the payload's per-use `routeConfig.policies` cannot tell one record seen through two uses from two records. Phase 6 joins on `RouteConfigUse.Source` with the full snapshot and the claim index, inside kgwctl, never on this JSON. The payload exposes `routeConfig.source` so a reader can see which uses share a source, but it promises no join semantics. Sources that no HCM joined are not in the payload: a static entry that an inline config reports (`RouteConfigSource.Reported`) would look unused there although it is a used configuration, and nothing in this phase needs the list. (inferred: the owner accepted the finding; the field names are ours)

### 5.9 Conventions kept
Refs are objects; an empty `group` is omitted in `proxy routes` refs. Optional fields are omitted when empty. Lists documented as always present print `[]`. Certainty is a name (`unknown`, `ambiguous`, `inferred`, `exact`).

## 6. Envelope `v1alpha2`
**DD-18. Every kind moves to `kgwctl.kgateway.dev/v1alpha2` at once.** One constant (`print.SchemaVersion`) serves `Describe`, `Version`, `Check`, `ProxyRoutes` and `BundleManifest`, and the envelope versions the whole output contract. docs/output.md says that only `ProxyRoutes` changed shape, so readers of the other kinds only change the version they accept. `get -o json|yaml` stays a plain Kubernetes List, outside the envelope. The bump lands in the same commit as the `ProxyRoutes` migration, so no commit carries the new shape under `v1alpha1`.

## 7. Snellius v0.9.0
**DD-19. `install.Scope.Excluded` replaces the local loop** in `discoveryScope` (`internal/app/bundle_scope.go`). Behaviour is unchanged: the names are computed only for mode `selectors` with readable Namespaces; `known == false` keeps mode `unknown` with no names and the existing reason. The comment "local loop until Snellius ships Scope.Excluded" goes.

**DD-20. Check behaviour follows Snellius.** The v0.9.0 finding changes (§2) reach `check` and the bundle's `check.json`. kgwctl does not mask them; check goldens are reviewed and the changes are named in the release notes. The finding-ID change for policy-ancestor KGWUI-002 is a Snellius decision (D-132), not a kgwctl schema change. One observable case proves the bump: a TrafficPolicy with current `Accepted=False/Invalid` and `Attached=False/Pending` gives KGWUI-002 through `check` and the bundle's `check.json` (R15a). The golden review also accounts for KGWUI-016, which v0.9.0 no longer raises from a stale `TargetNotFound` condition (`health/targets.go`, D-135).

## 8. Release `v0.1.0`
**DD-21. Client version.** `cmd/kgwctl` resolves the build info in this order: ldflags (`main.version`, `main.commit`) when set; else `debug.ReadBuildInfo`: `Main.Version` when it is not empty or `(devel)`, and the `vcs.revision` setting as the commit; else `dev` and `unknown`. Downstream builds keep setting `cli.WithBuildInfo` themselves.

**DD-22. One release script, run by the controller.** `hack/release.sh <tag>`:
1. A real build refuses unless the tree is clean and HEAD is exactly the commit `<tag>` points to. Only a dry run (`RELEASE_DRY_RUN=1`) skips these checks.
2. Builds `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=<tag> -X main.commit=<sha>"` for linux and darwin, amd64 and arm64.
3. Writes into its own directory, `hack/release/out/<tag>/` (git-ignored). It refuses when that directory exists and is not empty, so no earlier build can mix in. A dry run (`RELEASE_DRY_RUN=1`) writes to `hack/release/out/dry-run/<tag>/` instead, which it clears first, and never touches a tag directory.
4. Packs each binary with `LICENSE` and `README.md` as `kgwctl_<tag>_<os>_<arch>.tar.gz`. It writes `checksums.txt` over exactly those four archives, named one by one, never by glob; `commit.txt` (the full build commit); and `publish-files.txt`, the four archive paths plus `checksums.txt`, one per line.
5. Runs the host binary's `version --client` and checks it prints `<tag>`.
6. A real build prints the publish steps below, with the file list read from its own `publish-files.txt`. A dry run writes no `publish-files.txt` and prints no publish steps, only `dry run: not for release`. The checksum check and the upload in the close sequence read the real build's directory and manifest only. The script never pushes and never publishes.

**Close sequence (controller, after the owner's go-ahead), in this order:**
1. Tag `<tag>` on the merged main commit and run `hack/release.sh <tag>` (not a dry run). It writes `hack/release/out/<tag>/` and refuses if that directory is not empty.
2. Push exactly that tag: `git push origin refs/tags/<tag>`.
3. Check that the remote tag resolves to the build commit: `git ls-remote origin "refs/tags/<tag>^{}" "refs/tags/<tag>"` (the dereferenced line for an annotated tag) equals `hack/release/out/<tag>/commit.txt`. Stop on a mismatch.
4. Check that `hack/release/out/<tag>/checksums.txt` lists exactly the four archive names of `publish-files.txt`, and that `shasum -a 256 -c` passes on them.
5. In a clean `GOPATH` and `GOMODCACHE` under `.agents-tmp/`, run `go install github.com/DuncanDoyle/kgwctl/cmd/kgwctl@<tag>` and check `version --client` prints `<tag>` (R16). If the module proxy has not seen the tag yet, retry with `GOPROXY=direct`.
6. `gh release create <tag> --verify-tag --title <tag> --notes-file <file>` followed by the five paths in `publish-files.txt`, as listed, never a glob. `--verify-tag` makes `gh` fail instead of creating a missing tag from the default branch.

No new Go dependency, no CI release job, no signing. (inferred: owner asked for the smallest thing.)

## 9. Testing
- **Unit, `internal/proxyview`.** Every Phase 3 case carried over to the new API: exact selection, ambiguous without metadata, a single survivor that Known left is not a match, warming listener under narrowing, parser warning, policy chain, response order with an unknown side, rule selection, parse error, narrowed note in the full tree, ambiguous per route config, no listeners after narrowing, unresolved never matches, unresolved ignores the vhost prefix, ambiguous judged on all readings.
- **Strategy, command boundary.** One capture (`policies.json`, Known with and without `proxy/orders-kgwctl`) under `strict` and `known`: R2/R20 and R3/R19, as text and JSON. Live fake-dialer `collide-meta.json` under both (R21). Live with and without `--route`, listing allowed and denied, both strategies: stdout and stderr exactly as R23 and R24. `--file --select known` and `--select bogus`: exit 2 before any read (R22). The normalised marker with kept claims that differ only in `IndexCertainty`, `Incomplete` or `Exhaustive`, through text and JSON, with the default and a wrapping module (R26).
- **Warnings.** A retained HCM with no routes after pruning and an ECDS slot warning; one slot warning under two routes; a pruned-subject warning: each once in JSON and once in the text block, none inline (R10). A dropped claim under two wrapper names: byte-equal output, kgwctl's message, no claim ID or module name (R12b).
- **Payload.** An unresolved ECDS network filter (R15b) and a present-but-ambiguous HTTP ECDS entry (R15c). `routeConfig.source` on a hand-built dump with one source used by two HCMs and two sources with one name; no field lists unjoined sources.
- **Release.** A dry run writes only under `hack/release/out/dry-run/<tag>/`, writes no `publish-files.txt` and prints no publish steps; a real build refuses a dirty tree and a HEAD that is not the tag commit, and prints the steps only from its own manifest; a real run refuses a non-empty `hack/release/out/<tag>/`; `checksums.txt` and `publish-files.txt` name exactly the four archives.
- **Seam.** Under both strategies (R25), a wrapping module named `example` that delegates to `kgateway.New()`: same `Select` result, byte-equal text and JSON on every golden case (R11). A wrapper that adds one `PolicyRecord`: in the payload and the text (R12). A wrapper whose `FilterLink` places an inner record on a slot, through `pkg/cli` (R12a). Duplicate names: exit 2 (R13). `examples/downstream` passes a wrapper through `cli.WithProvenanceModules`.
- **Hygiene.** No `Claim.Module` read and no `kgateway.ModuleName` in kgwctl's non-test code (DD-9). The binary dependency test allows `pkg/envoy/{parser,chain,model,render,provenance}` and `pkg/adapter/kgateway` only. The model coverage test of DD-13.
- **Command boundary, `internal/app`.** Text, JSON and YAML goldens per recorded dump; live tests with the fake dialer and envtest (Known, narrowing, deadline, list denied); exit 2 paths unchanged. Every golden diff is read and the expected differences of §3.3 are confirmed in the sub-phase record; a golden is never accepted only because `-update` wrote it.
- **Envelope.** One test pins the literal `kgwctl.kgateway.dev/v1alpha2`; other tests use the constant. docs/output.md names it.
- **Snellius.** The existing bundle scope tests pass unchanged. R15a through `check` and the bundle's `check.json`. Check goldens are reviewed against §2 and the KGWUI-016 stale-condition rule.
- **Suite.** Every task ends with `make test` (race, with the envtest assets it sets up). No task leaves a red suite.
- **Version.** Table tests for DD-21; a `go install` of the tagged module in the close step (R16).
- **Live check on kind.** `hack/live-check.sh routes` keeps its four runs (metadata on and off, live and file) and the reader-only exit 2, with expectations updated to the new text and a JSON run checked with `jq` (`schemaVersion`, `selection.routes`, `selection.ambiguous`, the orders route's filter names in order). Live and file views must still be equal below the header. `goldens-check` also runs `proxy routes -o json` on each recorded dump and checks the v1alpha2 shape. The recorded dumps are not captured again.

## 10. Dependencies
- xdsview `v0.4.3` → `v0.7.0` (E15, kgwctl#15). Imports `parser`, `chain`, `model`, `render`, `provenance`, `adapter/kgateway`. `origin` and `filter` are gone.
- Snellius `v0.8.2` → `v0.9.0` (kgwctl#16).
- No other module changes. Both use Go 1.26.0 and client-go v0.36.2, as kgwctl does.

## 11. Risks
- **kgwctl and krp still differ.** By default (`strict`) kgwctl lists a route that krp prints. With `--select known` the rule is krp's, but Known is not: krp narrows with `--route` alone, kgwctl with the cluster's HTTPRoutes. On the colliding fixtures with both HTTPRoutes present, krp picks a route and kgwctl does not (DD-4, R20). The README says this (open item 4).
- **`known` can pick a deleted HTTPRoute's route.** That is why `strict` is the default. The mark in text and JSON is the user's only signal.
- **The payload lags xdsview.** A new model field or claim kind is not in kgwctl's JSON until kgwctl maps it. The coverage test (DD-13) catches model fields on a bump; new claim kinds decode as `Opaque` in xdsview and are dropped by `Compose`, so they never reach the payload unmapped.
- **Occurrence IDs get stored.** Someone may key data on `id` across dumps. The docs say not to (DD-15).
- **Downstream builds pin xdsview.** `pkg/cli` now takes `provenance.Module`, an xdsview type, so a downstream build must use kgwctl's xdsview minor. xdsview contract changes after v0.7.0 are additive (xdsview R15), which bounds the risk.
- **Text changes are wide.** Every `proxy routes` golden changes, and the live check's text expectations with them. Review against §3.3, not by regeneration.
- **Live and file can differ.** A complete live capture can produce `rds-missing` or ECDS-default verdicts a file never shows. The live fixtures have neither, so the `same_view` check holds; a cluster with a missing route config would differ, by design.
- **Snellius behaviour changes.** Users who key on KGWUI-003 for a rejected policy, or on a policy-ancestor finding ID, see a change. Named in the release notes.
- **Memory.** `Compose` copies the snapshot once per module (about 85 MB at 5,000 routes, xdsview §5). With the 64 MiB dump cap and one module this is acceptable; a downstream build with several modules pays more.
- **Change size.** 4b.1 touches more than five files and 400 lines (AGENTS §6). This design and plan are the approval request.

## 12. Proposed sub-phases
| Sub-phase | Kind | Content | Waits on |
|---|---|---|---|
| 4b.1 | planned | One migration commit: xdsview v0.7.0 pipeline, text, warnings, complete `ProxyRoutes` payload, envelope `v1alpha2` for every kind, updated assertions and reviewed goldens. Then `--select strict\|known`, the module seam, `proxy routes` docs | none |
| 4b.2 | planned | Snellius v0.9.0, client version, release script, live check | 4b.1 |

The migration is one commit because the xdsview bump breaks the old view at once, and the new shape must never sit under `v1alpha1`.

## 13. Left to the builder
- Go names of unexported helpers, and help text.
- The exact `gh release create` notes file, written at the close from the phase record.

## 14. Open items (owner)
1. **Incomplete readings (DD-7).** `Select` selects a route whose only reading is marked `Incomplete`. Recommended: follow `Select`, show `incomplete: true` in JSON. Alternative: treat it as ambiguous too, which `Select` does not offer, so kgwctl would add its own rule.
2. **Context label wording (DD-11).** `Gateway ns/gw (from the command line)` for both live and `--file`. krp writes `(from --gateway)`. One wording keeps the live and file views equal.
3. **Release notes.** Should the `v0.1.0` notes list the Snellius v0.9.0 `check` changes (DD-20), or only kgwctl's own?
4. **D-032 consequence wording.** D-032 says `--select known` and krp give the same selection for the same input. They do not when the cluster holds both colliding HTTPRoutes (DD-4, R20). Proposed: "uses krp's rule; krp narrows with `--route` alone and kgwctl with the cluster's HTTPRoutes, so the two can differ."
