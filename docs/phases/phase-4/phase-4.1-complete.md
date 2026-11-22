# Phase 4.1 — Contract, redaction, writer (Complete)

**Completed:** 2026-11-22
**Range:** 40a50b6..9d61761 on `duncandoyle/phase4Design`

## Summary
The support bundle core, with no cluster access yet:
- `pkg/bundle` is the public collector contract (`Collector`, `Env`, `Sink`, `Path`, `Source`, `Scope`, `Replica`, `ExcludeReason`).
- `internal/redact` redacts each content type: Kubernetes objects, Envoy config dumps, JSON, Prometheus text, logs and errors. A canary corpus covers every design §10 row.
- `internal/bundle` holds the manifest, the writer and the runner. The writer redacts before staging, enforces item caps and `--max-size`, spills to a `0600` spool, seals at the deadline and commits in a fixed order. The runner runs collectors under one deadline and records panics, timeouts and skips.
- `docs/bundle-threat-model.md` lists assets, what is never requested, the redaction rules, the RBAC grants, archive handling and the residual risks.

Evidence at 9d61761:
- `make test` (race, envtest) exits 0.
- `go test -race -count=1 ./internal/bundle/... ./internal/redact/... ./pkg/...` passes in 47 s.
- `go vet ./...` and gofmt are clean, and `go mod tidy -diff` is empty.
- `pkg/bundle` imports no Cobra or `internal/` package. `internal/redact` imports no bundle package.
- Every corpus case goes through the production writer. The extracted archive is scanned for canaries in members, names, footers, decoded base64 runs and nested gzip. The replay also asserts the expected rule and location, and that useful content is kept. Breaking the redaction on purpose makes 78 and 30 replay assertions fail.

## Sub-phases (phase record only)
n/a

## Reviews
- Per-task reviews (Claude subagents): Tasks 4.1.1–4.1.5 approved after fix rounds. The redactor and writer reviews found leaks: timestamped debug headers, `earlyRequestHeaderModifier`, escaped JSON, a Code-0 Status message. All were fixed.
- reviews/4.1-codex.md: 8 P1, 4 P2, 3 P3, 2 P4. 15 accepted and fixed in fix waves A and B. P3-3, P4-1 and P4-2 were deferred to plan notes in Tasks 4.2.1, 4.2.4 and 4.3.1.
- reviews/4.1-codex-r2.md: 7 open, all accepted and fixed.
- reviews/4.1-codex-r3.md: 6 accepted and fixed. 1 was rejected: the injected test clock can't cancel a context in production.
- reviews/4.1-codex-r4.md: 4 accepted and fixed. The author closed the loop here, so no re-check of the r4 fixes ran. The phase review covers them.

## Waivers
none

## Deliverables
- `pkg/bundle/`.
- `internal/redact/`, with `testdata/canary/` (21 rows).
- `internal/bundle/` (manifest, writer, stage, dest, run, flags) and `internal/bundle/bundletest/`.
- `docs/bundle-threat-model.md`.
- The `BundleManifest` section of `docs/output.md`.
- Design §7, §7.1, §8.3 and §8.4 synced with the code.

Dependencies: `github.com/prometheus/common` and `go.yaml.in/yaml/v2` become direct requirements. No new modules.

## Notes for later phases
- **Paths:** `ValidatePath` accepts only Kubernetes name grammar or `pkg/bundle` constants, plus a file extension. Collectors pass real object names, never captured data.
- **Map keys in findings:** `redactions.json` locations show `[key]` for every key below a map field. Rules match on the uncapped key path; the 512-byte cap applies to the displayed location only.
- **Object YAML:** written by streaming encode into a limit writer, so memory stays near the item cap.
- **Test time:** the `internal/bundle` race suite takes about 46 s. Keep new tests on the small test limits.
- **Size:** `internal/bundle` is about 1,900 lines of non-test code, and `writer.go` is about 1,100. Codex rated a split non-blocking. Split before 4.3 adds more.

## Deferred / open
- P4-1, Task 4.2.4: long object names exceed the 255-byte component limit. Decide a bounded, collision-safe file name.
- P4-1, Task 4.2.1: the command passes the `<apiserver>` placeholder as the manifest server.
- P4-2, before Task 4.3.1: decide how the Sink records post-capture identity on a written item, and intentional outcomes (`skipped: not running`, `disabled`).
- P3-3, Task 4.3.1: `pem-tail` finding accuracy. This is reporting only; nothing leaks.
- Minor:
  - over-redaction under credential-named objects (`serviceAccountToken.audience`, `sds_config` versions);
  - a stray PEM BEGIN marker redacts the rest of a log;
  - `run.log` lines follow completion order;
  - hung collectors outlive `Run` by design.
- Split `internal/bundle/writer.go` into files before 4.3.
