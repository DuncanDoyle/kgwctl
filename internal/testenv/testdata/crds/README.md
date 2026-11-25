# Test CRDs

`gateway-api/` and `kgateway/` are installed into every `testenv` envtest. Versions follow the latest kgateway OSS release when fetched (2026-11-05): **kgateway v2.5.0**, whose `go.mod` pins `sigs.k8s.io/gateway-api v1.7.0`.

| Directory | Source | Version | Changes |
|---|---|---|---|
| `gateway-api/standard/standard-install.yaml` | https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.7.0/standard-install.yaml | v1.7.0, standard channel | none |
| `gateway-api/experimental/experimental-only.yaml` | https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.7.0/experimental-install.yaml | v1.7.0, experimental channel | only the `gateway.networking.x-k8s.io` CRDs, so the standard CRDs are not installed twice |
| `xlistenerset/xlistenersets.yaml` | https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.4.1/experimental-install.yaml | v1.4.1, experimental channel | only the `xlistenersets.gateway.networking.x-k8s.io` CRD. Not installed by default: tests that need XListenerSet pass this directory in `Options.CRDDirs` (ruling R13) |
| `kgateway/` | https://github.com/kgateway-dev/kgateway/tree/v2.5.0/install/helm/kgateway-crds/templates | v2.5.0 | Helm escapes (`{{ "{{x}}" }}`) in one TrafficPolicy description rendered to `{{x}}` |

Fixtures label the controller to match these CRDs: `app.kubernetes.io/version: 2.5.0` and image tag `v2.5.0` (ruling R6). Bump both together with the CRDs.

envtest installs only `CustomResourceDefinition` documents, so the bundles' ValidatingAdmissionPolicy is ignored.

Gateway API v1.7.0's experimental channel has no `XListenerSet`; ListenerSet is in the standard channel. v1.4.1 is the newest release whose experimental channel still ships `XListenerSet`.

## When you bump these CRDs

1. Derive the Gateway API version from the kgateway release `go.mod`:
   ```bash
   gh api repos/kgateway-dev/kgateway/contents/go.mod?ref=<tag> --jq .content | base64 -d | grep sigs.k8s.io/gateway-api
   ```
2. Refresh the CRD files, then `git diff` them against the old ones. This is the CRD diff of decision P-012.
3. For each new kind, add it to `docs/rbac.yaml` (`TestReaderRoleCoversServedKinds` enforces this) and decide whether kgwctl supports it.
4. For each new served kind in the listed groups, check that the effective registry covers it. That is the descriptor set after deduplication, where a Snellius descriptor shadows an extra kind of `collect.Registry` in `internal/bundle/collect/resources.go`. If the effective registry lacks the kind or a served version, add an extra kind, or fix the Snellius descriptor that shadows it. `TestRegistryCoversCollectedKinds` enforces this. If a kind is left out on purpose, record the exclusion.
5. For each changed or removed kind, check served and storage versions, and stale watches or version mappings for a removed kind or version. Check new fields that hold credentials or Secret references, which redaction must cover.
6. Check every table or mapping keyed to a product, Gateway API or Envoy version. An empty CRD diff does not make these current.
7. Before claiming support for a new kgateway minor, review the mirrors of controller logic, such as the discovery namespace selector parsing and the Gateway label value helper. Update the minors they are pinned to and compare them with real controller output.
8. Check that each kind list has a test for its intended scope. A guard test, a behavioural test or a recorded exclusion counts. The lists are `docs/rbac.yaml` and the registry extra kinds.
9. Bump the fixture version labels with the CRDs.

This checklist follows decision P-012.
