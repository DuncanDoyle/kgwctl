# Sub-phase 1.3 — `describe`

Kind: planned
Base: c18aae579bc634402c400cc7f3b42e70d92c3b32
Issues: n/a | Plan steps: implementation-plan.md Tasks 1.3.1–1.3.2

## Problem / goal
`get` shows one line per object. Debugging "why isn't my route attached" needs one object in full: its spec summary, every status block with the controller that wrote it, and its resolved relations. 1.3 adds `describe` and documents the versioned JSON/YAML schema that `describe` and `version` emit.

## Expected behaviour

| Input / action | Expected |
|---|---|
| `describe gw http -n kgateway-system` (healthy) | Object, Spec (listeners), Conditions (object + per listener), Relations: attached routes per listener, not-accepted routes with their deciding condition, ListenerSets (design §3.2) |
| `describe hr` with mixed parents | per-parent condition blocks naming the controller; parents with state as in `get` (D-012) |
| `describe hr` whose parent doesn't exist | Relations headed "Declared references (not resolved)": parentRefs and backendRefs from spec |
| a delegation child route | delegation parents listed |
| a policy with ancestors / with none | ancestor condition blocks labelled as status / no ancestor blocks |
| GatewayParameters | no spec summary; conditions only |
| another controller's Gateway under `--all-controllers` | unresolved relations (declared only) |
| Pods denied | an Incomplete section listing pods |
| Namespaces denied, describe a Gateway | "Relations unknown" naming namespaces; conditions still shown; exit 0 |
| `describe ls <name>` where both ListenerSet APIs have that name | exit 1, `ambiguous: name exists as ListenerSet and XListenerSet; use listenersets.<group>` |
| missing name / unknown kind / requested kind denied | exit 1, empty stdout (same texts as `get`) |
| `-o json` / `-o yaml` | envelope `kgwctl.kgateway.dev/v1alpha1`, `kind: Describe`, same content as text |
| `docs/output.md` | every field of the envelope, `Describe` and `Version` items, `incomplete`, the stability rule and exit codes; a test fails if the doc and the Go types drift |

## Root cause (if known)
n/a

## Acceptance tests
Plan Tasks 1.3.1–1.3.2, through `internal/testenv` with production clients. `describe` joins `TestExitCodeMatrix`. Goldens are checked by eye against design §3.2.

## Scope
In: `internal/view/describe.go`, `internal/print/describe.go`, `internal/app/describe.go`, tests, goldens, fixtures as needed, `docs/output.md`.
Out: attached or effective policy views (E7, Phase 6); changes to `get` output beyond shared helpers; any kgateway-ui change.

## Plan / design impact
none. Design §3.2 names `HTTPRouteView`; the library entry point is `objects.DecodeHTTPRoute`, as the plan says. Status cells and parent states follow D-012.

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] full test suite passes (`make test vet lint build`, `-race`, shuffled), observed
- [ ] cross-model review of `git diff c18aae579bc634402c400cc7f3b42e70d92c3b32..HEAD` + triage
- [ ] phase-1.3-complete.md written; phase-status.md updated
