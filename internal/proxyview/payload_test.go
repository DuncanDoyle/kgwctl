package proxyview_test

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/DuncanDoyle/xdsview/pkg/envoy/model"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/provenance"
	"k8s.io/apimachinery/pkg/types"

	"github.com/DuncanDoyle/kgwctl/internal/proxyview"
)

// payloadRoutes returns every route of p by ID.
func payloadRoutes(p proxyview.ProxyRoutes) map[string]proxyview.Route {
	out := map[string]proxyview.Route{}
	for _, l := range p.Listeners {
		for _, fc := range l.FilterChains {
			if fc.HCM == nil {
				continue
			}
			for _, vh := range fc.HCM.VirtualHosts {
				for _, r := range vh.Routes {
					out[r.ID] = r
				}
			}
		}
	}
	return out
}

// payloadFilter returns the chain entry of slot on route in v's payload.
func payloadFilter(t *testing.T, v proxyview.View, route, slot model.ID) proxyview.Filter {
	t.Helper()
	r, ok := payloadRoutes(v.Payload(proxyview.Source{}))[string(route)]
	if !ok {
		t.Fatalf("payload lacks route %s", route)
	}
	for _, f := range r.Filters {
		if f.Slot == string(slot) {
			return f
		}
	}
	t.Fatalf("route %s has no filter %s", route, slot)
	return proxyview.Filter{}
}

// filterNamed returns the chain entry named name of route r.
func filterNamed(t *testing.T, r proxyview.Route, name string) proxyview.Filter {
	t.Helper()
	for _, f := range r.Filters {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("route %s has no filter %s", r.Name, name)
	return proxyview.Filter{}
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func tp(name string) proxyview.Ref {
	return proxyview.Ref{Group: "gateway.kgateway.dev", Kind: "TrafficPolicy", Namespace: "kgwctl-proxy", Name: name}
}

// TestPayloadExactRoute: R4, the exact route of collide-meta.json under --route.
func TestPayloadExactRoute(t *testing.T) {
	v := build(t, golden(t, "collide-meta.json"), proxyview.Options{Gateway: proxyGW, Selection: proxyview.Selection{Route: orders}})
	p := v.Payload(proxyview.Source{})
	if len(p.Selection.Routes) != 1 || len(p.Selection.Ambiguous) != 0 {
		t.Fatalf("selection %+v, want one route", p.Selection)
	}
	rs := payloadRoutes(p)
	r, ok := rs[p.Selection.Routes[0].ID]
	if !ok || len(rs) != 1 {
		t.Fatalf("listeners hold %d routes, want the selected one", len(rs))
	}
	if r.Origin.Certainty != "exact" || len(r.Origin.Readings) != 1 || r.Origin.Readings[0].Route.Name != "orders" {
		t.Errorf("origin %+v, want exact kgwctl-proxy/orders", r.Origin)
	}
	want := []proxyview.Policy{{Policy: tp("orders-extauth"), Field: "extAuth", Level: "route", Certainty: "inferred"}}
	if got := filterNamed(t, r, "ext_auth/kgwctl-proxy/authz").Policies; !slices.Equal(got, want) {
		t.Errorf("ext_authz policies %+v, want %+v", got, want)
	}
	wantRoute := []proxyview.Policy{
		{Policy: tp("orders-extauth"), Field: "extAuth", Level: "route", Certainty: "exact"},
		{Policy: tp("orders-ratelimit"), Field: "rateLimit.local", Level: "route", Certainty: "exact"},
		{Policy: tp("orders-transformation"), Field: "transformation", Level: "route", Certainty: "exact"},
	}
	if !slices.Equal(r.Policies, wantRoute) {
		t.Errorf("route policies %+v, want %+v", r.Policies, wantRoute)
	}
	wantBackend := proxyview.BackendRef{Backend: proxyview.Ref{Kind: "Service", Namespace: "kgwctl-proxy", Name: "backend"}, Port: 8080}
	if r.Backend == nil || r.Backend.Cluster != "kube_kgwctl-proxy_backend_8080" || !slices.Equal(r.Backend.Candidates, []proxyview.BackendRef{wantBackend}) {
		t.Errorf("backend %+v, want Service kgwctl-proxy/backend port 8080", r.Backend)
	}
}

// TestPayloadAmbiguousSelection: R1, the colliding routes from a file.
func TestPayloadAmbiguousSelection(t *testing.T) {
	v := build(t, golden(t, "policies.json"), proxyview.Options{Gateway: proxyGW, Selection: proxyview.Selection{Route: orders}})
	p := v.Payload(proxyview.Source{})
	if p.Selection.Routes == nil || len(p.Selection.Routes) != 0 || p.Listeners == nil || len(p.Listeners) != 0 {
		t.Fatalf("routes %v, listeners %v; want both []", p.Selection.Routes, p.Listeners)
	}
	if len(p.Selection.Ambiguous) != 2 {
		t.Fatalf("ambiguous %+v, want 2 entries", p.Selection.Ambiguous)
	}
	for _, a := range p.Selection.Ambiguous {
		if len(a.Readings) != 2 || a.Narrowed {
			t.Errorf("%s: readings %+v narrowed %v, want two and not narrowed", a.Name, a.Readings, a.Narrowed)
		}
	}
	if s := marshal(t, p); !strings.Contains(s, `"routes":[]`) || !strings.Contains(s, `"listeners":[]`) {
		t.Errorf("JSON lacks empty routes or listeners: %s", s)
	}
}

// TestPayloadNarrowed: R3 and R6, a route Known narrowed to one reading.
func TestPayloadNarrowed(t *testing.T) {
	known := refs(orders, types.NamespacedName{Namespace: "kgwctl-proxy", Name: "public"})
	check := func(t *testing.T, readings []proxyview.Reading) {
		t.Helper()
		for _, rd := range readings {
			if want := rd.Route.Name == "orders-kgwctl"; rd.NotInCluster != want {
				t.Errorf("reading %+v: notInCluster %v, want %v", rd.Route, rd.NotInCluster, want)
			}
		}
	}
	// R3: under --route, each colliding route is ambiguous and narrowed.
	p := build(t, golden(t, "policies.json"), proxyview.Options{Gateway: proxyGW, Known: known, Selection: proxyview.Selection{Route: orders}}).Payload(proxyview.Source{})
	if len(p.Selection.Ambiguous) != 2 || len(p.Selection.Routes) != 0 {
		t.Fatalf("selection %+v, want two ambiguous and no route", p.Selection)
	}
	for _, a := range p.Selection.Ambiguous {
		if !a.Narrowed || len(a.Readings) != 2 {
			t.Errorf("%s: narrowed %v readings %d, want narrowed with two", a.Name, a.Narrowed, len(a.Readings))
		}
		check(t, a.Readings)
	}
	// R6: without --route, the origin is narrowed; no selection.
	p = build(t, golden(t, "policies.json"), proxyview.Options{Gateway: proxyGW, Known: known}).Payload(proxyview.Source{})
	if p.Selection != nil {
		t.Errorf("selection %+v, want none", p.Selection)
	}
	n := 0
	for _, r := range payloadRoutes(p) {
		if !r.Origin.Narrowed {
			continue
		}
		n++
		if r.Origin.Certainty != "ambiguous" || r.Origin.ChosenBy != "" {
			t.Errorf("%s: origin %+v, want ambiguous and not chosen", r.Name, r.Origin)
		}
		if strings.Contains(r.Name, "orders") {
			check(t, r.Origin.Readings)
		}
	}
	if n != 3 {
		t.Errorf("%d narrowed routes, want the colliding pair and public", n)
	}
}

// TestPayloadPolicyLevels: each record stays at its own level (DD-16).
func TestPayloadPolicyLevels(t *testing.T) {
	v := build(t, handBuilt(t, "levels.json"), proxyview.Options{})
	p := v.Payload(proxyview.Source{})
	shop := func(name, level string) []proxyview.Policy {
		return []proxyview.Policy{{Policy: proxyview.Ref{Group: "gateway.kgateway.dev", Kind: "TrafficPolicy", Namespace: "shop", Name: name}, Field: "cors", Level: level, Certainty: "exact"}}
	}
	l := p.Listeners[0]
	hcm := l.FilterChains[0].HCM
	vh := hcm.VirtualHosts[0]
	for _, tc := range []struct {
		where     string
		got, want []proxyview.Policy
	}{
		{"listener", l.Policies, shop("listener-p", "listener")},
		{"routeConfig", hcm.RouteConfig.Policies, shop("rc-p", "routeConfig")},
		{"virtualHost", vh.Policies, shop("vh-p", "virtualHost")},
		{"route", vh.Routes[0].Policies, shop("route-p", "route")},
	} {
		if !slices.Equal(tc.got, tc.want) {
			t.Errorf("%s policies %+v, want %+v", tc.where, tc.got, tc.want)
		}
	}
	out := text(t, v, proxyview.TextOptions{})
	for _, want := range []string{provenance.PoliciesLabel + ": TrafficPolicy shop/route-p (cors)\n", provenance.AboveRouteLabel + ": TrafficPolicy shop/vh-p (cors; vhost), TrafficPolicy shop/rc-p (cors; route config), TrafficPolicy shop/listener-p (cors; listener)\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("text lacks %q:\n%s", want, out)
		}
	}
	// Where the records are, not inheritance (D-024).
	if strings.Contains(out, "inherited") {
		t.Errorf("text claims inheritance:\n%s", out)
	}
}

// claimID matches a Compose claim ID such as "kgateway/12".
var claimID = regexp.MustCompile(`"[A-Za-z0-9_.-]+/[0-9]+"`)

// TestPayloadNoRawConfig: no raw config, module name or claim ID in any
// recorded dump's payload (DD-10, DD-14).
func TestPayloadNoRawConfig(t *testing.T) {
	for _, name := range []string{"demo.json", "policies.json", "collide-meta.json"} {
		for _, sel := range []proxyview.Selection{{}, {Route: orders}} {
			s := marshal(t, build(t, golden(t, name), proxyview.Options{Gateway: proxyGW, Selection: sel}).Payload(proxyview.Source{}))
			for _, bad := range []string{`"typedConfig"`, `"typedPerFilterConfig"`, `"metadata"`, `"module"`, `"kgateway/`} {
				if strings.Contains(s, bad) {
					t.Errorf("%s %v: payload holds %s", name, sel, bad)
				}
			}
			if m := claimID.FindString(s); m != "" {
				t.Errorf("%s %v: payload holds a claim ID %s", name, sel, m)
			}
		}
	}
}

// TestPayloadIDsJoin: every selection entry and filter slot names an ID the
// payload's listeners hold.
func TestPayloadIDsJoin(t *testing.T) {
	v := build(t, golden(t, "collide-meta.json"), proxyview.Options{Gateway: proxyGW, Selection: proxyview.Selection{Route: orders}})
	p := v.Payload(proxyview.Source{})
	rs := payloadRoutes(p)
	for _, m := range p.Selection.Routes {
		if _, ok := rs[m.ID]; !ok {
			t.Errorf("selected route %s not in listeners", m.ID)
		}
	}
	slots := map[string]bool{}
	for _, l := range p.Listeners {
		for _, fc := range l.FilterChains {
			for _, f := range fc.HCM.HTTPFilters {
				slots[f.Slot] = true
			}
		}
	}
	for _, r := range rs {
		for _, f := range r.Filters {
			if !slots[f.Slot] {
				t.Errorf("route %s filter slot %s not an HCM filter", r.ID, f.Slot)
			}
		}
	}
}

// TestPayloadRouteConfigSources: a source used by two HCMs, and two sources
// with one name (DD-23).
func TestPayloadRouteConfigSources(t *testing.T) {
	v := build(t, handBuilt(t, "shared-source.json"), proxyview.Options{})
	p := v.Payload(proxyview.Source{})
	rc := map[string]proxyview.RouteConfig{}
	paths := map[string]string{}
	for _, l := range p.Listeners {
		hcm := l.FilterChains[0].HCM
		rc[l.Name] = hcm.RouteConfig
		paths[l.Name] = hcm.VirtualHosts[0].Routes[0].SourcePath
	}
	if a, b := rc["listener~8080"], rc["listener~8081"]; a.Source == "" || a.Source != b.Source || paths["listener~8080"] != paths["listener~8081"] {
		t.Errorf("shared source: %+v %s and %+v %s, want one source and source path", a, paths["listener~8080"], b, paths["listener~8081"])
	}
	if a, b := rc["listener~8082"], rc["listener~8083"]; a.Name != "other" || b.Name != "other" || a.Source == b.Source || a.Kind != "inline" || b.Kind != "dynamic" {
		t.Errorf("one name: %+v and %+v, want two sources named other", a, b)
	}
	if s := marshal(t, p); strings.Contains(s, `"routeConfigs"`) {
		t.Errorf("payload lists route config sources: %s", s)
	}

	// Under --route, each use is a JSON entry, and the text lists the route once.
	v = build(t, handBuilt(t, "shared-source.json"), proxyview.Options{Selection: proxyview.Selection{Route: orders}})
	if amb := v.Payload(proxyview.Source{}).Selection.Ambiguous; len(amb) != 2 || amb[0].ID == amb[1].ID {
		t.Errorf("ambiguous %+v, want one entry per use", amb)
	}
	if out := text(t, v, proxyview.TextOptions{}); strings.Count(out, "orders_example-route-0") != 1 {
		t.Errorf("text lists the shared route other than once:\n%s", out)
	}
}

// TestPayloadECDSNetworkFilter: R15b, an unresolved ECDS network filter.
func TestPayloadECDSNetworkFilter(t *testing.T) {
	v := build(t, handBuilt(t, "ecds-network.json"), proxyview.Options{})
	fc := v.Payload(proxyview.Source{}).Listeners[1].FilterChains[0]
	if len(fc.NetworkFilters) != 1 || fc.NetworkFilters[0].ConfigSource != "unresolved" || fc.HCM != nil {
		t.Errorf("filter chain %+v, want one unresolved network filter and no hcm", fc)
	}
	out := text(t, v, proxyview.TextOptions{})
	for _, want := range []string{"ecds/net (ECDS, unresolved)", "HTTP filters unknown"} {
		if !strings.Contains(out, want) {
			t.Errorf("text lacks %q:\n%s", want, out)
		}
	}
}

// TestPayloadECDSAmbiguousHTTPFilter: R15c, an ECDS entry present in the dump
// but ambiguous. Execution is xdsview's, never derived from configSource.
func TestPayloadECDSAmbiguousHTTPFilter(t *testing.T) {
	v := build(t, handBuilt(t, "ecds-ambiguous.json"), proxyview.Options{Capture: model.CaptureComplete})
	p := v.Payload(proxyview.Source{})
	hcm := p.Listeners[0].FilterChains[0].HCM
	for _, f := range hcm.HTTPFilters[:2] {
		if f.Name != "ecds/x" || f.ConfigSource != "unresolved" {
			t.Errorf("filter %+v, want ecds/x unresolved", f)
		}
	}
	if !slices.ContainsFunc(p.Warnings, func(w proxyview.Warning) bool { return w.Code == model.WarnECDSAmbiguous }) {
		t.Errorf("warnings %+v lack ecds-ambiguous", p.Warnings)
	}
	r := hcm.VirtualHosts[0].Routes[0]
	chain := v.Chains[model.ID(r.ID)].Filters
	for i, want := range [][2]string{{"unknown", "unresolved"}, {"no", "off"}} {
		f := r.Filters[i]
		if f.Runs != want[0] || f.State != want[1] || f.ConfigSource != "unresolved" {
			t.Errorf("chain entry %d: %+v, want runs %s, state %s, unresolved", i, f, want[0], want[1])
		}
		if f.Runs != string(chain[i].Runs) || f.State != string(chain[i].State) {
			t.Errorf("chain entry %d: %s/%s, xdsview has %s/%s", i, f.Runs, f.State, chain[i].Runs, chain[i].State)
		}
	}
}

// TestPayloadStrategy: R19, a route --select known chose, and R21, a route
// Select matched under known.
func TestPayloadStrategy(t *testing.T) {
	known := refs(orders, types.NamespacedName{Namespace: "kgwctl-proxy", Name: "public"})
	v := build(t, golden(t, "policies.json"), proxyview.Options{Gateway: proxyGW, Known: known, Selection: proxyview.Selection{Route: orders}, Strategy: proxyview.StrategyKnown})
	p := v.Payload(proxyview.Source{})
	if p.Selection.Strategy != "known" || len(p.Selection.Routes) != 2 || len(p.Selection.Ambiguous) != 0 {
		t.Fatalf("selection %+v, want known with two routes and none ambiguous", p.Selection)
	}
	rs := payloadRoutes(p)
	for _, m := range p.Selection.Routes {
		r, ok := rs[m.ID]
		if m.ChosenBy != "known" || !ok {
			t.Fatalf("%s: chosenBy %q, in listeners %v; want known and true", m.Name, m.ChosenBy, ok)
		}
		if o := r.Origin; o.ChosenBy != "known" || !o.Narrowed || o.Certainty != "ambiguous" {
			t.Errorf("%s: origin %+v, want chosenBy known, narrowed, ambiguous", m.Name, o)
		}
	}
	if s := marshal(t, p); !strings.Contains(s, `"strategy":"known"`) || strings.Count(s, `"chosenBy":"known"`) != 4 {
		t.Errorf("JSON lacks the strategy or four chosenBy known: %s", s)
	}

	// A route Select matched stays chosenBy select under known.
	p = build(t, golden(t, "collide-meta.json"), proxyview.Options{Known: refs(orders, ordersKgwctl), Selection: proxyview.Selection{Route: orders}, Strategy: proxyview.StrategyKnown}).Payload(proxyview.Source{})
	if p.Selection.Strategy != "known" || len(p.Selection.Routes) != 1 || p.Selection.Routes[0].ChosenBy != "select" {
		t.Errorf("collide-meta: selection %+v, want known with one route chosen by select", p.Selection)
	}
}

// TestPayloadStrategyField: under strict, the selection names its strategy
// and every route was chosen by Select.
func TestPayloadStrategyField(t *testing.T) {
	p := build(t, golden(t, "collide-meta.json"), proxyview.Options{Selection: proxyview.Selection{Route: orders}}).Payload(proxyview.Source{})
	if p.Selection.Strategy != "strict" || len(p.Selection.Routes) == 0 {
		t.Fatalf("selection %+v, want strict with routes", p.Selection)
	}
	for _, m := range p.Selection.Routes {
		if m.ChosenBy != "select" {
			t.Errorf("route %s chosenBy %q, want select", m.ID, m.ChosenBy)
		}
	}
}
