# Review: Phase 1 (phase diff)

**Reviewer:** Codex (codex exec, read-only)
**Author:** Claude
**Cross-model:** yes
**Scope:** git diff 779c421..e6a8ef4 against roadmap, decisions D-001–D-013, design.md, implementation-plan.md; sub-phase reviews excluded
**Date:** 2026-11-06

---

Changes needed before Phase 1 closes: deadline handling and `version --install` have correctness gaps; several output contracts and embedding seams also need attention.

1. **P1 — Exec credential plugins can bypass the command deadline.**  
   `internal/load/clients.go:32`  
   `BindContext` bounds HTTP transport activity, but client-go v0.36.2’s exec authenticator calls `getCreds()` synchronously and launches credentials with `exec.Command(...).Run()`, without a context. A stalled credential helper can therefore block discovery beyond `--request-timeout`, including after interruption. The existing timeout tests inject HTTP delays and do not cover this supported kubeconfig path.

2. **P1 — `version` silently ignores `--install`.**  
   `internal/app/version.go:66`  
   `loadVersion` parses the global flags but never passes `f.Install` into loading or view selection. `view.Version` then emits every install. Consequently, selecting one of two installs still reports both; selecting a nonexistent or malformed install succeeds instead of following the error behavior of `get` and `describe`. This contradicts design §6.2 and the README’s global flag contract.

3. **P2 — `--all-controllers` suppresses unavailable-input warnings.**  
   `internal/view/filter.go:62`, `internal/app/get.go:64`  
   Every filter warning is suppressed under `--all-controllers`, including ownership failures. The fallback relation warning only covers Gateway and route tables. For example, `get tp -A --all-controllers` with Deployments denied can exit 0 without warning, while classifying controller-written ancestors as `other` because no installs were detected. `describe` reports the unavailable ownership input for the same object. Showing all controllers should suppress the hiding explanation, not the acquisition failure.

4. **P2 — Parent summaries and condition details disagree about staleness.**  
   `internal/view/status.go:170`, `internal/view/describe.go:420`  
   Parent summaries call `objects.IsStale`; condition details call `StaleCondition`, which exempts `Unknown` with zero `observedGeneration`. For a generation-1 route with such an Accepted condition, `get` and `describe.relations.parents` report `stale`, while `describe.conditions` omits `stale`. D-012’s exception and the per-parent design wording need one consistent interpretation before this schema freezes.

5. **P2 — README overstates the structured incomplete-output contract.**  
   `README.md:97`  
   This promises an `incomplete` list whenever an input cannot be read. `get -o json|yaml` intentionally emits a plain Kubernetes List and has no such field; unreadable requested kinds instead fail with empty stdout. Other commands can succeed with available data and unknown dependent sections. Document those distinctions. Also, line 33 says `all` includes every served kind, whereas `internal/resolve/resolve.go:122` explicitly excludes GatewayClass.

6. **P3 — The close-time timing fix leaves equivalent fragile harness assertions.**  
   `internal/testenv/testenv_test.go:121`, `internal/testenv/testenv_test.go:147`  
   These tests still require controller acquisition or an unmatched request to finish within 300 ms. The 1.3 close already established that scheduling under race instrumentation and concurrent packages can exceed that bound. The revised `TestLoadWaitsForProxyPods` correctly checks ordering; these sibling tests can still fail on correct behavior under load.

7. **P4 — The klog fix assumes ownership of process initialization.**  
   `internal/quiet/quiet.go:15`  
   `sync.Once` prevents competing kgwctl initializations, but does not synchronize with an embedding application’s existing client-go goroutines. Its first `cli.Execute` can still race their logging and replaces the host’s logger process-wide. Calling `quiet.Klog` from `testenv.Run` makes this suite safe without exercising that embedding case. Vendor builds need an explicit initialization contract or host-controlled logging behavior.

8. **P4 — The loader discards the state needed to mature Phase 2 checks.**  
   `internal/load/load.go:181`  
   `Load` constructs an engine, computes once and returns only its model. Calling `Load` again at `m.Next.At` creates fresh first-seen times, check stores and verifier persistence, potentially restarting grace periods and holds indefinitely. Phase 2 needs to retain the same engine across recomputations. This also matters when Phase 4 collects `check` results into a bundle.

9. **P4 — Vendor cluster commands cannot reuse the core acquisition path through `pkg/cli`.**  
   `pkg/cli/cli.go:35`, `internal/app/root.go:85`  
   `WithCommands` accepts arbitrary commands, but flag decoding, REST configuration, deadline construction and model loading are all internal. The example’s `hello` command does not exercise this limitation. A downstream cluster command must duplicate these rules or bypass them. Before D-009’s public contract settles, define a narrow facade for that use case while keeping implementation packages internal.

The sampled RBAC rules match design §6.3. I found no additional blocker in the live fixtures or scale scripts. Tests were not run; verification results are author-supplied. Goldens were sampled, and no files were edited.

## Triage

| # | Pri | Verdict | Note |
|---|---|---|---|
| 1 | P1 | accept — Q12 | A stalled exec credential plugin must not hold a command past `--request-timeout`. Return at the deadline with a timeout error. Test with a kubeconfig whose exec plugin sleeps. |
| 2 | P1 | accept — Q13 | `version --install` selects one install, and an unknown value is an error, as in `get` and `describe`. |
| 3 | P2 | accept — Q14 | `--all-controllers` suppresses only the hiding explanation, never input-failure warnings. |
| 4 | P2 | accept — Q15 | One stale rule for object conditions and per-parent entries (`StaleCondition`); D-012 wording aligned. |
| 5 | P2 | accept — Q16 | README: `incomplete` only in describe/version JSON; `get` fails on unreadable requested kinds; `all` excludes GatewayClass. |
| 6 | P3 | accept — Q17 | testenv timing asserts (testenv_test.go:121, :147) become order-based. |
| 7 | P4 | accept — Q18 | Document in `pkg/cli` that `Execute` replaces klog's logger and must run before the host starts client-go goroutines. A host-controlled logging option is deferred to the first vendor build that needs it. |
| 8 | P4 | defer: Phase 2 design | `check` must keep one engine across recomputes so grace periods mature; `Load` returns a model only. Carry-over. |
| 9 | P4 | defer: Phase 2 design | A narrow `pkg/cli` facade for vendor cluster commands (flags, REST config, deadline, load). Carry-over; D-009 follow-up. |
