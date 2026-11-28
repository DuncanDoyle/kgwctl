Cross-model: YES (author: Claude Opus 5.5 subagent, reviewer: Codex, command: codex exec --sandbox read-only)
Scope: phase 4 design + plan r4 (scoped)
Diff: db8821b..1dec60d
Timing: on time

**r4 still has P2 blockers.** I reviewed `db8821b..1dec60d` against the six binding r3 rulings.

| Triage row | Status | Evidence |
|---|---|---|
| 1. Remote errors | **Partial** | The guard sanitizes errors before `check` flattens them, and the production denial test is specified. The port-forward task adds a test but omits the adapter change. [design.md:187](docs/phases/phase-4/design.md#L187), [implementation-plan.md:228](docs/phases/phase-4/implementation-plan.md#L228), [implementation-plan.md:261](docs/phases/phase-4/implementation-plan.md#L261) |
| 2. Replica attribution | **Partial** | Label-only Pods are skipped and all four capture paths require a UID re-read. The Sink has no way to attach that outcome to a successful item. [design.md:220](docs/phases/phase-4/design.md#L220), [design.md:224](docs/phases/phase-4/design.md#L224), [implementation-plan.md:82](docs/phases/phase-4/implementation-plan.md#L82) |
| 3. Writer lifecycle | **Partial** | Spool limits, cleanup, seal semantics and size-only finalization are specified. The finalization gate still permits exceeding the advertised total cap. [design.md:228](docs/phases/phase-4/design.md#L228), [implementation-plan.md:167](docs/phases/phase-4/implementation-plan.md#L167) |
| 4. Gate placement | **Partial** | Bootstrap watches are permitted and the command gates moved. The writer task still inherits a full-corpus gate containing command and transport inputs. [design.md:77](docs/phases/phase-4/design.md#L77), [design.md:273](docs/phases/phase-4/design.md#L273), [implementation-plan.md:155](docs/phases/phase-4/implementation-plan.md#L155), [implementation-plan.md:309](docs/phases/phase-4/implementation-plan.md#L309) |
| 5. Metrics refusal | **Partial** | The plan always tries 9091 and tests a working endpoint without a port declaration. Refusal detection conflicts with the guard contract. [implementation-plan.md:15](docs/phases/phase-4/implementation-plan.md#L15), [implementation-plan.md:285](docs/phases/phase-4/implementation-plan.md#L285) |
| 6. Grant counts | **Resolved** | The reader-only gate checks per-item `denied` statuses and one grant line per grant and namespace. [design.md:136](docs/phases/phase-4/design.md#L136), [implementation-plan.md:306](docs/phases/phase-4/implementation-plan.md#L306) |

**Section 12 calls.** Calls 1–9 are consistent with the rulings. Calls 10 and 11 need an explicit transport order: classify the refusal before replacing the body. Call 12 puts the command gates at feasible milestones, but the writer gate needs the same scope correction. [design.md:297](docs/phases/phase-4/design.md#L297)

**Remaining findings**

- **P2 · Metrics transport.** Task 4.3.2 needs a 502/503 body to set `Refused`, while §6 says the wrapper never reads error bodies and the guard replaces them. Define where the bounded inspection runs or the 503 gate cannot pass under both contracts. [design.md:137](docs/phases/phase-4/design.md#L137), [implementation-plan.md:287](docs/phases/phase-4/implementation-plan.md#L287)
- **P2 · Port-forward gate.** Task 4.2.3 requires a body-free denial at the SPDY boundary but lists no production adapter change or bundle-specific wiring to deliver it. [implementation-plan.md:228](docs/phases/phase-4/implementation-plan.md#L228), [implementation-plan.md:237](docs/phases/phase-4/implementation-plan.md#L237)
- **P2 · Capture identity.** `Item.Identity` exists only in the manifest type. The collector-facing `Source` and Sink methods cannot carry the re-read result, so the capture tasks cannot record it through the planned interface. [implementation-plan.md:82](docs/phases/phase-4/implementation-plan.md#L82), [implementation-plan.md:108](docs/phases/phase-4/implementation-plan.md#L108)
- **P2 · Archive cap.** The CLI promises an uncompressed **total** cap, but the finalization gate permits `MaxSize` plus footers. Reserve the footer bytes inside the cap or change the CLI contract. [design.md:55](docs/phases/phase-4/design.md#L55), [implementation-plan.md:167](docs/phases/phase-4/implementation-plan.md#L167)
- **P2 · Writer corpus gate.** Task 4.1.4 accepts Sink fixtures only. A replay there cannot exercise flag recording, registry resolution or production transport denials. Limit that gate to Sink inputs and require the other cases at their command or transport boundaries. [design.md:267](docs/phases/phase-4/design.md#L267), [implementation-plan.md:155](docs/phases/phase-4/implementation-plan.md#L155)
## Triage

| # | Finding | Decision |
|---|---|---|
| Metrics transport | refusal detection vs body replacement | accept: the guard classifies first (reads at most 4 KiB of a pods/proxy 502/503 body for "connection refused", sets a typed `Refused` marker), then replaces the body; the body is never stored |
| Port-forward gate | adapter change missing | accept: `statusError` in `proxydump` always returns code and reason only, for every command; `proxy dump`'s RBAC hint keys on the typed Forbidden, so its messages stay the same; Task 4.2.3 lists the change |
| Capture identity | `Source` cannot carry the re-read | accept: `Source` gains `Identity` (`verified`, `changed`, `gone`, `reread-failed`); every Sink method takes a `Source` |
| Archive cap | footers past the total cap | accept: the footer reserve sits inside `--max-size`; items get `--max-size` minus the reserve |
| Writer corpus gate | Task 4.1.4 cannot replay command and transport inputs | accept: the 4.1.4 gate covers Sink inputs only; flag recording, registry resolution and transport denials are gated at 4.2.5 and 4.3.x as already placed |
