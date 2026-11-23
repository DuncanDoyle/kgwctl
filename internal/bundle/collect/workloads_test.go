package collect_test

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"

	"github.com/DuncanDoyle/kgwctl/internal/bundle/collect"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// recordSink keeps Object writes by path and counts everything else.
type recordSink struct {
	mu       sync.Mutex
	objects  map[string]*unstructured.Unstructured
	errs     []error
	excluded map[schema.GroupKind]map[pb.ExcludeReason]int
}

func newRecordSink() *recordSink {
	return &recordSink{objects: map[string]*unstructured.Unstructured{}, excluded: map[schema.GroupKind]map[pb.ExcludeReason]int{}}
}

func (s *recordSink) Object(p pb.Path, u *unstructured.Unstructured, _ pb.Source) error {
	if err := pb.ValidatePath(p); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[p.String()] = u.DeepCopy()
	return nil
}
func (*recordSink) ConfigDump(pb.Path, io.Reader, pb.Source) error { return nil }
func (*recordSink) JSON(pb.Path, io.Reader, pb.Source) error       { return nil }
func (*recordSink) Prometheus(pb.Path, io.Reader, pb.Source) error { return nil }
func (*recordSink) Text(pb.Path, io.Reader, pb.Source) error       { return nil }
func (s *recordSink) Error(_ pb.Path, _ pb.Source, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs = append(s.errs, err)
}
func (s *recordSink) Exclude(gk schema.GroupKind, r pb.ExcludeReason) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.excluded[gk] == nil {
		s.excluded[gk] = map[pb.ExcludeReason]int{}
	}
	s.excluded[gk][r]++
	return nil
}

// listItems returns the names in the List written at path.
func listItems(t *testing.T, s *recordSink, path string) []string {
	t.Helper()
	u, ok := s.objects[path]
	if !ok {
		t.Fatalf("no object at %s; have %v", path, keys(s.objects))
	}
	items, _, _ := unstructured.NestedSlice(u.Object, "items")
	var names []string
	for _, it := range items {
		names = append(names, (&unstructured.Unstructured{Object: it.(map[string]any)}).GetName())
	}
	return names
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func event(name string, fields map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Event",
		"metadata": map[string]any{"namespace": "team-a", "name": name}}}
	for k, v := range fields {
		u.Object[k] = v
	}
	return u
}

// TestEventsWindow: an Event counts by the latest of lastTimestamp, eventTime
// and series.lastObservedTime.
func TestEventsWindow(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	old, recent := "2025-12-01T00:00:00Z", "2026-01-01T12:00:00.000000Z"
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		{Group: "apps", Version: "v1", Resource: "deployments"}: "DeploymentList",
		{Version: "v1", Resource: "pods"}:                       "PodList",
		{Version: "v1", Resource: "services"}:                   "ServiceList",
		{Version: "v1", Resource: "events"}:                     "EventList",
	},
		event("series", map[string]any{"lastTimestamp": old, "eventTime": old, "series": map[string]any{"lastObservedTime": recent}}),
		event("event-time", map[string]any{"lastTimestamp": old, "eventTime": recent}),
		event("last", map[string]any{"lastTimestamp": recent}),
		event("stale", map[string]any{"lastTimestamp": old, "eventTime": old, "series": map[string]any{"lastObservedTime": old}}),
	)
	env := pb.Env{Dynamic: dyn, Scope: pb.Scope{WorkloadNamespaces: []string{"team-a"}}, Window: pb.Window{Since: since}}
	s := newRecordSink()
	if err := collect.Workloads().Collect(context.Background(), env, s); err != nil {
		t.Fatal(err)
	}
	if len(s.errs) != 0 {
		t.Fatalf("errors: %v", s.errs)
	}
	got := map[string]bool{}
	for _, n := range listItems(t, s, "workloads/team-a/events.yaml") {
		got[n] = true
	}
	if !got["series"] || !got["event-time"] || !got["last"] || got["stale"] || len(got) != 3 {
		t.Errorf("events kept %v; want series, event-time and last", got)
	}
	if names := listItems(t, s, "workloads/team-a/pods.yaml"); len(names) != 0 {
		t.Errorf("pods %v, want an empty list", names)
	}
}
