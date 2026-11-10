# Sub-phase 2.4 — Phase review fixes

Kind: stabilization
Base: 1e1bb0b838f679efd0fdabfeadf51d99dd1de7e9
Issues: n/a | Plan steps: reviews/phase-codex.md triage Q1–Q7

## Problem / goal
The Codex phase review found ways `check` can hang, mislead or leak: see reviews/phase-codex.md #1–4, 6–8.

## Expected behaviour

| Input / action | Expected |
|---|---|
| exec credential plugin that ignores cancellation, `check --request-timeout 1s` (and `--live`) | returns within ~2s, exit 2, empty stdout |
| a check that ran and raised findings | not counted as "passed"; text counts checks that ran without findings and with no gaps |
| a check that ran with partial coverage (OK with a gap detail) | shown in the report with its detail, not hidden |
| `--live`: a finding disappears in a model where checks became not-checked | Observed says "no longer observed" (not "cleared"); no CLEARED progress line |
| a scrape that turns failing then recovers during `--live` | progress shows the check becoming not-checked and checked again |
| transport error text containing `https://host:port/...` | host and port redacted in text and JSON |
| `--live` JSON | observed entries carry the latest evidence, nextSteps and fix |
| live script `--live` scenario | asserts exit, one document, the 013 observation entry with clearedAt |

## Root cause (if known)
See reviews/phase-codex.md.

## Acceptance tests
Command-level tests per row; live script on kind.

## Scope
In: internal/app, internal/view, internal/print, internal/observe/diff*.go, docs/output.md, hack/live-check.sh.
Out: the `cluster access` exception (#5, rejected); library changes (proposed to the owner).

## Plan / design impact
design §3.3 rule 6 (passed count) and §4.2 (no longer observed) updated with the fixes.

## Definition of done
- [ ] acceptance tests fail before, pass after
- [ ] full suite passes
- [ ] cross-model review of `git diff 1e1bb0b838f679efd0fdabfeadf51d99dd1de7e9..HEAD` + triage
- [ ] phase-2.4-complete.md written; phase-status.md updated
