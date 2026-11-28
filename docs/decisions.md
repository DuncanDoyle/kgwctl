# Decision log

Append-only. Newest at the bottom. Supersede, don't edit.

## D-001 — Build from scratch (2026-11-04, phase 0)
**Context:** Earlier prototypes exist: kgateway PR #13654 (closed unreviewed) forks about 19 files of gwctl, pulls in go-graphviz and has tests only for its printers.
**Decision:** Start a new codebase. Take ideas, not code: the relation-based resource graph, policy discovery through the Gateway API policy label, `check`, `analyze -f`, and a hook for vendor builds (program P-003).
**Alternatives:** Revive the PR — rejected: fork upkeep, heavy dependencies, thin tests.
**Consequences:** Gateway API logic comes from kgateway-ui packages (D-002), not from gwctl.

## D-002 — Libraries, not a backend (2026-11-04, phase 0)
**Context:** kgateway-ui exposes its Gateway API model and analyzer as versioned Go packages; krp is becoming the Envoy data-plane library.
**Decision:** Import both directly and talk to the cluster with the user's kubeconfig. No UI backend is needed (program P-004).
**Alternatives:** Thin client over the UI API — deferred; needs the UI running and its per-user auth.
**Consequences:** The CLI pins tagged versions of krp and kgateway-ui; contract changes flow through the program file.

## D-003 — No reimplemented translation or merge logic (2026-11-04, phase 0)
**Context:** Two tools computing effective policy or Envoy naming differently would contradict each other.
**Decision:** Effective policy, findings and Envoy → Gateway API mapping come from the shared libraries (program P-001). The CLI formats; it does not decide.
**Alternatives:** CLI-local logic for speed — rejected.
**Consequences:** Some CLI phases wait on library deliveries (edges E1–E3, E7).
**Superseded in part by D-024 (2026-11-22):** effective policy no longer comes from the shared libraries; they provide relations and naming only.

## D-004 — Vendor-neutral core (2026-11-04, phase 0)
**Context:** The CLI is headed for kgateway OSS.
**Decision:** No vendor or edition names in this repo. Vendor policy kinds plug in through a registration hook from outside (program P-002).
**Alternatives:** Build-tag variants in one repo — rejected: leaks vendor code into OSS.
**Consequences:** Phase 4 design defines the hook.

## D-005 — Light dependencies (2026-11-04, phase 0)
**Context:** go-graphviz in the prototype pulls in a WASM runtime to print DOT text.
**Decision:** Text graph formats (DOT, Mermaid) are written directly. Each new dependency is justified in the phase design.
**Alternatives:** n/a.
**Consequences:** No rendered images; users pipe DOT to their own tools.

## D-006 — Working name and home (2026-11-04, phase 0)
**Context:** The final name and repo are open; kgateway's security self-assessment already lists `kgwctl`.
**Decision:** Use `kgwctl` as the working name. The project moves into kgateway-dev/kgateway (roadmap Phase 5); maintainers decide the final name.
**Alternatives:** n/a.
**Consequences:** Keep the name in one place (root command, module path) so a rename is cheap.

## D-007 — `check` comes early, as its own phase (2026-11-04, phase 0)
**Context:** Users need a `check` command for CI gates and support triage, in the shape of earlier gateway CLIs: one-shot, scriptable, with exit codes. The roadmap had it in Phase 3, behind the data-plane work and the UI analyzer. The prior art's JSON mode exits 0 even when checks fail.
**Decision:** Phase 2 delivers a `check` MVP on the UI's existing health checks (edge E8): process health, failing status conditions (which include unresolved references), routes not attached, xDS rejections. Exit codes are identical across output formats. The full ranked analysis and `analyze -f` move to Phase 4.
**Alternatives:** Keep `check` in Phase 3 — rejected: blocks a stated requirement on unrelated work.
**Consequences:** Phases renumber: data plane 3, full analysis 4, policies 5, upstream move 6. Program edges E1, E3 and E7 move to the new phase numbers.

## D-008 — No compatibility with earlier kgwctl prototypes (2026-11-04, phase 0)
**Context:** Besides kgateway#13654, two earlier versions exist: an internal gwctl-based prototype (dormant since 2025-07, 12 release downloads) and a vendor CLI subcommand built from it. Neither reached OSS users; the OSS docs for it were removed in 2025-09.
**Decision:** Treat both as idea sources only, like D-001. No command, flag or output compatibility with them, and no migration path for the vendor CLI.
**Alternatives:** Keep the vendor subcommand's shapes so it can move onto this CLI — rejected: no evidence of use, and it would carry the gwctl-fork design forward.
**Consequences:** Command and output design is free in Phase 1. A vendor build may consume this CLI through the hook (P-002), but that is not a design constraint.

## D-009 — Extension API in Phase 1: library plus downstream main (2026-11-04, phase 1) — proposed, pending owner approval
**Context:** D-004 left the vendor hook to Phase 4. Every command added before then would have to be retrofitted. The CLI survey found the build-time model (an exported root-command constructor plus registries, as in cilium-cli and kgateway-ui's own D-003/D-038) the best fit for an OSS core with extended builds. Users of an older gateway CLI reported OSS and extended binaries drifting apart.
**Decision:** Phase 1 exports `pkg/cli` (`NewRootCommand(opts...)`, `Execute`) with options for name, kgateway-ui extension registry, extra commands and build info. Everything else is `internal/`. `examples/downstream` is built in CI.
**Alternatives:** PATH plugins (`kgwctl-<name>`) — rejected as primary: plugins can't see the shared model, so vendor kinds would be missing from `get`/`describe`. Fork per vendor — rejected: permanent rebase cost.
**Consequences:** Supersedes the "Phase 4 design defines the hook" consequence of D-004. `pkg/cli` is a semver contract from `v0.1.0`. Later phases extend the registry, not the CLI options, where possible.

## D-010 — Phase 1 design approved; support bundle phase (2026-11-05, phase 1)
**Context:** Phase 1 design r4 and plan r3 passed four Codex review rounds. Research ranked a support bundle the second most frequent support ask, and "Accepted, but rejected by Envoy" the costliest theme.
**Decision:** The owner approved the design, the plan, every *(our call)* item in the design, D-009, and envtest as a test-only dependency. The roadmap gains Phase 4 "Support bundle" (secrets redacted by default). Phase 2 builds the xDS-rejection check first and adds version skew.
**Alternatives:** Bundle at the end of the roadmap — rejected: its demand is higher than analysis or policies.
**Consequences:** Phases renumber: full analysis 5, topology and policies 6, upstream move 7. Program edges E3 and E7 move to CLI Phases 5 and 6. D-009 is accepted.

## D-011 — No maintainer proposal for now (2026-11-05, phase 1)
**Context:** Program P-005 asked for early maintainer buy-in through a short proposal. The owner leads both the vendor product and most of the kgateway maintainers, so direction is already aligned, and the project is not ready to be announced.
**Decision:** Don't file the proposal. The draft (`docs/phases/phase-1/maintainer-proposal.md`) stays as a reference for when the project goes public.
**Alternatives:** File now — rejected by the owner.
**Consequences:** P-005 is deferred in the program file. Phase 1 has no proposal step; the question returns at Phase 7 (upstream move) at the latest.

## D-012 — `get` details settled during sub-phase 1.2 (2026-11-05, phase 1) — our call, owner may override
**Context:** Implementing `get` raised cases design §3.1 and §6.2 left open or worded loosely. Each was ruled during implementation (SDD ledger R15–R28) and checked by task reviews and the Codex sub-phase review.
**Decision:**
- INSTALL shows in table and wide output, on every attributed kind, whenever more than one install is detected. It lists every owning install, or `unknown` when ownership inputs are not ok.
- ROUTES is the library's linked-route count for the Gateway plus ` (n not accepted)`; `-` marks an object no install indexes.
- The policy ANCESTORS denominator counts each distinct ancestorRef once, even when two controllers write it.
- For a parent with only foreign status entries, the owning install's index state (`unwatched`, `declared`) wins over `other`.
- A condition with no `observedGeneration` and status Unknown is never stale: an object condition renders `Unknown (<Reason>)`, not `<stale>`, and the same rule applies to per-parent and per-ancestor entries.
- `get <kind> <name>` bypasses the hiding rule. With several kinds and a name, a kind without the object fails the command: exit 1, empty stdout.
- `--install` naming no detected install is exit 1 when ownership inputs are ok; otherwise a warning, and nothing is hidden.
- Overlapping kind arguments (`ls,xlistenersets`) list each object once; the alias table wins.
**Alternatives:** kubectl-style partial output for multi-kind named lookups — rejected: stdout stays empty on every error (§8).
**Consequences:** Design §3.1 and §6.2 updated to match. `describe` (1.3) reuses the same cells.

## D-013 — `describe` details settled during sub-phase 1.3 (2026-11-06, phase 1) — our call, owner may override
**Context:** Implementing `describe` and freezing its JSON schema raised cases design §3.2 left open. Each was ruled during implementation (SDD ledger R29–R33) and checked by task reviews and the Codex sub-phase review.
**Decision:**
- `describe <kind> <name>` shows an object even when `get` would hide it; without an owning install, its relations are the declared form.
- A Gateway's notAccepted list is the rejected set `get` counts; pending, unwatched and declared links are a separate pending list.
- The Describe JSON item is structured: absolute ref objects, conditions as fields, booleans for declared and mirror. An unknown listener is omitted. Age is text-only.
- With index inputs down, parent states still come from status, relations show as unknown, and `resolved` is absent. Ownership-input failures reach `incomplete` for every attributed kind.
**Alternatives:** Display strings in JSON (as text prints them) — rejected: JSON is the stable interface (§8).
**Consequences:** `docs/output.md` documents the schema; `TestOutputDocMatchesSchema` keeps it in step with the Go types.

## D-014 — `check` is a support first look (2026-11-10, phase 2)
**Context:** The roadmap framed `check` as a CI gate after install or upgrade, and split checks between Phase 2 and Phase 5. Snellius now ships all 26 checks (E3).
**Decision:** `check` is a support engineer's first command on a cluster they troubleshoot. It runs all 26 checks in Phase 2; text for reading and pasting, `-o json|yaml` as the full mode.
**Alternatives:** CI gate first — rejected by the owner; Phase 2/5 split — outdated once E3 shipped.
**Consequences:** Roadmap Phase 2 reworded; Phase 5 keeps only `analyze -f`.

## D-015 — Deferred from Phase 2 (2026-11-10, phase 2)
**Context:** Phase 2 keeps to the support first look.
**Decision:** Defer `--only`, `--exclude`, `--fail-on` (until a CI user asks); controller logs as evidence; the `pkg/cli` facade for vendor cluster commands (Phase 1 carry-over, to the phase that first needs it); `-o agent` (next step after Phase 2, roadmap).
**Alternatives:** Keep them in Phase 2 — rejected by the owner.
**Consequences:** Exit 1 counts error and warning findings; info never fails a run.

## D-016 — No cluster-derived timing; `--live` instead of `--wait` (2026-11-10, phase 2)
**Context:** Grace periods and holds (003's 60s, 018's 5m) live in engine memory, so a one-shot run can't decide them. Seeding them from cluster timestamps was analysed with Codex and the Snellius session.
**Decision:** Don't derive timing from the cluster: deletions, controller requeues, webhook changes and clock skew leave no usable timestamp, which gives early or false findings. Instead `check --live` keeps one engine running for a bounded time (default 6m): progress on stderr, one report on stdout, stops at `--timeout` or Ctrl-C, never on "nothing pending". Holds come from the engine (E9, snellius#33).
**Alternatives:** `--wait` (no progress) — the owner prefers `--live`; cluster-derived timing — rejected under KISS.
**Consequences:** A plain `check` shows held checks as "still deciding" with their decision time and suggests `--live` only with that reason.

## D-017 — Controller metrics and proxy stats on by default (2026-11-10, phase 2)
**Context:** 001, 007, 024, 025 and 026 need controller metrics or Envoy stats, read through the apiserver's `pods/proxy`; Phase 1's ClusterRole excluded it.
**Decision:** `check` reads them through `pods/proxy` by default. A denied read puts those checks under "Not checked" with the RBAC line to add. No port-forward.
**Alternatives:** An opt-in flag — rejected: the default first look would miss xDS rejections; cache inputs only — rejected.
**Consequences:** `docs/rbac.yaml` gains `pods/proxy` get, Events, Services, EndpointSlices.

## D-018 — kgwctl releases depend on final Snellius releases (2026-11-10, phase 2)
**Context:** E9 shipped as Snellius `v0.7.0-alpha.1`.
**Decision:** Development may depend on a Snellius pre-release. A kgwctl release only ever depends on a final Snellius release.
**Alternatives:** Release on a pre-release — rejected by the owner.
**Consequences:** No kgwctl release tag while go.mod names a Snellius pre-release.

## D-019 — Support bundle: agctl prior art, CLI only (2026-11-17, phase 4)
**Context:** `agctl bug-report` (agentgateway#3991) ships a support bundle much like Phase 4. It is numbered after Phase 2's D-014 to D-018, which merged later.
**Decision:** Phase 4 borrows three ideas from it: kind discovery by CRD category, partial results with `errors.txt`, and previous-container logs only after a restart. The bundle stays a CLI feature, with no shared library for now.
**Alternatives:** A Snellius "download bundle" — deferred: a bundle crosses Snellius's per-Gateway authorization (Snellius D-013). The owner revisits it after Snellius Phase 12.
**Consequences:** Phase 4's design settles Secret and ConfigMap capture and the `pods/log` RBAC grant. Collector, manifest and redaction interfaces stay free of CLI concerns, so they can move to Snellius `pkg/` later.

## D-020 — Ctrl-C ends `check --live` like `--timeout` (2026-11-20, phase 2)
**Context:** Design §15 kept exit 2 for an interrupted `--live` and said to revisit it if it annoyed in use. In the owner's try-out, Ctrl-C printed the report and then `Error: interrupted`, which read like a crash.
**Decision:** A Ctrl-C during `--live` after an install was found prints the report, then `Stopped by Ctrl-C.` on stderr. The findings decide the exit code, as with `--timeout`. Exit 2 keeps meaning "could not check": a plain `check` interrupted, or `--live` interrupted before an install was found. The text footer says "stopped by Ctrl-C" or "stopped at --timeout"; JSON keeps `stopReason: interrupted`.
**Alternatives:** Keep exit 2 and only reword the message — rejected by the owner: the report is a valid check of the state the user watched.
**Consequences:** Scripts tell a deliberate stop from a failure by exit code. Design §3.2, §3.4 and the README updated (sub-phase 2.5).

## D-021 — `proxy routes` reads the config dump over port-forward (2026-11-20, phase 3)
**Context:** kgateway binds Envoy admin to 127.0.0.1:19000. `pods/proxy` reaches only the Pod IP, and the 9091 stats listener doesn't serve `/config_dump` (measured on v2.5.1). D-017 keeps `check` on `pods/proxy`.
**Decision:** Live `proxy routes` port-forwards to 19000 and sends only `GET /config_dump`, with a 30 s deadline and a 64 MiB cap. `create pods/portforward` is a separate, optional Role. `--file` needs no cluster access.
**Alternatives:** `pods/exec` — more power than port-forward. The controller's `/snapshots/xds` — intended config, not what Envoy accepted. A config_dump route on the stats listener — needs a deployment change; possible upstream opt-in later. File only — loses the live view.
**Consequences:** `docs/rbac.yaml` and the README say the grant also allows Envoy admin writes. The base reader role is unchanged. Reviewed with Codex (2026-11-20).

## D-022 — xdsview owns the Envoy view (2026-11-20, phase 3)
**Context:** xdsview's tree renderer is internal, its model lacks listener state, and it has no effective filter chain per route. Snellius Phase 12 (E4) will read the same model.
**Decision:** The renderer, effective chain and listener state go into xdsview (E11, xdsview#27). kgwctl writes no Envoy parsing or tree of its own; it owns access, origins input and the JSON envelope.
**Alternatives:** A tree in kgwctl over `model` — two views of filter order that drift, and Snellius gets neither.
**Consequences:** Sub-phase 3.2 waits on an xdsview minor release. 3.1 ships on v0.3.0.

## D-023 — Add-on commands such as a local MCP server ship in downstream builds (2026-11-21, phase 3)
**Context:** A local MCP server (`kgwctl mcp`, program edge E10) and traffic debugging were candidates for this CLI. The owner may build them outside this repo. kgwctl moves into kgateway (Phase 7) and must stay vendor-neutral, with no dependency on code outside the program (E6).
**Decision:** kgwctl does not ship `mcp` or debug commands. A downstream build adds them through `pkg/cli` (`WithCommands`, `WithRegistry`; D-009), as `examples/downstream` shows. `-o agent` stays here: it is output, not a server.
**Alternatives:** An MCP command in kgwctl over a shared tool set — ties kgwctl to that tool set's license and roadmap.
**Consequences:** `pkg/cli` is a contract for downstream builds: breaking changes need a version bump and the downstream example as the test. E10's consumer is a downstream build, not kgwctl.

## D-024 — The controller owns policy semantics; Phase 6 shows evidence, not an effective-policy model (2026-11-22, phase 4)
**Context:** kgateway's controller owns policy resolution, precedence, inheritance, merging and translation. Phase 6 assumed a library (E7) would give `describe` the attached and effective policies. A library outside the controller can only approximate those rules, and an approximation that disagrees with the controller misleads more than no answer.
**Decision:** kgwctl does not compute or promise effective policy. Phase 6 shows four kinds of evidence and links them:
1. declared policies and their `targetRefs`/selectors, as written;
2. the controller's status conditions on those policies and targets;
3. what Envoy runs for a route or listener, from xdsview;
4. links between them, each marked verified, inferred or unknown.

Each source verifies only its own claim:

| Source | Verifies | Does not verify |
|---|---|---|
| `route_source` route metadata | the originating route, rule and match of an Envoy route | which policy won, merged or produced a filter |
| A route's `merge.*` metadata | the policy IDs merged at that route's level | which level (listener, virtual host, route) governs a request |
| A policy's status condition | the controller's result for the target, controller and generation the condition names | targets, controllers or generations it does not name |
| Envoy config dump | what one replica ran at capture time | other replicas, or later times |

A link is `verified` only when one of these sources states it for the objects shown. It is `inferred` when it comes from names or selectors. It is `unknown` when evidence is absent, stale (another generation) or ambiguous. Per-target policy status is attachment evidence with at most a lossy hint of effect, not resolution (P-008). No effective-policy view is planned.
**Alternatives:** Snellius's policy model as the effective view (E7 as written): rejected as authoritative, because it re-derives controller rules. It is still usable as an inferred link if marked so.
**Consequences:** Supersedes D-003's "effective policy from the shared libraries": libraries may provide relations and naming, never semantics. E7 is redefined in the program file (P-008): declared policies, controller-reported attachment, and a route's `merge.*` policy IDs, from the Snellius Phase 8 declared-policy model and xdsview `origin.Policies` (E5, E13). Listener, RouteConfiguration and virtual-host metadata have no provider yet. Anything beyond controller-reported evidence stays out of scope. The deferred `proxy diff` (generic intended-versus-accepted) is dropped from the roadmap. A data-plane convergence candidate takes its place: it reads the raw per-replica evidence the support bundle keeps, and gives a stale-config verdict only with comparable controller evidence.

## D-025 — Envoy admin reads are an allowlist of operations (2026-11-22, phase 4) — accepted by the owner 2026-11-22
**Context:** D-021 limits port-forward to one `GET /config_dump`. The support bundle also needs the config dump with endpoints, `/stats` (text readouts such as `version_text`), `/server_info`, `/certs` and `/clusters`.
**Decision:** `proxydump` sends only fixed operations. Each binds port, method, path and exact query; callers pick one by constant, and no API takes a path:

| Operation | Request |
|---|---|
| `config-dump` | `GET :19000 /config_dump` |
| `config-dump-eds` | `GET :19000 /config_dump?include_eds` |
| `stats` | `GET :19000 /stats` |
| `server-info` | `GET :19000 /server_info` |
| `certs` | `GET :19000 /certs` |
| `clusters` | `GET :19000 /clusters` |

An unknown operation fails before a stream opens, and redirects are refused. `proxy dump` and `proxy routes` keep `config-dump`. The bundle uses `config-dump-eds` by default; `--no-endpoints` uses `config-dump` and skips `clusters`. No controller admin path is on the list: `/snapshots/xds` holds TLS keys and inline credentials in clear, and `/snapshots/krt` can return raw snapshots and ConfigMaps (kgateway v2.5.1, read from source).
**Alternatives:** One path per command, as D-021 has it — the bundle would need its own transport. A path allowlist without the query — cannot express `include_eds`. Any GET — opens admin endpoints whose output nobody reviewed.
**Consequences:** The `pods/portforward` grant and its warning are unchanged. A new operation needs a decision entry.

## D-026 — Redaction is a mandatory stage of the bundle writer (2026-11-22, phase 4) — accepted by the owner 2026-11-22
**Context:** A bundle leaves the customer's trust boundary. Envoy redacts only fields it marks sensitive. Header values (including values kgateway resolves from a Secret), ext-auth `headers_to_add`, `initial_metadata`, inline JWKS, Lua code, transformation templates and `last-applied-configuration` carry secrets past it.
**Decision:** Collectors hand typed content to the writer, which runs the redactor for that type and refuses undeclared types.
- Every header value in header-mutation structures, header modifiers, ext-auth `headersToAdd`, tunnel headers and `initialMetadata` is redacted, whatever the header's name. Names are kept.
- Envoy debug header lines keep values only for an allowlist of non-secret names (`:authority`, `:method`, `:scheme`, `:status`, `content-type`, `content-length`, `user-agent`, `x-request-id`, `x-envoy-*`).
- Inline JWKS are rebuilt as `{"keys":[...]}` from a typed public projection per `kty` (RSA `n`, `e`; EC `crv`, `x`, `y`; OKP `crv`, `x`; plus `kid`, `alg`, `use`, `key_ops`, `x5c`, `x5t`, `x5t#S256`). Any other shape or type is redacted whole.
- Prometheus content must parse as text exposition, or it is dropped with an error. Label values and comments get the pattern pass.
- Remote errors are recorded as HTTP status and Kubernetes reason/code only, never bodies. The bundle's client transport replaces non-2xx bodies with a code-and-reason status before any error string is built, which covers the reused `check` path. It first reads at most 4 KiB of a pods/proxy 502/503 body to mark "connection refused"; the body is never stored. `proxydump`'s port-forward status errors carry code and reason only, in every command. Other error text, including panic values, is redacted and cut at 512 bytes.
- No context name in file or member names; path components only from validated Kubernetes names or constants; operation codes, not text; flag values only for enum, numeric, duration and boolean flags; map keys shown as `[key]` in redaction locations.
- Structured rules for objects and JSON, a pattern pass for text, the marker `[redacted]`, and a `redactions.json` report without values.
- kgwctl makes no API request for Secrets or ConfigMaps in Phase 4. A guard on every bundle-owned client refuses such requests before they are sent, and the object Sink refuses both kinds. Derived artifacts can still carry values the controller resolved from Secrets; the redaction above covers known structures and patterns.

Downstream collectors are trusted in-process code. The Sink and the guard protect against their mistakes, not against malice.
**Alternatives:** Redaction per collector — a downstream collector could skip it. Header redaction by name — misses a Secret-backed custom header. Hashing values — low-entropy secrets can be guessed offline.
**Consequences:** Redaction is tested on the extracted archive with a canary corpus per artifact class. Its limits are documented in docs/bundle-threat-model.md and the README.

## D-027 — The Sink re-reads Pods and records intentional omissions (2026-11-23, phase 4) — accepted by the owner 2026-11-23
**Context:** Review item P4-2 (4.2). A collector sets `Source.Identity` before it writes, so a streamed capture (logs, metrics) cannot report the re-read that §8.2 requires after the read ends. A capture skipped on purpose (Pod not running, no install, no metrics port) has no way to be recorded except as an error.
**Decision:**
- Every Sink write method takes a leading `context.Context`, e.g. `Text(ctx, Path, io.Reader, Source) error`.
- When `Source.Pod` is set, `Namespace` and the expected `UID` are required. After the content is read, also when it was cut, the Sink re-reads the Pod with the guarded client, bounded by `--request-timeout` and the capture context, and publishes the outcome with the item under the writer lock. A write that already failed is not re-read. `Source.Identity` is output only; a value passed in is rejected.
- `Omit(Path, Source, OmitReason) error` records an item that was not captured on purpose. The reasons are fixed: `not running` and `no install` give `skipped`, `no metrics port` gives `disabled`. An omission has no archive member, size, redactions entry or `errors.txt` line, claims no path and does not degrade its collector. After the seal it returns `ErrSealed`.
- At the seal a pending re-read result is discarded. Pod text kept by the seal gets `reread-failed`.
- `verified`, `changed`, `gone` and `reread-failed` are metadata. None makes an item or collector fail.
**Alternatives:** An item handle with a one-shot `SetIdentity`: collectors can forget it, and a failed write has no handle. A path-based `Identity` amendment plus `Outcome`: a wrong path can amend another item, and the writer needs an amendment index.
**Consequences:** `pkg/bundle.Sink` changes before any release; the 4.2 collectors and `examples/downstream` move to it in the same task. One GET per Pod capture, on the collector's own worker. Design §5 and §8.1–§8.4 updated.

## D-028 — The reader role lists resources explicitly, guarded by a test (2026-11-24, phase 4) — decided by the owner 2026-11-24
**Context:** The bundle lists every served Gateway API kind. Gateway API v1.7.0 serves UDPRoute and three experimental kinds the `kgwctl-reader` ClusterRole did not grant, so a reader-only bundle recorded them as `denied`.
**Decision:** `docs/rbac.yaml` names every resource; no `*` in `resources` or `apiGroups`. `TestReaderRoleCoversServedKinds` fails when a test CRD in a group the bundle lists has a kind the role lacks. Each bump of the test CRDs follows the checklist in `internal/testenv/testdata/crds/README.md`: diff the CRDs, review new kinds (RBAC, support) and changed kinds (served versions, credential fields for redaction).
**Alternatives:** `resources: ["*"]` per group: covers new kinds without a change, but admission policies and security reviews often reject a wildcard Role. Listing only registry kinds: drops new kinds from the bundle without a trace.
**Consequences:** Each new kind needs a one-line RBAC change, which the test enforces on the next CRD bump. On a cluster running a newer Gateway API than the test CRDs, a reader-only bundle shows the new kind as `denied` with a reader grant line only when the resources collector discovers and lists it. The collector finds a kind on one of two paths. The first is the preferred-version scan, which takes a resource in its group's preferred version when the group is listed whole or the resource has the `kgateway` category. The second is registry resolution, which takes a `collect.Registry` descriptor in any of its served versions, in any group or category. Both paths leave out subresources, resources without the `list` verb, and cluster-scoped kinds other than GatewayClass. On a live cluster, a served kind outside `collect.Registry` is collected without the ownership filter, or is missed when it is served only in a non-preferred version, until it is added to that list. An unregistered route can be archived without its backend Service namespaces being added to workload collection, because that expansion walks only registry kinds.

## D-029 — The public Sink has no generic JSON or config-dump method (2026-11-25, phase 4) — decided under the owner's delegation 2026-11-25
**Context:** Phase 4 review finding 10. `Sink.JSON` accepts any JSON document and applies only generic rules. `Sink.ConfigDump` does the same with the Envoy rules, at any path. A collector that passes a serialized Kubernetes object through either skips kind redaction and the Secret and ConfigMap refusal. Detection by shape cannot be complete: nested objects, stripped type metadata and objects serialized into strings all escape it. kgwctl's own report envelopes would also match it falsely. The owner asked for the best design, verified with Codex, since no downstream collector outside our control exists (reviews: `.agents-tmp/sink-json-design-codex.md`).
**Decision:**
- `pkg/bundle.Sink` drops `JSON` and `ConfigDump`. Downstream collectors write Kubernetes resources and lists through `Object`, plain text through `Text` and metrics through `Prometheus`.
- Built-in JSON artifacts (check report, `version.json`, Envoy `server_info`, `certs` and `config_dump`) use internal writers that only built-in collectors can reach. Each owns its redaction, as D-026 requires; the config dump keeps the config-dump rules.
- `Object` refuses an object or List item without a kind, since both the refusal and the kind rules need it.
- A new structured format needs a reviewed schema and its own writer.
**Alternatives:**
- Route objects in `JSON` to the Object rules: a heuristic with false negatives, and it misreads kgwctl's own envelopes.
- Refuse objects in `JSON`: the same false negatives, and a false positive loses a whole artifact.
**Consequences:** A downstream call to `Sink.JSON` or `Sink.ConfigDump` fails to compile. A kind-less object, such as one from a typed clientset list, is a failed item with reason `missing kind`. `examples/downstream` and the README extension-point text change. The downstream build is told the new contract. Check findings carry opaque `json.RawMessage` evidence. Projecting it to known fields is a separate item for the owner.

## D-030 — The bundle seals an archive once its output exists (2026-11-25, phase 4) — decided by the owner 2026-11-25
**Context:** Phase 4.5 reviews (4.5-codex.md #2, -r2, -r3). Whether a troubled install detection gave a partial archive (exit 0) or no archive (exit 2) depended on classifying the error. Each review round found another case on the wrong side: a TLS handshake timeout, an HTTP 504, an `--install` pin that a partial model could not confirm. A support engineer gets nothing from a run that removes its own output.
**Decision:**
- Once the output file or stream is created, `kgwctl bundle` always seals an archive. This includes no reachable cluster, a detection error of any kind, and `--install` naming no detected install. The manifest records `inventory/detection` with its outcome (`failed`, `timeout` or `denied`) and a fixed reason. The collectors that never ran are recorded the same way they are after a detection timeout.
- Exit codes (design §3.1):
  - 0: the archive holds cluster data.
  - 2: no archive was written, or the archive holds no cluster data. The archive holds cluster data when a collector other than `version` finished `ok` or `partial`, or any item other than `cluster/version.json` was written `ok`. `version.json` is excluded because it carries kgwctl's build data even when the cluster never answers. Causes of no archive: a usage error, an output that exists or cannot be created, a fatal write failure, or an interrupt.
  - stderr says which case applies.
- An interrupt (Ctrl-C, or the caller cancelling the command context) keeps today's behaviour.
- Errors before the output is created keep exit 2 with no archive. Examples: no kubeconfig, an unknown context, invalid flags.
**Alternatives:** Classify detection errors as timeout or fast failure (4.5 S3/S8). Every new transport or server failure mode needs its own rule, and the reviews kept finding more.
**Consequences:**
- The error classifier (`timeoutClass`, the `reached` logic) goes away.
- A run against a dead cluster leaves a small archive and exits 2, so scripts that test for the file must also check the exit code.
- Design §3.1 and §3.2 and docs/output.md change.

## D-031 — Migrate to xdsview v0.7.0 before the first release (2026-11-27, phase 4 close) — decided by the owner 2026-11-27
**Context:** Phase 4 closed with xdsview v0.7.0 (E15, kgwctl#15) and Snellius v0.9.0 (E7, `install.Scope.Excluded`, kgwctl#16) both released. kgwctl has no release yet. The E15 migration moves every command's JSON and YAML to `kgwctl.kgateway.dev/v1alpha2`, including the bundle's `manifest.json`.
**Decision:**
- The next phase is Phase 4b: migrate `proxy routes` to xdsview v0.7.0, bump Snellius to v0.9.0, move the envelope to `v1alpha2`, then tag `v0.1.0`. Phase 5 (full analysis) keeps its number.
- `v0.1.0` is tagged when Phase 4b closes. It depends on final Snellius and xdsview releases only.
**Alternatives:** Tag `v0.1.0` now on `v1alpha1`. Rejected: the first format people script against would change in the next release, and no one waits on a release.
**Consequences:** Phase 4b is a planned phase with its own design, Codex review and live check. Downstream builds (D-023 in xdsview) replace the kgateway module, so the module set kgwctl composes is a `pkg/cli` seam, and downstream goldens differ from kgwctl's.

## D-032 — `proxy routes` takes a route selection strategy (2026-11-27, phase 4b) — decided by the owner 2026-11-27
**Context:** Under xdsview v0.7.0, `kgateway.Select` leaves a route ambiguous when only the cluster's HTTPRoutes (Known) narrowed its readings to one. kgwctl's selection has been strict so far (D-022). krp also selects such a route (xdsview D-022). The owner wants both behaviours available in kgwctl.
**Decision:**
- `proxy routes --select exact|strict|known`, default `strict`. Each value adds one kind of evidence to the one before it.
- `exact` (added by the owner 2026-11-27): only a route whose one reading has certainty `exact` is selected; with `--rule`, its rule index must be exact too. With kgateway, exact readings come from route metadata. A route `strict` would select on an inferred reading is listed as inferred, not matched. Works with `--file`.
- `strict`: a route Known narrowed to one reading stays ambiguous and is listed, never selected.
- `known`: such a route is also selected when the one reading Known kept matches the query. Text and JSON mark it as chosen from known HTTPRoutes, and JSON records the strategy used.
- `--select known` needs the cluster's HTTPRoutes, so with `--file` it is a usage error.
**Alternatives:** Strict only (the previous rule); `known` as the default (rejected: Envoy can still serve a deleted HTTPRoute's route, so the default never guesses).
**Consequences:** `--select known` uses krp's rule. krp narrows with `--route` alone and kgwctl with the cluster's HTTPRoutes, so the two can differ. Scripts can tell how each route was chosen.

## D-033 — Downstream builds set the provenance modules through `pkg/cli` (2026-11-27, phase 4b) — decided by the owner 2026-11-27 (design approval)
**Context:** xdsview v0.7.0 composes claims from modules (xdsview D-023). A downstream build replaces the kgateway module with its own module that wraps it, under its own name. Claim IDs embed the module name and mean something only within one `Compose` result.
**Decision:** `cli.WithProvenanceModules(m ...provenance.Module)` replaces the module set `proxy routes` composes. The default is `kgateway.New()`, and an empty call keeps it. Repeated calls add to one set, as `WithBundleCollectors` does. kgwctl reads claims only through `Resolve`, `ResolveMirror`, `Records`, `Select`, `Narrow` and the assertion types. It never reads `Claim.Module` or `kgateway.ModuleName`, and a test enforces this. The `proxy routes` output carries no claim ID and no module name. `Compose` puts the claim ID, and so the module name, into the message of `claim-dropped` and `link-dropped` warnings; kgwctl never copies that message, it keeps the code and subject and writes its own message. Duplicate module names make `proxy routes` exit 2.
**Alternatives:** Append downstream modules to the default (rejected: a wrapper would duplicate the kgateway claims). Expose claim IDs (rejected: not stable, and they change with the module name).
**Consequences:** A wrapper that adds no claim gives byte-equal output. A wrapper that adds claims gives its own goldens. `pkg/cli` exposes an xdsview type, so a downstream build uses kgwctl's xdsview minor.

## D-034 — `proxy routes` JSON is kgwctl's own payload (2026-11-27, phase 4b) — decided by the owner 2026-11-27 (design approval)
**Context:** v1alpha1 embedded xdsview's model, so every xdsview model change was a kgwctl schema change. xdsview v0.7.0 splits execution (snapshot, chains) from provenance (claims).
**Decision:** `ProxyRoutes` is a tree of kgwctl types: listener, filter chain, virtual host, route. Each route carries its readings, runtime filter chain with attribution, policies, backend and actions. Policy records stay on their own level, labelled with it, and are never merged. Raw `typedConfig`, `typedPerFilterConfig` and `metadata` are left out; `proxy dump` has the raw dump. Occurrence IDs are exposed as `id` and are valid only within one document. Warnings are one deduplicated list of every parser and index warning of the whole dump: JSON carries it as `warnings`, and the text prints it once at the end, with none inline. A network or HTTP filter carries a typed `configSource`: `ecds` or `ecdsDefault` when xdsview resolved the ECDS config, `unresolved` when it could not establish the config for that occurrence (missing, ambiguous, or any network filter with discovery). Execution state (`runs`, `state`) is copied from xdsview and is independent of `configSource`; an unresolved filter can still be `runs: no`. Each HCM's `routeConfig` names its source ID. The payload is a presentation projection: Phase 6 joins runtime policy evidence on `RouteConfigUse.Source` with the full snapshot and claim index, never on this JSON. A test fails when an xdsview model field is neither mapped nor listed as left out.
**Alternatives:** Embed xdsview's v0.7.0 model and claims, as krp does (rejected: it ties kgwctl's schema to xdsview and exposes module claim IDs). Keep the raw config (rejected: it is the main place for credentials Envoy does not redact).
**Consequences:** kgwctl maps new xdsview fields by hand, and the coverage test flags them on each bump.

## D-035 — Release `v0.1.0` with a script; the version comes from build info (2026-11-27, phase 4b) — decided by the owner 2026-11-27 (design approval)
**Context:** `go install …@v0.1.0` passes no ldflags, so the client printed `dev`. kgwctl has no release tooling.
**Decision:** `cmd/kgwctl` takes the version from ldflags, else `debug.ReadBuildInfo` (the module version and `vcs.revision`), else `dev` and `unknown`. `hack/release.sh <tag>` builds linux and darwin for amd64 and arm64 with ldflags. It packs each binary with LICENSE and README, writes SHA-256 checksums, checks `version --client`, and prints the next steps. Each tag builds into its own empty directory, and a dry run writes only to a separate directory. A dry run never writes a publish manifest or prints publish steps. A real build refuses a dirty tree and a HEAD that is not the tag commit. Checksums and the publish file list name exactly the four archives of that build. The controller pushes the exact tag, checks the remote tag against `commit.txt`, checks a clean `go install …@<tag>`, then runs `gh release create --verify-tag` with the listed files. The script never pushes or publishes.
**Alternatives:** goreleaser or a CI release job (rejected for now: more machinery than one release needs).
**Consequences:** Releases are manual. A CI job can call the same script later.
