# Sub-phase 4.4 — Extension point, README, live check

Kind: planned
Base: b12d3a7
Issues: none | Plan steps: Tasks 4.4.1–4.4.3 of implementation-plan.md

## Problem / goal
After 4.3, `kgwctl bundle` collects everything the design lists, but three things are missing. A downstream build cannot add its own collectors, the README says nothing about the bundle, and nothing proves the bundle on a real cluster with planted secrets. 4.4 closes those three gaps and makes Phase 4 ready for its Codex phase review.

## Expected behaviour

| Input / action | Expected |
|---|---|
| A downstream build passes `cli.WithBundleCollectors(c)` | `c` runs in `kgwctl bundle`, is listed by `--list-collectors`, honours `--skip`, and writes through the same Sink: redaction, guard and path rules apply |
| A downstream collector with an invalid name, or a name equal to a built-in (`logs`) or to another downstream collector | `NewRootCommand` fails, the same way a registry error does |
| A downstream collector writes a canary through `JSON` and `Text`, tries an undeclared content type, and tries a Secret object | No canary in any archive member; the undeclared type and the Secret are refused and recorded as errors |
| A reader reads the README `bundle` section | It covers what is collected, what is never requested and how that is enforced, the residual risk of derived artifacts and of `-n`, the redaction limits, "review before sharing", and RBAC (reader, optional log and port-forward Roles) |
| `hack/live-check.sh bundle` on kind `kgwctl-live` as admin | Exit 0. Per proxy replica: `config_dump.json`, `stats.txt` with `version_text`, and a `metrics.txt` that parses as Prometheus. No planted canary in the extracted archive (raw, base64, gzip) |
| The same check as reader-only | Exit 0; affected items `denied`; one `pods/log` and one `pods/portforward` grant line per affected namespace |

Canaries planted by the live check: a TrafficPolicy `headerModifiers` value backed by a Secret with a pattern-free value; a GatewayExtension ext-auth `headersToAdd`; an Event; a GatewayParameters env; an inline JWKS with `k`. One proxy is raised to debug logging, and one request carries the Secret-backed header.

## Root cause (if known)
n/a (planned work)

## Acceptance tests
- `TestWithBundleCollectors` and `TestDownstreamCanaries` (plan Task 4.4.1), at the archive boundary through `pkg/cli` and `examples/downstream`.
- `hack/live-check.sh bundle` (plan Task 4.4.3), run by the controller against kind `kgwctl-live` (kgateway v2.5.1), with every kubectl/helm call passing `--context` and the kind kubeconfig.

## Scope
In:
- `pkg/cli` (`WithBundleCollectors`) and `examples/downstream`
- README `bundle` section; `docs/rbac.yaml` only if the README finds it out of date
- `hack/live-check.sh` and its fixtures under `hack/live/`

Out:
- new built-in collectors
- the xdsview `v0.5.0`/`v0.6.0` bump and goldens (separate carry-over)
- Snellius `Scope.Excluded` (waits on Snellius v0.9.0)
- live per-collector progress on stderr
- the Minor findings deferred to the phase review

## Plan / design impact
none. Task 4.4.1 builds on `Config.Collectors` (4.2 ruling).

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] full test suite passes (`make test`, `make lint`, `go mod tidy -diff`), run by the controller
- [ ] live check passes on kind, admin and reader-only
- [ ] phase-4.4-complete.md written; phase-status.md updated
