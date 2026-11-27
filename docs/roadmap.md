# kgwctl — Roadmap

**Last updated:** 2026-11-27
**Program:** [kgateway-tooling `program.md`](https://github.com/DuncanDoyle/kgateway-tooling/blob/main/program.md) (local checkout: `../kgateway-tooling-claude`)

## Goal
A command-line tool for operators to inspect and troubleshoot kgateway installations: what the Gateway API resources say, whether the controller accepted them, and what Envoy actually runs. It reuses the kgateway-ui Go packages for the Gateway API side and krp's `pkg/envoy` library for the data plane, so CLI and UI report the same facts. It is built to move into kgateway-dev/kgateway (`kgwctl` is a working name).

## Phases

### Phase 1 — Foundation and resource inspection
**Status:** Complete 2026-11-06 — `docs/phases/phase-1/phase-1-complete.md`
**Docs:** `docs/phases/phase-1/`
**Waits on:** none — E2 delivered in kgateway-ui v0.3.0

- Cobra scaffold, kubeconfig and `--context` flags, `version` (client and controller).
- `get` and `describe` for GatewayClass, Gateway, ListenerSet, HTTPRoute and kgateway kinds, with status conditions shown.
- Output: table, `-o yaml`, `-o json`.

### Phase 2 — `check`
**Status:** Complete 2026-11-20 — `docs/phases/phase-2/phase-2-complete.md`
**Docs:** `docs/phases/phase-2/`
**Waits on:** none — E3, E8, E9 delivered (Snellius `v0.8.2`)

A support engineer's first command on a cluster they troubleshoot: what is wrong, what could not be checked, what is still deciding. Readable in the terminal, pasteable into Slack or a ticket (D-014).
- All 26 Snellius checks in one pass, built on Snellius `v0.8.2` (E3, E8, E9). xDS rejections stay first in testing (D-010).
- Text report plus `-o json|yaml` keyed by finding code and finding ID, both stable; severity is data on each finding.
- Exit codes: 0 clean, 1 problems found, 2 could not check.
- `--live`: observe for a bounded time so grace periods and holds decide; progress on stderr, one report on stdout (D-016).
- Controller metrics and proxy stats through the apiserver pod proxy; no port-forward (D-017).
- Deferred: `--only`, `--exclude`, `--fail-on`, controller logs (D-015).
- Compared with `glooctl check` (2026-11-20). Candidate checks for Snellius, which owns the rules:
  - proxy xDS `update_failure` growth, which shows a flapping control-plane connection that KGWUI-026 misses;
  - a missing GatewayClass;
  - kgateway CRD drift against the installed chart, as `glooctl check-crds` does.
- Vendor builds add auth and rate-limit checks through the registry: config status, refs to missing configs, and server health and connectivity.
- A repeatable `-n` joins the deferred flags.
- Next step after the Phase 2 MVP (user, 2026-11-09): `-o agent`, an output for AI agents (Codex, Claude, Snellius AI-assisted diagnosis). Per finding: what the code means, the evidence objects, the suggested fix; plus what was not checked or is pending. Cluster text is marked as untrusted data; never Secret material. Phase 2 fixes the finding schema so this format adds no new data. Aim for one finding schema with the Snellius MCP server (a new program edge, Snellius as provider).

### Phase 3 — Data plane
**Status:** Complete 2026-11-21 — `docs/phases/phase-3/phase-3-complete.md`
**Docs:** `docs/phases/phase-3/`
**Waits on:** none — E1 and E11 delivered (xdsview `v0.4.3`)

- `proxy routes`: the Envoy listeners, filter chains, routes and HTTP filters for a Gateway, optionally narrowed to one HTTPRoute and rule.
- Sources: port-forward to the proxy admin port, or a config dump file.
- Output: tree view (xdsview `render.Text`), `-o json|yaml` (`ProxyRoutes` envelope).
- `proxy dump`: one replica's raw config dump, for tickets and `--file`.

### Phase 4 — Support bundle
**Status:** Complete 2026-11-27 — `docs/phases/phase-4/phase-4-complete.md`
**Docs:** `docs/phases/phase-4/`
**Waits on:** none — Phase 3 delivered proxy access (`internal/proxydump` `Fetch`/`FetchGateway`: pod identity, per-capture errors, deadline; D-021)

One command that collects what support asks for, so nobody hand-collects it (D-010).
- Gateway API and kgateway resources, workloads, install metadata, controller and proxy logs (including previous containers), controller metrics, and per-replica proxy config dumps, stats and metrics; `version` and `check` output.
- One `tar.gz`, cluster-wide by default; namespace, install and time-window filters.
- No API request for Secrets or ConfigMaps, enforced on every client, so no Helm values. No controller admin snapshots: `/snapshots/xds` holds private keys, `/snapshots/krt` can return raw ConfigMaps (D-025).
- Mandatory redaction in the archive writer, tested on the extracted archive (D-026).
- Bundle collectors are an extension point for downstream builds (D-009).
- Prior art: `agctl bug-report` ([agentgateway#3991](https://github.com/agentgateway/agentgateway/pull/3991)). Borrow kind discovery by CRD category (`kgateway`), partial results with an `errors.txt` under a global `--timeout`, and previous-container logs only after a restart (D-019).
- Needs `pods/log` in a new optional Role.

### Phase 4b — xdsview v0.7.0 migration and first release
**Status:** Not started (D-031)
**Docs:** `docs/phases/phase-4b/`
**Waits on:** none — E15 delivered (xdsview `v0.7.0`, kgwctl#15); Snellius `v0.9.0` released (kgwctl#16)

- `proxy routes` moves to the xdsview `v0.7.0` contract: parse, then `chain.Effective`, then `provenance.Compose` with the kgateway module, then explicit selection, then render.
- The unresolved-route listing stays, as explicit selection over candidate readings. kgwctl never guesses a route's origin.
- The module set passed to `provenance.Compose` is a seam that downstream builds change through `pkg/cli` (xdsview D-023). Claims are never selected by module name.
- kgwctl builds its own `ProxyRoutes` payload instead of embedding xdsview's `model`.
- The shared envelope moves to `kgwctl.kgateway.dev/v1alpha2`. That changes every command's JSON and YAML, including the bundle's `manifest.json`. Text, JSON and YAML goldens are reviewed together.
- Snellius `v0.9.0`: `install.Scope.Excluded` replaces the local loop from Task 4.2.2.
- The xdsview import allowlist follows the new package layout.
- Close: tag `v0.1.0`, the first release.

### Phase 5 — Full analysis
**Status:** Not started
**Docs:** `docs/phases/phase-5/`
**Waits on:** none — E3 delivered; the analyzer runs in `check` from Phase 2

- `analyze -f <file>`: validate manifests before applying, reporting new, fixed and unchanged problems against the live cluster.

### Phase 6 — Topology and policies
**Status:** Not started
**Docs:** `docs/phases/phase-6/`
**Waits on:** E7 = Snellius Phase 8 declared-policy model, plus the scoped `merge.*` records from E15 for the xDS part. No effective-policy view (D-024, P-008).

The controller owns policy semantics; kgwctl shows evidence and never re-derives precedence, inheritance or merging (D-024).
- `describe` lists policies, each labelled with its source (P-008):
  - declared: `targetRefs`/selectors as written;
  - controller-reported attached: ancestor conditions as the controller reports them, for the target, controller and generation each names;
  - runtime: the `merge.*` records of the listener, RouteConfiguration, virtual host and route (E15), joined with Snellius `pkg/policy` by ref. Each shows the level it came from. They are level-local: they do not say which level governs a request.
- What Envoy runs for the route or listener (xdsview): one replica at capture time, linked from `describe` and back.
- Each link marked verified, inferred (names, selectors) or unknown (no, stale or ambiguous evidence). `route_source` verifies the originating route and rule, not which policy won or merged.
- `get -o graph`: Gateway → route → backend topology as DOT or Mermaid text, written without a graphics library.
- Envoy-side mapping back to Gateway API refs in `proxy routes` (program edge E5).

### Phase 7 — Upstream move
**Status:** Not started
**Docs:** `docs/phases/phase-7/`

- Move into kgateway-dev/kgateway per the agreed proposal: module layout, build isolation from the controller, CI, release artifacts, docs.
- Libraries it depends on are license-clean and vendor-neutral (program edge E6).

## Deferred / out of roadmap
- `kgwctl mcp` and traffic debugging: downstream builds on `pkg/cli` (D-023), not this repo.
- Data-plane convergence (D-024): a candidate phase after Phase 4; it waits on E14, which the program's xDS convergence investigation may or may not create. It replaces a generic intended-versus-accepted `proxy diff`, which has no provider (P-009) and is no longer planned. The support bundle keeps the raw evidence per replica: pod identity, restart count, capture time, `version_text`, listener states (warming, draining) and rejection details where Envoy reports them. These show what each replica held at capture time. They do not show when a NACK happened. A stale-config verdict needs comparable controller evidence per replica; kgateway v2.5.1's snapshot metrics are per Gateway, not per pod.
- Thin-client mode against the kgateway-ui API — only useful once the UI has per-user auth (its Phase 10).
- Interactive TUI — krp's TUI covers this for now.
- Vendor policy plugins — the build-time hook is D-009 (Phase 1); the plugins live outside this repo (program P-002).
- kubectl plugin / krew packaging — revisit at Phase 6.
