# Phase 1.4 — Live check, scale, docs (Complete)

**Completed:** 2026-11-06
**Range:** 7702558a410baa6e5389574532dc10fc23e3f586..HEAD of `duncandoyle/phase1Live` at merge

## Summary
`version`, `get` and `describe` work against a real kgateway install on kind. The output matches `kubectl get -o yaml` for every fixture. A least-privilege reader gets the same output as an admin. `get httproutes -A` takes 0.37 s at the §6.4 scale, against a 5 s target. The README documents install, commands, RBAC, limits and exit codes.

## Sub-phases (phase record only)
n/a

## Reviews
- No separate sub-phase review: 1.4 adds scripts and docs, not command code. The controller checked each task's output: `docs/rbac.yaml` against §6.3, the live tables against kubectl, and the README for paths and names.
- The Phase 1 cross-model review covers this diff.

## Waivers
none

## Deliverables
- `hack/live-check.sh` (`setup`, `verify <kubeconfig>`, shellcheck clean), `hack/live/fixtures.yaml`, `docs/rbac.yaml`, `hack/scale/generate.go`, `hack/scale/run.sh`, `README.md`.
- Live check on 2026-11-06, kind cluster `kgwctl-live`:
  - kgateway OSS v2.5.0 (latest stable) with Gateway API v1.7.0 standard, both derived from the release's go.mod at run time.
  - Admin and reader runs: 16/16 commands exit 0, the orphan route is listed, and outputs differ only in AGE.
  - Observed cells: `bad-backend` RESOLVED `0/1 (1 rejected: BackendNotFound)`; `orphan` ACCEPTED/RESOLVED `0/1 (1 no status)`; `healthy` `True`/`True`.
- Scale benchmark on 1,000 namespaces, 500 Gateways and 5,000 HTTPRoutes:

  | Command | Wall time | Peak RSS |
  |---|---|---|
  | `get httproutes -A` ×5 | min 0.37 s, median 0.37 s, max 0.59 s | 196 MiB |
  | `get gateways -A` | 0.30 s | 153 MiB |
  | `describe httproute` | 0.31 s | 164 MiB |

  The controller reconciled all routes in 18 s. Gateways used a self-managed GatewayParameters, so no proxy Pods ran.

## Notes for later phases
- kgateway v2.5.0 no longer ships the HTTPListenerPolicy CRD. `get all` skips the unserved kind.
- `describe` prints `Observed generation: <none>` for routes and policies, whose generation is per parent. The wording could say so.

## Deferred / open
- none
