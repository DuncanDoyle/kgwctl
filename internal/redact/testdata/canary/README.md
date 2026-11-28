# Canary corpus

One directory per row of design §10's canary list: `<NN>-<short-name>/`. Each holds input files and a `fixture.yaml`. Every planted secret is a canary, `kgwctl-canary-<row>-<loc>` or a secret-shaped value (JWT, `sk-…`, AWS key, Bearer, PEM) that contains one. Rows 10 and 11 use values that match no pattern, so only structural rules can catch them.

## fixture.yaml

```yaml
row: 9
title: short description
cases:
  - name: config-dump            # unique within the fixture
    type: config-dump            # object | config-dump | json | prometheus | text | error-text | remote-error
    input: config_dump.json      # file next to fixture.yaml; object inputs are YAML
    options:
      extraHeaders: [x-custom]   # redact.Options.ExtraHeaders
    canaries:
      - value: kgwctl-canary-09-rh
        rule: header-value       # expected Finding.Rule; omit to check absence only
        location: configs[]...   # expected Finding.Location
        findingsOnly: true       # a map key: kept in the output, never in a Finding
    keep: ["[redacted]"]         # strings that must survive unchanged
    expectError: invalid-json    # forbidden-kind | invalid-json | not-prometheus
    readError: true              # text: the reader fails after the input
    server: https://10.0.0.1:6443 # error-text: the API server
    error:                       # remote-error: the error to build
      kind: status               # status | log-stream
      code: 403
      reason: Forbidden
      message: pods "x" is forbidden
deferred:
  - part: what this row also covers
    task: "4.1.4"                # the task that replays it
```

A case passes when no canary appears in the output (raw or standard base64), no Finding location holds a canary, each canary with a `rule` has a Finding with that rule and location, and every `keep` string survives. Only fixed schema field names appear literally in locations; every other key appears as `[key]`, array elements as `[]`, text lines as `line:<n>`.

Every case without `expectError` lists `keep` content beyond the marker, and an object case keeps each object's name (each member's for a List), so a redactor that empties its input fails.

## Replayed later

Parts of a row that need the writer, the command or the transport live under `deferred`, with a fixture file where one makes sense. Task 4.1.4 replays every Sink input row through the production writer and scans the extracted archive.

- Row 17: a denied list seen by the bundle `check` path: 4.2.5 (command).
- Row 19: the `--redact-header` value and an `-o` path in the manifest and member names: 4.2.5 (command).
- Row 20: a downstream kind resolving to `secrets`: 4.1.4 (writer request guard), 4.3.x (transport).
- Row 21: a downstream collector writing an undeclared type: 4.1.4 (writer).
- Row 17: a port-forward denial at the transport: 4.3.x; its error shape is tested here.
