# Sub-phase 3.4 — Phase review fixes

Kind: stabilization
Base: 0e84898
Issues: none | Plan steps: reviews/phase-codex.md findings 1, 2, 5, 6, 7 (3 and 4 via an xdsview bump, when released)

## Problem / goal
The Phase 3 review found five kgwctl gaps:

1. While a capture runs, any local process can reach Envoy admin through kgwctl's loopback listener, without Kubernetes credentials.
2. `proxy routes` can run past `--request-timeout` when the HTTPRoute list hangs in a credential plugin after the capture.
3. (finding 5) An oversized live dump advises `proxy dump` plus `--file`, which hits the same cap.
4. (finding 6) The live check proves a copy of the port-forward Role, not the one in `docs/rbac.yaml`.
5. (finding 7) The deadline handling lives in each command, so Phase 4's bundle can't reuse the capture as is.

## Expected behaviour

| Input / action | Expected |
|---|---|
| During a live capture, a local process scans 127.0.0.1 for listening ports | kgwctl opens no listening socket; nothing reaches the tunnel except kgwctl's one `GET /config_dump` |
| `proxy dump` / `proxy routes` live, normal | Same output and exit codes as at 0e84898 (goldens unchanged) |
| `proxy routes` live; the capture succeeds; listing HTTPRoutes blocks without honouring ctx (exec plugin hang) | Exit 2 at `--request-timeout` with the existing "could not finish loading the HTTPRoutes … within --request-timeout" message |
| A caller of the capture package (not a command) passes a ctx with a deadline; pod selection or the dial blocks without honouring ctx | The capture call returns ctx's error by the deadline; nothing it opened outlives it |
| Live dump larger than `--max-size` | Exit 2; the message says to raise `--max-size`, or to save the dump with `kgwctl proxy dump` and read it with `--file <path> --max-size <n>` |
| `--file` larger than `--max-size` | Exit 2; the message says to raise `--max-size` (unchanged wording is fine) |
| README `proxy routes` section | The recovery path for a large dump names `--max-size` on the `--file` read |
| `hack/live-check.sh proxy` and `routes` | The positive and negative RBAC checks use the Role and RoleBinding from `docs/rbac.yaml` (namespace and subject substituted), not a separate copy in the fixtures |

## Root cause (if known)
1. `proxydump.Dialer` returns a base URL; the port forwarder serves it through `portforward.NewOnAddresses` on 127.0.0.1 (internal/proxydump/portforward.go:59).
2. internal/app/proxy_routes.go:264 calls `knownHTTPRoutes` synchronously after the cancellable capture.
3. internal/app/proxy_routes.go:281 `tooLarge`; README.md:138.
4. hack/live/proxy-fixtures.yaml:212 duplicates the commented Role in docs/rbac.yaml:81.
5. The goroutine plus `select` sits in internal/app/proxy.go and proxy_routes.go, above `proxydump.Fetch`.

## Acceptance tests
- proxydump: a test that fails if the dialer path opens a listening socket (e.g. the dialer interface no longer yields an address to connect to, and a fake SPDY connection serves the GET); existing fetch tests (redirect refusal, release on close and on ctx end, typed Forbidden) keep passing.
- proxydump: a capture call whose pod lookup or dial blocks ignoring ctx returns by the deadline.
- app: `proxy routes` live with a capture that succeeds and an HTTPRoute list that blocks ignoring ctx exits 2 by `--request-timeout`.
- app: oversized live dump message names `--file` and `--max-size`.
- live: `hack/live-check.sh proxy` and `routes` pass on kind.

## Scope
In: `internal/proxydump`, `internal/app/proxy.go`, `internal/app/proxy_routes.go` and their tests, `README.md`, `hack/live-check.sh`, `hack/live/proxy-fixtures.yaml`, `docs/phases/phase-3/design.md` §4.1 transport line, `docs/phases/phase-3/`.
Out: xdsview findings 3, 4 and 8 (an xdsview version bump only, if released before close); output format changes; the CONNECT/SOCKS5 dialer; new flags.

## Plan / design impact
design.md §4.1: the transport line drops "on an OS-assigned loopback port"; the GET goes over the port-forward data stream in process. No decisions.md change: D-021 does not name the listener.

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] full test suite passes (race), vet, gofmt, deps check, goldens-check
- [ ] live check `proxy` and `routes` pass on kind
- [ ] cross-model review of `git diff 0e84898..HEAD` + triage
- [ ] phase-3.4-complete.md written; phase-status.md updated
