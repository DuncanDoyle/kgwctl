# Sub-phase 1.2 — `get`

Kind: planned
Base: a66aa1d56de9bd44e203c350503a184b0f341d7b
Issues: n/a | Plan steps: implementation-plan.md Tasks 1.2.1–1.2.4, plus carry-over from phase-1.1-complete.md

## Problem / goal
`kgwctl` can only print versions. 1.2 adds `get`: kubectl-style tables of Gateway API and kgateway objects with status summaries the library already computes, hiding only what another controller positively owns, and the stored objects as a `v1` `List` in YAML or JSON.

## Expected behaviour

| Input / action | Expected |
|---|---|
| `get gw`, `get gateway`, `get gateways.gateway.networking.k8s.io` | one Gateway table: NAMESPACE, NAME, CLASS, ADDRESS, PROGRAMMED, LISTENERS, ROUTES, AGE (design §3.1) |
| `get gateways,hr` / `get all` | one table per kind / every served kind except GatewayClass |
| `get ls` | ListenerSet and XListenerSet in one table with a GROUP column; `xlistenersets` selects only the experimental kind |
| `get directresponses` | exit 1, unknown kind (not modelled, design §5) |
| `get tcproutes` on a cluster without TCPRoute | exit 1, kind not served |
| a route with parents A accepted, B no entry, C stale, D only another controller | ACCEPTED `1/4 (1 stale, 1 no status, 1 other)` |
| a route with two listeners of one Gateway, one rejected | ACCEPTED `1/2 (1 rejected: NotAllowedByListeners)` |
| another controller's GatewayClass, its Gateway and a route only to it | hidden; visible with `--all-controllers` |
| a route to a missing Gateway, a route with no parents, a policy with no ancestors, GatewayParameters, a Backend | visible |
| a route retargeted to our Gateway that still carries another controller's status | visible, its parent `no status` |
| no install detected, or an ownership input denied | nothing hidden; stderr warning naming why |
| `--install a/x` with two installs | objects owned only by install `b` hidden; INSTALL column when >1 install |
| Namespaces denied, `get gateways` | exit 0, ROUTES `unknown (namespaces: denied)` |
| requested kind denied, failed (500) or still loading at the deadline | exit 1, `cannot read <resource>: <state>: <reason>`, empty stdout |
| list allowed, watch denied after LIST sync | either exit 0 with the full table, or exit 1 `denied`; never a partial table |
| `get gw missing` | exit 1, `gateways "missing" not found` |
| `-o yaml` / `-o json` | a `v1` `List` of stored objects, `managedFields` stripped |
| empty result | `No resources found in <ns> namespace.` on stderr, exit 0 |
| any error, any format | same exit code across formats; stdout empty |
| vendor build from `examples/downstream` | its policy kind appears in `get` (un-skip `TestDownstreamPolicyInGet`) |

## Root cause (if known)
n/a

## Acceptance tests
Plan Tasks 1.2.1–1.2.4, through `internal/testenv` with production clients and a real kubeconfig. Each must fail before its code exists. Goldens are checked by eye against design §3.1.

## Scope
In: `internal/resolve`, `internal/view` (filter, status, get), `internal/print` (table, rawlist), `internal/app/get.go`, `internal/load` (see impact), test fixtures and goldens, `examples/downstream` test.
Out: `describe` (1.3), kind descriptors for DirectResponse, GatewayExtension, ReferenceGrant (kgateway-ui #17), list-only or namespace-scoped access (#18, #19), any kgateway-ui change.

## Plan / design impact
- Task 1.2.0 (added): `internal/load/wiring.go` is replaced by kgateway-ui v0.3.0 `pkg/setup` (its header says to, once #13 landed). Behaviour is unchanged; the `version` goldens and lifecycle tests are the check.
- Carry-over from 1.1: a requested GVR missing from `Inputs` must not be skipped (1.1-codex #5, in Task 1.2.1); un-skip `TestDownstreamPolicyInGet` (Task 1.2.3); fix the AtomicFIFO comment in `load.go` and the misleading `TestLoadPostDeadlineFailure` comment.
- Experimental XListenerSet CRDs are added to the test env through `CRDDirs` (R6).

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] full test suite passes (`make test vet lint build`, `-race`), observed
- [ ] cross-model review of `git diff a66aa1d56de9bd44e203c350503a184b0f341d7b..HEAD` + triage
- [ ] phase-1.2-complete.md written; phase-status.md updated
