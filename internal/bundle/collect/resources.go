// Package collect holds the built-in support-bundle collectors (phase 4
// design §6). Each reads through Env's guarded clients and writes only
// through its Sink.
package collect

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/DuncanDoyle/kgwctl/internal/proxydump"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// pageSize bounds each list request.
const pageSize = 500

// kgatewayCategory is the discovery category of kgateway's CRDs.
const kgatewayCategory = "kgateway"

// gatewayAPIGroups are listed whole: every served kind in them is collected.
var gatewayAPIGroups = map[string]bool{"gateway.networking.k8s.io": true, "gateway.networking.x-k8s.io": true}

var gatewayClassGK = schema.GroupKind{Group: gatewayGroup, Kind: "GatewayClass"}

// gatewayGroup is the Gateway API group.
const gatewayGroup = "gateway.networking.k8s.io"

// gatewayXGroup is the experimental Gateway API group.
const gatewayXGroup = "gateway.networking.x-k8s.io"

// unregistered are namespaced kinds in gatewayAPIGroups that the Snellius
// registry lacks. TestRegistryCoversCollectedKinds keeps the list complete.
var unregistered = []kinds.Descriptor{
	{Group: gatewayGroup, Kind: "UDPRoute", Resource: "udproutes", Versions: []string{"v1", "v1alpha2"}, Namespaced: true,
		Category: kinds.CategoryRoute, DisplayName: "UDPRoutes"},
	{Group: gatewayXGroup, Kind: "XBackend", Resource: "xbackends", Versions: []string{"v1alpha1"}, Namespaced: true,
		Category: kinds.CategoryBackend, DisplayName: "Backends (experimental)"},
	{Group: gatewayXGroup, Kind: "XBackendTrafficPolicy", Resource: "xbackendtrafficpolicies", Versions: []string{"v1alpha1"}, Namespaced: true,
		Category: kinds.CategoryPolicy, DisplayName: "BackendTrafficPolicies (experimental)"},
	{Group: gatewayXGroup, Kind: "XTelemetryPolicy", Resource: "xtelemetrypolicies", Versions: []string{"v1alpha1"}, Namespaced: true,
		Category: kinds.CategoryPolicy, DisplayName: "TelemetryPolicies (experimental)"},
}

// Registry returns a copy of reg that also describes the kinds the resources
// collector lists but reg lacks, so install detection loads them and the
// scope rules apply to them (design §3.2). reg's own descriptors win.
func Registry(reg *extension.Registry) *extension.Registry {
	r := *reg
	r.Kinds = append(slices.Clone(unregistered), reg.Kinds...)
	return &r
}

// forbidden are the core resources the bundle never requests (design §6.1).
var forbidden = map[string]bool{"secrets": true, "configmaps": true}

// kind is one resource type a collector lists.
type kind struct {
	gvr        schema.GroupVersionResource
	kind       string
	namespaced bool
}

func (k kind) gk() schema.GroupKind { return schema.GroupKind{Group: k.gvr.Group, Kind: k.kind} }

type resources struct{ reg *extension.Registry }

// Resources returns the collector of Gateway API kinds, kinds in the
// kgateway discovery category and the registry's kinds, filtered by Env.Keep
// and written under resources/<group>/<kind>/<namespace|_cluster>/.
func Resources(reg *extension.Registry) pb.Collector { return resources{reg: reg} }

// Name implements pb.Collector.
func (resources) Name() string { return "resources" }

// Collect implements pb.Collector.
func (c resources) Collect(ctx context.Context, env pb.Env, s pb.Sink) error {
	ks, err := c.kinds(ctx, env, s)
	if err != nil {
		return err
	}
	for _, k := range ks {
		if err := collectKind(ctx, env, s, k); err != nil {
			return err
		}
	}
	return nil
}

// discovered is one ServerGroupsAndResources answer.
type discovered struct {
	groups []*metav1.APIGroup
	lists  []*metav1.APIResourceList
}

// discover reads groups and resources bounded by ctx and RequestTimeout;
// client-go's discovery calls take no context.
func discover(ctx context.Context, env pb.Env) (discovered, error) {
	rctx, cancel := requestContext(ctx, env.RequestTimeout)
	defer cancel()
	return proxydump.Await(rctx, func() (discovered, error) {
		g, l, err := env.Discovery.ServerGroupsAndResources()
		return discovered{g, l}, err
	}, nil)
}

// kinds resolves the kinds to list from one discovery read, in group and kind
// order. A registry kind resolving to a forbidden core resource is recorded as
// an error and never requested.
func (c resources) kinds(ctx context.Context, env pb.Env, s pb.Sink) ([]kind, error) {
	d, err := discover(ctx, env)
	groups, lists := d.groups, d.lists
	if err != nil {
		if len(lists) == 0 {
			return nil, err
		}
		// Partial discovery: the groups that answered are still collected.
		s.Error(pb.Path{pb.ResourcesDir}, pb.Source{Operation: pb.OpList}, err)
	}
	byGV := map[string]*metav1.APIResourceList{}
	for _, l := range lists {
		byGV[l.GroupVersion] = l
	}
	var out []kind
	seen := map[schema.GroupKind]bool{}
	add := func(gv string, r metav1.APIResource) {
		v, err := schema.ParseGroupVersion(gv)
		if err != nil || strings.Contains(r.Name, "/") || !slices.Contains(r.Verbs, "list") {
			return
		}
		k := kind{gvr: v.WithResource(r.Name), kind: r.Kind, namespaced: r.Namespaced}
		// GatewayClass is the only cluster-scoped kind the bundle collects (design §3.2).
		if seen[k.gk()] || (!k.namespaced && k.gk() != gatewayClassGK) {
			return
		}
		seen[k.gk()] = true
		if k.gvr.Group == "" && forbidden[k.gvr.Resource] {
			p := pb.Path{pb.ResourcesDir, pb.Core}
			if _, err := pb.Name(pb.Kind, k.kind); err == nil {
				p = append(p, k.kind)
			}
			s.Error(p, pb.Source{Operation: pb.OpList}, pb.ErrForbiddenKind)
			return
		}
		out = append(out, k)
	}
	for _, g := range groups {
		l := byGV[g.PreferredVersion.GroupVersion]
		if l == nil {
			continue
		}
		for _, r := range l.APIResources {
			if gatewayAPIGroups[g.Name] || slices.Contains(r.Categories, kgatewayCategory) {
				add(l.GroupVersion, r)
			}
		}
	}
	if c.reg != nil {
		served, err := kinds.Resolve(lookup(byGV), c.reg.Kinds)
		if err != nil {
			return nil, err
		}
		for _, k := range served {
			gv := k.GVR().GroupVersion().String()
			for _, r := range byGV[gv].APIResources {
				if r.Name == k.Resource {
					add(gv, r)
				}
			}
		}
	}
	slices.SortFunc(out, func(a, b kind) int {
		return cmp.Or(cmp.Compare(a.gvr.Group, b.gvr.Group), cmp.Compare(a.kind, b.kind))
	})
	return out, nil
}

// lookup serves kinds.Resolve from the discovery read; a missing group
// version is NotFound, which Resolve treats as not served.
type lookup map[string]*metav1.APIResourceList

func (l lookup) ServerResourcesForGroupVersion(gv string) (*metav1.APIResourceList, error) {
	if r, ok := l[gv]; ok {
		return r, nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{}, gv)
}

// collectKind lists k in each scope namespace, or cluster-wide, and writes
// each kept object. It returns only ErrSealed or the context's error.
func collectKind(ctx context.Context, env pb.Env, s pb.Sink, k kind) error {
	group := pb.Core
	if k.gvr.Group != "" {
		group = k.gvr.Group
	}
	_, gerr := pb.Name(pb.Group, group)
	_, kerr := pb.Name(pb.Kind, k.kind)
	if err := errors.Join(gerr, kerr); err != nil {
		s.Error(pb.Path{pb.ResourcesDir}, pb.Source{Operation: pb.OpList}, err)
		return nil
	}
	base := pb.Path{pb.ResourcesDir, group, k.kind}
	nss := []string{""}
	if k.namespaced && env.Scope.Namespaces() != nil {
		nss = env.Scope.Namespaces()
	}
	for _, ns := range nss {
		if err := ctx.Err(); err != nil {
			return err
		}
		items, err := list(ctx, env, k.gvr, ns)
		if err != nil {
			p := base
			if ns != "" {
				p = append(slices.Clone(base), ns)
			}
			s.Error(p, pb.Source{Namespace: ns, Operation: pb.OpList}, err)
			continue
		}
		for i := range items {
			if err := writeResource(ctx, env, s, k, base, &items[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeResource writes u unless Env.Keep leaves it out, which is counted.
func writeResource(ctx context.Context, env pb.Env, s pb.Sink, k kind, base pb.Path, u *unstructured.Unstructured) error {
	u.SetGroupVersionKind(k.gvr.GroupVersion().WithKind(k.kind))
	if env.Keep != nil {
		if r := env.Keep(u); r != pb.ExcludeNone {
			return sealed(s.Exclude(k.gk(), r))
		}
	}
	ns := pb.Cluster
	if k.namespaced {
		ns = u.GetNamespace()
	}
	src := pb.Source{Namespace: u.GetNamespace(), Operation: pb.OpList}
	f, err := pb.File(u.GetName(), pb.YAML)
	if err != nil {
		s.Error(base, src, err)
		return nil
	}
	// The Sink validates ns and records a failure itself.
	return sealed(s.Object(ctx, append(slices.Clone(base), ns, f), u, src))
}

// sealed passes on ErrSealed only; the Sink records every other failure.
func sealed(err error) error {
	if errors.Is(err, pb.ErrSealed) {
		return err
	}
	return nil
}

// list returns every object of gvr in ns ("" for all namespaces or a
// cluster-scoped kind), page by page, each request bounded by RequestTimeout.
func list(ctx context.Context, env pb.Env, gvr schema.GroupVersionResource, ns string) ([]unstructured.Unstructured, error) {
	var ri dynamic.ResourceInterface = env.Dynamic.Resource(gvr)
	if ns != "" {
		ri = env.Dynamic.Resource(gvr).Namespace(ns)
	}
	var out []unstructured.Unstructured
	opts := metav1.ListOptions{Limit: pageSize}
	for {
		l, err := func() (*unstructured.UnstructuredList, error) {
			rctx, cancel := requestContext(ctx, env.RequestTimeout)
			defer cancel()
			return ri.List(rctx, opts)
		}()
		if err != nil {
			return nil, err
		}
		out = append(out, l.Items...)
		if l.GetContinue() == "" {
			return out, nil
		}
		opts.Continue = l.GetContinue()
	}
}

// requestContext bounds one request by d; d <= 0 leaves only ctx.
func requestContext(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}
