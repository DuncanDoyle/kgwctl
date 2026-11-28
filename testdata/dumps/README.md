# Golden Envoy config dumps

Raw `kgwctl proxy dump` output from kind running kgateway OSS, used by the `proxy routes` tests.

| File | Gateway | Contents |
|---|---|---|
| `demo.json` | `team-a/team-a` | snellius-demo use cases |
| `policies.json` | `kgwctl-proxy/proxy` | transformation, ext_authz and local rate limit on `orders`, ext_authz disabled on `public`, colliding routes `kgwctl-proxy/orders` and `proxy/orders-kgwctl`; no route metadata |
| `collide-meta.json` | `kgwctl-proxy/proxy` | the same Gateway with `dev.kgateway.route_source` route metadata |

The `kgwctl-proxy/proxy` fixtures are applied from `hack/live/proxy-fixtures.yaml` by `hack/live-check.sh proxy`. `hack/live-check.sh goldens` captures all three files.

## Capture

| What | Value |
|---|---|
| kgateway | v2.5.1 (Helm chart `kgateway-v2.5.1`) |
| Envoy (`/server_info`) | `aacd0340210b536e99ec44ebe59ab9d16b259552/1.39.6/Clean/RELEASE/BoringSSL` |
| Helm `enableRouteSourceMetadata` | `false` (default) for `demo.json` and `policies.json`; `true` for `collide-meta.json` |
| Date | 2026-11-21 |

## Content check

The dumps must hold no credential material. `hack/live-check.sh goldens-check` runs both queries; each must return 0.

(a) Secret entries hold only file references or `[redacted]`. The kgateway bootstrap has two, both file references: `SYSTEM_CA_CERT` and `xds-jwt-token`.

```
[.configs[] | select((."@type" // "") | endswith(".SecretsConfigDump"))
  | ((.static_secrets // []) + (.dynamic_active_secrets // []) + (.dynamic_warming_secrets // []))[].secret
  | paths(scalars) as $p | ($p | map(tostring) | join(".")) as $k
  | select(($k | test("(^|\\.)inline_(bytes|string)$"))
      or (($k == "@type" or $k == "name" or ($k | test("(^|\\.)filename$")) or getpath($p) == "[redacted]") | not))]
  | length
```

(b) Any leaf whose key is exactly one of these names is `[redacted]`.

```
[paths(scalars) as $p
  | select($p[-1] | IN("inline_bytes", "inline_string", "private_key", "password", "client_secret", "token"))
  | getpath($p) | select(. != "[redacted]")] | length
```

`goldens-check` also runs `kgwctl proxy routes --file <dump> -o json` on each dump: a `kgwctl.kgateway.dev/v1alpha2` document with listeners and a route with filters.

Result on 2026-11-27:

```
content demo.json: secret-leaves=0 unredacted-sensitive=0
routes json demo.json: ok
content policies.json: secret-leaves=0 unredacted-sensitive=0
routes json policies.json: ok
content collide-meta.json: secret-leaves=0 unredacted-sensitive=0
routes json collide-meta.json: ok
```
