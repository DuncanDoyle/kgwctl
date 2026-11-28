# Phase 4.3 — Logs, metrics, Envoy (Complete)

**Completed:** 2026-11-23
**Range:** dd98ce7..HEAD on `duncandoyle/phase4Design` (setup 9d6e3a0; work a19f675..bb63cac; close commit below)

## Summary
The bundle now captures what each controller and proxy replica was doing, and works on restricted RBAC:
- **Writer split** (`internal/bundle`): `writer.go` split by responsibility into records, staging, sink, commit, footers and errtext. No behaviour change.
- **Sink contract (D-027, review item P4-2):** every write method takes a context. The Sink re-reads a Pod source after the read and sets `Source.Identity` (`verified`, `changed`, `gone`, `reread-failed`), all metadata, never a failure. `Sink.Omit` records `skipped: not running`, `skipped: no install` and `disabled: no metrics port` with no member and no error line.
- **Logs** (`collect/logs.go`): every inventory container incl. init, `.previous.log` after a restart, the §3 window, the local 10 MiB cap, `--skip logs` with no `pods/log` request. 4.1 P3-3: `pem-tail` only when the block's opening line was kept, also at the seal. A stream cut by its request timeout reads `truncated: timeout`.
- **Metrics** (`collect/metrics.go`, `scraper.go`): controller metrics on the install's port, proxy metrics on 9091, one guarded `pods/proxy` GET each (the Snellius scraper drops the refused cause; design §6 updated). `denied` on 403, `disabled` when refused on an undeclared port, `failed` otherwise. The guard is idempotent.
- **Envoy captures** (`collect/proxyadmin.go`): per proxy replica `config_dump.json` (EDS by default), `stats.txt`, `certs.json`, `clusters.txt`; `--no-endpoints` drops EDS and clusters. A dial or tunnel failure or a timeout ends the replica after one request. Kubelet error text reaches no member.
- **RBAC** (`docs/rbac.yaml`, README, `internal/bundle/grants.go`): optional `kgwctl-bundle-logs` Role. A reader-only run exits 0 with `denied` items, and `errors.txt` ends with one grant line per grant and namespace, reader grants included.

Evidence at the close commit:
- `make test` (race, envtest) exits 0 at bb63cac plus the close docs and Makefile, run by the controller: 16 packages ok, `internal/app` 226 s.
- `make lint` (vet + gofmt on tracked files) and `go mod tidy -diff` are clean.
- Every new test failed before its change; mutation checks in the SDD task reports (request set with a cluster-wide list, mixed outcomes, double guard, four dials for a hung replica).

## Sub-phases (phase record only)
n/a

## Reviews
Per-task reviews (Claude subagents, opus), each fix round with a scoped re-review (sonnet):

| Task | Rounds | Main findings fixed |
|---|---|---|
| 4.3.0 writer split | 0 | none (pure move verified) |
| 4.3.0b Sink contract (P4-2) | 1 | `Sink.Error` kept a caller-set identity; nil context |
| 4.3.1 logs | 0 | none Important; timeout label carried to 4.3.4 |
| 4.3.2 metrics | 1 | no command-level test; double guard dropped the refused cause |
| 4.3.3 Envoy capture | 1 | a hung replica cost four items and four timeouts |
| 4.3.4 RBAC, partial runs | 1 | grant lines only for Pod subresources; `rbac.yaml` said only `GET /config_dump` |

A cross-model review is not required for a planned sub-phase; the Phase 4 review (Codex) covers 4.3.

## Waivers
none

## Deliverables
- `pkg/bundle`: Sink with context, `Omit`, `OmitReason`; `Source.Identity` output only.
- `internal/bundle`: split writer, Pod re-read, omissions, grant lines (`grants.go`), idempotent guard.
- `internal/bundle/collect`: `logs.go`, `metrics.go`, `scraper.go`, `proxyadmin.go`.
- `internal/app/bundle.go`: hooks and built-ins for the four collectors, `--no-endpoints`.
- `docs/rbac.yaml`, README permissions, threat model; D-027; design §5, §6, §8.1–§8.4 and plan synced.
- Makefile: `lint` runs gofmt on tracked files only.

No new modules.

## Notes for later phases
- **Rulings (from the SDD ledger):** `Error` and `Exclude` take no context (no I/O); omissions have their own bound of 4096 per collector; a fully read text item waiting on its re-read at the seal is kept with `reread-failed`; `TestVersionFile` skips `proxy-admin` (its fake dialer replaces the Pod on every dial).
- **4.3.3 gate miss:** `TestVersionFile` failed from 574c5fe until dc90778 while the 4.3.3 report claimed a clean suite. Close gates are run by the controller, not taken from reports.

## Deferred / open
- **Minor (phase review to triage):**
  - Logs: a second copy of the 10 MiB cap in `logs.go`; the redact subtest checks pem-tail at `line:2` only; app tests send real `pods/log` requests to envtest and record failed log items.
  - Metrics: an unguarded config would let a Pod body spoof `Refused` (no in-tree path); controller port 0 gives `failed`.
  - `TestNamespacedRequestSet` compares distinct request keys, not counts.
- **Carried:** Snellius v0.9.0 `Scope.Excluded`; live per-collector progress on stderr; the 4.2 minors.
