# Phase 2 — `check`: Design

Status: r2, approved by the owner 2026-11-10 (r2 adds §6.1 on the owner's request). Base: `980d32d`.
Inputs: the brainstorm with the owner (2026-11-09/10), Codex reviews of cluster-derived timing and live mode (owner's Desktop; summarised in §2), Snellius `v0.7.0-alpha.1` (E8, E3, E9).

## 1. Goal and scope

`kgwctl check` is **a support engineer's first command on a cluster they troubleshoot** (owner). It runs every Snellius health check against one or more kgateway installs and prints what is wrong, what it could not check, and what is still deciding. The engineer reads it in the terminal; if they can't fix the problem, they paste it into Slack or a Zendesk ticket.

In scope:
1. Move to Snellius `github.com/DuncanDoyle/snellius@v0.7.0-alpha.1` (kgwctl#5–#7), no behaviour change to `get`/`describe`/`version`.
2. `check`: all 26 KGWUI checks in one pass, text and `-o json|yaml`, exit codes 0/1/2.
3. `check --live`: observe for a bounded time, show progress on stderr, print one final report.
4. Controller metrics and proxy stats read through the apiserver (`pods/proxy`), on by default.

Non-goals (deferred, D-015):
- `--only`, `--exclude`, `--fail-on`: CI-gate features; add when a CI user asks.
- `-o agent`: the next step after this phase (roadmap). Phase 2 fixes the finding schema so that format adds no new data.
- Controller logs as evidence (`pods/log`).
- A `pkg/cli` facade for vendor cluster commands (carry-over from Phase 1, moved to the phase that first needs it).
- `analyze -f` (stays in Phase 5).

### Anti-goals
- **Early or false findings.** kgwctl never invents timing: no cluster timestamps stand in for observed duration (D-016). A check that has not decided says so.
- **Green that means "not looked".** A check that could not run or is still deciding is never shown as passed.
- **Secrets or cluster-internal detail in paste output.** No Secret data, no server URL, no tokens.
- **Re-implementing Snellius rules** in the CLI (D-003). kgwctl renders what the engine decides.

## 2. Evidence

| Decision | Source |
|---|---|
| Support first look, paste-friendly, full mode via `-o json/yaml` | owner, 2026-11-09 |
| Run all 26 checks now; the roadmap's Phase 2/5 split is outdated | owner agreed 2026-11-09; E3 delivered |
| Priority themes: install down, route doesn't work, controller stuck, accepted but traffic broken | owner, 2026-11-09 |
| No cluster-derived timing: deletions, requeues, webhooks and clock skew leave no usable timestamp; it would report early or false findings | Claude + Codex, owner KISS rule, 2026-11-10 |
| `--live`, not `--wait`; progress on stderr, one report on stdout; stop at `--timeout` or Ctrl-C, never on "nothing pending" | owner asked; Claude + Codex agreed, 2026-11-10 |
| Holds exposed by the engine (E9), counts only | snellius#33, shipped `v0.7.0-alpha.1` |
| `pods/proxy` on by default; logs off; CI flags and `pkg/cli` facade deferred | owner, 2026-11-10 |
| A kgwctl release depends only on a final Snellius release | owner, 2026-11-10 |

Snellius facts this design relies on (all at `v0.7.0-alpha.1`):
- One engine computes per install; `health.Report` has `State`, `Findings`, `Checks`, `Counts`. Finding IDs and codes are stable (Snellius D-028, D-040).
- `CheckResult` has `OK`, `Required`, `Pending`, `Unavailable` (off/denied/failing/not-on-version/not-observed) and `Hold{Until, Count, Reason}`.
- Hold, grace and first-seen state lives in the engine's memory, so every new process starts its holds again. This is why `--live` exists.
- `Engine.Run` recomputes on cache changes, on due deadlines (`Model.Next`) and on a safety timer. Collectors (controller metrics, proxy stats) are exported and built from the registry (`reg.PackCollectors`); `pkg/app` is not needed.
- `diagnose` groups findings by likely cause and builds next-step commands; `diagnose.Fix` gives a structured fix for 013 and 014.

## 3. Commands

### 3.1 `kgwctl check`

```
kgwctl check [--live [--timeout 6m]] [-o text|json|yaml]
             [--install ID | --all-controllers] [-n NS | -A] [global flags]
```

1. Acquire inputs (§4.1) within `--request-timeout` (default 30s).
2. Compute once all inputs are in, or at the deadline.
3. Print the report (§3.3) and exit (§3.4).

`-n NS` shows only findings on resources in that namespace, plus findings with no namespace (install-level). `-A` and no `-n` show everything. Checks always run over the whole install; `-n` filters what is printed and what the exit code counts. `--install` and `--all-controllers` select installs as in Phase 1 (design §6.2).

A plain run ends with a hint only when there is a reason: "2 checks are still deciding (up to 4m48s). Run `kgwctl check --live` to see them decide." The duration is the latest `hold.until` minus now. Denied or failing inputs get their remedy instead (more time won't help).

### 3.2 `kgwctl check --live`

Keeps one cache, engine and set of collectors alive and lets the engine recompute until:
- `--timeout` passes (default **6m**: 003's 60s grace plus 018's 5m hold, rounded up), or
- the user presses Ctrl-C (a second Ctrl-C exits at once, without a report).

It never stops because nothing is pending: a clean run may be a fix just applied, a rollout, or a flapping proxy.

Progress goes to **stderr, only when stderr is a terminal**. Off a terminal, stderr carries only warnings and errors. The first version appends timestamped lines; no redraw, no TUI library:

```
14:02:10  observing prod: kgateway-system/kgateway for 6m (Ctrl-C: stop and report)
14:02:12  ERROR    KGWUI-001  Gateway edge/public: Envoy rejects configuration
14:02:12  WAITING  status progress  12 held; decides 14:07:12 (Waiting to see if status catches up)
14:02:40  CLEARED  KGWUI-003  HTTPRoute shop/cart: not attached
14:03:10  1:00 / 6:00   1 error, 1 check deciding
```

Lines are written for: a finding that appears or clears, a hold that starts, changes count or ends, an input that becomes available or unavailable, and a heartbeat every 30s.

`--live` uses the same report as `check` (§3.3), plus an **Observed during this run** section: findings that appeared and cleared during the run, each with first-seen and cleared time. A resolved intermittent failure is evidence; it must not disappear from the paste.

`--timeout` without `--live` is a usage error (exit 1). `--live` with `-o json|yaml` writes exactly one document at the end, never a stream.

### 3.3 Report

Text layout (not a stable contract; the builder may refine it):

```
kgwctl check · context prod · 2026-11-10 14:02:10 UTC · kgwctl v0.1.0-dev
Install kgateway-system/kgateway (kgateway v2.5.1) · state: error

ERRORS (1)
  KGWUI-001  Gateway edge/public  Envoy rejects configuration
             2 of 3 proxies reject the latest config since 13:58:01.
             Next: kubectl --context prod -n edge describe gateway public

WARNINGS (2)
  KGWUI-003  HTTPRoute shop/cart  Route not attached to its parent
             ...
     └ caused by KGWUI-013  HTTPRoute shop/cart  backendRef Service shop/cart-v2 not found
             Fix: create Service shop/cart-v2, or change the backendRef

STILL DECIDING (1)
  status progress   12 held  decides 14:07:12  Waiting to see if status catches up

NOT CHECKED (1)
  proxy connection, proxy config warming   denied: get pods/proxy — see docs/rbac.yaml

PASSED: 17 checks

1 error, 2 warnings, 1 check deciding.
2 checks are still deciding (up to 5m). Run `kgwctl check --live` to see them decide.
```

Rules:
1. Header: context name, time in UTC, kgwctl version; per install its ID, kgateway version and state. No server URL.
2. Findings by severity (error, warning, info), then in the order of Snellius's diagnosis (likely cause first). Findings `diagnose` groups under one cause print under it.
3. Each finding: code, resource, message, detail, `since` when known, next-step commands with `--context` filled in, and a fix when `diagnose.Fix` has one.
4. **Still deciding:** every check with a `hold`: name, count, decision time, reason (§6.1: no per-check unit words). Checks that are `Pending` without a hold also appear here.
5. **Not checked:** every check with `Unavailable` set, grouped by cause, with the remedy (RBAC rule, flag, or "not on this kgateway version").
6. **Passed** counts only checks that are OK, have no hold, and are not unavailable.
7. No colour or box-drawing characters when stdout is not a terminal. In a terminal, severity colours are allowed but `NO_COLOR` turns them off.
8. Multiple installs: one block per install, one summary line at the end.

JSON/YAML: §7.

### 3.4 Exit codes

| Code | When | Precedence |
|---|---|---|
| 2 | Could not check: Ctrl-C; the deadline passed before the core kinds synced; no install found or selected; a kind the checks need could not be read (`cluster access` raised KGWUI-008) | highest |
| 1 | At least one shown finding has severity error or warning; or a usage error | |
| 0 | Otherwise. `info` findings, holds and optional inputs that were not checked don't change it | |

With exit 2 the report is still printed when there is one; findings are never dropped. Usage and setup errors (bad flag, no kubeconfig) stay as Phase 1: exit 1, empty stdout, `Error:` on stderr. `get`/`describe`/`version` keep 0/1.

## 4. Architecture

### 4.1 One acquisition path, two stopping rules

A new `internal/observe` package owns a running cache, engine and collectors for one command. `load.Load` stays as it is for `get`/`describe`/`version`.

```
observe.Start(ctx, clients, reg, opts)  // setup.Build, cache.Start, collectors, Engine.Run
  -> Session                            // Current(), Events(), Complete(), Stop()
check:        wait Complete() or deadline -> report from Current()
check --live: also stream Events() to stderr until --timeout / Ctrl-C -> report
```

1. `setup.Build` with Events, CRDs, Services/EndpointSlices and proxy pods on (`NoEvents`, `NoCRDs`, `NoEndpoints` false). `Changes` stays on: the engine recomputes on it.
2. Collectors come from the registry: `reg.PackCollectors(env)` and `RunCollectors`, so a vendor build's collectors run too (D-004). kgwctl stops ignoring registry collectors for `check` only; `logIgnored` in `pkg/cli` is updated to match.
3. `Engine.Run` drives recomputes; kgwctl reads `Engine.Current()` after each published model (`OnLoop` `EvPublished`). Holds mature because the engine wakes at `Model.Next`.
4. **Complete** means: core kinds synced; each optional watch (Events, CRDs, Services, EndpointSlices, proxy pods, list-only kinds) is OK or has an error; each started collector has published or failed once; and a model was computed after all of that. This is what Snellius's `setup` doc says a one-shot caller must wait for, otherwise output varies between runs.
5. At the `check` deadline, inputs that are not in become "not checked (timed out)". Core kinds not synced is exit 2.
6. `kinds.Monitor` does not run: served kinds are fixed for the run. A CRD installed during `--live` is not seen (documented limit).

### 4.2 Diffing models for progress

`--live` compares each published model with the previous one by finding ID, check hold and input availability, and emits the events of §3.2. It also keeps the first-seen and cleared times for the "Observed" section. The diff is presentation only; it never changes what the engine decides.

### 4.3 Shutdown

On stop, cancel the context passed to `cache.Start`, the collectors and `Engine.Run`; wait for `cache.Wait()` and the collector goroutines; then print. Phase 1's `TestNoAccumulation` and `waitNoWatches` invariants hold after `check` and after `check --live`.

### 4.4 Timing

kgwctl takes every time from the engine (`hold.until`, `since`, `Model.Next`) and its own clock for the observation window. It derives nothing from cluster timestamps (D-016).

## 5. Inputs and permissions

| Input | Checks | How | RBAC added to `docs/rbac.yaml` |
|---|---|---|---|
| Gateway API and kgateway kinds, Pods, Deployments, Namespaces, CRDs | most | watch via cache | add any kind the Snellius chart reads that Phase 1 lacks (e.g. `referencegrants`, list-only kinds) |
| Warning Events | 020 | watch | `events` get/list/watch |
| Services, EndpointSlices | 023 | watch | `services`, `discovery.k8s.io/endpointslices` get/list/watch |
| Controller metrics | 001, 007, 022 (reported version), 024 | `ctrlmetrics` collector via `PodProxyScraper` | `pods/proxy` get |
| Proxy stats | 025, 026, 001 evidence | `proxystats` collector via the apiserver pod proxy | `pods/proxy` get |
| Controller logs | evidence only | off | none |

No port-forward. A denied input never fails the run: its checks show under **Not checked** with the RBAC line to add. The rbac.yaml header's "no pods/portforward" stays; "no */proxy" changes to allow `pods/proxy` get, with a comment saying why.

Builder check: `docs/rbac.yaml` must list every kind and subresource the checks read at the Snellius tag; compare with the Snellius chart ClusterRole.

## 6. Installs and scope

Same as Phase 1: installs come from Snellius discovery; `--install` pins one; `--all-controllers` includes installs of other controllers. Pack checks run only on installs whose `Impl` matches the pack (Snellius `v0.6.0`). `check` reports every selected install. With none found: exit 2, "no kgateway install found" plus the hint from Phase 1's `get`.

### 6.1 Vendor distributions

A vendor distribution of kgateway will layer its own checks on top of the core's: are its external auth, rate-limit and WAF servers running and reachable, and are its own policy kinds accepted. It ships a vendor build of kgwctl through `pkg/cli` with its own Snellius registry (D-004, D-009). `check` must work for that build without changes to kgwctl:

1. **Checks, kinds, detectors, collectors and next-step commands come from the registry**, never from lists in kgwctl. A vendor adds them as a Snellius pack (`extension.WithPack`: `Kinds`, `Detectors`, `Checks`, `Collectors`, `Impl` hooks) or with `WithChecks`/`WithCollectors`.
2. **No kgateway special cases in `check`.** kgwctl never filters installs by `Impl == "kgateway"`; the engine picks each install's checks (`ChecksFor`) and each pack's collectors cover its own installs.
3. **Rendering is generic.** No table in kgwctl keyed by finding code or check name: unknown codes, check names, hold reasons and evidence render from the result alone. Hold counts print as a number with the engine's reason; there is no per-check unit word.
4. **Remedies are generic.** "Not checked" shows the engine's or collector's error text and points at the build's RBAC document; kgwctl has no per-check RBAC table.
5. **RBAC is per build.** `docs/rbac.yaml` covers the core; a vendor build documents its additions.
6. `examples/downstream` proves it: it registers a pack with one check, one collector and one policy kind, and a test asserts the check's finding and hold appear in `vendorctl check` text and JSON, and its collector runs.

## 7. Output contract (`-o json|yaml`)

Envelope as Phase 1: `{schemaVersion: "kgwctl.kgateway.dev/v1alpha1", kind: "Check", item}`. kgwctl defines its own view types; it does not marshal Snellius types directly, so a Snellius field rename can't silently change kgwctl's contract.

```yaml
item:
  context: prod
  generatedAt: 2026-11-10T14:02:10Z
  installs:
  - install: {id: kgateway-system.kgateway, namespace: kgateway-system, name: kgateway, version: v2.5.1}
    state: error                  # error | warning | unknown | healthy, from Snellius
    counts: {error: 1, warning: 2, info: 0}
    findings:
    - id: 3f2a…                   # stable
      code: KGWUI-001
      slug: xds-rejected
      severity: error
      resource: {group: gateway.networking.k8s.io, kind: Gateway, namespace: edge, name: public}
      message: …
      detail: …
      since: 2026-11-10T13:58:01Z # omitted when unknown
      causedBy: <finding id>      # from diagnose grouping, omitted when none
      evidence: {…}               # typed per code, passed through from Snellius
      nextSteps: [{description: …, command: "kubectl --context prod …"}]
      fix: {…}                    # when diagnose.Fix has one
    checks:
    - name: status progress
      ok: true
      pending: false
      unavailable: ""             # off | denied | failing | not-on-version | not-observed
      hold: {until: 2026-11-10T14:07:12Z, count: 12, reason: …}
  incomplete: [<Input>]           # Phase 1 Input type
  observation:                    # --live only
    startedAt: …
    endedAt: …
    stopReason: timeout           # timeout | interrupted
    observed: [{id, code, resource, severity, firstSeen, clearedAt}]
```

Phase 1 conventions hold: RFC 3339 UTC times, no relative-to-now fields, refs as objects, empty optional fields omitted, warnings never in JSON/YAML. `docs/output.md` documents the `Check` kind; `TestOutputDocMatchesSchema` covers it. The exit-code sentence in `docs/output.md` and the README changes to say `check` uses 0/1/2.

## 8. Snellius upgrade (first sub-phase)

1. `go.mod`: `github.com/DuncanDoyle/kgateway-ui v0.3.0` → `github.com/DuncanDoyle/snellius v0.7.0-alpha.1`; rewrite imports.
2. Adapt to API changes between `v0.3.0` and the tag (e.g. `setup.Options`, `install.Install.Impl`).
3. `lifecycle_test.go`'s import guard names the new module path for `pkg/app`, so it keeps guarding.
4. `get`/`describe`/`version` behaviour and goldens unchanged; `hack/live-check.sh` passes.
5. Close kgwctl#5, #6, #7 with the commit.

## 9. Dependencies

No new module dependencies. TTY detection uses `os.File.Stat` (`ModeCharDevice`); no TUI library (D-005).

## 10. Testing

Boundary: the `check` command, run through `app.Execute` against envtest, as Phase 1's command tests. Collectors take an injected scraper/source (`CollectorEnv.Scraper`, proxystats `Source`) and the engine a fake clock (`setup.Options.Clock`), so holds mature without waiting minutes.

Required tests:
1. One fixture per priority theme gives the expected codes and exit code (install down: 005/006; route broken: 003/013/014; controller stuck: 018 after its hold with a fake clock; accepted but broken: 001 via fake metrics, 025/026 via fake stats).
2. Hold lifecycle in `--live`: hold appears, matures into a finding, or clears and resets.
3. A denied input (`pods/proxy` 403, Events forbidden via the testenv proxy) lands in Not checked, exit unchanged; a denied core kind gives exit 2.
4. Exit matrix across text/json/yaml, including Ctrl-C (2) and no install (2).
5. `--live -o json` writes one valid document; stdout and, off a terminal, stderr contain no ANSI escapes.
6. `--live` stops at `--timeout`; no watch, connection or goroutine left after return (`TestNoAccumulation` style).
7. `-n` filtering changes output and exit code as §3.1 says.
8. `TestOutputDocMatchesSchema` covers the Check view.

Live cluster: extend `hack/live-check.sh` with one scenario per priority theme on kind with real kgateway, including a real `pods/proxy` scrape.

## 11. Requests to other repos

None open. E9 is delivered (pre-release). A kgwctl release waits for the final Snellius `v0.7.0` (D-018).

## 12. Roadmap implications (proposals for the owner)

1. Phase 2 text: support first look; all 26 checks; `--live`; CI flags deferred. Drops "for CI gates".
2. Phase 5: `check` no longer "grows to the full analysis"; Phase 5 keeps `analyze -f`.
3. A follow-up phase or sub-phase: `-o agent`, already in the roadmap.

## 13. Proposed sub-phases

| Sub-phase | Delivers |
|---|---|
| 2.1 | Snellius upgrade (§8), no behaviour change |
| 2.2 | `check` one-shot: `internal/observe`, report text/json/yaml, exit codes, RBAC and docs, vendor pack test (§6.1) |
| 2.3 | `check --live`: progress, Observed section, timeout and Ctrl-C |

## 14. Left to the builder

- Exact text layout, column widths, wording of hints and the heartbeat interval.
- Package names inside `internal/` and how `observe` exposes events.
- Whether a finding links to its Snellius documentation; only with a URL verified to resolve.

## 15. Open items

- `-n` semantics for install-level findings (shown always) — our call; owner may override.
- Exit 2 for an interrupted `--live` even when the user stopped it on purpose — agreed in the live-mode review; revisit if it annoys in use.
