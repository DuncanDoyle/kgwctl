# Phase 3 — `proxy routes`: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `kgwctl proxy dump` and `kgwctl proxy routes`: one Gateway's Envoy config, live over port-forward or from a file, with the effective HTTP filter chain per route mapped back to Gateway API and kgateway objects.

**Architecture:** A new `internal/proxydump` package captures a config dump: it picks the Gateway's replica, opens a client-go port-forward to 127.0.0.1:19000 through a `Dialer`, sends one `GET /config_dump` and returns a `Capture` with pod identity. `proxy dump` streams it. In 3.2, `proxy routes` hands the body to xdsview (parse, annotate with kgwctl's known HTTPRoutes, narrow, effective chains, render) and wraps JSON in kgwctl's envelope.

**Tech Stack:** Go 1.26, cobra v1.10.x, client-go v0.36.x (`tools/portforward`, `transport/spdy`), `github.com/DuncanDoyle/xdsview` (3.2 only, the E11 minor), `github.com/DuncanDoyle/snellius v0.8.2` (unchanged).

**Spec:** [`design.md`](design.md) (r2). Read it first. §n refers to it.

## Global Constraints

- No vendor or edition names anywhere (D-004).
- Admin API: exactly one `GET /config_dump`, no query parameters, redirects refused (D-021, §4.1).
- Deadline: the existing global `--request-timeout` (default 30 s) bounds discovery, tunnel and read. No new `--timeout` flag (ruling: the design's "`--timeout`, default 30 s" is this flag).
- Parse cap: 64 MiB by default, `--max-size` to raise it (3.2). `proxy dump` streams without a cap.
- Exit codes: `proxy dump` and `proxy routes` 0 printed, 2 could not read (§3.3). Other commands unchanged.
- RBAC: base `kgwctl-reader` ClusterRole unchanged; `create pods/portforward` only in a separate namespaced Role (§5).
- No kgwctl Envoy parser or tree (D-022). Only xdsview packages parse config dumps.
- No charmbracelet module in `go list -deps ./cmd/kgwctl` (§7).
- Goldens come only from kgateway OSS on kind (§8, E6).
- Data to stdout, errors to stderr. Code comments 1–2 lines; Go doc comments on exported identifiers.
- Every `kubectl`/`helm` command in scripts passes `--context`/`--kube-context`; kind clusters use their own `--kubeconfig`.

## Review Focus

1. **No Ready replica** (proxy rolling, crash-looping). The user expects exit 2 naming the Gateway and the pods seen with their phase, and `--pod` as the way to target an unready one. Covered in Task 3.1.1.
2. **`pods/portforward` forbidden** (a reader-only account). The user expects exit 2 and the exact Role to add, not a raw SPDY error. Covered in Task 3.1.3.
3. **The proxy hangs or the admin answers slowly.** The user expects exit 2 at `--request-timeout` and the tunnel closed. Covered in Task 3.1.2.
4. **`--pod` names a pod of another Gateway.** The user expects exit 2, never a dump of the wrong proxy. Covered in Task 3.1.1.
5. **A Gateway name that doesn't exist.** The user expects exit 2 "Gateway <ns>/<name> not found", not "no pods". Covered in Task 3.1.1.

## Test harness

envtest (`internal/testenv`) runs no kubelet, so no port-forward can complete there. Split:
- Pod selection and Gateway lookup run against envtest with Pod and Gateway fixtures (status set through the status subresource, as Phase 2 does).
- Fetch, limits, redirects and tunnel teardown run against an `httptest.Server` through a fake `Dialer`.
- The RBAC-denied path runs against envtest with the testenv proxy denying `Match{Resource: "pods", Subresource: "portforward"}`. If the proxy can't match a non-GET subresource, extend `Match` (Task 3.1.3, Step 1).
- The real SPDY dialer is exercised only by the live check on kind (Task 3.1.4).

## Sub-phases

| Sub-phase | Delivers | Tasks | Waits on |
|---|---|---|---|
| 3.1 | Capture, `proxy dump`, RBAC Role, golden dumps | 3.1.1–3.1.4 | none |
| 3.2 | `proxy routes` on xdsview E11 | 3.2.1–3.2.3 | xdsview release with E11 |
| 3.3 | Live check and docs for `proxy routes` | 3.3.1 | 3.2 |

Each sub-phase ends with `make test`, `go vet ./...`, gofmt clean.

---

## Sub-phase 3.1 — Capture and `proxy dump`

### Task 3.1.1: Gateway and replica selection

**Files:**
- Create: `internal/proxydump/pod.go`, `internal/proxydump/pod_test.go`, `internal/proxydump/testdata/pods.yaml`

**Interfaces:**
- Produces: `func SelectPod(ctx context.Context, kube kubernetes.Interface, gw types.NamespacedName, pod string) (types.NamespacedName, error)`. `pod == ""` picks; otherwise validates.
- Produces: `var ErrNoReplica = errors.New("no ready replica")` and `var ErrNotReplica = errors.New("pod is not a replica of the Gateway")`, wrapped with detail.

- [ ] **Step 1: Write the failing tests** (envtest, fixtures: Gateway `team-a/web`; pods `web-a` Ready started T0, `web-b` Ready started T0-1m, `web-c` Ready but `deletionTimestamp` set, `web-d` not Ready, `other-x` labelled for Gateway `other`):
  - `TestSelectPodPicksOldestReady`: got `team-a/web-b`.
  - `TestSelectPodStableOnTie`: two Ready pods with equal start time → the lower name, on 5 calls.
  - `TestSelectPodSkipsTerminating`: only `web-c` and `web-d` → `errors.Is(err, ErrNoReplica)`; message lists `web-c (terminating)` and `web-d (not ready)`.
  - `TestSelectPodExplicitUnready`: `pod="web-d"` → `team-a/web-d`, no error.
  - `TestSelectPodRejectsOtherGateway`: `pod="other-x"` → `errors.Is(err, ErrNotReplica)`.
  - `TestSelectPodGatewayNotFound`: `gw=team-a/nope` → error text `Gateway team-a/nope not found`.
- [ ] **Step 2:** `go test ./internal/proxydump/ -run TestSelectPod` fails (undefined).
- [ ] **Step 3:** Implement `SelectPod`: get the Gateway (apiVersion `gateway.networking.k8s.io/v1`, via the dynamic or typed client already in `load.Clients`; pick whichever the package can take without new deps), then list pods with `gateway.networking.k8s.io/gateway-name=<name>` in its namespace. Ready = `PodReady` condition True; order by `status.startTime`, then name.
- [ ] **Step 4:** Tests pass.
- [ ] **Step 5:** Commit `feat(proxydump): pick the Gateway replica to dump`.

### Task 3.1.2: Fetch through a dialer

**Files:**
- Create: `internal/proxydump/fetch.go`, `internal/proxydump/fetch_test.go`

**Interfaces:**
- Produces: `type Dialer interface { Dial(ctx context.Context, pod types.NamespacedName, port int) (baseURL string, stop func(), err error) }`
- Produces: `type Capture struct { Pod types.NamespacedName; CapturedAt time.Time; Body io.ReadCloser }` — closing `Body` also calls `stop`.
- Produces: `const AdminPort = 19000`
- Produces: `func Fetch(ctx context.Context, d Dialer, pod types.NamespacedName, now func() time.Time) (*Capture, error)`
- Produces: `func ReadCapped(r io.Reader, max int64) ([]byte, error)` and `var ErrTooLarge` (error text includes the limit; used by 3.2).

- [ ] **Step 1: Write the failing tests** with a fake `Dialer` returning an `httptest.Server` URL and counting `stop` calls:
  - `TestFetchOneGet`: the server records exactly one request, `GET /config_dump`, empty query; `Capture.Pod` and `CapturedAt` set; body bytes equal.
  - `TestFetchRefusesRedirect`: server answers 302 to `/elsewhere`; error mentions `redirect`; `/elsewhere` never requested; `stop` called once.
  - `TestFetchNon200`: 503 → error includes the status; `stop` called once.
  - `TestFetchDeadline`: handler blocks; `ctx` with 200 ms timeout → `errors.Is(err, context.DeadlineExceeded)` within 2 s; `stop` called once.
  - `TestFetchCloseStopsTunnel`: after `Body.Close()`, `stop` called once; a second `Close` doesn't call it again.
  - `TestReadCappedOver`: 65 bytes with `max=64` → `errors.Is(err, ErrTooLarge)`; `TestReadCappedAt`: 64 bytes → ok.
- [ ] **Step 2:** Tests fail.
- [ ] **Step 3:** Implement. Own `http.Client` with `CheckRedirect` returning an error, request built with `http.NewRequestWithContext`. `ReadCapped` reads through `io.LimitReader(r, max+1)`.
- [ ] **Step 4:** Tests pass, also under `-race`.
- [ ] **Step 5:** Commit `feat(proxydump): fetch one config dump through a dialer`.

### Task 3.1.3: Port-forward dialer and `proxy dump`

**Files:**
- Create: `internal/proxydump/portforward.go`, `internal/app/proxy.go`, `internal/app/proxy_test.go`
- Modify: `internal/app/root.go` (add `newProxyCmd`), `internal/testenv/proxy.go` (only if Step 1 needs it), `docs/rbac.yaml`, `README.md`

**Interfaces:**
- Consumes: `SelectPod`, `Fetch`, `Capture`, `AdminPort`.
- Produces: `func NewPortForwarder(cfg *rest.Config) Dialer` (client-go `transport/spdy` + `tools/portforward`, local address `127.0.0.1`, port `0`, stops on ctx cancel and on `stop`).
- Produces: `kgwctl proxy` (no-op parent with help) and `kgwctl proxy dump <gateway> [--pod <pod>]`, honouring `-n`, `--context`, `--kubeconfig`, `--request-timeout`.
- Produces: `internal/app` test hook to inject a `Dialer` (same pattern as Phase 2's `checkTestHooks`).

- [ ] **Step 1:** If the testenv proxy can't deny `create pods/portforward` (a POST/upgrade on the subresource), extend `Match` to match the subresource for any verb, with a test in `internal/testenv`.
- [ ] **Step 2: Write the failing tests** (envtest + fake dialer):
  - `TestProxyDumpStreams`: stdout equals the fake server body byte for byte; exit 0; stderr empty.
  - `TestProxyDumpNoGatewayArg`: exit 2, usage error.
  - `TestProxyDumpNoReplica`: exit 2; stderr names the Gateway and `--pod`.
  - `TestProxyDumpPortForwardForbidden`: real `NewPortForwarder` against envtest with the deny match → exit 2; stderr contains `create pods/portforward` and `docs/rbac.yaml`.
  - `TestProxyDumpDeadline`: `--request-timeout 300ms`, blocking fake server → exit 2 within 3 s.
  - `TestCheckUnaffected`: existing check tests still pass with `kgwctl-reader` only (no change expected; run them).
- [ ] **Step 3:** Implement. `proxy dump` copies `Capture.Body` to stdout with `io.Copy`, no cap. Help text carries §3.2's redaction paragraph verbatim. Error mapping: `apierrors.IsForbidden` (or a 403 from the SPDY upgrade) → the RBAC line.
- [ ] **Step 4:** `docs/rbac.yaml`: keep `kgwctl-reader`; add a commented, namespaced `Role` `kgwctl-proxy-portforward` with `pods/portforward` `create` and a `RoleBinding` example. Replace the header's "no pods/portforward" with: the reader role grants none; the optional Role adds it for `proxy` commands. README: a `proxy dump` section with §5's wording and §3.2's redaction note.
- [ ] **Step 5:** Tests pass; `make test`, `go vet ./...`.
- [ ] **Step 6:** Commit `feat: kgwctl proxy dump over port-forward (D-021)`.

### Task 3.1.4: Live check and golden dumps

**Files:**
- Create: `hack/live/proxy-fixtures.yaml`, `testdata/dumps/README.md`, `testdata/dumps/*.json`
- Modify: `hack/live-check.sh`

**Interfaces:**
- Produces: golden dumps used by 3.2: `demo.json` (snellius-demo use cases), `policies.json`, `collide.json`.

- [ ] **Step 1:** `hack/live/proxy-fixtures.yaml` in namespace `kgwctl-proxy` with its own Gateway `proxy`:
  - TrafficPolicies on HTTPRoute `orders`: a transformation (request header set), ext_authz via a GatewayExtension to a dummy Service, a local rate limit.
  - A second route whose TrafficPolicy disables ext_authz.
  - Colliding names: HTTPRoute `orders` in `kgwctl-proxy` and `orders-kgwctl` in `proxy` (create namespace `proxy`), both attached to the Gateway; both encode `httproute-orders-kgwctl-proxy-`.
- [ ] **Step 2:** `hack/live-check.sh` gains a `proxy` step: apply the fixtures, wait for the proxy Ready, run `kgwctl proxy dump` as the least-privilege user plus the port-forward Role, assert exit 0 and valid JSON with a `ListenersConfigDump`; run it as the reader only and assert exit 2 with `pods/portforward` on stderr.
- [ ] **Step 3:** Capture `demo.json`, `policies.json`, `collide.json` with `kgwctl proxy dump`; record kgateway version, Envoy version (`/server_info` is not fetched by kgwctl; take it with `kubectl exec ... wget -qO- 127.0.0.1:19000/server_info`), and date in `testdata/dumps/README.md`. Check each file holds no private key material (`grep -c '"private_key"'` shows only `[redacted]`).
- [ ] **Step 4:** Run the live check on kind; paste the result into the sub-phase record.
- [ ] **Step 5:** Commit `test: live proxy dump check and golden dumps`.

---

## Sub-phase 3.2 — `proxy routes` (waits on xdsview E11)

Task-level only. Exact xdsview signatures are filled in when the release is tagged; the design §7 shapes are the working assumption. If the released API differs, update this section before starting 3.2.

### Task 3.2.1: xdsview upgrade and pipeline

- Add `github.com/DuncanDoyle/xdsview@<E11 tag>`. Extend `TestBinaryDeps` to ban `github.com/charmbracelet/` and `github.com/muesli/`; prove it fails on a temporary blank import of `github.com/charmbracelet/lipgloss`, then revert.
- `internal/proxyview`: `Build(body []byte, gw *types.NamespacedName, known []model.Ref, sel Selection) (View, error)`: `parser.Parse` → `origin.Annotate(origin.Kgateway{Gateway, Known})` → narrow on resolved origin → effective chains per route within its HCM. Parser warnings kept in `View.Warnings`.
- Tests on the 3.1 goldens: `collide.json` with `--route kgwctl-proxy/orders` returns only that route; with `--file` and no Known, the colliding route is listed as ambiguous, never as a match; a non-active listener survives narrowing; `policies.json` shows ext_authz `enabled by route` on `orders` and disabled on the second route.

### Task 3.2.2: `proxy routes` command and tree

- `kgwctl proxy routes [<gateway>] [--file] [--pod] [--route ns/name] [--rule i] [--max-size 64Mi] [-o json|yaml]`. Live: `SelectPod` + `Fetch` + `ReadCapped`; file: open + `ReadCapped`. Known HTTPRoutes from kgwctl's one-shot load (`load.Load`), skipped with `--file` when no cluster is reachable, which is a warning, not an error.
- Tree from `render.Text` with kgwctl's header line (§3.1). Response order shown only where xdsview reports an encoder; otherwise the §3.1 wording.
- Exit 2 for: cap exceeded (message names `--max-size` and `proxy dump`), parse error, and every 3.1 capture error.
- Golden text tests per fixture dump.

### Task 3.2.3: JSON contract and docs

- Envelope per §6 (`kind: ProxyRoutes`), golden JSON per fixture, `docs/output.md` section, README `proxy routes` section with the filter-order example from `policies.json`.

---

## Sub-phase 3.3 — Live check and docs

### Task 3.3.1: `proxy routes` live check

- `hack/live-check.sh`: `proxy routes proxy -n kgwctl-proxy --route kgwctl-proxy/orders` as reader + Role; assert the three filters in request order with their policies. Same against a file from `proxy dump`, identical output except the header.
- README walkthrough: "does my transformation run before ext_authz?" answered from the live tree.

## Phase close prerequisites (not done by the implementer)
- Phase cross-model review (`reviews/phase-codex.md`) and triage.
- `phase-3-complete.md`, roadmap, program file (E1, E11 consumed).
