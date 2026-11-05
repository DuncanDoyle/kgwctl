# Draft: kgwctl proposal for kgateway maintainers (program P-005)

**Status:** draft, not filed. Deferred by the owner (D-011); kept for when the project goes public.
**Target:** a GitHub issue (or discussion) in kgateway-dev/kgateway, using the repo's feature-request template headings when filed.

---

**Title:** Proposal: `kgwctl`, a read-only CLI to inspect and troubleshoot kgateway

kgateway users debug with `kubectl get -o yaml`, port-forwards to admin ports and pasted config dumps. The issue tracker shows the same questions over and over:
- status that is missing, stale or misleading, including orphan routes a controller cannot report on ([#12224](https://github.com/kgateway-dev/kgateway/issues/12224), [#14012](https://github.com/kgateway-dev/kgateway/issues/14012));
- policies that don't apply ([#12846](https://github.com/kgateway-dev/kgateway/issues/12846));
- routes that report Accepted while Envoy rejects them ([#14453](https://github.com/kgateway-dev/kgateway/issues/14453));
- version skew ([#13735](https://github.com/kgateway-dev/kgateway/issues/13735)).

Comparable projects ship a CLI for exactly this: `istioctl analyze`/`proxy-config`, `egctl`, `linkerd check`. kgateway's security self-assessment already lists `kgwctl` as a component.

An earlier attempt, [#13654](https://github.com/kgateway-dev/kgateway/pull/13654), forked about 19 files of gwctl and closed unreviewed. This proposal takes a different shape:
- **Read-only.** No writes and no port-forward unless a flag allows it. Every command honours `--context`.
- **Built on shared libraries.** Gateway API state and findings come from a Go library also used by a kgateway web UI. Envoy views come from a config-dump library. Neither is a gwctl fork, and neither brings graphviz or similar heavy dependencies.
- **Delivered in steps:**
  1. `get`/`describe` with status;
  2. `check` with stable finding codes and CI exit codes;
  3. Envoy route and filter-chain views;
  4. `analyze -f` before apply;
  5. effective policy.
- **Extensible at build time.** Downstream distributions build their own binary from the same module. No vendor code lives in kgateway.

**Ask:** do maintainers want `kgwctl` in kgateway-dev/kgateway, and if so, as a separate Go module in this repo, or as its own repo under kgateway-dev?

The work is developed out of tree until then, at [DuncanDoyle/kgwctl](https://github.com/DuncanDoyle/kgwctl). It moves in once the libraries are license-clean and the maintainers agree on layout and CI.

Open question: should the libraries it depends on move into kgateway-dev as well, or stay separate modules?
