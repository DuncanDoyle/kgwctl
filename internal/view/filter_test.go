package view_test

import (
	"context"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DuncanDoyle/kgateway-ui/pkg/extension"
	"github.com/DuncanDoyle/kgateway-ui/pkg/kinds"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/resolve"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
	"github.com/DuncanDoyle/kgwctl/internal/view"
)

func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }

const (
	healthyFixture      = "../testenv/testdata/healthy.yaml"
	otherFixture        = "../testenv/testdata/other-controller.yaml"
	orphansFixture      = "../testenv/testdata/orphans.yaml"
	listenerSetsFixture = "../testenv/testdata/listenersets.yaml"
	twoInstallsFixture  = "../testenv/testdata/two-installs.yaml"
	xListenerSetCRDs    = "../testenv/testdata/crds/xlistenerset"
)

// all is the scope of -A with no other flags.
var all = view.Scope{AllNamespaces: true}

// loadEnv starts an Env with opts, lets rules install faults, and loads it.
func loadEnv(t *testing.T, opts testenv.Options, rules func(*testenv.Proxy)) *load.Result {
	t.Helper()
	env := testenv.New(t, opts)
	if rules != nil {
		rules(env.Proxies[0])
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", env.Kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	c, err := load.NewClients(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, err := load.Load(ctx, c, extension.New(), load.Needs{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return r
}

// object returns the served kind and stored object of kind ns/name, failing the test if absent.
func object(t *testing.T, r *load.Result, kind, ns, name string) (kinds.Served, *unstructured.Unstructured) {
	t.Helper()
	for _, k := range r.Served {
		if k.Kind != kind {
			continue
		}
		for _, u := range r.Model.Snapshot.Objects[k.GVR()] {
			if u.GetNamespace() == ns && u.GetName() == name {
				return k, u
			}
		}
	}
	t.Fatalf("no %s %s/%s in the snapshot", kind, ns, name)
	return kinds.Served{}, nil
}

// obj names one object as kind/namespace/name.
type obj struct{ kind, ns, name string }

// checkVisible asserts Visible for each object in want.
func checkVisible(t *testing.T, r *load.Result, f *view.Filter, want map[obj]bool) {
	t.Helper()
	for o, w := range want {
		k, u := object(t, r, o.kind, o.ns, o.name)
		if got := f.Visible(k, u); got != w {
			t.Errorf("Visible(%s %s/%s) = %v, want %v", o.kind, o.ns, o.name, got, w)
		}
	}
}

func noWarnings(t *testing.T, ws []string) {
	t.Helper()
	if len(ws) != 0 {
		t.Errorf("warnings = %q, want none", ws)
	}
}

func TestFilterHidesOtherController(t *testing.T) {
	r := loadEnv(t, testenv.Options{Fixtures: []string{healthyFixture, otherFixture}}, nil)
	f, ws := view.NewFilter(r, all)
	noWarnings(t, ws)
	foreign := map[obj]bool{
		{"GatewayClass", "", "other"}:                false,
		{"Gateway", "other", "edge"}:                 false,
		{"ListenerSet", "other", "edge-extra"}:       false,
		{"HTTPRoute", "other", "edge-route"}:         false,
		{"HTTPRoute", "other", "via-listenerset"}:    false,
		{"TrafficPolicy", "other", "edge-policy"}:    false,
		{"GatewayClass", "", "kgateway"}:             true,
		{"Gateway", "kgateway-system", "http"}:       true,
		{"HTTPRoute", "default", "example"}:          true,
		{"HTTPRoute", "other", "retargeted-ours"}:    true,
		{"HTTPRoute", "other", "retargeted-missing"}: true,
	}
	checkVisible(t, r, f, foreign)

	f, ws = view.NewFilter(r, view.Scope{AllNamespaces: true, AllControllers: true})
	noWarnings(t, ws)
	for o := range foreign {
		foreign[o] = true
	}
	checkVisible(t, r, f, foreign)

	if !f.Ours("kgateway.dev/kgateway") || f.Ours("example.com/other") {
		t.Errorf("Ours: want kgateway.dev/kgateway only")
	}
}

func TestFilterKeepsOrphans(t *testing.T) {
	r := loadEnv(t, testenv.Options{Fixtures: []string{healthyFixture, orphansFixture}}, nil)
	f, ws := view.NewFilter(r, all)
	noWarnings(t, ws)
	checkVisible(t, r, f, map[obj]bool{
		{"HTTPRoute", "default", "to-missing"}:       true,
		{"HTTPRoute", "default", "no-parents"}:       true,
		{"TrafficPolicy", "default", "no-ancestors"}: true,
		{"GatewayParameters", "default", "params"}:   true,
		{"Backend", "default", "static"}:             true,
		{"HTTPRoute", "default", "example"}:          true,
	})
	if _, ok := f.InstallOf(mustObj(t, r, "HTTPRoute", "default", "example")); !ok {
		t.Error("InstallOf(default/example): want the install")
	}
	if _, ok := f.InstallOf(mustObj(t, r, "Backend", "default", "static")); ok {
		t.Error("InstallOf(Backend): Backends are never attributed")
	}
}

func mustObj(t *testing.T, r *load.Result, kind, ns, name string) *unstructured.Unstructured {
	t.Helper()
	_, u := object(t, r, kind, ns, name)
	return u
}

func TestFilterRetargetedRoute(t *testing.T) {
	r := loadEnv(t, testenv.Options{Fixtures: []string{healthyFixture, otherFixture}}, nil)
	f, ws := view.NewFilter(r, all)
	noWarnings(t, ws)
	// Their only status entry is from example.com/other, for other/edge.
	checkVisible(t, r, f, map[obj]bool{
		{"HTTPRoute", "other", "retargeted-ours"}:    true,
		{"HTTPRoute", "other", "retargeted-missing"}: true,
	})
}

func TestFilterListenerSets(t *testing.T) {
	r := loadEnv(t, testenv.Options{Fixtures: []string{healthyFixture, listenerSetsFixture}, CRDDirs: []string{xListenerSetCRDs}}, nil)
	ts, err := resolve.Kinds(r.Served, extension.New().Kinds, "ls")
	if err != nil || len(ts) != 1 || len(ts[0].Kinds) != 2 {
		t.Fatalf("resolve ls = %+v, %v; want one target with ListenerSet and XListenerSet", ts, err)
	}
	f, ws := view.NewFilter(r, all)
	noWarnings(t, ws)
	checkVisible(t, r, f, map[obj]bool{
		{"ListenerSet", "kgateway-system", "extra"}:     true,
		{"XListenerSet", "kgateway-system", "extra-x"}:  true,
		{"HTTPRoute", "kgateway-system", "via-extra"}:   true,
		{"HTTPRoute", "kgateway-system", "via-extra-x"}: true,
	})
}

func TestFilterNoInstall(t *testing.T) {
	r := loadEnv(t, testenv.Options{Fixtures: []string{otherFixture}}, nil)
	f, ws := view.NewFilter(r, all)
	if !slices.ContainsFunc(ws, func(w string) bool { return strings.Contains(w, "no kgateway install detected") }) {
		t.Errorf("warnings = %q, want one containing %q", ws, "no kgateway install detected")
	}
	checkVisible(t, r, f, map[obj]bool{
		{"GatewayClass", "", "other"}:             true,
		{"Gateway", "other", "edge"}:              true,
		{"HTTPRoute", "other", "edge-route"}:      true,
		{"TrafficPolicy", "other", "edge-policy"}: true,
	})
}

func TestFilterOwnershipInputDown(t *testing.T) {
	ctrlPods := extension.New().Detectors[0].ControllerPodSelector().String()
	tests := []struct {
		name  string
		match testenv.Match
		label string // the input as the warning names it
	}{
		{"deployments", testenv.Match{Kind: testenv.KindAcquire, Resource: "deployments.apps"}, "deployments: denied"},
		{"namespaces", testenv.Match{Kind: testenv.KindAcquire, Resource: "namespaces"}, "namespaces: denied"},
		{"gatewayclasses", testenv.Match{Kind: testenv.KindAcquire, Resource: "gatewayclasses.gateway.networking.k8s.io"}, "gatewayclasses: denied"},
		{"gateways", testenv.Match{Kind: testenv.KindAcquire, Resource: "gateways.gateway.networking.k8s.io"}, "gateways: denied"},
		{"controller pods", testenv.Match{Kind: testenv.KindAcquire, Resource: "pods", LabelSelector: ctrlPods}, "pods: denied"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := loadEnv(t, testenv.Options{Fixtures: []string{healthyFixture, otherFixture}}, func(p *testenv.Proxy) {
				p.Rule(tc.match, testenv.Action{Status: http.StatusForbidden})
			})
			f, ws := view.NewFilter(r, all)
			if !slices.ContainsFunc(ws, func(w string) bool { return strings.Contains(w, tc.label) }) {
				t.Errorf("warnings = %q, want one naming %q", ws, tc.label)
			}
			// Every object that loaded is visible, the other controller's included.
			for _, k := range r.Served {
				for _, u := range r.Model.Snapshot.Objects[k.GVR()] {
					if !f.Visible(k, u) {
						t.Errorf("Visible(%s %s/%s) = false, want true", k.Kind, u.GetNamespace(), u.GetName())
					}
				}
			}
			if _, ok := f.InstallOf(mustObj(t, r, "HTTPRoute", "default", "example")); ok {
				t.Error("InstallOf: want unknown while an ownership input is down")
			}
		})
	}
}

func TestFilterInstallFlag(t *testing.T) {
	r := loadEnv(t, testenv.Options{Fixtures: []string{twoInstallsFixture}}, nil)
	want := map[obj]bool{
		{"GatewayClass", "", "kgateway"}:   true,
		{"Gateway", "team-a", "web"}:       true,
		{"HTTPRoute", "team-a", "web"}:     true,
		{"Gateway", "team-b", "api"}:       true,
		{"HTTPRoute", "team-b", "api"}:     true,
		{"TrafficPolicy", "team-b", "api"}: true,
	}
	f, ws := view.NewFilter(r, all)
	noWarnings(t, ws)
	checkVisible(t, r, f, want)

	f, ws = view.NewFilter(r, view.Scope{AllNamespaces: true, Install: "kgw-a/kgateway"})
	noWarnings(t, ws)
	// Owned only by kgw-b.
	want[obj{"Gateway", "team-b", "api"}] = false
	want[obj{"HTTPRoute", "team-b", "api"}] = false
	want[obj{"TrafficPolicy", "team-b", "api"}] = false
	checkVisible(t, r, f, want)
	if in, ok := f.InstallOf(mustObj(t, r, "Gateway", "team-a", "web")); !ok || in.ID() != "kgw-a.kgateway" {
		t.Errorf("InstallOf(team-a/web) = %q, %v; want kgw-a.kgateway", in.ID(), ok)
	}

	if _, ws := view.NewFilter(r, view.Scope{AllNamespaces: true, Install: "nope/nope"}); len(ws) == 0 {
		t.Error("an --install that names no detected install: want a warning")
	}
}

func TestFilterNamespaceAndSelector(t *testing.T) {
	r := loadEnv(t, testenv.Options{Fixtures: []string{healthyFixture}}, nil)
	f, _ := view.NewFilter(r, view.Scope{Namespace: "default"})
	checkVisible(t, r, f, map[obj]bool{
		{"HTTPRoute", "default", "example"}:    true,
		{"Gateway", "kgateway-system", "http"}: false,
		{"GatewayClass", "", "kgateway"}:       true,
	})
	f, _ = view.NewFilter(r, view.Scope{AllNamespaces: true, Selector: labels.SelectorFromSet(labels.Set{"app": "none"})})
	checkVisible(t, r, f, map[obj]bool{{"HTTPRoute", "default", "example"}: false})
}
