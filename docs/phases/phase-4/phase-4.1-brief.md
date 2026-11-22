# Sub-phase 4.1 — Contract, redaction, writer

Kind: planned
Base: 40a50b6
Issues: none | Plan steps: Tasks 4.1.1–4.1.6

## Problem / goal
The support bundle needs a core that no collector can bypass: a public collector contract, redaction per content type, and one writer that redacts, budgets and seals every archive member. 4.1 builds that core without any cluster access, so 4.2 and 4.3 only add collectors.

## Expected behaviour

| Input / action | Expected |
|---|---|
| Any design §10 corpus row written through its Sink method | Extracted archive holds no canary in any member, member name or footer; `redactions.json` names each location, never a value |
| A header value in any design §7 structure, any header name | `[redacted]`; the name kept |
| Inline JWKS | Public members only; anything else a whole `[redacted]` |
| A Secret or ConfigMap handed to `Object` | `ErrForbiddenKind`; nothing written |
| Items over `--max-size` minus the footer reserve | Same kept set whatever the staging order; the rest `dropped: size budget` |
| `--max-size 31Mi` | Error |
| A collector blocking past the deadline | `timeout`; finished collectors `ok`; manifest written |
| A text stream cut at the seal | Its complete lines plus one truncation line |
| A collector panicking with a token in the panic value | `failed`; the token in no member |
| `--skip logs` at the runner | `skipped`, reason `flag`, `Collect` not called, no `errors.txt` line |
| A failing destination | `Run` returns the error; the output file is removed |
| 100 000 items | Footers within their caps; omitted counts set |

## Root cause (if known)
n/a (new feature).

## Acceptance tests
As listed in plan Tasks 4.1.1–4.1.5: pure tests for `pkg/bundle` and `internal/redact`; archive-boundary tests through the production writer with `bundletest.Extract` and `AssertNoCanary`.

## Scope
In: `pkg/bundle/`, `internal/redact/`, `internal/bundle/` (manifest, writer, staging, runner, `bundletest`), `docs/output.md` (BundleManifest), `docs/bundle-threat-model.md`.
Out: the `bundle` command, request guard, scope, inventory, any collector, any cluster access (4.2+).

## Plan / design impact
none (design and plan r6, owner-approved 2026-11-22; reviews through design-codex-r9.md).

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] `make test` (race), `go vet ./...`, gofmt clean, deps check, `go mod tidy -diff` empty
- [ ] phase-4.1-complete.md written; phase-status.md updated
