# Phase status

## Phase
Phase: 4 — Support bundle
Base: 38acaa0
Author: anthropic (claude-opus-5-5 controller; Claude subagent implementers)
Advisory: none
Docs: docs/phases/phase-4/
Status: stabilizing; 4.1–4.5 closed; owner try-out next

### Queue
- [x] Q1 Phase 4 design (`design.md`) r1, with research notes (prior art, reuse, threats) — not committed, in .agents-tmp/
- [x] Q1b Roadmap: Phase 6 shows evidence, not effective policy; `proxy diff` replaced by a convergence candidate (D-024)
- [x] Q2 Phase 4 implementation plan (4 planned sub-phases)
- [x] Q3 Cross-model review of design + plan: 5 rounds (reviews/design-codex.md, -r2 to -r4 + triage); r5 at f4d2102
- [x] Q4 Owner go-ahead on design r5 and §12 items 1–6 (2026-11-22); D-025, D-026 accepted
- [x] Q5 Design r6 (owner additions: ownership and discovery-scope filters, `--skip logs`): Codex reviews r5–r9 + triage (reviews/design-codex-r5.md … -r9.md); no open P1/P2
- [x] Q6 Sub-phase 4.1 (contract, redaction, writer, runner, threat model) — phase-4.1-complete.md; Codex reviews 4.1-codex.md … -r4.md
- [x] Q7 Sub-phase 4.2 (command, guard, scope, inventory, Envoy transport, cluster-state collectors) — phase-4.2-complete.md
- [x] Q8 Sub-phase 4.3 (writer split, Sink contract D-027, logs, metrics, Envoy capture, RBAC and partial runs) — phase-4.3-complete.md
- [x] Q9 Sub-phase 4.4 (extension point, README, live check, reader role D-028) — phase-4.4-complete.md
- [x] Q10 Phase 4 review (Codex) + triage — reviews/phase-codex.md; owner agreed 2026-11-25
- [x] Q11 Sub-phase 4.5 (stabilization): accepted findings 1–5, 8–12, 14, 15 — phase-4.5-complete.md; Codex reviews 4.5-codex.md … -r7.md
- [ ] Q12 Owner try-out on kind; program file note ← CURRENT

### Exit gates
- [ ] tests/vet pass
- [ ] all queue items done or deferred
- [x] design+plan cross-model review + triage
- [x] phase cross-model review + triage
- [ ] decisions.md / design / plan in sync with code
- [ ] phase-4-complete.md written, roadmap updated, tracked items closed

## Sub-phase
none active (4.5 closed 2026-11-25: docs/phases/phase-4/phase-4.5-complete.md)

### Last sub-phase queue (4.5, base f39a936)

- [x] S1 (P2/P3 correctness) Guard: prefixed server paths, 3xx bodies — 2ea976b..1a6b096: base path stripped before matching; bundle clients never follow redirects; non-2xx status line and body code-only
- [x] S2 (P3 correctness) Diagnostics: manifest selector/owner, Snellius logger and `Warning` headers on stderr — e9cf428: fixed scope reason codes; owner only for a known Gateway; logger and warnings dropped
- [x] S3 (P2/P3 correctness) Time bounds: one clock from first request; context-bound discovery — 391c18c..dbb41e4: `--timeout` starts before detection; a cut detection still seals an archive (`inventory/detection` timeout); discovery bounded by `--request-timeout`
- [x] S4 (P2 both) Outcomes and scope: check-only denials, `skipped: no install`, non-registry kinds — fe26b10..142dff7: check-only denials recorded with grant line; `skipped: no install` only when absence is known; UDPRoute and x-k8s.io kinds in the scope registry
- [x] S5 (P3 architecture) `Sink.JSON` with Kubernetes objects — 5502015..a0eccee, D-029: public Sink has no `JSON` or `ConfigDump`; built-in JSON via internal writers; `Object` refuses kind-less objects
- [x] S6 (P3 correctness) Tests: reader live check, guard test verbs — a5e08a5..a04b574: guard rejects extra verbs, subresources, resourceNames, nonResourceURLs; reader check compares proxies and controllers with observed Pods, requires check.json and metrics, fails on non-ok/denied, derives grant lines from observed namespaces, scans stderr. Live run pending (kind unreachable)
- [x] S7 Gates, live check, Codex review of f39a936..HEAD + triage; docs: output.md scope reason codes and empty owner, threat-model note that `KGW_*` env and annotation values get only the pattern pass (S2 review); CRD README checklist step for `collect.Registry` extra kinds and `TestRegistryCoversCollectedKinds` (S4 review), checked against P-012's six review points; Codex prompt links program "Architecture guidelines" (protocol step 7) — 70527c0; review: reviews/4.5-codex.md
- [x] S8 Review fixes (4.5-codex accepted 1, 2, 6, 7, 8 and rounds r2–r7) — f6798b1..c6d04b6: selector redaction, seal on any detection outcome (D-030), exit code from archive content, setup before output, `check` without a report fails. Round 6 exceeded the fix-loop cap by ruling; live check passed at ff292ee

## Program
- Program file: https://github.com/DuncanDoyle/kgateway-tooling/blob/main/program.md (local: `../kgateway-tooling-claude/program.md`)
- Read 2026-11-22 at kgateway-tooling `56c8868` (main after the owner-approved history rewrite; E7 text merged, P-008, P-009, E13, E14). Phase 4 waits on no edge. The program session asked the bundle to capture per-replica convergence inputs (§5 of the design).
- Consumed: E1 and E11 — xdsview `v0.4.3` (kgwctl#1, kgwctl#10).
- 2026-11-23 at kgateway-tooling `4deab4d`: P-010 and E15, the xdsview Phase 9 contract. kgwctl migrates `proxy routes` when that tag ships; Phase 6 also waits on E15. Planned in `docs/roadmap.md`; no Phase 4 change.
- 2026-11-24 at kgateway-tooling `a0e1d7c` (4.4 start): E6 available — xdsview `v0.6.0` (kgwctl#12), Go API unchanged, OSS output byte-identical; the bump stays a carry-over. 4.4 waits on no edge.
- 2026-11-25 at kgateway-tooling `dbf7140`: P-012, owner-approved, adds a CRD and version bump review in program.md "Architecture guidelines". It cites D-028 and TestReaderRoleCoversServedKinds as the reference. P-011: lasting IDs come from scoped names, and a UID keys only one incarnation. Protocol step 7: review prompts link "Architecture guidelines". The 4.5 Codex review prompt follows it, and the CRD README checklist cites P-012.
- Waiting on: E7 and E15 (Phase 6). E15 (`proxy routes` migration). Snellius `install.Scope.Excluded` (DuncanDoyle/snellius#66, draft PR #67, unreleased): Task 4.2.2 keeps a local loop over `Scope.Contains` until a release ships it.
- Consumers: none

## Carry-over (deferred to later phases)
- kgwctl#8: Snellius list-only and namespace-scoped reads; owner to schedule.
- `get`/`describe` for list-only kinds (Phase 2.1 ruling).
- `--only`, `--exclude`, `--fail-on`, repeatable `-n`, controller logs (D-015).
- A narrow `pkg/cli` facade for vendor cluster commands (D-015).
- Phase 3 (reviews/3.4-codex.md P3-4): a test that cancels an established capture through a CONNECT or SOCKS5 proxy, when Phase 4 reuses the capture.
- 4.5 (reviews/4.5-codex.md, -r6, -r7): hung exec credential plugin blocks the seal (#3, documented); resources discovery is abandoned, not cancelled (#4); live kinds outside the fixed registry skip scope rules (#5, D-028); `check` and proxy capture build clients after output creation (r6 #2); positive truncation test (r6 #4); manifest overflow can hide the only qualifying collector (r6 #5, P4); `check.json` dropped by the archive budget at commit leaves `check` `partial` with no report (r7 P2).
- 4.5 owner item: project check-evidence to known fields instead of opaque `json.RawMessage` (D-029).
- 4.5 parked Minor (see phase-4.5-complete.md): S1 M1, M3, M4 and `lookupRe`; S2 selector-code test, dead DNS-1123 check, silent rejected install match; S3 m1, m3, n1; S4 m5; S5 R1-1 naming; three S6 minors.
- E13, E6: xdsview v0.5.0 (2026-11-22) and v0.6.0 (2026-11-23, kgwctl#11, kgwctl#12) are tagged; bump straight to v0.6.0 and update the `proxy routes` text and JSON goldens (certainty added). In docs, call `effectiveChain` the runtime filter chain; the JSON field keeps its name.

## Notes
- GitHub repo: https://github.com/DuncanDoyle/kgwctl (`kgwctl` remains a working name, D-006).
- 2026-11-06 Phase 1 closed: docs/phases/phase-1/phase-1-complete.md.
- 2026-11-20 Phase 2 closed: docs/phases/phase-2/phase-2-complete.md.
- 2026-11-21 Phase 3 closed: docs/phases/phase-3/phase-3-complete.md.
- 2026-11-22 Sub-phase 4.1 closed: docs/phases/phase-4/phase-4.1-complete.md.
- 2026-11-23 Sub-phase 4.2 closed: docs/phases/phase-4/phase-4.2-complete.md. Gates at 6376149 plus docs: `make test` (race, envtest) exit 0 (internal/app 211 s); `go vet ./...` and gofmt clean; `go mod tidy -diff` empty.
- 2026-11-23 Sub-phase 4.3 closed: docs/phases/phase-4/phase-4.3-complete.md. Gates at bb63cac plus close docs: `make test` (race, envtest) exit 0, 16 packages (internal/app 226 s); `make lint` and `go mod tidy -diff` clean.
- 2026-11-24 Sub-phase 4.4 closed: docs/phases/phase-4/phase-4.4-complete.md. Gates at 838c0b8: `make test` (race, envtest) exit 0, 17 packages (internal/app 223 s); `make lint` and `go mod tidy -diff` clean; live check bundle exit 0 with stock `docs/rbac.yaml`.
- 2026-11-25 Sub-phase 4.5 closed: docs/phases/phase-4/phase-4.5-complete.md. Gates: `make test` (race, envtest), `make lint` and `go mod tidy -diff` pass at 70527c0 (controller) and c6d04b6 (implementer). Live check at ff292ee exit 0: admin 117 files, 0 errors; reader 86 files, 31 denied, 11 grant lines; no canaries. Fix-loop cap exceeded once for S8 (round 6), by ruling.
