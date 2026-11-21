# Sub-phase 3.1 — Capture and `proxy dump`

Kind: planned
Base: c38fe47
Issues: none | Plan steps: Tasks 3.1.1–3.1.4

## Problem / goal
Support's first ask on a gateway problem is the proxy's config dump. Today that means a manual `kubectl port-forward` plus `curl`, with no deadline, no replica choice and no RBAC guidance. 3.1 adds `kgwctl proxy dump`, and the capture layer that `proxy routes` (3.2) and the Phase 4 bundle reuse.

## Expected behaviour

| Input / action | Expected |
|---|---|
| `kgwctl proxy dump web -n team-a`, reader + port-forward Role | Envoy's config dump on stdout, unchanged; exit 0 |
| Several Ready replicas | The oldest Ready, non-terminating one; the same one on every run |
| `--pod <unready replica of web>` | That replica's dump; exit 0 |
| `--pod <pod of another Gateway>` | Exit 2, says the pod isn't a replica of `team-a/web` |
| Gateway doesn't exist | Exit 2, `Gateway team-a/nope not found` |
| No Ready replica | Exit 2, names the pods seen and their state, suggests `--pod` |
| Reader role only | Exit 2, names `create pods/portforward` and `docs/rbac.yaml` |
| Admin hangs, or the upgrade hangs (also behind an HTTP or SOCKS5 proxy) | Exit 2 at `--request-timeout`; no connection left open |
| Admin answers with a redirect | Exit 2; the redirect target is never requested |
| No `<gateway>` argument | Exit 2, usage |
| `kgwctl check` with the reader role only | Unchanged findings |

## Root cause (if known)
n/a (new feature).

## Acceptance tests
At the command boundary (`internal/app`) and the capture package, as listed in plan Tasks 3.1.1–3.1.3; the live check and goldens in Task 3.1.4.

## Scope
In: `internal/proxydump/`, `internal/app/proxy.go` and root wiring, `internal/testenv` (subresource match for any verb), `docs/rbac.yaml`, `README.md`, `hack/live/`, `hack/live-check.sh`, `testdata/dumps/`.
Out: `proxy routes`, any xdsview import, Phase 4 redaction, `check` behaviour.

## Plan / design impact
none (design r4 with the owner's 2026-11-21 answers; plan r4).

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] full test suite passes
- [ ] live check on kind passes; goldens pass the content check
- [ ] phase-3.1-complete.md written; phase-status.md updated
