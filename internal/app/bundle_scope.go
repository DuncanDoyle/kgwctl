package app

import (
	"fmt"
	"slices"
	"strings"

	"github.com/DuncanDoyle/snellius/pkg/cache"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	ib "github.com/DuncanDoyle/kgwctl/internal/bundle"
	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/view"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// discoverySource names the interim mirror of the controller's discovery
// scope; it switches to the kgateway#15070 endpoint when that lands.
const discoverySource = "snellius M-4 (interim): " + install.DiscoveryNamespaceSelectorsEnv

// defaultControllerMetricsPort is used when an install names no metrics port.
const defaultControllerMetricsPort = 9092

// resolveScope resolves the root flags once into the run's Scope, the kept
// Gateways, the keep function and stderr warnings (design §3.2). An
// --install that names no detected install is a usage error (exit 2).
func resolveScope(f *Flags, explicitNamespace bool, res *load.Result) (pb.Scope, ib.GatewaySet, func(*unstructured.Unstructured) pb.ExcludeReason, []string, error) {
	sc := pb.Scope{AllControllers: f.AllControllers}
	if explicitNamespace && !f.AllNamespaces {
		sc.Namespace = f.Namespace
	}
	all := res.Model.Installs
	selected := all
	if f.Install != "" {
		id, err := install.ParsePinned(f.Install)
		if err != nil {
			return pb.Scope{}, nil, nil, nil, usageErr(err)
		}
		i := slices.IndexFunc(all, func(im engine.InstallModel) bool { return im.Resolved.ID() == id })
		if i < 0 {
			return pb.Scope{}, nil, nil, nil, usageErr(fmt.Errorf("--install %q: no such install detected", f.Install))
		}
		selected = all[i : i+1]
	}

	nsByName := map[string]*corev1.Namespace{}
	for _, n := range res.Model.Snapshot.Namespaces {
		nsByName[n.Name] = n
	}
	nsReadable := res.Inputs[cache.NamespacesGVR].State == load.StateOK
	for _, im := range selected {
		in := pb.Install{Namespace: im.Resolved.Namespace, Deployment: im.Resolved.Name, Chosen: "detected",
			MetricsPort: im.Resolved.MetricsPort, Discovery: discoveryScope(im.Resolved.Scope, nsReadable, res.Model.Snapshot.Namespaces)}
		if f.Install != "" {
			in.Chosen = "flag"
		}
		if in.MetricsPort == 0 {
			in.MetricsPort = defaultControllerMetricsPort
		}
		sc.Installs = append(sc.Installs, in)
	}

	filter, warns := view.NewFilter(res, view.Scope{AllNamespaces: true, Install: f.Install, AllControllers: f.AllControllers})
	switch {
	case len(all) == 0 && f.AllControllers:
		warns = append(warns, "no kgateway install detected; controller collectors are skipped")
	case len(selected) > 1:
		warns = append(warns, fmt.Sprintf("%d kgateway installs detected; the bundle covers all of them, --install selects one", len(selected)))
	}

	served := map[schema.GroupKind]kinds.Served{}
	for _, k := range res.Served {
		served[schema.GroupKind{Group: k.Group, Kind: k.Kind}] = k
	}
	// Scope filters only while every selected install's scope is known and Namespaces were read.
	scoped := nsReadable && len(selected) > 0
	for _, im := range selected {
		scoped = scoped && im.Resolved.Scope.Mode != install.ScopeUnknown
	}
	keep := func(u *unstructured.Unstructured) pb.ExcludeReason {
		if f.AllControllers {
			return pb.ExcludeNone
		}
		if !filter.Visible(served[u.GroupVersionKind().GroupKind()], u) {
			return pb.ExcludeOwnership
		}
		ns := u.GetNamespace()
		if !scoped || ns == "" {
			return pb.ExcludeNone
		}
		for _, im := range selected {
			if im.Resolved.Scope.Contains(ns, nsByName[ns]) {
				return pb.ExcludeNone
			}
		}
		return pb.ExcludeScope
	}

	gws := keptGateways(res, sc, keep)
	sc.WorkloadNamespaces = workloadNamespaces(res, sc, keep)
	return sc, gws, keep, warns, nil
}

// discoveryScope records an install's Snellius scope for the manifest.
// Unwatched is a local loop until Snellius ships Scope.Excluded.
func discoveryScope(s install.Scope, nsReadable bool, nss []*corev1.Namespace) pb.DiscoveryScope {
	d := pb.DiscoveryScope{Mode: string(s.Mode), Reason: s.Err, Source: discoverySource}
	switch {
	case strings.HasPrefix(s.Source, "pod "):
		d.Source += " on pod"
	case s.Source == install.SourceDeploymentTemplate:
		d.Source += " on deployment"
	}
	if s.Mode != install.ScopeSelectors {
		return d
	}
	for i := range s.Selectors {
		d.Selectors = append(d.Selectors, pb.FormatSelector(&s.Selectors[i]))
	}
	if !nsReadable {
		d.Mode, d.Reason = "namespaces-unreadable", "Namespaces could not be read; nothing is scope-filtered"
		return d
	}
	for _, n := range nss {
		if !s.Contains(n.Name, n) {
			d.Unwatched = append(d.Unwatched, n.Name)
		}
	}
	slices.Sort(d.Unwatched)
	return d
}

// keptGateways maps each kept Gateway in the -n bound to its single owning
// install; "" when no install or several claim it.
func keptGateways(res *load.Result, sc pb.Scope, keep func(*unstructured.Unstructured) pb.ExcludeReason) ib.GatewaySet {
	owners := map[objects.Ref][]string{}
	for _, im := range res.Model.Installs {
		if im.Owned == nil {
			continue
		}
		for _, g := range im.Owned.Gateways {
			ref := objects.RefOf(g)
			owners[ref] = append(owners[ref], im.Resolved.ID())
		}
	}
	gws := ib.GatewaySet{}
	for _, k := range kinds.ByCategory(res.Served, kinds.CategoryGateway) {
		for _, u := range res.Model.Snapshot.Objects[k.GVR()] {
			if (sc.Namespace != "" && u.GetNamespace() != sc.Namespace) || keep(u) != pb.ExcludeNone {
				continue
			}
			id := ""
			if o := owners[objects.RefOf(u)]; len(o) == 1 {
				id = o[0]
			}
			gws[types.NamespacedName{Namespace: u.GetNamespace(), Name: u.GetName()}] = id
		}
	}
	return gws
}

// workloadNamespaces bounds workload collection (design §3.2): install
// namespaces, kept namespaced resources and the Services kept routes name,
// within the -n bound. nil means all; --all-controllers gives the bound itself.
func workloadNamespaces(res *load.Result, sc pb.Scope, keep func(*unstructured.Unstructured) pb.ExcludeReason) []string {
	bound := sc.Namespaces()
	if sc.AllControllers {
		if bound != nil {
			slices.Sort(bound)
		}
		return bound
	}
	set := map[string]bool{}
	for _, in := range sc.Installs {
		set[in.Namespace] = true
	}
	for _, k := range res.Served {
		if !k.Namespaced {
			continue
		}
		for _, u := range res.Model.Snapshot.Objects[k.GVR()] {
			if keep(u) != pb.ExcludeNone {
				continue
			}
			set[u.GetNamespace()] = true
			if k.Category != kinds.CategoryRoute {
				continue
			}
			for _, t := range objects.RouteBackendTargets(u) {
				if t.Ref.Group == "" && t.Ref.Kind == "Service" {
					set[t.Ref.Namespace] = true
				}
			}
		}
	}
	return boundedSorted(set, bound)
}

// withReplicaNamespaces adds the replicas' namespaces to the workload
// namespaces, within the -n bound; all namespaces stay all.
func withReplicaNamespaces(sc pb.Scope, rs []pb.Replica) pb.Scope {
	if sc.WorkloadNamespaces == nil {
		return sc
	}
	set := map[string]bool{}
	for _, n := range sc.WorkloadNamespaces {
		set[n] = true
	}
	for _, r := range rs {
		set[r.Namespace] = true
	}
	sc.WorkloadNamespaces = boundedSorted(set, sc.Namespaces())
	return sc
}

// boundedSorted returns the names in set that bound allows (nil allows all), sorted and non-nil.
func boundedSorted(set map[string]bool, bound []string) []string {
	out := []string{}
	for n := range set {
		if n != "" && (bound == nil || slices.Contains(bound, n)) {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

// controllerSelectors returns each selected install's controller Pod selector,
// the Deployment's spec.selector. An install without a usable one gets an
// error naming it, so its missing controller replicas leave a trace.
func controllerSelectors(res *load.Result, sc pb.Scope) ([]ib.ControllerSelector, []error) {
	var (
		out  []ib.ControllerSelector
		errs []error
	)
	for _, in := range sc.Installs {
		skip := func(why string) {
			errs = append(errs, &ib.ReplicaError{Path: pb.Path{pb.ControllerDir, in.Namespace}, Source: pb.Source{Namespace: in.Namespace},
				Err: fmt.Errorf("install %s/%s: %s; controller replicas not inventoried", in.Namespace, in.Deployment, why)})
		}
		i := slices.IndexFunc(res.Model.Installs, func(im engine.InstallModel) bool {
			return im.Resolved.Namespace == in.Namespace && im.Resolved.Name == in.Deployment
		})
		if i < 0 || res.Model.Installs[i].Resolved.Deployment == nil {
			skip("no Deployment")
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(res.Model.Installs[i].Resolved.Deployment.Spec.Selector)
		if err != nil || sel.Empty() {
			skip("no valid Deployment selector")
			continue
		}
		out = append(out, ib.ControllerSelector{Install: in, Selector: sel})
	}
	return out, errs
}
