package collect_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	discfake "k8s.io/client-go/discovery/fake"
	dynfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/DuncanDoyle/kgwctl/internal/bundle/collect"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

func obj(apiVersion, kind, ns, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(apiVersion)
	u.SetKind(kind)
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

// TestResourcesKinds: Gateway API kinds and the kgateway category are listed,
// other groups are not, a registry kind naming Secrets is an error and never
// requested, and Keep's exclusions are counted.
func TestResourcesKinds(t *testing.T) {
	list := []string{"get", "list", "watch"}
	fakeAPI := &clienttesting.Fake{}
	fakeAPI.Resources = []*metav1.APIResourceList{
		{GroupVersion: "gateway.networking.k8s.io/v1", APIResources: []metav1.APIResource{
			{Name: "httproutes", Kind: "HTTPRoute", Namespaced: true, Verbs: list},
			{Name: "httproutes/status", Kind: "HTTPRoute", Namespaced: true, Verbs: []string{"get"}},
		}},
		{GroupVersion: "example.com/v1", APIResources: []metav1.APIResource{
			{Name: "widgets", Kind: "Widget", Namespaced: true, Verbs: list, Categories: []string{"kgateway"}},
			{Name: "gadgets", Kind: "Gadget", Namespaced: true, Verbs: list},
		}},
		{GroupVersion: "v1", APIResources: []metav1.APIResource{
			{Name: "secrets", Kind: "Secret", Namespaced: true, Verbs: list},
		}},
	}
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}: "HTTPRouteList",
		{Group: "example.com", Version: "v1", Resource: "widgets"}:                  "WidgetList",
		{Group: "example.com", Version: "v1", Resource: "gadgets"}:                  "GadgetList",
		{Version: "v1", Resource: "secrets"}:                                        "SecretList",
	},
		obj("gateway.networking.k8s.io/v1", "HTTPRoute", "team-a", "kept"),
		obj("gateway.networking.k8s.io/v1", "HTTPRoute", "team-a", "foreign"),
		obj("example.com/v1", "Widget", "team-a", "w"),
		obj("example.com/v1", "Gadget", "team-a", "g"),
	)
	reg := extension.NewCore(extension.WithKinds(kinds.Descriptor{Kind: "Secret", Resource: "secrets", Versions: []string{"v1"}, Namespaced: true}))
	env := pb.Env{Dynamic: dyn, Discovery: &discfake.FakeDiscovery{Fake: fakeAPI},
		Keep: func(u *unstructured.Unstructured) pb.ExcludeReason {
			if u.GetName() == "foreign" {
				return pb.ExcludeOwnership
			}
			return pb.ExcludeNone
		}}
	s := newRecordSink()
	if err := collect.Resources(reg).Collect(context.Background(), env, s); err != nil {
		t.Fatal(err)
	}
	got := slices.Sorted(slices.Values(keys(s.objects)))
	want := []string{
		"resources/example.com/Widget/team-a/w.yaml",
		"resources/gateway.networking.k8s.io/HTTPRoute/team-a/kept.yaml",
	}
	if !slices.Equal(got, want) {
		t.Errorf("objects %v, want %v", got, want)
	}
	if len(s.errs) != 1 || !errors.Is(s.errs[0], pb.ErrForbiddenKind) {
		t.Errorf("errors %v, want one ErrForbiddenKind", s.errs)
	}
	for _, a := range dyn.Actions() {
		if a.GetResource().Resource == "secrets" {
			t.Errorf("secrets requested: %v", a)
		}
	}
	if n := s.excluded[schema.GroupKind{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute"}][pb.ExcludeOwnership]; n != 1 {
		t.Errorf("excluded %v, want one HTTPRoute ownership", s.excluded)
	}
}
