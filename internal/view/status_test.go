package view_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/DuncanDoyle/kgateway-ui/pkg/extension"
	"github.com/DuncanDoyle/kgateway-ui/pkg/install"
	"github.com/DuncanDoyle/kgateway-ui/pkg/objects"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
	"github.com/DuncanDoyle/kgwctl/internal/view"
)

func TestSummarize(t *testing.T) {
	ps := func(states ...view.ParentState) []view.ParentSummary {
		out := make([]view.ParentSummary, 0, len(states))
		for _, s := range states {
			out = append(out, view.ParentSummary{State: s})
		}
		return out
	}
	rejected := func(reason string) view.ParentSummary {
		return view.ParentSummary{State: view.StateRejected, Reason: reason}
	}
	tests := []struct {
		name string
		in   []view.ParentSummary
		want string
	}{
		{"empty", nil, "<no status>"},
		{"one accepted", ps(view.StateAccepted), "True"},
		{"all accepted", ps(view.StateAccepted, view.StateAccepted), "True"},
		{"stale and no status", ps(view.StateAccepted, view.StateStale, view.StateNoStatus), "1/3 (1 stale, 1 no status)"},
		{"one rejected", []view.ParentSummary{rejected("NotAllowedByListeners")}, "0/1 (1 rejected: NotAllowedByListeners)"},
		{"two rejected", []view.ParentSummary{rejected("A"), rejected("B")}, "0/2 (2 rejected)"},
		{"only no status", ps(view.StateNoStatus), "0/1 (1 no status)"},
		// The count order is fixed, whatever the parent order.
		{"every state", append(ps(view.StateOther, view.StateDeclared, view.StateUnwatched, view.StateNoStatus, view.StateStale, view.StateAccepted),
			rejected("NotAllowedByListeners")), "1/7 (1 rejected: NotAllowedByListeners, 1 stale, 1 no status, 1 unwatched, 1 declared, 1 other)"},
		{"counts", ps(view.StateOther, view.StateOther, view.StateUnwatched), "0/3 (1 unwatched, 2 other)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := view.Summarize(tc.in); got != tc.want {
				t.Errorf("Summarize = %q, want %q", got, tc.want)
			}
		})
	}
}

// routeParents returns RouteParents of HTTPRoute ns/name for condType.
func routeParents(t *testing.T, r *load.Result, f *view.Filter, ns, name, condType string) []view.ParentSummary {
	t.Helper()
	return view.RouteParents(r, f, mustObj(t, r, "HTTPRoute", ns, name), condType)
}

func states(ps []view.ParentSummary) []view.ParentState {
	out := make([]view.ParentState, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.State)
	}
	return out
}

func checkStates(t *testing.T, ps []view.ParentSummary, want ...view.ParentState) {
	t.Helper()
	if got := states(ps); !slices.Equal(got, want) {
		t.Errorf("states = %v, want %v", got, want)
	}
}

func TestRouteParentsMixed(t *testing.T) {
	r := loadEnv(t, testenv.Options{Fixtures: []string{healthyFixture, mixedFixture}}, nil)
	f, ws := view.NewFilter(r, all)
	noWarnings(t, ws)
	ps := routeParents(t, r, f, "mixed", "mixed", "Accepted")
	checkStates(t, ps, view.StateAccepted, view.StateNoStatus, view.StateStale, view.StateOther)
	if got, want := view.Summarize(ps), "1/4 (1 stale, 1 no status, 1 other)"; got != want {
		t.Errorf("cell = %q, want %q", got, want)
	}
	for i, name := range []string{"a", "b", "c", "d"} {
		if ps[i].Parent.Name != name || ps[i].Parent.Kind != "Gateway" || ps[i].Parent.Namespace != "mixed" {
			t.Errorf("parent %d = %+v, want Gateway mixed/%s", i, ps[i].Parent, name)
		}
	}
	if ps[0].Controller != "kgateway.dev/kgateway" || ps[3].Controller != "example.com/other" {
		t.Errorf("controllers = %q, %q; want kgateway.dev/kgateway, example.com/other", ps[0].Controller, ps[3].Controller)
	}
	// ResolvedRefs follows the same rules.
	checkStates(t, routeParents(t, r, f, "mixed", "mixed", "ResolvedRefs"),
		view.StateAccepted, view.StateNoStatus, view.StateStale, view.StateOther)
}

func TestRouteParentsDuplicateRef(t *testing.T) {
	r := loadEnv(t, testenv.Options{Fixtures: []string{healthyFixture, mixedFixture}}, nil)
	f, _ := view.NewFilter(r, all)
	u := mustObj(t, r, "HTTPRoute", "mixed", "dup")
	if n := len(objects.RouteParentRefs(u)); n != 2 {
		t.Fatalf("spec parentRefs = %d, want 2 written", n)
	}
	ps := view.RouteParents(r, f, u, "Accepted")
	checkStates(t, ps, view.StateAccepted)
	if got := view.Summarize(ps); got != "True" {
		t.Errorf("cell = %q, want True", got)
	}
}

func TestRouteParentsTwoListeners(t *testing.T) {
	r := loadEnv(t, testenv.Options{Fixtures: []string{healthyFixture, mixedFixture}}, nil)
	f, _ := view.NewFilter(r, all)
	ps := routeParents(t, r, f, "mixed", "listeners", "Accepted")
	checkStates(t, ps, view.StateAccepted, view.StateRejected)
	if ps[1].Parent.SectionName != "b" || ps[1].Reason != "NotAllowedByListeners" {
		t.Errorf("rejected parent = %+v, want section b with reason NotAllowedByListeners", ps[1])
	}
	if got, want := view.Summarize(ps), "1/2 (1 rejected: NotAllowedByListeners)"; got != want {
		t.Errorf("cell = %q, want %q", got, want)
	}
}

// vendorController is the controller name of vendorDetector's installs.
const vendorController = "example.com/vendor"

// vendorDetector recognises Deployments labelled app=vendor-controller.
type vendorDetector struct{}

func (vendorDetector) Name() string { return "vendor" }

func (vendorDetector) ControllerSelector() labels.Selector {
	return labels.SelectorFromSet(labels.Set{"app": "vendor-controller"})
}

func (vendorDetector) ControllerPodSelector() labels.Selector {
	return vendorDetector{}.ControllerSelector()
}

func (vendorDetector) ProxySelector() labels.Selector {
	return labels.SelectorFromSet(labels.Set{"app": "vendor-proxy"})
}

func (vendorDetector) Detect(d *appsv1.Deployment) (install.Install, bool) {
	if d.Labels["app"] != "vendor-controller" {
		return install.Install{}, false
	}
	return install.Install{Namespace: d.Namespace, Name: d.Name, Edition: "vendor", Version: "1.0.0",
		ControllerNames: []string{vendorController}, Deployment: d, ProxySelector: vendorDetector{}.ProxySelector()}, true
}

func TestRouteParentsTwoOfOurs(t *testing.T) {
	reg := extension.New(extension.WithDetectors(vendorDetector{}))
	r := loadEnvWith(t, reg, testenv.Options{Fixtures: []string{healthyFixture, twoCtrlFixture}}, nil)
	if len(r.Model.Installs) != 2 {
		t.Fatalf("installs = %d, want 2", len(r.Model.Installs))
	}
	f, ws := view.NewFilter(r, all)
	noWarnings(t, ws)
	if !f.Ours(vendorController) {
		t.Fatalf("Ours(%s) = false, want true", vendorController)
	}
	tests := []struct {
		route      string
		state      view.ParentState
		reason     string
		controller string
	}{
		// kgateway owns kgateway-system/http, so its entry decides either way.
		{"owner-accepts", view.StateAccepted, "Accepted", "kgateway.dev/kgateway"},
		{"owner-rejects", view.StateRejected, "NotAllowedByListeners", "kgateway.dev/kgateway"},
		// No install owns a missing parent: the worst entry decides.
		{"no-owner", view.StateRejected, "NoMatchingParent", vendorController},
	}
	for _, tc := range tests {
		ps := routeParents(t, r, f, "shared", tc.route, "Accepted")
		if len(ps) != 1 {
			t.Fatalf("%s: %d parents, want 1", tc.route, len(ps))
		}
		if p := ps[0]; p.State != tc.state || p.Reason != tc.reason || p.Controller != tc.controller {
			t.Errorf("%s = {%s %s %s}, want {%s %s %s}", tc.route, p.State, p.Reason, p.Controller, tc.state, tc.reason, tc.controller)
		}
	}
}

func TestRouteParentsUnwatched(t *testing.T) {
	r := loadEnv(t, testenv.Options{Fixtures: []string{twoInstallsFixture}}, nil)
	f, _ := view.NewFilter(r, all)
	ps := routeParents(t, r, f, "team-c", "stray", "Accepted")
	checkStates(t, ps, view.StateUnwatched)
	if got, want := view.Summarize(ps), "0/1 (1 unwatched)"; got != want {
		t.Errorf("cell = %q, want %q", got, want)
	}
	if w := f.IndexUnavailable(); w != "" {
		t.Errorf("IndexUnavailable = %q, want empty", w)
	}
}

// With an index input down, unwatched degrades to no status and the Filter names the input.
func TestRouteParentsIndexInputDown(t *testing.T) {
	r := loadEnv(t, testenv.Options{Fixtures: []string{twoInstallsFixture}}, func(p *testenv.Proxy) {
		p.Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "grpcroutes.gateway.networking.k8s.io"}, testenv.Action{Status: http.StatusForbidden})
	})
	f, _ := view.NewFilter(r, all)
	checkStates(t, routeParents(t, r, f, "team-c", "stray", "Accepted"), view.StateNoStatus)
	if w := f.IndexUnavailable(); !strings.Contains(w, "grpcroutes: denied") {
		t.Errorf("IndexUnavailable = %q, want it to name grpcroutes: denied", w)
	}
}

func TestPolicyAncestors(t *testing.T) {
	r := loadEnv(t, testenv.Options{Fixtures: []string{healthyFixture, otherFixture}}, nil)
	f, _ := view.NewFilter(r, view.Scope{AllNamespaces: true, AllControllers: true})
	ps := view.PolicyAncestors(f, mustObj(t, r, "TrafficPolicy", "other", "edge-policy"), "Accepted")
	checkStates(t, ps, view.StateOther)
	// Ancestors other/edge (theirs) and kgateway-system/http (ours, accepted).
	ps = view.PolicyAncestors(f, mustObj(t, r, "TrafficPolicy", "other", "shared-policy"), "Accepted")
	checkStates(t, ps, view.StateOther, view.StateAccepted)
	if got, want := view.Summarize(ps), "1/2 (1 other)"; got != want {
		t.Errorf("shared-policy cell = %q, want %q", got, want)
	}
}

func TestObjectCondition(t *testing.T) {
	obj := func(gen int64, conds ...map[string]any) *unstructured.Unstructured {
		cs := make([]any, 0, len(conds))
		for _, c := range conds {
			cs = append(cs, c)
		}
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "gateway.networking.k8s.io/v1", "kind": "Gateway",
			"metadata": map[string]any{"name": "gw", "namespace": "ns", "generation": gen},
			"status":   map[string]any{"conditions": cs},
		}}
		return u
	}
	cond := func(typ, status, reason string, observed int64) map[string]any {
		return map[string]any{"type": typ, "status": status, "reason": reason, "message": "",
			"observedGeneration": observed, "lastTransitionTime": "2026-01-01T00:00:00Z"}
	}
	tests := []struct {
		name string
		u    *unstructured.Unstructured
		want string
	}{
		{"true", obj(2, cond("Programmed", "True", "Programmed", 2)), "True"},
		{"false", obj(2, cond("Programmed", "False", "AddressNotAssigned", 2)), "False (AddressNotAssigned)"},
		{"unknown", obj(2, cond("Programmed", "Unknown", "Pending", 2)), "Unknown (Pending)"},
		{"absent", obj(2, cond("Accepted", "True", "Accepted", 2)), "<no status>"},
		{"no status", obj(1), "<no status>"},
		// Stale is per condition: Accepted is current, Programmed is not.
		{"stale", obj(3, cond("Accepted", "True", "Accepted", 3), cond("Programmed", "True", "Programmed", 2)), "<stale>"},
		{"stale without observedGeneration", obj(1, cond("Programmed", "False", "Invalid", 0)), "<stale>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := view.ObjectCondition(tc.u, "Programmed"); got != tc.want {
				t.Errorf("ObjectCondition = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestObjectConditionStale(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1", "kind": "GatewayClass",
		"metadata": map[string]any{"name": "kgateway", "generation": int64(4)},
		"status": map[string]any{"conditions": []any{map[string]any{"type": "Accepted", "status": "True", "reason": "Accepted",
			"message": "", "observedGeneration": int64(3), "lastTransitionTime": "2026-01-01T00:00:00Z"}}},
	}}
	if got := view.ObjectCondition(u, "Accepted"); got != "<stale>" {
		t.Errorf("ObjectCondition = %q, want <stale>", got)
	}
}
