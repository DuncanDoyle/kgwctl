# Phase 4.5 — Phase review fixes (Complete)

**Completed:** 2026-11-25
**Range:** f39a936..HEAD on `duncandoyle/phase4Design` (setup c2747dc; work 2ea976b..c6d04b6; close commit below)

## Summary
Stabilization of the bundle after the Phase 4 review. Findings 1–5, 8–12, 14 and 15 are fixed. Eight tasks and seven Codex rounds:
- **Guard (S1):** the guard strips a prefixed API base path before matching. Bundle clients never follow redirects. A non-2xx result keeps a code and a canonical reason, not the body.
- **Diagnostics (S2):** the manifest holds fixed scope reason codes. Cluster diagnostics, the Snellius logger and `Warning` headers stay off stderr and out of the manifest.
- **Time bounds (S3):** `--timeout` runs from the first cluster request. Discovery stops at `--request-timeout`.
- **Outcomes and scope (S4):** check-only denials carry a grant line. `skipped: no install` is claimed only when detection read its inputs. UDPRoute and the x-k8s.io kinds are in the scope registry.
- **Sink contract (S5, D-029):** the public `Sink` has no `JSON` or `ConfigDump`. Built-in JSON goes through internal writers. `Object` refuses kind-less objects.
- **Live reader check (S6):** the guard test rejects extra verbs, subresources, resource names and non-resource URLs. The reader check compares proxies and controllers with observed Pods and requires metrics per replica.
- **Seal rule (S8, D-030):** once the output exists, the bundle always seals an archive. The exit code follows the archive content.

## Sub-phases (phase record only)
n/a

## Tasks
| Task | Commits | Result |
|---|---|---|
| S1 Guard paths, 3xx bodies | 2ea976b..1a6b096 | Base path stripped; no redirect followed; status line and body code-only. 4 fix rounds |
| S2 Diagnostics | e9cf428 | Fixed reason codes; owner only for a known Gateway; logger and warnings dropped |
| S3 Time bounds | 391c18c..dbb41e4 | One clock from the first request; a cut detection seals an archive; discovery bounded. 2 fix rounds |
| S4 Outcomes and scope | fe26b10..142dff7 | Denials with grant lines; no-install claim only when known; non-registry kinds loaded. 1 fix round |
| S5 Sink contract | 5502015..a0eccee | D-029. 1 fix round |
| S6 Tests | a5e08a5..a04b574 | Guard test verbs; reader check against observed Pods. 1 fix round |
| S7 Docs, gates, review | 70527c0 | Output, threat model and CRD checklist updates; Codex review r1 |
| S8 Review fixes | f6798b1..c6d04b6 | Selector redaction, seal on any detection outcome, exit code from archive content, setup before output, check without report fails. 6 rounds |

## Rulings and decisions
- **D-029:** the public Sink has no generic JSON or config-dump method (owner delegated the design; Codex advised).
- **D-030:** the bundle seals an archive once its output exists (owner, 2026-11-25, option B). Exit 0 means the archive holds cluster data. This replaced the classifier that rounds r2 and r3 kept finding gaps in.
- **S1:** the guard drops `Location` on every 3xx. Three rounds of kept-Location variants each leaked through `net/http` error text. A cluster behind a redirecting proxy gets a status-only error in `errors.txt`.
- **S3:** a detection cut by `--timeout` seals an archive with `inventory/detection` as `timeout`. Brief row 5 requires a sealed archive. A request-timeout cut keeps the true text `install detection timed out`.
- **S6:** a collector `timeout` in the live reader check stays a hard failure.
- **S5:** the check-evidence projection (known fields instead of opaque `json.RawMessage`) is held for the owner.
- **Fix-loop cap:** the controller allowed one extra round after round 5 for r6 #1 and #3, and deferred the rest.
- **Rank disagreements:** Codex ranked 4.5-codex #1–#3 as P1. The author ranked them P2, P2 and P3. The owner approved the author's ranks for #3, #4 and #5, which are deferred.

## Gates
- `make test` (race, envtest), `make lint` and `go mod tidy -diff` pass at 70527c0 (run by the controller) and at c6d04b6 (reported by the implementer).
- Live check on kind at ff292ee exits 0. Admin: 117 files, 0 errors, 5 replicas checked. Reader: 86 files, 31 `denied`, 11 grant lines. No canary in an archive, stdout or stderr. The S6 and S8 reader checks passed on a real archive.
- The live check did not run again after c6d04b6, which changes deadline start and the `check` outcome only.

## Reviews
Per-task reviews (Claude subagents, opus) with scoped re-reviews after each fix round. Cross-model review (Codex gpt-6.2-sol, read-only) of the sub-phase:
- reviews/4.5-codex.md: 8 findings: 5 accepted (1, 2, 6, 7, 8), 3 deferred (3, 4, 5).
- reviews/4.5-codex-r2.md: round 1 re-review. 2 resolved, 1 fixed with the live run open, 1 incomplete (transport timeouts), 4 doc notes sharpened.
- reviews/4.5-codex-r3.md: 3 P1 on the same behaviour, so the loop stopped; the owner chose D-030. 3 doc findings fixed.
- reviews/4.5-codex-r4.md: 3 findings, all accepted.
- reviews/4.5-codex-r5.md: 5 findings, all accepted.
- reviews/4.5-codex-r6.md: 5 findings: 2 accepted, 3 deferred. Fix-loop cap reached.
- reviews/4.5-codex-r7.md: 1 P2 finding, deferred. No P1.
- Design advice for D-029: `.agents-tmp/sink-json-design-codex.md` (not committed).

## Waivers
Fix-loop cap exceeded once for S8 (round 6: r6 #1, #3), ruled by the controller after the owner left the question open; owner may revert c6d04b6.

## Deliverables
- `internal/bundle`, `internal/app`, `pkg/bundle`: guard, seal and exit rules, internal JSON writers.
- `docs/decisions.md` D-029, D-030 and the D-028 consequences; design §3.1 and §3.2; `docs/output.md`; `docs/bundle-threat-model.md`; CRD README checklist.
- `hack/live-check.sh` reader checks; `internal/testenv` guard test.

No new modules.

## Notes for later phases
- A run against a dead cluster leaves a small archive and exits 2. Scripts that test for the file must also check the exit code.
- The fixed D-028 registry means a live kind outside `collect.Registry` skips ownership rules and workload namespace expansion until it is added.
- A hung exec credential plugin during inventory can prevent the seal. kubectl behaves the same.

## Deferred / open
- **Codex carry-over:**
  - 4.5-codex #3: hung exec credential plugin blocks the seal. Documented limitation.
  - 4.5-codex #4: resources discovery is abandoned at `--request-timeout`, not cancelled.
  - 4.5-codex #5: live kinds outside the fixed registry skip scope rules. Recorded in D-028.
  - r6 #2: `check` and proxy capture build clients after output creation.
  - r6 #4: positive truncation test (item in the tar, missing from the manifest).
  - r6 #5 (P4): manifest overflow can hide the only qualifying collector.
  - r7 P2: `check.json` is dropped by the archive budget at commit, so `check` shows `partial` with no report.
- **Owner:** check-evidence projection to known fields (D-029).
- **Parked Minor (S1):** service-proxy path to a nested API server; `..` and `//` in hand-built paths; prefixed-path test lacks dynamic and discovery clients; `view/redact.go` `lookupRe` keeps the looked-up host.
- **Parked Minor (S2):** only the `invalid selector` code is pinned by a test; a dead DNS-1123 check at `inventory.go:117`; a rejected install match is silent in `check`.
- **Parked Minor (S3):** any HTTP status counts as `reached`; no silent-host test; the not-started label relabels unfinished collectors.
- **Parked Minor (S4):** the install omission sits beside `install/_cluster` members.
- **Parked Minor (S5, S6):** internal writer naming; three S6 review minors.
- **Carried:** xdsview bump to v0.6.0 plus goldens; Snellius `Scope.Excluded`; generated command reference; the 4.2–4.4 minors.
