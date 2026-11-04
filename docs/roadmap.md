# kgwctl — Roadmap

**Last updated:** 2026-11-04
**Program:** [kgateway-tooling `program.md`](https://github.com/DuncanDoyle/kgateway-tooling/blob/main/program.md) (local checkout: `../kgateway-tooling-claude`)

## Goal
A command-line tool for operators to inspect and troubleshoot kgateway installations: what the Gateway API resources say, whether the controller accepted them, and what Envoy actually runs. It reuses the kgateway-ui Go packages for the Gateway API side and krp's `pkg/envoy` library for the data plane, so CLI and UI report the same facts. It is built to move into kgateway-dev/kgateway (`kgwctl` is a working name).

## Phases

### Phase 1 — Foundation and resource inspection
**Status:** Not started
**Docs:** `docs/phases/phase-1/`
**Waits on:** program edge E2 (kgateway-ui `pkg/cache` + `pkg/engine`, one-shot use)

- Cobra scaffold, kubeconfig and `--context` flags, `version` (client and controller).
- `get` and `describe` for GatewayClass, Gateway, ListenerSet, HTTPRoute and kgateway kinds, with status conditions shown.
- Output: table, `-o yaml`, `-o json`.
- Before design approval: a short design proposal for kgateway maintainers (program P-005), filed only with the owner's go-ahead.

### Phase 2 — Data plane
**Status:** Not started
**Docs:** `docs/phases/phase-2/`
**Waits on:** program edge E1 (krp `pkg/envoy` `v0.1.0`)

- `proxy routes`: the Envoy listeners, filter chains, routes and HTTP filters for a Gateway, optionally narrowed to one HTTPRoute and rule.
- Sources: port-forward to the proxy admin port, or a config dump file.
- Output: tree view (from krp's renderer or an equivalent), `-o json`.

### Phase 3 — Health and analysis
**Status:** Not started
**Docs:** `docs/phases/phase-3/`
**Waits on:** program edge E3 (kgateway-ui analyzer package)

- `check`: controller health, Gateway and route problems, xDS rejections, ranked like the UI's findings.
- `analyze -f <file>`: validate manifests before applying, reporting new, fixed and unchanged problems against the live cluster.

### Phase 4 — Topology and policies
**Status:** Not started
**Docs:** `docs/phases/phase-4/`
**Waits on:** kgateway-ui Phase 5 (direct, inherited and effective policy) — new program edge E7

- `describe` shows attached and effective policies with provenance.
- `get -o graph`: Gateway → route → backend topology as DOT or Mermaid text, written without a graphics library.
- Envoy-side mapping back to Gateway API refs in `proxy routes` (program edge E5).

### Phase 5 — Upstream move
**Status:** Not started
**Docs:** `docs/phases/phase-5/`

- Move into kgateway-dev/kgateway per the agreed proposal: module layout, build isolation from the controller, CI, release artifacts, docs.
- Libraries it depends on are license-clean and vendor-neutral (program edge E6).

## Deferred / out of roadmap
- `proxy diff` (intended vs accepted config) — waits for krp Phase 6; add as a phase when it lands.
- Thin-client mode against the kgateway-ui API — only useful once the UI has per-user auth (its Phase 10).
- Interactive TUI — krp's TUI covers this for now.
- Vendor policy plugins — the plugin hook belongs in Phase 4 design; the plugins live outside this repo (program P-002).
- kubectl plugin / krew packaging — revisit at Phase 5.
