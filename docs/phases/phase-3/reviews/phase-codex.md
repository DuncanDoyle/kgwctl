Cross-model: YES (author: Claude Opus 5.5 + Claude subagents, reviewer: Codex, command: codex exec --sandbox read-only)
Scope: phase 3
Diff: 44be3b6..0f3adc0
Timing: on time

## Phase 3 review

I would hold Phase 3 close for the transport and partial-output findings below. The base reader ClusterRole remains unchanged, and the optional port-forward Role grants only `create` on `pods/portforward` in the Gateway namespace.

### P1 · Correctness or safety

1. **The capture exposes the full Envoy admin interface to other local processes.** Both commands open a loopback TCP listener that forwards arbitrary bytes to port 19000. The one-GET restriction applies to kgwctl’s HTTP client. Other processes on the same host can use the listener without Kubernetes credentials while the capture runs, including for admin writes. Use an in-process stream or a listener with access controls. [portforward.go:59](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/internal/proxydump/portforward.go:59) · [fetch.go:82](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/internal/proxydump/fetch.go:82)

2. **`proxy routes` can miss its deadline after a successful capture.** Pod selection and capture run behind a cancellation `select` because client-go runs credential plugins without a context. The subsequent HTTPRoute list runs synchronously. If credentials expire or the list receives a 401 and triggers a hung refresh, the command can wait past `--request-timeout` instead of returning exit 2. The existing list-deadline test stalls the API response, so it misses this path. [proxy_routes.go:231](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/internal/app/proxy_routes.go:231) · [proxy_routes.go:264](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/internal/app/proxy_routes.go:264) · [proxy_routes_test.go:386](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/internal/app/proxy_routes_test.go:386) · [client-go exec.go:378](/Users/ddoyle/go/pkg/mod/k8s.io/client-go@v0.36.2/plugin/pkg/client/auth/exec/exec.go:378)

3. **A malformed HCM can disappear from a successful JSON result without a warning.** xdsview returns `nil` when it cannot decode an HCM and silently skips it. kgwctl copies only xdsview’s warnings, so a saved dump can return exit 0 with the HCM and routes missing and no `warnings` field. That breaks the published partial-result guarantee. The parser fix belongs in xdsview under D-022. [xdsview parser.go:168](/Users/ddoyle/go/pkg/mod/github.com/!duncan!doyle/xdsview@v0.4.2/pkg/envoy/parser/parser.go:168) · [xdsview parser.go:188](/Users/ddoyle/go/pkg/mod/github.com/!duncan!doyle/xdsview@v0.4.2/pkg/envoy/parser/parser.go:188) · [view.go:106](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/internal/proxyview/view.go:106) · [output.md:500](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/output.md:500)

### P2 · Contract or plan mismatch

4. **Narrowing hides listeners with an active version and a warming or draining version.** xdsview marks these listeners `active` and records the other versions in `updating` or `draining`. Its filter ignores those flags when deciding what survives a zero-match selection. A route waiting on a warming update can therefore produce an empty tree with no listener state, despite design §4.3. [design.md:89](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/phases/phase-3/design.md:89) · [xdsview parser.go:126](/Users/ddoyle/go/pkg/mod/github.com/!duncan!doyle/xdsview@v0.4.2/pkg/envoy/parser/parser.go:126) · [xdsview filter.go:301](/Users/ddoyle/go/pkg/mod/github.com/!duncan!doyle/xdsview@v0.4.2/pkg/envoy/filter/filter.go:301)

5. **The oversized-dump recovery instructions hit the same cap.** The README and error suggest saving the dump with `proxy dump` and reading it with `--file`. File mode enforces the same `--max-size`, so the documented sequence still exits 2 unless the operator also raises the cap. [README.md:138](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/README.md:138) · [proxy_routes.go:189](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/internal/app/proxy_routes.go:189) · [proxy_routes.go:281](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/internal/app/proxy_routes.go:281)

### P3 · Test gap

6. **The live check exercises a duplicate Role.** The script applies the base reader manifest and a separate fixture whose Role and RoleBinding duplicate the commented operator example. The check can pass if the published example drifts in verb, resource, or binding. Exercise the published grant in the positive and negative RBAC checks. [live-check.sh:313](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/hack/live-check.sh:313) · [proxy-fixtures.yaml:212](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/hack/live/proxy-fixtures.yaml:212) · [rbac.yaml:81](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/rbac.yaml:81)

### P4 · Later-phase fit

7. **Phase 4 inherits the stream but has to rebuild the deadline orchestration.** `Fetch` synchronously enters the dialer. Both commands supply their own worker and cancellation handling above it. A bundle collector calling `Fetch` directly can stall the global bundle deadline and delay `errors.txt` when an exec plugin hangs. Put the reusable deadline behavior at the capture boundary. [fetch.go:74](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/internal/proxydump/fetch.go:74) · [proxy.go:118](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/internal/app/proxy.go:118) · [roadmap.md:59](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/docs/roadmap.md:59)

8. **The pinned xdsview release leaves E6 open.** xdsview is inside the program and has an Apache-2.0 license. The pinned module still ships the enterprise corpus slated for removal, and the core origin mapper directly recognizes the enterprise transformation type. The binary dependency test permits that core package and cannot verify either cleanup. Phase 7 needs a release that satisfies E6. [go.mod:7](/Users/ddoyle/Development/claude/kgwctl-claude-phase3/go.mod:7) · [enterprise corpus README:7](/Users/ddoyle/go/pkg/mod/github.com/!duncan!doyle/xdsview@v0.4.2/testdata/enterprise/README.md:7) · [xdsview kgateway.go:276](/Users/ddoyle/go/pkg/mod/github.com/!duncan!doyle/xdsview@v0.4.2/pkg/envoy/origin/kgateway.go:276) · [program.md:38](/Users/ddoyle/Development/claude/kgateway-tooling-claude/program.md:38)

Static review. I relied on the author’s passing race tests, clean vet and formatting, dependency check, goldens check, and kind routes check.
## Triage

| # | Finding | Decision |
|---|---|---|
| 1 P1 | Loopback listener exposes the admin port to local processes | accept: stabilization 3.4 — send the one GET over the SPDY data stream in process, no local listener |
| 2 P1 | HTTPRoute list can outlive `--request-timeout` (exec plugin) | accept: 3.4 — the list runs behind the same cancellation as the capture |
| 3 P1 | Malformed HCM skipped silently | accept, xdsview: parser must warn (D-022); 3.4 bumps xdsview when released |
| 4 P2 | Active listener with warming/draining version hidden under narrowing | accept, xdsview: filter keeps listeners with `updating`/`draining`; 3.4 bumps |
| 5 P2 | Oversized-dump recovery hits the same cap | accept: 3.4 — message and README name `--file … --max-size <n>` |
| 6 P3 | Live check uses a duplicate Role | accept: 3.4 — apply the Role from docs/rbac.yaml |
| 7 P4 | Deadline orchestration above `Fetch` | accept: 3.4 — the capture boundary owns the deadline, reused by Phase 4 |
| 8 P4 | xdsview E6 (enterprise corpus, enterprise transformation type in core) | defer: xdsview/Phase 7 (E6 open in the program) |
