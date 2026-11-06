# Sub-phase 1.4 — Live check, scale, docs

Kind: planned
Base: 7702558a410baa6e5389574532dc10fc23e3f586
Issues: n/a | Plan steps: implementation-plan.md Tasks 1.4.1–1.4.3

## Problem / goal
Every command so far is tested against envtest, which runs no controller. 1.4 checks `version`, `get` and `describe` against a real kgateway install on kind. It measures `get httproutes -A` at the §6.4 scale and documents install, usage and the least-privilege RBAC.

## Expected behaviour

| Input / action | Expected |
|---|---|
| `hack/live-check.sh setup` | kind cluster `kgwctl-live` (own kubeconfig file), Gateway API CRDs at the kgateway release's go.mod pin, latest kgateway OSS via Helm, the fixtures, and a `kgwctl-reader` ServiceAccount bound to `docs/rbac.yaml`. No token is printed. |
| `hack/live-check.sh verify <admin kubeconfig>` | `version`, `get all -A` in every format and `describe` on each fixture exit 0. The orphan route is in `get httproutes -A`. Status cells agree with `kubectl get -o yaml`. |
| `verify` with the reader kubeconfig | same commands succeed, and the outputs equal the admin run's |
| `get httproutes -A` on 1,000 namespaces, 500 Gateways, 5,000 HTTPRoutes | under 5 s (§6.4); 5 runs, min/median/max wall time and peak RSS recorded. Over target: stop and report to the owner. |
| README | install, one example per command, global flags, RBAC, the §6.3 limits, exit codes, link to `docs/output.md` |

## Root cause (if known)
n/a

## Acceptance tests
The live check and benchmark are scripts run by hand. Their outputs and numbers are recorded in `phase-1.4-complete.md`. The unit suite stays green.

## Scope
In: `hack/live-check.sh`, `hack/live/`, `hack/scale/`, `docs/rbac.yaml`, `README.md`, `.gitignore` (for `hack/live/out/`).
Out: command behaviour changes. A live-check mismatch becomes a tracked item, fixed in a stabilization sub-phase unless it is a one-line bug.

## Plan / design impact
none.

## Definition of done
- [ ] live check passes with the admin and reader kubeconfigs (outputs recorded)
- [ ] scale numbers recorded against the target
- [ ] full test suite passes, observed
- [ ] phase-1.4-complete.md written; phase-status.md updated
