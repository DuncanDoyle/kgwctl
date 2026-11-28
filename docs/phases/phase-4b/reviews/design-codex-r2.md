Cross-model: YES (author: Claude Opus 5.5 subagent, reviewer: Codex gpt-6.2-sol, command: codex exec --sandbox read-only via run-reviewer.sh)
Scope: Phase 4b design r3 + plan (round 2)
Dimensions: correctness, architecture
Diff: 8494b39..29f4eb5 (docs)
Timing: on time

Phase 4b r3 addresses the substance of all ten round-one findings. Two design passages still contradict the rewritten plan, and the plan schedules a test helper after its first use. I found five new P2 issues and no new P1. D-031 and D-032 can stand as written.

## New findings

1. **P2 · correctness · The migration and strategy commits cannot pass their test gates in the planned order.** Task 4b.1.1’s dropped-claim test uses a wrapper, and Task 4b.1.2’s marker test explicitly calls `proxyviewtest.Wrap`. The plan creates that helper in 4b.1.3. Move the test-only helper into 4b.1.1 so both earlier commits can pass `make test` independently. [Plan:85](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/implementation-plan.md:85) · [Plan:105](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/implementation-plan.md:105) · [Plan:119](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/implementation-plan.md:119)

2. **P2 · correctness · The release can still upload archives from another build.** The rewrite drops dry-run output isolation, then publishes `hack/release/out/*.tar.gz` from a git-ignored directory. A dry run can leave four archives there. The tagged build can overwrite `commit.txt`, pass the remote tag check, and still upload those dry-run archives alongside the release. Isolate the dry run and generate checksums and publish arguments from the same explicit set of four tagged archives. [Plan:164](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/implementation-plan.md:164) · [Plan:189](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/implementation-plan.md:189)

3. **P2 · architecture · D-034 defines `unresolved` more strongly than xdsview can establish.** The design says the dump does not hold the config. In v0.7.0, a network filter’s discovery remains unresolved even when the dump contains ECDS entries. HTTP discovery can also remain unresolved when matching entries exist but their category or subscription is ambiguous. Define `unresolved` as “xdsview could not establish the config for this occurrence” and exercise a present-but-ambiguous HTTP entry alongside R15b. [Design:190](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/design.md:190) · [Network parser:534](/Users/ddoyle/go/pkg/mod/github.com/!duncan!doyle/xdsview@v0.7.0/pkg/envoy/parser/parser.go:534) · [HTTP parser:733](/Users/ddoyle/go/pkg/mod/github.com/!duncan!doyle/xdsview@v0.7.0/pkg/envoy/parser/parser.go:733)

4. **P2 · architecture · `unusedRouteConfigs` conflates a reported copy with a genuinely unjoined source.** xdsview joins an HCM to its inline source and records the matching static dump entry in `RouteConfigSource.Reported`. It keeps both when pruning. The new payload lists sources with no direct HCM join and explicitly drops `Reported`, so it can label the static copy `unused` and gives readers no way to identify it as the reported copy of a used configuration. Keep `routeConfig.source` and the internal Phase 6 boundary. Either omit the new `unusedRouteConfigs` field or account for `Reported` when defining which sources it lists. [Design:180](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/design.md:180) · [Plan:87](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/implementation-plan.md:87) · [Parser:586](/Users/ddoyle/go/pkg/mod/github.com/!duncan!doyle/xdsview@v0.7.0/pkg/envoy/parser/parser.go:586) · [Prune:39](/Users/ddoyle/go/pkg/mod/github.com/!duncan!doyle/xdsview@v0.7.0/pkg/envoy/model/prune.go:39)

5. **P2 · correctness · The live-check task drops the call sites for its JSON assertions.** The previous plan explicitly ran `expect_json` on live JSON with metadata on and off. The rewrite specifies the helper and a metadata-off `known` run, but never wires the matched live JSON run or requires both helper calls to fail the gate. The existing script emits text in both branches. Restore the live JSON invocation and assertion in each branch so the gate verifies `chosenBy: "select"`, filter order, and the unmatched case on kind. [Plan:169](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/implementation-plan.md:169) · [Live script:605](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/hack/live-check.sh:605)

## Round-one verification

| # | Status |
|---|---|
| 1 | The marker fix and text/JSON test are specified. DD-11 still says `Labels(ix, nil)` and should match DD-6 and the plan. [Design:150](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/design.md:150) |
| 2 | Resolved. Both drop codes get kgwctl-owned messages, with wrapper-name equality covered. |
| 3 | Resolved. One warning list serves text and JSON, including the retained slot with no rendered row. |
| 4 | The atomic migration and envtest gates are fixed. The new helper-order dependency is finding 1 above. |
| 5 | R21–R24 and the CLI tests agree. DD-4 still needs to limit the extra stderr line to `--route` and recoverable list failures. [Design:122](/Users/ddoyle/Development/claude/kgwctl-claude-phase4/docs/phases/phase-4b/design.md:122) |
| 6 | Resolved. The typed network discovery state is present. Its meaning needs the clarification in finding 3. |
| 7 | Resolved. The close sequence verifies the pushed tag and checks clean `go install` before publishing. Archive selection is finding 2. |
| 8 | Resolved. KGWUI-002 is exercised through `check` and bundle output, and stale KGWUI-016 is in the golden review. |
| 9 | Resolved. The `pkg/cli` fixture links a filter slot to a record emitted by the wrapped module. |
| 10 | Resolved. Phase 6 joins on `RouteConfigUse.Source` using the full snapshot and index. Finding 4 concerns the new public summary. |

The cached xdsview v0.7.0 decision log ends at D-022. I checked D-023 through the local program’s E15 entry and the `Compose` and `Index.Record` implementations. This was a read-only source review. The passing test, lint, and tidy results are the author’s baseline at `cd2fbaa`.
## Triage

| # | Finding | Dimension | Source | Decision | Note |
|---|---|---|---|---|---|
| 1 | Test helper scheduled after first use | correctness | reviewer | accept (P2) | `proxyviewtest.Wrap` moves to 4b.1.1 |
| 2 | Release can upload dry-run archives | correctness | reviewer | accept (P2) | Isolated dry run; publish the explicit four archives |
| 3 | `unresolved` defined too strongly | architecture | reviewer | accept (P2) | "xdsview could not establish the config"; ambiguous HTTP ECDS case |
| 4 | `unusedRouteConfigs` conflates a reported copy | architecture | reviewer | accept (P2) | Field dropped |
| 5 | Live JSON assertions not wired | correctness | reviewer | accept (P2) | JSON runs with metadata on and off fail the gate |
| r1-1, r1-5 | Leftover DD-11 and DD-4 wording | correctness | reviewer | accept | Aligned with DD-6 and R23 |
