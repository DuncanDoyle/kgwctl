package view

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	"github.com/DuncanDoyle/kgateway-ui/pkg/cache"
	"github.com/DuncanDoyle/kgateway-ui/pkg/engine"
	"github.com/DuncanDoyle/kgateway-ui/pkg/kinds"
	"github.com/DuncanDoyle/kgateway-ui/pkg/objects"
	"github.com/DuncanDoyle/kgateway-ui/pkg/process"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/DuncanDoyle/kgwctl/internal/load"
)

// VersionView is the output of version (design §3.3).
type VersionView struct {
	Client BuildInfo `json:"client"`
	// Installs is empty for --client and when no install is detected.
	Installs []InstallVersion `json:"installs,omitempty"`
	// InstallsUnknown names the inputs that kept installs from being detected.
	InstallsUnknown string `json:"installsUnknown,omitempty"`
	// GatewayAPICRDs is zero for --client.
	GatewayAPICRDs CRDVersion `json:"gatewayAPICRDs,omitzero"`
	// Incomplete are the inputs this output depends on that are not ok (§6.3).
	Incomplete []load.Input `json:"incomplete,omitempty"`
}

// InstallVersion is one detected install and its processes' versions.
type InstallVersion struct {
	ID      string `json:"id"`
	Display string `json:"display"`
	// Controller is the install's version without "v"; "" when unknown.
	Controller string `json:"controller"`
	// Replicas is the controller Deployment's desired count; nil when there is none.
	Replicas *int `json:"replicas"`
	// Ready counts Ready controller Pods; nil when ReadyUnknown is set.
	Ready *int `json:"ready"`
	// ReadyUnknown says why Ready is not known, as "pods: denied".
	ReadyUnknown string `json:"readyUnknown,omitempty"`
	// Proxies is null when ProxiesUnknown is set, else one entry per Gateway and version.
	Proxies []ProxyVersion `json:"proxies"`
	// ProxiesUnknown names the ownership inputs that are not ok, as "gateways: denied".
	ProxiesUnknown string `json:"proxiesUnknown,omitempty"`
}

// ProxyVersion is the proxy Pods of one Gateway that run one version.
type ProxyVersion struct {
	// Gateway is "namespace/name".
	Gateway string `json:"gateway"`
	// Version is the proxy image tag without "v"; "" when unknown.
	Version string `json:"version,omitempty"`
	// Pods is set, possibly to 0, for every observed entry; nil when Unknown is set.
	Pods *int `json:"pods,omitempty"`
	// DiffersFromController marks a version other than the controller's.
	DiffersFromController bool `json:"differsFromController,omitempty"`
	// Unknown says why the Gateway's proxies were not read, as "proxy pods: denied".
	Unknown string `json:"unknown,omitempty"`
}

// CRDVersion is the Gateway API CRD bundle in the cluster.
type CRDVersion struct {
	BundleVersion string `json:"bundleVersion,omitempty"`
	Channel       string `json:"channel,omitempty"`
	// Unknown is the reason when the bundle is not known, as "denied".
	Unknown string `json:"unknown,omitempty"`
}

// gatewayCRD is the CRD whose bundle label wins a tie, as in the library's skew check.
const gatewayCRD = "gateways.gateway.networking.k8s.io"

// Version builds the version view of b and, when r is not nil, of the cluster
// r loaded, narrowed to the --install install f selects.
func Version(b BuildInfo, r *load.Result, f *Filter) (VersionView, error) {
	v := VersionView{Client: b}
	if r == nil {
		return v, nil
	}
	if err := f.pinErr(); err != nil {
		return VersionView{}, err
	}
	// Ownership inputs enumerate each install's Gateways (§6.3).
	detect := []schema.GroupVersionResource{cache.NamespacesGVR, cache.DeploymentsGVR, cache.PodsGVR}
	owners := slices.Clone(detect)
	for _, c := range []kinds.Category{kinds.CategoryGatewayClass, kinds.CategoryGateway, kinds.CategoryListenerSet} {
		for _, k := range kinds.ByCategory(r.Served, c) {
			owners = append(owners, k.GVR())
		}
	}
	for _, gvr := range append(slices.Clone(owners), cache.ProxyPodsGVR, cache.CRDsGVR) {
		if in, ok := r.Inputs[gvr]; ok && in.State != load.StateOK {
			v.Incomplete = append(v.Incomplete, in)
		}
	}
	slices.SortFunc(v.Incomplete, func(a, b load.Input) int { return cmp.Compare(a.Resource, b.Resource) })

	ownersDown := unavailable(r, owners)
	proxyPods, watched := r.Inputs[cache.ProxyPodsGVR]
	for i, im := range r.Model.Installs {
		if f.selected < 0 || i == f.selected {
			v.Installs = append(v.Installs, installVersion(im, r.Inputs[cache.PodsGVR], ownersDown, proxyPods, watched))
		}
	}
	if len(v.Installs) == 0 {
		v.InstallsUnknown = unavailable(r, detect)
	}
	v.GatewayAPICRDs = crdVersion(r)
	return v, nil
}

// installVersion builds im's entry; pods is the controller Pod input.
func installVersion(im engine.InstallModel, pods load.Input, ownersDown string, proxyPods load.Input, watched bool) InstallVersion {
	iv := InstallVersion{ID: im.Resolved.ID(), Display: im.Resolved.Display(), Controller: trimV(im.Resolved.Version)}
	proc := im.Process
	if proc != nil {
		if d := proc.Controller.Desired; d != nil {
			iv.Replicas = new(int(*d))
		}
		// Not observed means the Pod input is down or there is no Deployment: never 0 ready.
		if proc.Controller.Observed {
			n := 0
			for _, p := range proc.Controller.Processes {
				if p.Ready != nil && *p.Ready {
					n++
				}
			}
			iv.Ready = &n
		}
	}
	if iv.Ready == nil {
		iv.ReadyUnknown = "no controller Deployment"
		if pods.State != load.StateOK && pods.State != "" {
			iv.ReadyUnknown = label(pods)
		}
	}
	if ownersDown != "" {
		iv.ProxiesUnknown = ownersDown
		return iv
	}
	iv.Proxies = []ProxyVersion{}
	if im.Owned == nil {
		return iv
	}
	gws := make([]objects.Ref, 0, len(im.Owned.Gateways))
	for _, gw := range im.Owned.Gateways {
		gws = append(gws, objects.RefOf(gw))
	}
	slices.SortFunc(gws, objects.Ref.Compare)
	for _, gw := range gws {
		name := gw.Namespace + "/" + gw.Name
		switch {
		case !watched:
			iv.Proxies = append(iv.Proxies, ProxyVersion{Gateway: name, Unknown: "proxy pods: not watched"})
			continue
		case proxyPods.State != load.StateOK:
			iv.Proxies = append(iv.Proxies, ProxyVersion{Gateway: name, Unknown: label(proxyPods)})
			continue
		}
		var set process.Set
		if proc != nil {
			set = proc.Proxies[gw]
		}
		iv.Proxies = append(iv.Proxies, proxyVersions(name, set, iv.Controller)...)
	}
	return iv
}

// proxyVersions groups set's Pods by version, in version order.
func proxyVersions(gw string, set process.Set, controller string) []ProxyVersion {
	if !set.Observed {
		reason := "no proxy Deployment"
		if set.Desired != nil {
			reason = "proxy Pods not observed"
		}
		return []ProxyVersion{{Gateway: gw, Unknown: reason}}
	}
	if len(set.Processes) == 0 {
		return []ProxyVersion{{Gateway: gw, Pods: new(0)}}
	}
	count := map[string]int{}
	for _, p := range set.Processes {
		count[p.Version]++
	}
	var out []ProxyVersion
	for _, ver := range slices.Sorted(maps.Keys(count)) {
		out = append(out, ProxyVersion{Gateway: gw, Version: ver, Pods: new(count[ver]),
			DiffersFromController: ver != "" && controller != "" && trimV(ver) != trimV(controller)})
	}
	return out
}

// crdVersion is the most common bundle label among the annotated Gateway API
// CRDs; a tie goes to the Gateway CRD, else the first by name.
func crdVersion(r *load.Result) CRDVersion {
	in, ok := r.Inputs[cache.CRDsGVR]
	switch {
	case !ok:
		return CRDVersion{Unknown: "not watched"}
	case in.State != load.StateOK:
		return CRDVersion{Unknown: string(in.State)}
	}
	crds := slices.Clone(r.Model.Snapshot.CRDs)
	slices.SortFunc(crds, func(a, b cache.CRDMeta) int { return cmp.Compare(a.Name, b.Name) })
	key := func(c cache.CRDMeta) string { return c.BundleVersion + " " + c.Channel }
	count := map[string]int{}
	var best *cache.CRDMeta
	for i, c := range crds {
		if c.BundleVersion == "" {
			continue
		}
		count[key(c)]++
		if best == nil {
			best = &crds[i]
		}
	}
	if best == nil {
		if len(crds) == 0 {
			return CRDVersion{Unknown: "not installed"}
		}
		return CRDVersion{Unknown: "no bundle-version annotation"}
	}
	for i, c := range crds {
		if c.BundleVersion == "" {
			continue
		}
		n, top := count[key(c)], count[key(*best)]
		if n > top || n == top && c.Name == gatewayCRD {
			best = &crds[i]
		}
	}
	return CRDVersion{BundleVersion: best.BundleVersion, Channel: best.Channel}
}

// unavailable labels the inputs of gvrs that are not ok, as "gateways: denied, pods: loading".
func unavailable(r *load.Result, gvrs []schema.GroupVersionResource) string {
	var out []string
	for _, gvr := range gvrs {
		if in, ok := r.Inputs[gvr]; ok && in.State != load.StateOK {
			out = append(out, label(in))
		}
	}
	slices.Sort(out)
	return strings.Join(slices.Compact(out), ", ")
}

// label is an input's short name and state, as "gateways: denied".
func label(in load.Input) string {
	name := in.Resource
	if in.GVR != cache.ProxyPodsGVR {
		name = in.GVR.Resource
	}
	return name + ": " + string(in.State)
}

func trimV(s string) string { return strings.TrimPrefix(s, "v") }
