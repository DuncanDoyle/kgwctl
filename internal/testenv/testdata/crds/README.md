# Test CRDs

Installed into every `testenv` envtest. Versions follow the latest kgateway OSS release when fetched (2026-11-05): **kgateway v2.5.0**, whose `go.mod` pins `sigs.k8s.io/gateway-api v1.7.0`.

| Directory | Source | Version | Changes |
|---|---|---|---|
| `gateway-api/standard/standard-install.yaml` | https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.7.0/standard-install.yaml | v1.7.0, standard channel | none |
| `gateway-api/experimental/experimental-only.yaml` | https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.7.0/experimental-install.yaml | v1.7.0, experimental channel | only the `gateway.networking.x-k8s.io` CRDs, so the standard CRDs are not installed twice |
| `kgateway/` | https://github.com/kgateway-dev/kgateway/tree/v2.5.0/install/helm/kgateway-crds/templates | v2.5.0 | Helm escapes (`{{ "{{x}}" }}`) in one TrafficPolicy description rendered to `{{x}}` |

envtest installs only `CustomResourceDefinition` documents, so the bundles' ValidatingAdmissionPolicy is ignored.

Gateway API v1.7.0's experimental channel has no `XListenerSet`; ListenerSet is in the standard channel.

To refresh, derive the Gateway API version from the release's `go.mod`:

```bash
gh api repos/kgateway-dev/kgateway/contents/go.mod?ref=<tag> --jq .content | base64 -d | grep sigs.k8s.io/gateway-api
```
