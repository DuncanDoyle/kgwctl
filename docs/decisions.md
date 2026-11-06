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

