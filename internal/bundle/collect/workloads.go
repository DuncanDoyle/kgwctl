package collect

import (
	"context"
	"maps"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// workloadKind is one workload list and the file it is written to.
type workloadKind struct {
	kind
	file string
}

var workloadKinds = []workloadKind{
	{kind{gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, kind: "Deployment", namespaced: true}, pb.DeploymentsFile},
	{kind{gvr: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, kind: "Pod", namespaced: true}, pb.PodsFile},
	{kind{gvr: schema.GroupVersionResource{Version: "v1", Resource: "services"}, kind: "Service", namespaced: true}, pb.ServicesFile},
	{kind{gvr: schema.GroupVersionResource{Version: "v1", Resource: "events"}, kind: "Event", namespaced: true}, pb.EventsFile},
}

type workloads struct{}

// Workloads returns the collector of Deployments, Pods, Services and Events
// in Scope.WorkloadNamespaces (nil means all), one List per namespace and
// kind under workloads/<namespace>/. Workloads are filtered per namespace,
// never per object; Events only by the window.
func Workloads() pb.Collector { return workloads{} }

// Name implements pb.Collector.
func (workloads) Name() string { return "workloads" }

// Collect implements pb.Collector.
func (workloads) Collect(ctx context.Context, env pb.Env, s pb.Sink) error {
	for _, wk := range workloadKinds {
		byNS := map[string][]unstructured.Unstructured{}
		if nss := env.Scope.WorkloadNamespaces; nss == nil {
			items, err := list(ctx, env, wk.gvr, "")
			if err != nil {
				s.Error(pb.Path{pb.WorkloadsDir, wk.file}, pb.Source{Operation: pb.OpList}, err)
			}
			for _, u := range items {
				byNS[u.GetNamespace()] = append(byNS[u.GetNamespace()], u)
			}
		} else {
			for _, ns := range nss {
				if err := ctx.Err(); err != nil {
					return err
				}
				items, err := list(ctx, env, wk.gvr, ns)
				if err != nil {
					s.Error(pb.Path{pb.WorkloadsDir, ns, wk.file}, pb.Source{Namespace: ns, Operation: pb.OpList}, err)
					continue
				}
				byNS[ns] = items // an empty namespace still gets its file
			}
		}
		for _, ns := range slices.Sorted(maps.Keys(byNS)) {
			if err := writeList(ctx, env, s, wk, ns, byNS[ns]); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeList writes objs as one v1 List; Events outside the window are left out.
func writeList(ctx context.Context, env pb.Env, s pb.Sink, wk workloadKind, ns string, objs []unstructured.Unstructured) error {
	items := []any{}
	for i := range objs {
		u := &objs[i]
		if wk.kind.kind == "Event" && !inWindow(u, env.Window.Since) {
			continue
		}
		u.SetGroupVersionKind(wk.gvr.GroupVersion().WithKind(wk.kind.kind))
		items = append(items, u.Object)
	}
	l := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "List", "metadata": map[string]any{}, "items": items}}
	// The Sink validates ns and records a failure itself.
	return sealed(s.Object(ctx, pb.Path{pb.WorkloadsDir, ns, wk.file}, l, pb.Source{Namespace: ns, Operation: pb.OpList}))
}

// inWindow reports whether an Event's latest of lastTimestamp, eventTime and
// series.lastObservedTime is at or after since. A zero since keeps all.
func inWindow(u *unstructured.Unstructured, since time.Time) bool {
	if since.IsZero() {
		return true
	}
	// An Event with none of the three timestamps stays zero and is left out by design.
	var latest time.Time
	for _, f := range [][]string{{"lastTimestamp"}, {"eventTime"}, {"series", "lastObservedTime"}} {
		v, _, _ := unstructured.NestedString(u.Object, f...)
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil && t.After(latest) {
			latest = t
		}
	}
	return !latest.Before(since)
}
