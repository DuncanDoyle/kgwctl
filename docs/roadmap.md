# kgwctl — Roadmap

**Last updated:** 2026-11-05
**Program:** [kgateway-tooling `program.md`](https://github.com/DuncanDoyle/kgateway-tooling/blob/main/program.md) (local checkout: `../kgateway-tooling-claude`)

## Goal
A command-line tool for operators to inspect and troubleshoot kgateway installations: what the Gateway API resources say, whether the controller accepted them, and what Envoy actually runs. It reuses the kgateway-ui Go packages for the Gateway API side and krp's `pkg/envoy` library for the data plane, so CLI and UI report the same facts. It is built to move into kgateway-dev/kgateway (`kgwctl` is a working name).

## Phases

### Phase 1 — Foundation and resource inspection
**Status:** In progress — design approved 2026-11-05; 1.1 (foundation, `version`) and 1.2 (`get`) complete 2026-11-05
**Docs:** `docs/phases/phase-1/`
**Waits on:** none — E2 delivered in kgateway-ui v0.3.0

- Cobra scaffold, kubeconfig and `--context` flags, `version` (client and controller).
- `get` and `describe` for GatewayClass, Gateway, ListenerSet, HTTPRoute and kgateway kinds, with status conditions shown.
- Output: table, `-o yaml`, `-o json`.

### Phase 2 — `check`
**Status:** Not started
**Docs:** `docs/phases/phase-2/`
**Waits on:** program edge E8 (kgateway-ui health checks callable one-shot)

One-shot install and config health, for CI gates after install or upgrade and as the first step in support triage.
- xDS rejections are built and tested first: "Accepted, but Envoy rejected it" is the costliest support theme (D-010).
- Version skew between controller, proxies and Gateway API CRDs (the library's skew check).
- Controller and proxy Deployments and Pods ready; Gateway API and kgateway resources with failing or missing status conditions (including `ResolvedRefs`); routes not attached; xDS rejections from the controller's metrics.
- Documented exit codes, the same in every output format: 0 clean, 1 problems found, 2 could not check.
- `-o json` keyed by finding code and finding ID, both stable; severity is data on each finding.
- `--exclude` / `--only` by check name or code, `--fail-on <severity>`, `--timeout`.
- Read-only by default: no port-forward unless a flag allows it. Signals that need two samples or a time window are reported as pending, not failing.

### Phase 3 — Data plane
**Status:** Not started
**Docs:** `docs/phases/phase-3/`
**Waits on:** program edge E1 (krp `pkg/envoy` `v0.1.0`)

- `proxy routes`: the Envoy listeners, filter chains, routes and HTTP filters for a Gateway, optionally narrowed to one HTTPRoute and rule.
- Sources: port-forward to the proxy admin port, or a config dump file.
- Output: tree view (from krp's renderer or an equivalent), `-o json`.

### Phase 4 — Support bundle
**Status:** Not started
**Docs:** `docs/phases/phase-4/`
**Waits on:** Phase 3 (proxy admin access through krp `source`)

One command that collects what support asks for, so nobody hand-collects it (D-010).
- Gateway API and kgateway resources, Helm values, controller and proxy logs (including previous containers), proxy config dumps and stats, controller snapshots and metrics from every replica, `version` and `check` output.
- One `tar.gz`; namespace, install and time-window filters.
- Secrets redacted by default, with an explicit opt-in. Redaction is its own tested component.
- Bundle collectors are a registry extension point for vendor builds (D-009).

### Phase 5 — Full analysis
**Status:** Not started
**Docs:** `docs/phases/phase-5/`
**Waits on:** program edge E3 (kgateway-ui analyzer package)

- `check` grows to the full ranked analysis of the UI analyzer (references, conflicts, rollouts, CRD and version skew, backends without endpoints).
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
