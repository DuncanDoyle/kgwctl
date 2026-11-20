# kgwctl — Roadmap

**Last updated:** 2026-11-20
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
**Status:** Design
**Docs:** `docs/phases/phase-3/`
**Waits on:** E11 (xdsview renderer, effective chain, listener state; xdsview#27) for sub-phase 3.2. E1 delivered (xdsview `v0.3.0`, formerly krp)

- `proxy routes`: the Envoy listeners, filter chains, routes and HTTP filters for a Gateway, optionally narrowed to one HTTPRoute and rule.
- Sources: port-forward to the proxy admin port, or a config dump file.
- Output: tree view (from krp's renderer or an equivalent), `-o json`.

### Phase 4 — Support bundle
**Status:** Not started
**Docs:** `docs/phases/phase-4/`
**Waits on:** Phase 3 (proxy access through kgwctl's `internal/proxydump` `Capture`: pod identity, per-capture errors; D-021)

One command that collects what support asks for, so nobody hand-collects it (D-010).
- Gateway API and kgateway resources, Helm values, controller and proxy logs (including previous containers), proxy config dumps and stats, controller snapshots and metrics from every replica, `version` and `check` output.
- One `tar.gz`; namespace, install and time-window filters.
- Secrets redacted by default, with an explicit opt-in. Redaction is its own tested component.
- Bundle collectors are a registry extension point for vendor builds (D-009).
- Prior art: `agctl bug-report` ([agentgateway#3991](https://github.com/agentgateway/agentgateway/pull/3991)). Borrow kind discovery by CRD category (`kgateway`), partial results with an `errors.txt` under a global `--timeout`, and previous-container logs only after a restart (D-019).
- Open design point: agctl captures ConfigMaps and redacted Secrets. kgwctl's RBAC reads neither, and the bundle needs `pods/log`.

### Phase 5 — Full analysis
**Status:** Not started
**Docs:** `docs/phases/phase-5/`
**Waits on:** none — E3 delivered; the analyzer runs in `check` from Phase 2

- `analyze -f <file>`: validate manifests before applying, reporting new, fixed and unchanged problems against the live cluster.

### Phase 6 — Topology and policies
**Status:** Not started
**Docs:** `docs/phases/phase-6/`
**Waits on:** kgateway-ui Phase 5 (direct, inherited and effective policy) — new program edge E7

- `describe` shows attached and effective policies with provenance.
- `get -o graph`: Gateway → route → backend topology as DOT or Mermaid text, written without a graphics library.
- Envoy-side mapping back to Gateway API refs in `proxy routes` (program edge E5).

### Phase 7 — Upstream move
**Status:** Not started
**Docs:** `docs/phases/phase-7/`

- Move into kgateway-dev/kgateway per the agreed proposal: module layout, build isolation from the controller, CI, release artifacts, docs.
- Libraries it depends on are license-clean and vendor-neutral (program edge E6).

## Deferred / out of roadmap
- `proxy diff` (intended vs accepted config) — waits for krp Phase 6; add as a phase when it lands.
- Thin-client mode against the kgateway-ui API — only useful once the UI has per-user auth (its Phase 10).
- Interactive TUI — krp's TUI covers this for now.
- Vendor policy plugins — the build-time hook is D-009 (Phase 1); the plugins live outside this repo (program P-002).
- kubectl plugin / krew packaging — revisit at Phase 6.
