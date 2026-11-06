package view_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DuncanDoyle/kgateway-ui/pkg/extension"
	"github.com/DuncanDoyle/kgateway-ui/pkg/objects"

	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/resolve"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
	"github.com/DuncanDoyle/kgwctl/internal/view"
)

const (
	describeFixture   = "../testenv/testdata/describe.yaml"
	sameNameLSFixture = "../testenv/testdata/listenersets-same-name.yaml"
	delegationFixture = "../testenv/testdata/delegation.yaml"
)

// describe runs view.Describe for kind arg and name in ns with scope s.
func describe(t *testing.T, r *load.Result, s view.Scope, arg, ns, name string) (*view.DescribeView, error) {
	t.Helper()
	ts, err := resolve.Kinds(r.Served, extension.New().Kinds, arg)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := view.NewFilter(r, s)
	return view.Describe(r, f, ts[0], ns, name, time.Now())
}

func mustDescribe(t *testing.T, r *load.Result, s view.Scope, arg, ns, name string) *view.DescribeView {
	t.Helper()
	v, err := describe(t, r, s, arg, ns, name)
	if err != nil {
		t.Fatalf("Describe(%s %s/%s): %v", arg, ns, name, err)
	}
	return v
}

// gwRef and hrRef are the refs of Gateway and HTTPRoute ns/name.
func gwRef(ns, name string) objects.Ref {
	return objects.Ref{Group: objects.GatewayAPIGroup, Kind: "Gateway", Namespace: ns, Name: name}
}

func hrRef(ns, name string) objects.Ref {
	return objects.Ref{Group: objects.GatewayAPIGroup, Kind: "HTTPRoute", Namespace: ns, Name: name}
}

func routeNames(ls []view.RouteLink) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Route.String()+" "+l.Attachment)
	}
	return out
}

func TestDescribeGatewayRelations(t *testing.T) {
	r := loadEnv(t, testenv.Options{Fixtures: []string{healthyFixture, listenerSetsFixture, describeFixture}}, nil)
	v := mustDescribe(t, r, view.Scope{}, "gw", "kgateway-system", "http")
	rel := v.Relations
	if rel.Resolved == nil || !*rel.Resolved || rel.Declared != nil {
		t.Fatalf("relations = %+v, want resolved", rel)
	}
	http := gwRef("kgateway-system", "http")
	if len(rel.AttachedRoutes) != 1 || rel.AttachedRoutes[0].Parent != http || rel.AttachedRoutes[0].Listener != "http" ||
		!slices.Equal(rel.AttachedRoutes[0].Routes, []objects.Ref{hrRef("default", "example")}) {
		t.Errorf("attached = %+v, want default/example on listener http", rel.AttachedRoutes)
	}
	// R30: not accepted is the rejected set get counts; pending routes are listed apart.
	want := view.RouteLink{Route: hrRef("default", "rejected"), Attachment: "not-accepted",
		ParentRef: objects.ParentRef{Group: objects.GatewayAPIGroup, Kind: "Gateway", Namespace: "kgateway-system", Name: "http", SectionName: "http"},
		Condition: &view.Condition{Type: "Accepted", Status: "False", Reason: "NotAllowedByListeners", Message: "hostname rejected.example.com matches no listener"}}
	if len(rel.NotAccepted) != 1 || rel.NotAccepted[0].Route != want.Route || rel.NotAccepted[0].ParentRef != want.ParentRef ||
		rel.NotAccepted[0].Condition == nil || *rel.NotAccepted[0].Condition != *want.Condition {
		t.Errorf("not accepted = %+v, want only %+v", rel.NotAccepted, want)
	}
	if got := routeNames(rel.Pending); !slices.Equal(got, []string{"kgateway-system/via-extra pending", "kgateway-system/via-extra-x pending"}) {
		t.Errorf("pending = %v", got)
	}
	if len(rel.ListenerSets) != 2 || rel.ListenerSets[0].Kind != "ListenerSet" || rel.ListenerSets[1].Kind != "XListenerSet" {
		t.Errorf("listenersets = %v", rel.ListenerSets)
	}
	if v.Object.Install != "kgateway-system/kgateway" || v.Spec == nil || len(v.Spec.Listeners) != 1 {
		t.Errorf("object %+v, spec %+v", v.Object, v.Spec)
	}
}

// R29: an object get hides is described, with declared relations since no install owns it.
func TestDescribeHiddenObject(t *testing.T) {
	r := loadEnv(t, testenv.Options{Fixtures: []string{healthyFixture, otherFixture}}, nil)
	v := mustDescribe(t, r, view.Scope{}, "hr", "other", "edge-route")
	edge := objects.ParentRef{Group: objects.GatewayAPIGroup, Kind: "Gateway", Namespace: "other", Name: "edge"}
	if d := v.Relations.Declared; *v.Relations.Resolved || d == nil || !slices.Equal(d.ParentRefs, []objects.ParentRef{edge}) {
		t.Errorf("relations = %+v, want declared parent other/edge", v.Relations)
	}
	if len(v.Conditions) != 1 || v.Conditions[0].Controller != "example.com/other" || *v.Conditions[0].Ref != edge {
		t.Errorf("conditions = %+v, want one parent block by example.com/other", v.Conditions)
	}
}

func TestDescribeRoute(t *testing.T) {
	r := loadEnv(t, testenv.Options{Fixtures: []string{healthyFixture, mixedFixture, delegationFixture}}, nil)
	v := mustDescribe(t, r, view.Scope{}, "hr", "mixed", "dup")
	a := objects.ParentRef{Group: objects.GatewayAPIGroup, Kind: "Gateway", Namespace: "mixed", Name: "a"}
	if !slices.Equal(v.Relations.DuplicateParents, []objects.ParentRef{a}) || len(v.Relations.Parents) != 1 {
		t.Errorf("dup: relations = %+v, want one parent noted as duplicate", v.Relations)
	}
	v = mustDescribe(t, r, view.Scope{}, "hr", "default", "parent")
	if !slices.Equal(v.Relations.DelegationChildren, []view.DelegationChild{{Ref: hrRef("default", "child-a")}}) {
		t.Errorf("parent: children = %v", v.Relations.DelegationChildren)
	}
	// The listener a parentRef names decides the state, as in get.
	v = mustDescribe(t, r, view.Scope{}, "hr", "mixed", "listeners")
	got := []view.ParentState{}
	for _, p := range v.Relations.Parents {
		got = append(got, p.State)
	}
	if !slices.Equal(got, []view.ParentState{view.StateAccepted, view.StateRejected}) {
		t.Errorf("listeners: states = %v", got)
	}
}

// R33: with an index input down, a route keeps its parent states from status.
func TestDescribeIndexDown(t *testing.T) {
	r := loadEnv(t, testenv.Options{Fixtures: []string{healthyFixture, mixedFixture}}, func(p *testenv.Proxy) {
		p.Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "namespaces"}, testenv.Action{Status: http.StatusForbidden})
	})
	v := mustDescribe(t, r, view.Scope{}, "hr", "mixed", "listeners")
	if v.Relations.Unknown != "namespaces: denied" || len(v.Relations.Parents) != 2 || v.Relations.Parents[1].State != view.StateRejected {
		t.Errorf("relations = %+v, want unknown with both parent states", v.Relations)
	}
	if len(v.Incomplete) != 1 || v.Incomplete[0].Resource != "namespaces" {
		t.Errorf("incomplete = %+v", v.Incomplete)
	}
}

// A policy's relations are omitted from JSON when it has no ancestors; age is text only.
func TestDescribeJSONOmissions(t *testing.T) {
	r := loadEnv(t, testenv.Options{Fixtures: []string{healthyFixture, orphansFixture}}, nil)
	v := mustDescribe(t, r, view.Scope{}, "tp", "default", "no-ancestors")
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); strings.Contains(s, `"relations"`) || strings.Contains(s, `"age"`) {
		t.Errorf("JSON = %s, want no relations and no age", s)
	}
}

func TestDescribeLookupErrors(t *testing.T) {
	r := loadEnv(t, testenv.Options{Fixtures: []string{healthyFixture, sameNameLSFixture, listenerSetsFixture}}, nil)
	if _, err := describe(t, r, view.Scope{}, "ls", "kgateway-system", "same"); err == nil ||
		err.Error() != "ambiguous: name exists as ListenerSet and XListenerSet; use listenersets.<group>" {
		t.Errorf("ambiguous ls: err = %v", err)
	}
	// The alias takes the one kind that has the name.
	if v := mustDescribe(t, r, view.Scope{}, "ls", "kgateway-system", "extra-x"); v.Object.Kind != "XListenerSet" {
		t.Errorf("ls extra-x: kind %q", v.Object.Kind)
	}
	if _, err := describe(t, r, view.Scope{}, "listenersets.gateway.networking.k8s.io", "kgateway-system", "same"); err != nil {
		t.Errorf("qualified ls: %v", err)
	}
	if _, err := describe(t, r, view.Scope{}, "gw", "kgateway-system", "missing"); err == nil || err.Error() != `gateways "missing" not found` {
		t.Errorf("missing: err = %v", err)
	}
}
