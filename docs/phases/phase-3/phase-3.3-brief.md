# Sub-phase 3.3 — Live check and docs

Kind: planned
Base: e398be9
Issues: none | Plan steps: Task 3.3.1, plus the 3.2 re-review deferral

## Problem / goal
`proxy routes` is tested on goldens and envtest only. 3.3 proves it on a real kgateway, and answers the lead question in the README from a live tree: does my transformation run before ext_authz?

## Expected behaviour

| Input / action | Expected |
|---|---|
| `hack/live-check.sh` routes step: `proxy routes proxy -n kgwctl-proxy --route kgwctl-proxy/orders` as reader + port-forward Role, route metadata on | Exit 0; the transformation, ext_authz and local rate-limit filters in request order; the ext_authz policy contribution; the route's policy list |
| The same view from a `proxy dump` file with `--file --gateway kgwctl-proxy/proxy` | Identical output except the header |
| Route metadata off (the default) | Live and file both list the colliding routes as ambiguous, none as a match |
| Reader only | Exit 2 naming `create pods/portforward` |
| `--route team-a/orders` with a vhost whose name contains `httproute-orders-team-a-` and an unrelated route under it | The unrelated route is not listed as unresolved for that HTTPRoute (3.2 re-review deferral) |
| After the run | `enableRouteSourceMetadata` back to false; the owner's demo namespaces untouched |

## Root cause (if known)
The unresolved-list over-report: the unresolved check reuses xdsview's whole-name substring fallback, so the marker can match inside the vhost prefix.

## Acceptance tests
The live-check routes step on kind (script assertions). A proxyview or command test for the unresolved over-report.

## Scope
In: `hack/live-check.sh`, `internal/proxyview` (unresolved matching), `README.md`, `docs/phases/phase-3/`.
Out: xdsview changes, new flags, recapturing the committed goldens.

## Plan / design impact
none.

## Definition of done
- [ ] the live check passes on kind (output pasted into the close record); the unresolved test fails before, passes after
- [ ] full test suite passes
- [ ] phase-3.3-complete.md written; phase-status.md updated
