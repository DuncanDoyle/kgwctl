# Sub-phase 1.1 — Foundation

Kind: planned
Base: d109da6e0c37af774744eed31806cc6e0ddd562e
Issues: n/a | Plan steps: implementation-plan.md Tasks 1.1.1–1.1.5

## Problem / goal
There is no code yet. 1.1 delivers the module, the one-shot model load, the root command with cluster flags, `version`, the `pkg/cli` extension API with a downstream example, and the envtest harness every later command test uses.

## Expected behaviour

| Input / action | Expected |
|---|---|
| `kgwctl version` on a cluster with one kgateway install | client version, controller version and replicas, proxy versions per Gateway, Gateway API CRD bundle version and channel (design §3.3) |
| `kgwctl version` when a proxy group runs another version | that group is marked as differing from the controller |
| `kgwctl version` with proxy Pods or CRDs denied, or still loading at the deadline | the affected field prints `unknown (<state>)`, a warning goes to stderr, exit 0 |
| `kgwctl version` with Gateways denied | `Proxies: unknown (gateways: denied)`, never an empty list |
| `kgwctl version --client` | client version only, no request to any server |
| `--context two` in a kubeconfig whose current context is `one` | every request, discovery included, goes to context two's server |
| `--request-timeout 1s` with slow or retrying discovery | exit 1 within ~1.5 s, error names stage `discovery` |
| `-o json` / `-o yaml` | the versioned envelope `kgwctl.kgateway.dev/v1alpha1` |
| a vendor `main` built on `pkg/cli` with its own kind, detector and command | its install appears in `version`, its policy kind is registered, its command runs |
| any command ending on success, error or timeout | all watch connections closed within 2 s; repeated runs don't accumulate |

## Root cause (if known)
n/a

## Acceptance tests
As listed in plan Tasks 1.1.1–1.1.5. They run through `internal/testenv` (envtest behind the fault proxy) with production clients and a real kubeconfig. Each test must fail before its code exists. Golden files are checked by eye against design §3.3.

## Scope
In: `go.mod`, `Makefile`, `cmd/kgwctl`, `pkg/cli`, `internal/{load,app,view,print,testenv}` (version-related parts only), `examples/downstream`, `.github/workflows/ci.yaml`.
Out: `get`, `describe`, kind resolution, the hiding rule, status cells (1.2/1.3). No changes to kgateway-ui; anything missing there becomes a cross-repo issue.

## Plan / design impact
none. kgateway-ui is pinned to a pseudo-version of `main` (`9f44baa`) until #16 tags it.

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] full test suite passes (`make test vet lint`), observed
- [ ] cross-model review of `git diff d109da6e0c37af774744eed31806cc6e0ddd562e..HEAD` + triage (planned sub-phase: reviewed at phase review; run early if cheap)
- [ ] phase-1.1-complete.md written; phase-status.md updated
