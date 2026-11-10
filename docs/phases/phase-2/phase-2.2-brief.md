# Sub-phase 2.2 — `check`

Kind: planned
Base: (set in the setup commit; see phase-status.md)
Issues: n/a | Plan steps: implementation-plan.md Tasks 2.2.1–2.2.6

## Problem / goal
A support engineer has no single kgwctl command that says what is wrong with a kgateway install. 2.2 adds `kgwctl check`: every Snellius check over the selected installs in one pass, a paste-friendly text report, `-o json|yaml`, exit codes 0/1/2 (design §3.1, §3.3, §3.4, §7).

## Expected behaviour

| Input / action | Expected |
|---|---|
| healthy install | report with header, `PASSED: N checks`, exit 0 |
| controller Deployment not ready | KGWUI-005 under ERRORS or WARNINGS (engine severity), exit 1 |
| route with a backendRef to a missing Service | KGWUI-013 with a next step and fix, grouped under "Likely cause: Service …", exit 1 |
| controller reports active xDS rejects | KGWUI-001, exit 1 |
| proxy disconnected per Envoy stats | KGWUI-026, exit 1 |
| a check still holding | listed under STILL DECIDING with count, decision time, reason; hint names the earliest `hold.until`; exit unchanged |
| `pods/proxy` forbidden | metrics/stats checks under NOT CHECKED with the error text and a pointer to `docs/rbac.yaml`; other findings intact; exit from findings |
| a core kind (e.g. httproutes) forbidden | report printed, exit 2 |
| no kgateway install | exit 2, "no kgateway install found" plus Phase 1's hint |
| discovery slower than `--request-timeout` | exit 2, empty stdout, within the timeout + 1s |
| a scrape that hangs | `check` returns at `--request-timeout`; that input under NOT CHECKED |
| `-o json` / `-o yaml` | exactly one envelope, kind `Check`, schema per design §7 |
| `-n shop` | only findings in `shop` plus findings without a namespace; exit counts only those |
| `vendorctl check` with the example pack | the vendor check's finding and hold render like core ones |
| stdout not a terminal | no ANSI escapes |
| `get` / `describe` / `version` | unchanged |

## Root cause (if known)
n/a (new command).

## Acceptance tests
Command-level tests through `app.Execute` against testenv, as listed in Tasks 2.2.1–2.2.5; the live check of Task 2.2.6 on kind.

## Scope
In: `internal/observe` (new), `internal/view/check.go`, `internal/print/check.go`, `internal/print/term.go`, `internal/app/check.go`, `internal/app/exit.go`, `internal/app/root.go`, `internal/testenv` (subresource matching), `pkg/cli/cli.go` (`logIgnored`), `examples/downstream`, `docs/output.md`, `docs/rbac.yaml`, `README.md`, `hack/live-check.sh`, `hack/live/fixtures.yaml`.
Out: `--live`/`--timeout` (2.3); `load.Load` behaviour; list-only kinds in `get`/`describe`; any per-code or per-check-name logic.

## Plan / design impact
none (design r3, plan as reviewed in reviews/design-codex*.md)

## Definition of done
- [ ] acceptance tests fail before, pass after
- [ ] full test suite passes (`make test`, `go vet ./...`, gofmt)
- [ ] live check on kind run and recorded (or Docker unavailable, stated)
- [ ] phase-2.2-complete.md written; phase-status.md updated
