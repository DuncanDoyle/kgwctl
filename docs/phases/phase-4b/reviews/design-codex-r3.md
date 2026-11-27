Cross-model: YES (author: Claude Opus 5.5 subagent, reviewer: Codex gpt-6.2-sol, command: codex exec --sandbox read-only via run-reviewer.sh)
Scope: Phase 4b design r4 + plan (round 3)
Dimensions: correctness, architecture
Diff: 29f4eb5..98d4aa4 (docs)
Timing: on time

Phase 4b design r4 and the plan have three new P2 findings. I found no new P1 issues.

1. **P2 · architecture · R15c conflates unresolved configuration with unknown execution.** xdsview v0.7.0 changes the chain entry to `runs: "unknown"` and `state: "unresolved"` only when the filter can still run. An HCM-disabled filter or a route-level disable keeps `runs: "no"` even when ECDS resolution is ambiguous. As written, R15c would misreport disabled filters. Specify an enabled filter for the ambiguous-ECDS fixture and preserve xdsview’s execution state independently of `configSource`. [design.md:73](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/design.md:73) · [implementation-plan.md:87](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/implementation-plan.md:87) · [xdsview chain.go:78](/Users/ddoyle/go/pkg/mod/github.com/!duncan!doyle/xdsview@v0.7.0/pkg/envoy/chain/chain.go:78)
2. **P2 · correctness · The dry run prints a publish command for its own archives.** Task 4b.2.3 explicitly requires the dry run to print `gh release create --verify-tag` with paths from the dry-run manifest. A dry run can build from a dirty checkout while recording the tagged HEAD in `commit.txt`. The remote tag and clean `go install` checks verify the tag, so neither proves those archives came from the tagged tree. Print publish instructions only after a real build, and use the same real-build manifest for checksum verification and upload. [design.md:260](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/design.md:260) · [implementation-plan.md:165](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/implementation-plan.md:165)
3. **P2 · correctness · DD-4 and the plan disagree when a recoverable list failure is followed by a fatal error.** DD-4 adds the `known` warning after the recoverable HTTPRoute-list warning. The plan also suppresses it on any exit 2. The current command lists HTTPRoutes before parsing the captured dump, so a denied list followed by a malformed dump reaches both rules. Tie the warning to the recoverable list branch and state explicitly whether a later error suppresses it. [design.md:123](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/design.md:123) · [implementation-plan.md:102](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/implementation-plan.md:102) · [proxy_routes.go:250](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/internal/app/proxy_routes.go:250)

**Round-two verification**

- **R2-1 resolved.** `proxyviewtest.Wrap` is created in 4b.1.1, before either later task needs it. [implementation-plan.md:76](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/implementation-plan.md:76)
- **R2-2 resolved for the original glob issue.** The tagged build uses an isolated directory and an explicit archive list. The new dry-run publish command is finding 2 above. [implementation-plan.md:167](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/implementation-plan.md:167)
- **R2-3 resolved for `configSource`.** `unresolved` now covers present-but-ambiguous ECDS entries and network discovery. The new execution-state assertion is finding 1 above. [design.md:190](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/design.md:190)
- **R2-4 resolved.** `unusedRouteConfigs` is dropped, `routeConfig.source` remains, and Phase 6 keeps the full snapshot and index for its join. [design.md:241](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/design.md:241)
- **R2-5 resolved.** The metadata-on and metadata-off live JSON runs both call `expect_json` and fail the gate on a mismatch. [implementation-plan.md:181](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/implementation-plan.md:181)
- **Round-one follow-ups.** DD-11 now passes the normalized chosen routes to `Labels`. DD-4 matches R23 and R24, with the mixed failure case in finding 3 above. [design.md:152](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/design.md:152) · [implementation-plan.md:113](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/implementation-plan.md:113)

This was a read-only source review. No files changed. The reported test, lint, and tidy results are the author’s baseline at `cd2fbaa`.
## Triage

| # | Finding | Dimension | Source | Decision | Note |
|---|---|---|---|---|---|
| 1 | R15c conflates unresolved config with execution | architecture | reviewer | accept (P2) | Execution state kept from xdsview; enabled and disabled cases |
| 2 | Dry run prints a publish command | correctness | reviewer | accept (P2) | Only a real build from a clean tagged tree prints it |
| 3 | `known` warning vs a later fatal error | correctness | reviewer | accept (P2) | A later exit 2 suppresses it; tested |
