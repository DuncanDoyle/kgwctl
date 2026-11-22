# Decision log

Append-only. Newest at the bottom. Supersede, don't edit.

## D-001 — Build from scratch (2026-11-04, phase 0)
**Context:** Earlier prototypes exist: kgateway PR #13654 (closed unreviewed) forks about 19 files of gwctl, pulls in go-graphviz and has tests only for its printers.
**Decision:** Start a new codebase. Take ideas, not code: the relation-based resource graph, policy discovery through the Gateway API policy label, `check`, `analyze -f`, and a hook for vendor builds (program P-003).
**Alternatives:** Revive the PR — rejected: fork upkeep, heavy dependencies, thin tests.
**Consequences:** Gateway API logic comes from kgateway-ui packages (D-002), not from gwctl.

## D-002 — Libraries, not a backend (2026-11-04, phase 0)
**Context:** kgateway-ui exposes its Gateway API model and analyzer as versioned Go packages; krp is becoming the Envoy data-plane library.
**Decision:** Import both directly and talk to the cluster with the user's kubeconfig. No UI backend is needed (program P-004).
**Alternatives:** Thin client over the UI API — deferred; needs the UI running and its per-user auth.
**Consequences:** The CLI pins tagged versions of krp and kgateway-ui; contract changes flow through the program file.

## D-003 — No reimplemented translation or merge logic (2026-11-04, phase 0)
**Context:** Two tools computing effective policy or Envoy naming differently would contradict each other.
**Decision:** Effective policy, findings and Envoy → Gateway API mapping come from the shared libraries (program P-001). The CLI formats; it does not decide.
**Alternatives:** CLI-local logic for speed — rejected.
**Consequences:** Some CLI phases wait on library deliveries (edges E1–E3, E7).

## D-004 — Vendor-neutral core (2026-11-04, phase 0)
**Context:** The CLI is headed for kgateway OSS.
**Decision:** No vendor or edition names in this repo. Vendor policy kinds plug in through a registration hook from outside (program P-002).
**Alternatives:** Build-tag variants in one repo — rejected: leaks vendor code into OSS.
**Consequences:** Phase 4 design defines the hook.

## D-005 — Light dependencies (2026-11-04, phase 0)
**Context:** go-graphviz in the prototype pulls in a WASM runtime to print DOT text.
**Decision:** Text graph formats (DOT, Mermaid) are written directly. Each new dependency is justified in the phase design.
**Alternatives:** n/a.
**Consequences:** No rendered images; users pipe DOT to their own tools.

## D-006 — Working name and home (2026-11-04, phase 0)
**Context:** The final name and repo are open; kgateway's security self-assessment already lists `kgwctl`.
**Decision:** Use `kgwctl` as the working name. The project moves into kgateway-dev/kgateway (roadmap Phase 5); maintainers decide the final name.
**Alternatives:** n/a.
**Consequences:** Keep the name in one place (root command, module path) so a rename is cheap.

## D-007 — `check` comes early, as its own phase (2026-11-04, phase 0)
**Context:** Users need a `check` command for CI gates and support triage, in the shape of earlier gateway CLIs: one-shot, scriptable, with exit codes. The roadmap had it in Phase 3, behind the data-plane work and the UI analyzer. The prior art's JSON mode exits 0 even when checks fail.
**Decision:** Phase 2 delivers a `check` MVP on the UI's existing health checks (edge E8): process health, failing status conditions (which include unresolved references), routes not attached, xDS rejections. Exit codes are identical across output formats. The full ranked analysis and `analyze -f` move to Phase 4.
**Alternatives:** Keep `check` in Phase 3 — rejected: blocks a stated requirement on unrelated work.
**Consequences:** Phases renumber: data plane 3, full analysis 4, policies 5, upstream move 6. Program edges E1, E3 and E7 move to the new phase numbers.

## D-008 — No compatibility with earlier kgwctl prototypes (2026-11-04, phase 0)
**Context:** Besides kgateway#13654, two earlier versions exist: an internal gwctl-based prototype (dormant since 2025-07, 12 release downloads) and a vendor CLI subcommand built from it. Neither reached OSS users; the OSS docs for it were removed in 2025-09.
**Decision:** Treat both as idea sources only, like D-001. No command, flag or output compatibility with them, and no migration path for the vendor CLI.
**Alternatives:** Keep the vendor subcommand's shapes so it can move onto this CLI — rejected: no evidence of use, and it would carry the gwctl-fork design forward.
**Consequences:** Command and output design is free in Phase 1. A vendor build may consume this CLI through the hook (P-002), but that is not a design constraint.

## D-009 — Extension API in Phase 1: library plus downstream main (2026-11-04, phase 1) — proposed, pending owner approval
**Context:** D-004 left the vendor hook to Phase 4. Every command added before then would have to be retrofitted. The CLI survey found the build-time model (an exported root-command constructor plus registries, as in cilium-cli and kgateway-ui's own D-003/D-038) the best fit for an OSS core with extended builds. Users of an older gateway CLI reported OSS and extended binaries drifting apart.
**Decision:** Phase 1 exports `pkg/cli` (`NewRootCommand(opts...)`, `Execute`) with options for name, kgateway-ui extension registry, extra commands and build info. Everything else is `internal/`. `examples/downstream` is built in CI.
**Alternatives:** PATH plugins (`kgwctl-<name>`) — rejected as primary: plugins can't see the shared model, so vendor kinds would be missing from `get`/`describe`. Fork per vendor — rejected: permanent rebase cost.
**Consequences:** Supersedes the "Phase 4 design defines the hook" consequence of D-004. `pkg/cli` is a semver contract from `v0.1.0`. Later phases extend the registry, not the CLI options, where possible.

## D-010 — Phase 1 design approved; support bundle phase (2026-11-05, phase 1)
**Context:** Phase 1 design r4 and plan r3 passed four Codex review rounds. Research ranked a support bundle the second most frequent support ask, and "Accepted, but rejected by Envoy" the costliest theme.
**Decision:** The owner approved the design, the plan, every *(our call)* item in the design, D-009, and envtest as a test-only dependency. The roadmap gains Phase 4 "Support bundle" (secrets redacted by default). Phase 2 builds the xDS-rejection check first and adds version skew.
**Alternatives:** Bundle at the end of the roadmap — rejected: its demand is higher than analysis or policies.
**Consequences:** Phases renumber: full analysis 5, topology and policies 6, upstream move 7. Program edges E3 and E7 move to CLI Phases 5 and 6. D-009 is accepted.

## D-011 — No maintainer proposal for now (2026-11-05, phase 1)
**Context:** Program P-005 asked for early maintainer buy-in through a short proposal. The owner leads both the vendor product and most of the kgateway maintainers, so direction is already aligned, and the project is not ready to be announced.
**Decision:** Don't file the proposal. The draft (`docs/phases/phase-1/maintainer-proposal.md`) stays as a reference for when the project goes public.
**Alternatives:** File now — rejected by the owner.
**Consequences:** P-005 is deferred in the program file. Phase 1 has no proposal step; the question returns at Phase 7 (upstream move) at the latest.

## D-012 — `get` details settled during sub-phase 1.2 (2026-11-05, phase 1) — our call, owner may override
**Context:** Implementing `get` raised cases design §3.1 and §6.2 left open or worded loosely. Each was ruled during implementation (SDD ledger R15–R28) and checked by task reviews and the Codex sub-phase review.
**Decision:**
- INSTALL shows in table and wide output, on every attributed kind, whenever more than one install is detected. It lists every owning install, or `unknown` when ownership inputs are not ok.
- ROUTES is the library's linked-route count for the Gateway plus ` (n not accepted)`; `-` marks an object no install indexes.
- The policy ANCESTORS denominator counts each distinct ancestorRef once, even when two controllers write it.
- For a parent with only foreign status entries, the owning install's index state (`unwatched`, `declared`) wins over `other`.
- A condition with no `observedGeneration` and status Unknown is never stale: an object condition renders `Unknown (<Reason>)`, not `<stale>`, and the same rule applies to per-parent and per-ancestor entries.
- `get <kind> <name>` bypasses the hiding rule. With several kinds and a name, a kind without the object fails the command: exit 1, empty stdout.
- `--install` naming no detected install is exit 1 when ownership inputs are ok; otherwise a warning, and nothing is hidden.
- Overlapping kind arguments (`ls,xlistenersets`) list each object once; the alias table wins.
**Alternatives:** kubectl-style partial output for multi-kind named lookups — rejected: stdout stays empty on every error (§8).
**Consequences:** Design §3.1 and §6.2 updated to match. `describe` (1.3) reuses the same cells.

## D-013 — `describe` details settled during sub-phase 1.3 (2026-11-06, phase 1) — our call, owner may override
**Context:** Implementing `describe` and freezing its JSON schema raised cases design §3.2 left open. Each was ruled during implementation (SDD ledger R29–R33) and checked by task reviews and the Codex sub-phase review.
**Decision:**
- `describe <kind> <name>` shows an object even when `get` would hide it; without an owning install, its relations are the declared form.
- A Gateway's notAccepted list is the rejected set `get` counts; pending, unwatched and declared links are a separate pending list.
- The Describe JSON item is structured: absolute ref objects, conditions as fields, booleans for declared and mirror. An unknown listener is omitted. Age is text-only.
- With index inputs down, parent states still come from status, relations show as unknown, and `resolved` is absent. Ownership-input failures reach `incomplete` for every attributed kind.
**Alternatives:** Display strings in JSON (as text prints them) — rejected: JSON is the stable interface (§8).
**Consequences:** `docs/output.md` documents the schema; `TestOutputDocMatchesSchema` keeps it in step with the Go types.

## D-014 — `check` is a support first look (2026-11-10, phase 2)
**Context:** The roadmap framed `check` as a CI gate after install or upgrade, and split checks between Phase 2 and Phase 5. Snellius now ships all 26 checks (E3).
**Decision:** `check` is a support engineer's first command on a cluster they troubleshoot. It runs all 26 checks in Phase 2; text for reading and pasting, `-o json|yaml` as the full mode.
**Alternatives:** CI gate first — rejected by the owner; Phase 2/5 split — outdated once E3 shipped.
**Consequences:** Roadmap Phase 2 reworded; Phase 5 keeps only `analyze -f`.

## D-015 — Deferred from Phase 2 (2026-11-10, phase 2)
**Context:** Phase 2 keeps to the support first look.
**Decision:** Defer `--only`, `--exclude`, `--fail-on` (until a CI user asks); controller logs as evidence; the `pkg/cli` facade for vendor cluster commands (Phase 1 carry-over, to the phase that first needs it); `-o agent` (next step after Phase 2, roadmap).
**Alternatives:** Keep them in Phase 2 — rejected by the owner.
**Consequences:** Exit 1 counts error and warning findings; info never fails a run.

## D-016 — No cluster-derived timing; `--live` instead of `--wait` (2026-11-10, phase 2)
**Context:** Grace periods and holds (003's 60s, 018's 5m) live in engine memory, so a one-shot run can't decide them. Seeding them from cluster timestamps was analysed with Codex and the Snellius session.
**Decision:** Don't derive timing from the cluster: deletions, controller requeues, webhook changes and clock skew leave no usable timestamp, which gives early or false findings. Instead `check --live` keeps one engine running for a bounded time (default 6m): progress on stderr, one report on stdout, stops at `--timeout` or Ctrl-C, never on "nothing pending". Holds come from the engine (E9, snellius#33).
**Alternatives:** `--wait` (no progress) — the owner prefers `--live`; cluster-derived timing — rejected under KISS.
**Consequences:** A plain `check` shows held checks as "still deciding" with their decision time and suggests `--live` only with that reason.

## D-017 — Controller metrics and proxy stats on by default (2026-11-10, phase 2)
**Context:** 001, 007, 024, 025 and 026 need controller metrics or Envoy stats, read through the apiserver's `pods/proxy`; Phase 1's ClusterRole excluded it.
**Decision:** `check` reads them through `pods/proxy` by default. A denied read puts those checks under "Not checked" with the RBAC line to add. No port-forward.
**Alternatives:** An opt-in flag — rejected: the default first look would miss xDS rejections; cache inputs only — rejected.
**Consequences:** `docs/rbac.yaml` gains `pods/proxy` get, Events, Services, EndpointSlices.

## D-018 — kgwctl releases depend on final Snellius releases (2026-11-10, phase 2)
**Context:** E9 shipped as Snellius `v0.7.0-alpha.1`.
**Decision:** Development may depend on a Snellius pre-release. A kgwctl release only ever depends on a final Snellius release.
**Alternatives:** Release on a pre-release — rejected by the owner.
**Consequences:** No kgwctl release tag while go.mod names a Snellius pre-release.

## D-019 — Support bundle: agctl prior art, CLI only (2026-11-17, phase 4)
**Context:** `agctl bug-report` (agentgateway#3991) ships a support bundle much like Phase 4. It is numbered after Phase 2's D-014 to D-018, which merged later.
**Decision:** Phase 4 borrows three ideas from it: kind discovery by CRD category, partial results with `errors.txt`, and previous-container logs only after a restart. The bundle stays a CLI feature, with no shared library for now.
**Alternatives:** A Snellius "download bundle" — deferred: a bundle crosses Snellius's per-Gateway authorization (Snellius D-013). The owner revisits it after Snellius Phase 12.
**Consequences:** Phase 4's design settles Secret and ConfigMap capture and the `pods/log` RBAC grant. Collector, manifest and redaction interfaces stay free of CLI concerns, so they can move to Snellius `pkg/` later.

## D-020 — Ctrl-C ends `check --live` like `--timeout` (2026-11-20, phase 2)
**Context:** Design §15 kept exit 2 for an interrupted `--live` and said to revisit it if it annoyed in use. In the owner's try-out, Ctrl-C printed the report and then `Error: interrupted`, which read like a crash.
**Decision:** A Ctrl-C during `--live` after an install was found prints the report, then `Stopped by Ctrl-C.` on stderr. The findings decide the exit code, as with `--timeout`. Exit 2 keeps meaning "could not check": a plain `check` interrupted, or `--live` interrupted before an install was found. The text footer says "stopped by Ctrl-C" or "stopped at --timeout"; JSON keeps `stopReason: interrupted`.
**Alternatives:** Keep exit 2 and only reword the message — rejected by the owner: the report is a valid check of the state the user watched.
**Consequences:** Scripts tell a deliberate stop from a failure by exit code. Design §3.2, §3.4 and the README updated (sub-phase 2.5).

## D-021 — `proxy routes` reads the config dump over port-forward (2026-11-20, phase 3)
**Context:** kgateway binds Envoy admin to 127.0.0.1:19000. `pods/proxy` reaches only the Pod IP, and the 9091 stats listener doesn't serve `/config_dump` (measured on v2.5.1). D-017 keeps `check` on `pods/proxy`.
**Decision:** Live `proxy routes` port-forwards to 19000 and sends only `GET /config_dump`, with a 30 s deadline and a 64 MiB cap. `create pods/portforward` is a separate, optional Role. `--file` needs no cluster access.
**Alternatives:** `pods/exec` — more power than port-forward. The controller's `/snapshots/xds` — intended config, not what Envoy accepted. A config_dump route on the stats listener — needs a deployment change; possible upstream opt-in later. File only — loses the live view.
**Consequences:** `docs/rbac.yaml` and the README say the grant also allows Envoy admin writes. The base reader role is unchanged. Reviewed with Codex (2026-11-20).

## D-022 — xdsview owns the Envoy view (2026-11-20, phase 3)
**Context:** xdsview's tree renderer is internal, its model lacks listener state, and it has no effective filter chain per route. Snellius Phase 12 (E4) will read the same model.
**Decision:** The renderer, effective chain and listener state go into xdsview (E11, xdsview#27). kgwctl writes no Envoy parsing or tree of its own; it owns access, origins input and the JSON envelope.
**Alternatives:** A tree in kgwctl over `model` — two views of filter order that drift, and Snellius gets neither.
**Consequences:** Sub-phase 3.2 waits on an xdsview minor release. 3.1 ships on v0.3.0.

## D-023 — Add-on commands such as a local MCP server ship in downstream builds (2026-11-21, phase 3)
**Context:** A local MCP server (`kgwctl mcp`, program edge E10) and traffic debugging were candidates for this CLI. The owner may build them outside this repo. kgwctl moves into kgateway (Phase 7) and must stay vendor-neutral, with no dependency on code outside the program (E6).
**Decision:** kgwctl does not ship `mcp` or debug commands. A downstream build adds them through `pkg/cli` (`WithCommands`, `WithRegistry`; D-009), as `examples/downstream` shows. `-o agent` stays here: it is output, not a server.
**Alternatives:** An MCP command in kgwctl over a shared tool set — ties kgwctl to that tool set's license and roadmap.
**Consequences:** `pkg/cli` is a contract for downstream builds: breaking changes need a version bump and the downstream example as the test. E10's consumer is a downstream build, not kgwctl.

## D-024 — The controller owns policy semantics; Phase 6 shows evidence, not an effective-policy model (2026-11-22, phase 4)
**Context:** kgateway's controller owns policy resolution, precedence, inheritance, merging and translation. Phase 6 assumed a library (E7) would give `describe` the attached and effective policies. A library outside the controller can only approximate those rules, and an approximation that disagrees with the controller misleads more than no answer.
**Decision:** kgwctl does not compute or promise effective policy. Phase 6 shows four kinds of evidence and links them:
1. declared policies and their `targetRefs`/selectors, as written;
2. the controller's status conditions on those policies and targets (authoritative);
3. what Envoy runs for a route or listener, from xdsview (authoritative for the data plane);
4. links between them, each marked verified (route metadata, a policy's own status, `route_source`) or inferred (names, selectors).

An effective-policy view ships only when the controller exposes the evidence itself, such as per-target policy status or route source metadata.
**Alternatives:** Snellius's policy model as the effective view (E7 as written): rejected as authoritative, because it re-derives controller rules. It is still usable as an inferred link if marked so.
**Consequences:** Narrows D-003: libraries may provide relations and naming, never semantics. E7 is redefined in the program file (owner to approve). The deferred `proxy diff` (generic intended-versus-accepted) is dropped from the roadmap. Replica convergence, NACKs, stale config and listener lifecycle states take its place as a candidate, subject to what the controller and Envoy expose.

## D-025 — Envoy admin reads are an allowlist of read-only GETs (2026-11-22, phase 4) — proposed, pending owner approval
**Context:** D-021 limits port-forward to one `GET /config_dump`. The support bundle also needs Envoy `/stats` (text readouts such as `version_text`), `/server_info`, `/certs` and `/clusters`, and optionally the controller's `/snapshots/krt`.
**Decision:** `proxydump` sends only GETs on a fixed allowlist: Envoy `/config_dump`, `/stats`, `/server_info`, `/certs`, `/clusters`; controller `/snapshots/krt`. Any other path is refused before a stream opens. The controller's `/snapshots/xds` is never on the list: it holds TLS keys and inline credentials in clear (kgateway v2.5.1, read from source).
**Alternatives:** One path per command, as D-021 has it — the bundle would need its own transport. Any GET — opens Envoy admin endpoints whose output nobody reviewed.
**Consequences:** The `pods/portforward` grant and its warning are unchanged. A new path needs a decision entry.

## D-026 — Redaction is a mandatory stage of the bundle writer (2026-11-22, phase 4) — proposed, pending owner approval
**Context:** A bundle leaves the customer's trust boundary. Envoy redacts only fields it marks sensitive; header values, `initial_metadata`, Lua code, transformation templates and `last-applied-configuration` carry secrets past it.
**Decision:** Collectors hand typed content to the writer, which runs the redactor for that type and refuses undeclared types. Structured rules for objects and JSON, a pattern pass for text, the marker `[redacted]`, and a `redactions.json` report without values. No Secret or ConfigMap is read in Phase 4.
**Alternatives:** Redaction per collector — a vendor collector could skip it. Hashing values — low-entropy secrets can be guessed offline.
**Consequences:** Redaction is tested with a canary corpus per artifact class. Its limits are documented in docs/bundle-threat-model.md and the README.
