# Sub-phase 1.5 — Phase review fixes

Kind: stabilization
Base: e6a8ef4
Issues: Q12–Q18 (phase-status.md) | Plan steps: n/a (from reviews/phase-codex.md)

## Problem / goal
The Phase 1 cross-model review found two correctness bugs, three behaviour mismatches with the design or docs, a fragile test and an undocumented embedding rule. Each is fixed here, then Phase 1 closes.

## Expected behaviour

| Input / action | Expected |
|---|---|
| kubeconfig whose exec credential plugin hangs, `--request-timeout 1s` (Q12) | the command exits 1 within about 1.5 s with a timeout error naming the stage; it does not wait for the plugin |
| `version --install a/x` with two installs (Q13) | only install `a/x` is reported |
| `version --install nope/none` with ownership inputs readable (Q13) | exit 1, the same error text as `get`/`describe` for an unknown install |
| `get tp -A --all-controllers` with Deployments denied (Q14) | exit 0, and stderr warns that deployments are unreadable, as without the flag. Only the "hidden objects" explanation is suppressed. |
| a route whose Accepted condition for a parent is Unknown with no `observedGeneration` (Q15) | `get`, `describe` relations and `describe` conditions all agree: not stale, `Unknown (<Reason>)`. D-012 says so. |
| README (Q16) | `incomplete` is described only for `describe`/`version` JSON. `get` fails on an unreadable requested kind. `all` excludes GatewayClass. |
| testenv harness tests under `-race` with parallel packages (Q17) | they check order, not a 300 ms bound |
| `pkg/cli` docs (Q18) | they state that `Execute` replaces klog's logger process-wide and must run before the host starts client-go goroutines |

## Root cause (if known)
- Q12: `BindContext` bounds HTTP transport only; client-go's exec authenticator runs the plugin synchronously without a context.
- Q13: `loadVersion` parses `--install` but never applies it.
- Q14: `NewFilter` suppresses every warning under `--all-controllers` (filter.go:62).
- Q15: parent summaries use `objects.IsStale`; condition rows use `StaleCondition`.
- Q17: `testenv_test.go:121` and `:147` assert wall-clock bounds.

## Acceptance tests
At the command boundary through `internal/testenv` for Q12–Q15. Q12 uses a kubeconfig with an exec plugin script that sleeps. Each must fail before the fix. Q16 and Q18 are doc edits. Q17 tests must pass repeatedly under `go test -race -shuffle=on -count=3`.

## Scope
In: `internal/load`, `internal/app/{version,get,root}.go`, `internal/view/{filter,status,version}.go`, tests and goldens, `README.md`, `pkg/cli` doc, `docs/decisions.md` (D-012 wording), `design.md` if a sentence changes.
Out: Phase 2 carry-over (engine reuse, `pkg/cli` facade); new flags or options.

## Plan / design impact
D-012's stale bullet extends to per-parent entries (Q15).

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] full test suite passes, observed, including shuffled `-race` runs
- [ ] cross-model review of `git diff e6a8ef4..HEAD` + triage
- [ ] phase-1.5-complete.md written; phase-status.md updated; Q12–Q18 ticked with root cause + commit
