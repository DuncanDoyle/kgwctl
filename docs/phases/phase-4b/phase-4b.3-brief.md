# Sub-phase 4b.3 — Phase review fixes

Kind: stabilization
Base: 5645777
Issues: reviews/phase-codex.md #1–#8 | Plan steps: n/a

## Problem / goal
The Phase 4b Codex review holds `v0.1.0` on one P1 and seven other findings. Fix them before the first release.

## Expected behaviour

| # | Input / action | Expected |
|---|---|---|
| 1 | Two `hack/release.sh <tag>` real builds start at the same time on one tree | One claims `hack/release/out/<tag>/`; the other refuses before writing anything. The checksums and `publish-files.txt` describe the four archives of one build |
| 2 | The printed install check, run with `GOBIN` set in the environment or by `go env -w`, and with an older binary in the scratch path | `go install …@<tag>` writes into a fresh directory that the next printed command reads; the version printed is the tag's |
| 3 | `--select exact --rule N` on a route whose metadata claim has exact indices (`IndexCertainty` unset), plus an agreeing claim from another module with inferred indices | The route matches. Exact index evidence present in any agreeing claim is enough; merging must not lose it |
| 4 | Text output where two uses of one route config have different outcomes (one ambiguous, one inferred under `exact`; or different readings) | Text lists both outcomes. Uses with the same outcome and readings still print once |
| 5 | `-o json --select known --route …` live, when listing HTTPRoutes fails and the command recovers | The JSON says the cluster's HTTPRoutes were not available (a machine-readable field in `selection`). When the list succeeds, it says so. With `strict`/`exact` and with `--file`, the field states what applies. docs/output.md documents it |
| 6 | Text for a route with policy records at its virtual host, route config or listener | The trailer label says where the policy was recorded (for example "policies recorded at virtual host"), not "inherited policies". It makes no claim about inheritance or precedence (D-024) |
| 7 | README `proxy routes` | Names what the output can still contain (header and query match values, header values, listener error details) and asks the reader to check it before sharing, like `proxy dump` |
| 8 | An exported field added to a model type the payload reads, including `model.Ref` | The coverage test fails until the field is mapped or listed as left out |

## Root cause (if known)
1. hack/release.sh:37 checks the directory is empty, then creates it with `mkdir -p` later.
2. hack/release.sh:114 fixes `GOPATH` and `GOMODCACHE` but not `GOBIN`, and reuses a path.
3. internal/proxyview/view.go:60 reads `IndexCertainty` after xdsview's `combine` took the higher of unset and inferred (xdsview pkg/envoy/provenance/index.go:133).
4. internal/proxyview/text.go:103 deduplicates by `SourcePath` alone.
5. R24 reports the list failure on stderr only (internal/app/proxy_routes.go:273).
6. The trailer is xdsview's `provenance.Notes`, whose label is "inherited policies".
7. README.md:169 mentions only the raw-config exclusion.
8. internal/proxyview/coverage_test.go:12 lists model types by hand and omits `model.Ref`.

## Acceptance tests
At the command boundary where the behaviour shows (internal/app for 3–6, the release script in a scratch clone for 1–2), plus proxyview tests where a fixture needs a wrapper module. Each fails before its fix.
- 1: two concurrent real builds in a scratch clone; exactly one succeeds, the other refuses with nothing written.
- 2: the printed install step, run in a scratch clone with a stale binary and a `GOBIN` set, reads the new binary (a local tag; nothing pushed).
- 3: a wrapper adds an agreeing claim with inferred indices; `--select exact --rule 0` still matches.
- 4: a wrapper gives the two uses of `shared-source.json` different outcomes; text shows both.
- 5: JSON golden for `known` with a denied list and with a successful list, and for `strict` and `--file`.
- 6: the `levels.json` text golden shows the new label.
- 8: a test that adds a field to a copied type fails; `model.Ref` is covered.

## Scope
In: hack/release.sh, internal/proxyview, internal/app/proxy_routes.go, goldens, docs/output.md, README.md, docs/phases/phase-4b/design.md (DD-4, DD-5, R24, DD-22 wording).
Out: xdsview changes (report the "inherited" wording to the xdsview session instead); the Phase 4 deferred list; parked Minors from 4b.1 and 4b.2.

## Plan / design impact
Design: DD-4 (index certainty before merge), DD-5 (dedup by outcome), R24 (JSON input status), DD-22 (atomic tag directory, `GOBIN`), DD-14 (sharing warning). docs/output.md: the new `selection` field. No new decision; D-032 to D-035 stand.

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] full test suite passes (`make test`, `make lint`, `go mod tidy -diff`), and `hack/live-check.sh routes` on kind
- [ ] cross-model review of `git diff 5645777..HEAD` + triage (stabilization)
- [ ] phase-4b.3-complete.md written; phase-status.md updated
