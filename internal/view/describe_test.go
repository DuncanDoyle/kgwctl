package view_test

import (
	"slices"
	"testing"
	"time"

	"github.com/DuncanDoyle/kgateway-ui/pkg/extension"

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

func TestDescribeGatewayRelations(t *testing.T) {
	r := loadEnv(t, testenv.Options{Fixtures: []string{healthyFixture, listenerSetsFixture, describeFixture}}, nil)
	v := mustDescribe(t, r, view.Scope{}, "gw", "kgateway-system", "http")
	rel := v.Relations
	if !rel.Resolved || rel.Declared != nil {
		t.Fatalf("relations = %+v, want resolved", rel)
	}
	want := []view.ListenerRoutes{{Listener: "http", Routes: []string{"HTTPRoute/default/example"}}}
	if !slices.EqualFunc(rel.AttachedRoutes, want, func(a, b view.ListenerRoutes) bool {
		return a.Listener == b.Listener && slices.Equal(a.Routes, b.Routes)
	}) {
		t.Errorf("attached = %+v, want %+v", rel.AttachedRoutes, want)
	}
	// The rejected route carries the controller's deciding condition.
	i := slices.IndexFunc(rel.NotAccepted, func(n view.RouteRef) bool { return n.Route == "HTTPRoute/default/rejected" })
	if i < 0 || rel.NotAccepted[i].Attachment != "not-accepted" || rel.NotAccepted[i].Listener != "http" ||
		rel.NotAccepted[i].Condition != "Accepted=False (NotAllowedByListeners): hostname rejected.example.com matches no listener" {
		t.Errorf("not accepted = %+v", rel.NotAccepted)
	}
	if !slices.Equal(rel.ListenerSets, []string{"ListenerSet/extra", "XListenerSet/extra-x"}) {
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
	if v.Relations.Resolved || v.Relations.Declared == nil || !slices.Equal(v.Relations.Declared.ParentRefs, []string{"Gateway/edge"}) {
		t.Errorf("relations = %+v, want declared parent Gateway/edge", v.Relations)
	}
	if len(v.Conditions) != 1 || v.Conditions[0].Controller != "example.com/other" {
		t.Errorf("conditions = %+v, want one parent block by example.com/other", v.Conditions)
	}
}

func TestDescribeRoute(t *testing.T) {
	r := loadEnv(t, testenv.Options{Fixtures: []string{healthyFixture, mixedFixture, delegationFixture}}, nil)
	v := mustDescribe(t, r, view.Scope{}, "hr", "mixed", "dup")
	if !slices.Equal(v.Relations.DuplicateParents, []string{"Gateway/a"}) || len(v.Relations.Parents) != 1 {
		t.Errorf("dup: relations = %+v, want one parent noted as duplicate", v.Relations)
	}
	v = mustDescribe(t, r, view.Scope{}, "hr", "default", "parent")
	if !slices.Equal(v.Relations.DelegationChildren, []string{"HTTPRoute/child-a"}) {
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
