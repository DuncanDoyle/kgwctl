# Phase 4.4 — Extension point, README, live check (Complete)

**Completed:** 2026-11-24
**Range:** b12d3a7..HEAD on `duncandoyle/phase4Design` (setup 163694a; work ba5080b..838c0b8; close commit below)

## Summary
Downstream builds can add bundle collectors, the README documents the bundle, and a live check proves it on kind with planted secrets:
- **Extension point** (`pkg/cli`, `examples/downstream`): `cli.WithBundleCollectors` adds collectors that run in `kgwctl bundle`, appear in `--list-collectors`, honour `--skip` and write through the same Sink. An invalid or duplicate name fails with exit 2 at run time. Downstream canaries never reach the archive; an undeclared content type and a Secret are refused and recorded.
- **README and threat model:** what is collected, what is never requested and how that is enforced, residual risk of derived artifacts and `-n`, redaction limits, "review before sharing", RBAC. Design §6: a reader-only run includes metrics.
- **Live check** (`hack/live-check.sh bundle`, `hack/bundlecheck`): canaries in namespace `kgwctl-canary`, one proxy at debug with a Secret-backed request, full bundle as admin and as reader-only, scan of every member (raw, base64, gzip). The scan reports hits by index, never by value; the debug level is restored by a trap.
- **Reader role (D-028):** `docs/rbac.yaml` adds `udproutes` and three experimental kinds. `TestReaderRoleCoversServedKinds` fails when a test CRD has a kind the role lacks; the CRD README has a bump checklist.

Evidence at the close commit:
- `make test` (race, envtest) exits 0 at 838c0b8, run by the controller: 17 packages ok, `internal/app` 223 s.
- `make lint` and `go mod tidy -diff` are clean.
- `hack/live-check.sh bundle` on kind `kgwctl-live` (kgateway v2.5.1) with stock `docs/rbac.yaml` exits 0: admin 122 files, 0 errors, 5 proxy replicas with `config_dump.json`, `stats.txt` (`version_text`) and Prometheus `metrics.txt`; reader 86 files, 36 `denied`, 11 grant lines (`pods/log` in 6 namespaces, `pods/portforward` in 5); no canary in either archive.
- The guard test failed before the role change (udproutes plus three x-k8s.io kinds missing).

## Sub-phases (phase record only)
n/a

## Reviews
Per-task reviews (Claude subagents, opus), each fix round with a scoped re-review (sonnet):

| Task | Rounds | Main findings fixed |
|---|---|---|
| 4.4.1 `WithBundleCollectors` | 0 | M1 name errors surface at run time (docs) |
| 4.4.2 README | 1 | `server_info` placement; `-n` risk; `--no-endpoints` IPs; port-forward Role scope |
| 4.4.3 live check | 1 | scan printed a canary-bearing member name; leftover canary dump; guard skipped XListenerSet; no trap on debug level |

A cross-model review is not required for a planned sub-phase; the Phase 4 review (Codex) covers 4.4.

## Waivers
none

## Deliverables
- `pkg/cli`: `WithBundleCollectors`; `examples/downstream` collector and tests.
- README `bundle` section, `docs/bundle-threat-model.md`, design §6, plan 4.4.1.
- `hack/live-check.sh bundle`, `hack/bundlecheck` (scan helper with tests).
- `docs/rbac.yaml` reader role, `internal/testenv/rbac_guard_test.go`, CRD README checklist, D-028.

No new modules (`k8s.io/apiextensions-apiserver` moved from indirect to direct).

## Notes for later phases
- **Owner ruling:** the reader role stays an explicit list (D-028). Every CRD bump for a newer kgateway or Gateway API version follows the checklist.
- kind `kgwctl-live` is unhealthy while other kind clusters run on the same colima VM (scheduler crash-looping); the check still passed.

## Deferred / open
- **Minor (phase review to triage):**
  - 4.4.1 M2–M5; `pkg/cli/cli.go:45` "run after the built-in ones" comment; README port-forward advice for a shared controller/Gateway namespace.
  - 4.4.3 M2 failed (not denied) items pass silently; M3 expected grant lines derive from the manifest; `bundlecheck` `extract` errors could print a member path; Ctrl-C during `dump_until` leaves `canary-wait.json`.
- **Downstream:** the downstream build has no reader role or CRD guard for its own kinds; owner to decide.
- **Carried:** xdsview bump to v0.6.0 plus goldens; Snellius v0.9.0 `Scope.Excluded`; generated command reference; the 4.2 and 4.3 minors.
