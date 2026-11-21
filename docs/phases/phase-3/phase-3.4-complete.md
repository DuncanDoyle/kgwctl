# Phase 3.4 — Phase review fixes (Complete)

**Completed:** 2026-11-21
**Range:** 0e84898..59dec54 on `duncandoyle/phase3Design`

## Summary
The capture opens no local listener. kgwctl writes its one `GET /config_dump` on the port-forward data stream and reads the reply in process. An error the kubelet reports on the error stream fails the read, also when it arrives after the data stream ends.

`proxydump.Fetch` and `proxydump.FetchGateway` own the deadline. They return ctx's error by the deadline even when pod selection, the dial or the GET ignore ctx, and they release a capture that completes late. `proxy dump` and `proxy routes` use them as is, and Phase 4 can capture a known Pod the same way.

`proxy routes` returns by `--request-timeout` when the HTTPRoute list hangs. An oversized live dump names `--file <path> --max-size <n>` as the way back. The live check applies the port-forward Role from `docs/rbac.yaml`. On xdsview v0.4.3, a malformed HCM shows as a warning, and an active listener with an updating or draining version survives narrowing.

Evidence at 59dec54:
- `make test` (race), `go vet`, `gofmt -l .`, `go mod tidy -diff` clean; no charmbracelet or muesli module.
- proxydump tests pass 10 times under `-race -count=10`, the app proxy tests 3 times.
- `hack/live-check.sh goldens-check`, `proxy` and `routes` pass on kind (kgateway v2.5.1, Envoy 1.39.6). Goldens unchanged.
- Fail before, pass after:
  - `TestPortForwardErrorStream`, `TestPortForwardLateErrorStream`, `TestPortForwardCloseEndsErrorWait`, `TestPortForwardCtxEndsErrorWait`
  - `TestFetchDialIgnoresCtx`, `TestFetchLateCapture`, `TestFetchGatewayLateCapture`, `TestAwaitReleaseOffCaller`, `TestAwaitDoneWins`
  - `TestProxyRoutesRouteLoadIgnoresContext` (exit 0 before; exit 2 in 2.1 s after)
  - `TestProxyRoutesUndecodableHCM`, `TestProxyRoutesUpdatingListenerKept` (fail on xdsview v0.4.2)
  - the oversized-live message test (fails with the 0e84898 message)
  - A probe before S1 read the dump through the old 127.0.0.1 port without credentials.

## Sub-phases (phase record only)
n/a

## Reviews
- reviews/3.4-codex.md: 2 P1, 1 P2, 4 P3, 1 P4. Six accepted and fixed; P3-1 rejected (an in-process socket scan would be flaky; the Dialer returns the stream itself); P3-4 deferred.
- reviews/3.4-codex-r2.md: P1-1, P3-3 and P4-1 partial; two new P1, one P3 accepted and fixed; the P4 on parked goroutines rejected (a context-blind exec plugin can't be interrupted from Go).
- reviews/3.4-codex-r3.md: all accepted rows resolved, no new P1 or P2.

## Waivers
none

## Deliverables
0e5df04, 5b2bbd0, bc06a9e, efa679f, 9414571, 4be9744, 59dec54. Dependency: xdsview v0.4.3 (xdsview#31).

## Deferred
- P3-4: a test that cancels an established SPDY capture through a CONNECT or SOCKS5 proxy. Phase 4 reuses the capture.
- Phase review finding 8 (E6): xdsview#32, planned as xdsview Phase 8.
- Test-only seams in production code: `proxyTestHooks.WrapTransport` and `portStream.waiting`, both nil in production.
