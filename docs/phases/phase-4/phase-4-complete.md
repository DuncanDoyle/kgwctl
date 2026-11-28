# Phase 4 — Support bundle (Complete)

**Completed:** 2026-11-27
**Range:** 38acaa0..HEAD of `duncandoyle/phase4Design` (merged into main)

## Summary
`kgwctl bundle` writes one `tar.gz` with what support asks for, so nobody collects it by hand:
- Gateway API and kgateway resources, workloads, install metadata and Events.
- Controller and proxy logs, including the previous container after a restart.
- Controller metrics per replica.
- Per proxy replica: `config_dump.json`, `stats.txt`, `metrics.txt`, `server_info.json`, `certs.json` and `clusters.txt`.
- `version` and `check` output.
- `manifest.json`, which lists every item with its status, and `errors.txt` with the failed, denied and skipped items, plus RBAC grant lines that fix the denials.

Default scope is cluster-wide. `-n`, `--install`, `--since`, `--tail`, `--skip`, `--no-endpoints`, `--max-size` and `--timeout` narrow it.

Safety:
- No client requests Secrets or ConfigMaps, and no client follows redirects. A guard on every bundle client enforces this (D-025, D-026).
- Mandatory redaction in the archive writer, tested on the extracted archive.
- The public `pkg/bundle.Sink` accepts only typed content (D-029).

Outcome: once the output file exists, the archive is always sealed. Exit 0 means it holds cluster data; exit 2 means it does not (D-030).

Downstream builds add collectors through `cli.WithBundleCollectors` (D-009). `docs/rbac.yaml` keeps an explicit reader role, guarded by `TestReaderRoleCoversServedKinds` (D-028), plus optional `pods/portforward` and `pods/log` Roles.

## Sub-phases (phase record only)
| Sub-phase | Kind | Issues | Description |
|---|---|---|---|
| 4.1 | planned | — | Contract, redaction, writer, runner, threat model (phase-4.1-complete.md) |
| 4.2 | planned | — | Command, guard, scope, inventory, Envoy transport, cluster-state collectors (phase-4.2-complete.md) |
| 4.3 | planned | — | Writer split, Sink contract (D-027), logs, metrics, Envoy capture, RBAC and partial runs (phase-4.3-complete.md) |
| 4.4 | planned | — | Extension point, README, live check with canaries, reader role (D-028) (phase-4.4-complete.md) |
| 4.5 | stabilization | — | Phase review fixes: guard paths and redirects, time bounds, outcomes, D-029, D-030 (phase-4.5-complete.md) |

## Reviews
- reviews/design-codex.md (+ -r2 to -r9): design and plan, 9 rounds; owner go-ahead 2026-11-22 (r5) and on the r6 additions.
- reviews/4.1-codex.md (+ -r2 to -r4): the redaction and writer sub-phase, reviewed cross-model on its own because it holds the safety contract. All findings fixed, except 3 deferred into later plan tasks and 1 rejected.
- Planned sub-phases 4.2–4.4: per-task Claude reviews; the phase review covers them.
- reviews/phase-codex.md: 15 findings (14 from the reviewer, 1 from the author); 12 accepted and fixed in 4.5, 1 rejected (#7, deliberate and tested), 2 deferred (#6 bounded acquisition, #13 `capturedAt` skew).
- reviews/4.5-codex.md (+ -r2 to -r7): 8 findings, then six re-review rounds. The r3 loop on the seal rule went to the owner (D-030). The fix-loop cap was passed once by ruling (r6). Open at the close: 3 from the first round, 3 from r6, 1 from r7; all are deferred below.

## Waivers
none

## Deliverables
- Packages: `pkg/bundle` (Sink, Source, collectors API), `internal/redact`, `internal/bundle` (manifest, writer, guard, inventory, grants, commit), `internal/bundle/collect` (resources, workloads, install, version, logs, metrics, proxy admin), `internal/app/bundle*.go`, `pkg/cli` `WithBundleCollectors`, `examples/downstream`.
- Docs: README `bundle`, docs/output.md `BundleManifest`, docs/bundle-threat-model.md, docs/rbac.yaml, D-025 to D-030.
- Tests: redaction canaries (`internal/redact/testdata/canary`), the guard and reader-role tests, `hack/live-check.sh bundle` with `hack/bundlecheck`.
- Dependencies: `github.com/prometheus/common`, `go.yaml.in/yaml/v2` and `k8s.io/apiextensions-apiserver` become direct. No new modules.

Evidence at the close (5ab78b9):
- `make test` (race, envtest) exits 0, 17 packages.
- `make lint` and `go mod tidy -diff` are clean.
- The live check passed at ff292ee (4.5 record).
- Owner try-out on kind `kgwctl-live` (kgateway v2.5.1), 2026-11-27: `version`, `get`, `describe`, `check`, `proxy`, and `bundle` as admin and as reader. Owner: "All looks good."
- During the try-out, `hack/live-check.sh proxy` failed: its port-forward Role extract ran into the `pods/log` block added in 4.3. Fixed in 5ab78b9.

## Notes for later phases
- A JSON envelope change also changes `manifest.json`. `docs/output.md` names the fields readers may rely on.
- Convergence: the bundle is an offline record only (E14). Per replica it keeps pod identity and UID, restart counts, start time, the time of each capture, and the config dump with `version_text`, listener states and the rejection details Envoy reports. It leaves out the controller's admin snapshots (D-025), so it can't say whether a proxy runs what the controller intends.
- kind `kgwctl-live` is unhealthy while other kind clusters run on the same colima VM. Stop them before a live check.
- Route attribution without `enableRouteSourceMetadata` stays ambiguous by design (kgateway#15067). The try-out cluster runs with it off.

## Deferred / open
- phase-codex #6 (P3 architecture): list pages are buffered whole before the size cap; bounded acquisition needs a design amendment.
- phase-codex #13 (P4): `capturedAt` includes the Pod re-read.
- 4.5-codex #3: a hung exec credential plugin blocks the seal (documented).
- 4.5-codex #4: resource discovery is abandoned, not cancelled.
- 4.5-codex #5: live kinds outside the fixed registry skip the scope rules (D-028).
- 4.5-codex-r6 #2: `check` and proxy capture build their clients after the output file is created.
- 4.5-codex-r6 #4: no positive truncation test.
- 4.5-codex-r6 #5 (P4): manifest overflow can hide the only collector that counts as cluster data.
- 4.5-codex-r7 (P2): `check.json` dropped by the archive budget at commit leaves `check` `partial` with no report.
- Owner item: project check evidence to known fields instead of opaque `json.RawMessage` (D-029).
- The parked Minor findings in phase-4.1 to phase-4.5-complete.md.
- Phase 3 carry-over: a test that cancels an established capture through a CONNECT or SOCKS5 proxy.
