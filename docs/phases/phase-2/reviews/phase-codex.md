Cross-model: YES (author: Claude Opus 5.5 + Claude subagents, reviewer: Codex, command: codex exec --sandbox read-only)
Scope: phase 2
Diff: 980d32d..1e1bb0b
Timing: on time

Found eight issues. Reviewed the requested documents, diff and Snellius tag; no files edited or tests run. The supplied passing results do not cover the cases below.

1. **P1 — Discovery timeout and interruption can hang indefinitely.**  
   [internal/app/check.go:187](/Users/ddoyle/Development/claude/kgwctl-claude-phase2/internal/app/check.go:187)

   Both deadline and cancellation branches cancel the session, then synchronously receive from `ch`. A client-go exec credential plugin can ignore that cancellation and block `observe.Start`. The repository already documents and tests this exact limitation in `TestExecPluginHangsPastDeadline`, but its command matrix excludes `check`.

   Consequently, `check --request-timeout 1s` can remain blocked indefinitely; ordinary one-shot Ctrl-C also cannot release this wait. **Suggested change:** stop waiting at the deadline, arrange cleanup if startup eventually returns, and extend the existing credential-plugin regression to both check modes.

2. **P1 — Partial coverage is hidden behind “PASSED”.**  
   [internal/view/check.go:336](/Users/ddoyle/Development/claude/kgwctl-claude-phase2/internal/view/check.go:336), [internal/print/check.go:169](/Users/ddoyle/Development/claude/kgwctl-claude-phase2/internal/print/check.go:169)

   Snellius’s `proxyStatsCheck.Run` can return `OK=true` with `Gaps` and a detail such as “1 of 2 Gateways have no proxy stats.” The mapper drops `Gaps`; the text renderer displays `Detail` only for checks classified as not checked. This check instead contributes to `PASSED`, concealing the coverage limitation.

   More generally, Snellius defines `CheckResult.OK` as successful input collection, whereas `docs/output.md` defines `ok` as having found nothing. Controller-down and xDS-rejected checks return OK alongside their findings and are counted as passed.

   **Suggested change:** distinguish execution/coverage from health success, preserve structured gaps, and render partial coverage explicitly. Align the JSON meaning and text labels without introducing per-check tables.

3. **P1 — Losing visibility is reported as a problem clearing.**  
   [internal/observe/diff.go:219](/Users/ddoyle/Development/claude/kgwctl-claude-phase2/internal/observe/diff.go:219)

   Every absent finding receives `ClearedAt`; `Diff` similarly emits `FindingCleared` without examining whether its check still ran. At the library tag, an xDS scrape failure replaces existing KGWUI-001 findings with an unavailable KGWUI-008 finding. The tracker therefore reports the rejection as **cleared** when metrics access fails—even if every subsequent scrape fails.

   That misrepresents lost evidence as recovery in the pasteable observation history. **Suggested change:** distinguish confirmed clearance from loss of observability, retaining the last known finding and marking its current status unknown. Obtain generic finding-to-check provenance from the library if necessary. Test rejection → denied scrape → timeout, separately from rejection → successful clean scrape.

4. **P1 — Transport failures can expose the API-server URL in reports.**  
   [internal/view/check.go:312](/Users/ddoyle/Development/claude/kgwctl-claude-phase2/internal/view/check.go:312), [internal/view/check.go:337](/Users/ddoyle/Development/claude/kgwctl-claude-phase2/internal/view/check.go:337)

   Finding details and check errors are copied verbatim. Snellius’s controller-metrics `PodProxyScraper` returns raw `http.Client.Do` errors; its collector embeds them in `Result.Err`, and the xDS check exposes that string. TLS and connection errors can therefore include `Get "https://<api-server>/api/..."` in both text and JSON.

   Terminal-control cleaning does not remove URLs. This violates the design’s explicit exclusion of server URLs from paste output. **Suggested change:** normalize transport errors before they become report strings, preferably in Snellius, and cover TLS/connection failures in every output format.

5. **P2 — Exit-2 classification depends on a reserved check name.**  
   [internal/app/check.go:251](/Users/ddoyle/Development/claude/kgwctl-claude-phase2/internal/app/check.go:251)

   `reportExit` recognizes unreadable required kinds only through `cs.Name == "cluster access"`. This follows the individual app task, but conflicts with design §6.1 and the global constraint forbidding check-name-specific logic.

   Today’s vendor example inherits the core check, so it does not prove independence from that name. The CLI owns a semantic dependency on one library check’s identity. **Suggested change:** derive inability to check from structured required-input access information or a generic library result classification, preserving exit-2 precedence in both modes.

6. **P2 — Live availability transitions miss real scrape outages.**  
   [internal/observe/diff.go:162](/Users/ddoyle/Development/claude/kgwctl-claude-phase2/internal/observe/diff.go:162)

   Availability events inspect only `Check.Incomplete`. However, `collectorInput` deliberately treats a published value containing scrape errors as OK. Controller metrics and proxy stats commonly report failures through those values, so their dependent checks become unavailable without corresponding `INPUT` progress events. Recovery is likewise missed.

   `TestLiveInputTransitions` exercises an artificial collector calling `Deps.Fail`, which takes a different path. **Suggested change:** include generic per-check availability transitions, with partial coverage where available, and test the actual scrape-failure/recovery paths.

7. **P3 — The live verification script does not establish the claimed observation behavior.**  
   [hack/live-check.sh:185](/Users/ddoyle/Development/claude/kgwctl-claude-phase2/hack/live-check.sh:185)

   The live scenario accepts exit 2 and checks only `stopReason == timeout` plus absence of final KGWUI-013. It never requires a matching observation entry or `clearedAt`. An implementation that never saw the broken route—or discarded all historical findings—could pass. `jq -e` also does not establish that stdout contains exactly one JSON document.

   **Suggested change:** assert the expected exit, exactly one document, and the specific install/resource/code observation with valid first-seen and clearance times. Keep the author’s manual observation distinct from what this script verifies automatically.

8. **P4 — Resolved findings lose the evidence needed by planned agent output.**  
   [internal/observe/diff.go:203](/Users/ddoyle/Development/claude/kgwctl-claude-phase2/internal/observe/diff.go:203)

   Tracking retains message, detail and severity history, but discards `Evidence`, `NextSteps`, `Fix`, and diagnosis context. Once a finding clears, it disappears from final `installs[].findings`; its observation entry is then the only remaining representation.

   The roadmap’s planned `-o agent` requires evidence and suggested fixes while promising no new data acquisition. For intermittent failures, those data are already lost. **Suggested change:** retain a complete finding snapshot for transitioned findings, with clearly documented first/latest evidence semantics, so future renderers can explain resolved failures from this run alone.
## Triage
| # | Finding | Source | Decision | Note |
|---|---|---|---|---|
| 1 | Discovery wait can hang (exec plugin) | reviewer | accept → 2.4 Q1 | Stop waiting at the deadline; cleanup when Start returns; extend the plugin-hang regression to check and check --live. |
| 2 | PASSED hides partial coverage; `ok` semantics | reviewer | accept → 2.4 Q2 | Snellius `OK` = the check ran. Text label becomes "checks ran", partial coverage shown generically; docs/output.md `ok` reworded. |
| 3 | Lost visibility reported as cleared | reviewer | accept → 2.4 Q3 | Generic rule: a finding that disappears in a model where checks became not-checked is "no longer observed", not cleared. Library provenance (finding → check) proposed to the owner. |
| 4 | API-server URL in transport errors | reviewer | accept → 2.4 Q4 | Redact URLs in cluster-derived text in kgwctl; propose the library fix to the owner. |
| 5 | Exit 2 via the `cluster access` name | reviewer | reject: Snellius has no generic "could not check" signal at the tag; the one named exception is documented (ruling in Task 2.2.4) and proposed as a library signal to the owner. |
| 6 | Scrape outages produce no INPUT events | reviewer | accept → 2.4 Q5 | Per-check availability transitions (NotChecked flips) in Diff/progress/Observed. |
| 7 | Live script under-asserts | reviewer | accept → 2.4 Q6 | Exact exit, one document, observation entry with clearedAt. |
| 8 | Resolved findings lose evidence | reviewer | accept → 2.4 Q7 | ObservedFinding keeps the latest evidence, nextSteps, fix. |
