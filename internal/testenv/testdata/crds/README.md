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

To refresh, derive the Gateway API version from the release's `go.mod`:

```bash
gh api repos/kgateway-dev/kgateway/contents/go.mod?ref=<tag> --jq .content | base64 -d | grep sigs.k8s.io/gateway-api
```
