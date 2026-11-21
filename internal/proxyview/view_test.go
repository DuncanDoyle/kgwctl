package proxyview_test

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/DuncanDoyle/xdsview/pkg/envoy/model"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/render"
	"k8s.io/apimachinery/pkg/types"

	"github.com/DuncanDoyle/kgwctl/internal/proxyview"
)

var (
	proxyGW = &types.NamespacedName{Namespace: "kgwctl-proxy", Name: "proxy"}
	demoGW  = &types.NamespacedName{Namespace: "team-a", Name: "team-a"}
	orders  = types.NamespacedName{Namespace: "kgwctl-proxy", Name: "orders"}

	// The two route names that both read as kgwctl-proxy/orders and proxy/orders-kgwctl.
	collidingRoutes = []string{
		"listener~8080~orders-kgwctl_proxy_example-route-0-httproute-orders-kgwctl-proxy-0-0-matcher-0",
		"listener~8080~orders_kgwctl_example-route-0-httproute-orders-kgwctl-proxy-0-0-matcher-0",
	}
)

// golden reads a 3.1 golden dump.
func golden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/dumps/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// edit returns a copy of a dump changed by fn, which gets the decoded "configs" array.
func edit(t *testing.T, body []byte, fn func(configs []any) []any) []byte {
	t.Helper()
	var dump map[string]any
	if err := json.Unmarshal(body, &dump); err != nil {
		t.Fatal(err)
	}
	dump["configs"] = fn(dump["configs"].([]any))
	out, err := json.Marshal(dump)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// section returns the config section whose @type ends with suffix.
func section(t *testing.T, configs []any, suffix string) map[string]any {
	t.Helper()
	for _, c := range configs {
		m := c.(map[string]any)
		if strings.HasSuffix(m["@type"].(string), suffix) {
			return m
		}
	}
	t.Fatalf("no %s section", suffix)
	return nil
}

func build(t *testing.T, body []byte, gw *types.NamespacedName, known []model.Ref, sel proxyview.Selection) proxyview.View {
	t.Helper()
	v, err := proxyview.Build(body, gw, known, sel)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func text(t *testing.T, v proxyview.View, opts render.Options) string {
	t.Helper()
	var b bytes.Buffer
	if err := proxyview.Text(&b, v, opts); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// routes returns every route in snap with the domains of its virtual host.
func routes(snap *model.EnvoySnapshot) map[string]model.Route {
	out := map[string]model.Route{}
	for _, l := range snap.Listeners {
		for _, fc := range l.FilterChains {
			if fc.HCM == nil || fc.HCM.RouteConfig == nil {
				continue
			}
			for _, vh := range fc.HCM.RouteConfig.VirtualHosts {
				for _, r := range vh.Routes {
					out[strings.Join(vh.Domains, ",")] = r
				}
			}
		}
	}
	return out
}

func ambiguousNames(v proxyview.View) []string {
	var out []string
	for _, a := range v.Ambiguous {
		out = append(out, a.Name)
	}
	slices.Sort(out)
	return out
}

func TestBuildExactSelection(t *testing.T) {
	body := golden(t, "collide-meta.json")
	for _, known := range [][]model.Ref{nil, {proxyview.HTTPRouteRef(orders), proxyview.HTTPRouteRef(types.NamespacedName{Namespace: "proxy", Name: "orders-kgwctl"})}} {
		v := build(t, body, proxyGW, known, proxyview.Selection{Route: orders})
		got := routes(v.Snapshot)
		if len(got) != 1 {
			t.Fatalf("known=%v: routes %v, want only orders.kgwctl.example", known, got)
		}
		r, ok := got["orders.kgwctl.example"]
		if !ok {
			t.Fatalf("known=%v: routes %v, want orders.kgwctl.example", known, got)
		}
		if r.Origin == nil || r.Origin.Certainty != model.Exact || r.Origin.Candidates[0].Route != proxyview.HTTPRouteRef(orders) {
			t.Errorf("origin %+v, want Exact kgwctl-proxy/orders", r.Origin)
		}
		if v.Matched() != 1 || len(v.Ambiguous) != 0 {
			t.Errorf("matched %d, ambiguous %v; want 1 and none", v.Matched(), v.Ambiguous)
		}
	}
}

func TestBuildAmbiguousWithoutMetadata(t *testing.T) {
	body := golden(t, "policies.json")
	both := []model.Ref{proxyview.HTTPRouteRef(orders), proxyview.HTTPRouteRef(types.NamespacedName{Namespace: "proxy", Name: "orders-kgwctl"})}
	for _, known := range [][]model.Ref{nil, both} {
		v := build(t, body, proxyGW, known, proxyview.Selection{Route: orders})
		if v.Matched() != 0 {
			t.Errorf("known=%v: matched %d routes, want none", known, v.Matched())
		}
		if got := ambiguousNames(v); !slices.Equal(got, collidingRoutes) {
			t.Errorf("known=%v: ambiguous %v, want %v", known, got, collidingRoutes)
		}
		for _, a := range v.Ambiguous {
			if len(a.Candidates) != 2 || a.NarrowedByKnown {
				t.Errorf("known=%v: %s candidates %v narrowed %v, want two and not narrowed", known, a.Name, a.Candidates, a.NarrowedByKnown)
			}
		}
		if v.Snapshot.Listeners == nil {
			t.Errorf("known=%v: empty listeners are nil, want an empty slice", known)
		}
		out := text(t, v, render.Options{})
		for _, want := range []string{"no route matches HTTPRoute kgwctl-proxy/orders", collidingRoutes[0], "ambiguous: HTTPRoute kgwctl-proxy/orders, HTTPRoute proxy/orders-kgwctl"} {
			if !strings.Contains(out, want) {
				t.Errorf("known=%v: text lacks %q:\n%s", known, want, out)
			}
		}
		if strings.Contains(out, "one candidate exists in the cluster") {
			t.Errorf("known=%v: text claims one candidate exists:\n%s", known, out)
		}
	}
}

func TestBuildSingleSurvivorIsNotAMatch(t *testing.T) {
	body := golden(t, "policies.json")
	// proxy/orders-kgwctl is deleted; Envoy may still serve its route.
	known := []model.Ref{proxyview.HTTPRouteRef(orders), proxyview.HTTPRouteRef(types.NamespacedName{Namespace: "kgwctl-proxy", Name: "public"})}

	v := build(t, body, proxyGW, known, proxyview.Selection{Route: orders})
	if v.Matched() != 0 {
		t.Errorf("matched %d routes, want none", v.Matched())
	}
	if got := ambiguousNames(v); !slices.Equal(got, collidingRoutes) {
		t.Errorf("ambiguous %v, want %v", got, collidingRoutes)
	}
	for _, a := range v.Ambiguous {
		if !a.NarrowedByKnown || len(a.Candidates) != 2 {
			t.Errorf("%s: narrowed %v candidates %v, want narrowed with both readings", a.Name, a.NarrowedByKnown, a.Candidates)
		}
	}
	out := text(t, v, render.Options{})
	if !strings.Contains(out, "one candidate exists in the cluster") {
		t.Errorf("text lacks the one-candidate note:\n%s", out)
	}

	// The unnarrowed tree never shows the route as kgwctl-proxy/orders either.
	full := build(t, body, proxyGW, known, proxyview.Selection{})
	out = text(t, full, render.Options{})
	for _, bad := range []string{"chosen from known HTTPRoutes", "HTTPRoute kgwctl-proxy/orders · rule"} {
		if strings.Contains(out, bad) {
			t.Errorf("full tree shows %q:\n%s", bad, out)
		}
	}
	if r := routes(full.Snapshot)["orders.kgwctl.example"]; r.Origin == nil || r.Origin.Certainty != model.Ambiguous {
		t.Errorf("orders origin %+v, want ambiguous", r.Origin)
	}
}

func TestBuildWarmingListenerSurvivesNarrowing(t *testing.T) {
	body := edit(t, golden(t, "demo.json"), func(configs []any) []any {
		l := section(t, configs, ".ListenersConfigDump")["dynamic_listeners"].([]any)[0].(map[string]any)
		l["warming_state"] = l["active_state"]
		delete(l, "active_state")
		return configs
	})
	v := build(t, body, demoGW, nil, proxyview.Selection{Route: types.NamespacedName{Namespace: "team-a", Name: "nope"}})
	if len(v.Snapshot.Listeners) != 1 || v.Snapshot.Listeners[0].State.Status != model.ListenerWarming {
		t.Fatalf("listeners %+v, want listener~80 warming", v.Snapshot.Listeners)
	}
	if v.Matched() != 0 {
		t.Errorf("matched %d routes, want none", v.Matched())
	}
	if out := text(t, v, render.Options{}); !strings.Contains(out, "listener~80") || !strings.Contains(out, "warming") {
		t.Errorf("text lacks the warming listener:\n%s", out)
	}
}

func TestBuildParserWarning(t *testing.T) {
	// A second routes section whose only route config is malformed.
	body := edit(t, golden(t, "demo.json"), func(configs []any) []any {
		return append(configs, map[string]any{
			"@type":                 "type.googleapis.com/envoy.admin.v3.RoutesConfigDump",
			"dynamic_route_configs": []any{map[string]any{"route_config": map[string]any{"name": 81}}},
		})
	})
	v := build(t, body, demoGW, nil, proxyview.Selection{})
	if len(v.Warnings) != 1 || !strings.Contains(v.Warnings[0], "skipped route config") {
		t.Fatalf("warnings %q, want one for the malformed route config", v.Warnings)
	}
	if got := len(routes(v.Snapshot)); got != 5 {
		t.Errorf("%d virtual hosts with routes, want the 5 that survive", got)
	}
	out := text(t, v, render.Options{})
	if !strings.Contains(out, "warning: "+v.Warnings[0]) || !strings.Contains(out, "shop.team-a.example") {
		t.Errorf("text lacks the warning or the surviving routes:\n%s", out)
	}
	j, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Snapshot model.EnvoySnapshot `json:"snapshot"`
		Warnings []string            `json:"warnings"`
	}
	if err := json.Unmarshal(j, &doc); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(doc.Warnings, v.Warnings) || len(doc.Snapshot.Listeners) != 1 {
		t.Errorf("JSON warnings %q, listeners %d; want the warning next to the snapshot", doc.Warnings, len(doc.Snapshot.Listeners))
	}
}

// entry returns the chain entry named name.
func entry(t *testing.T, r model.Route, name string) model.ChainEntry {
	t.Helper()
	if r.Chain == nil {
		t.Fatalf("%s has no chain", r.Name)
	}
	for _, e := range r.Chain.Filters {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("%s has no filter %s", r.Name, name)
	return model.ChainEntry{}
}

func TestBuildPolicyChain(t *testing.T) {
	v := build(t, golden(t, "policies.json"), proxyGW, nil, proxyview.Selection{})
	rs := routes(v.Snapshot)

	ord := rs["orders.kgwctl.example"]
	authz := entry(t, ord, "ext_auth/kgwctl-proxy/authz")
	if !authz.Runs || authz.State != model.FilterEnabled || authz.Level != model.LevelRoute {
		t.Errorf("orders ext_authz %+v, want enabled at route level", authz)
	}
	wantPolicy := model.Contribution{Kind: model.ContributionPolicy, Ref: model.Ref{Group: "gateway.kgateway.dev", Kind: "TrafficPolicy", Namespace: "kgwctl-proxy", Name: "orders-extauth"}, Field: "extAuth"}
	if !slices.Contains(authz.Contributions, wantPolicy) {
		t.Errorf("orders ext_authz contributions %+v lack %+v", authz.Contributions, wantPolicy)
	}

	// kgateway disables ext_authz for a route by enabling its global_disable
	// filter; the ext_authz filter itself stays off, as the HCM default.
	pub := rs["public.kgwctl.example"]
	if e := entry(t, pub, "ext_auth/kgwctl-proxy/authz"); e.Runs || e.State != model.FilterOff {
		t.Errorf("public ext_authz %+v, want off", e)
	}
	if e := entry(t, pub, "global_disable/ext_auth"); !e.Runs || e.State != model.FilterEnabled || e.Level != model.LevelRoute {
		t.Errorf("public global_disable/ext_auth %+v, want enabled at route level", e)
	}

	// Response order: reversed, without the request-only router.
	var resp []string
	for _, e := range ord.Chain.ResponseOrder() {
		resp = append(resp, e.Name)
	}
	if want := []string{"dynamic_modules/simple_mutations", "ratelimit/local", "ext_auth/kgwctl-proxy/authz"}; !slices.Equal(resp, want) {
		t.Errorf("orders response order %v, want %v", resp, want)
	}
}

func TestBuildResponseOrderMarksUnknownSide(t *testing.T) {
	// The transformation filter with a type xdsview has no side record for.
	body := edit(t, golden(t, "policies.json"), func(configs []any) []any {
		l := section(t, configs, ".ListenersConfigDump")["dynamic_listeners"].([]any)[0].(map[string]any)
		hcm := l["active_state"].(map[string]any)["listener"].(map[string]any)["filter_chains"].([]any)[0].(map[string]any)["filters"].([]any)[0].(map[string]any)["typed_config"].(map[string]any)
		for _, f := range hcm["http_filters"].([]any) {
			f := f.(map[string]any)
			if f["name"] == "dynamic_modules/simple_mutations" {
				f["typed_config"].(map[string]any)["@type"] = "type.googleapis.com/example.v1.Unrecorded"
			}
		}
		return configs
	})
	v := build(t, body, proxyGW, nil, proxyview.Selection{})
	out := text(t, v, render.Options{ResponseOrder: true})
	if !strings.Contains(out, "1? dynamic_modules/simple_mutations") {
		t.Errorf("response order lacks the unknown mark:\n%s", out)
	}
	resp := strings.SplitN(out, "filters (response order):", 2)
	if len(resp) != 2 || strings.Contains(strings.SplitN(resp[1], "policies:", 2)[0], "envoy.filters.http.router") {
		t.Errorf("response order shows the request-only router:\n%s", out)
	}
}

func TestBuildRuleSelection(t *testing.T) {
	body := golden(t, "collide-meta.json")
	for rule, want := range map[int]int{0: 1, 1: 0} {
		v := build(t, body, proxyGW, nil, proxyview.Selection{Route: orders, Rule: &rule})
		if v.Matched() != want {
			t.Errorf("rule %d: matched %d routes, want %d", rule, v.Matched(), want)
		}
	}
	neg := -1
	if _, err := proxyview.Build(body, proxyGW, nil, proxyview.Selection{Route: orders, Rule: &neg}); err == nil {
		t.Error("rule -1: want an error")
	}
}

func TestBuildParseError(t *testing.T) {
	if _, err := proxyview.Build([]byte("{"), nil, nil, proxyview.Selection{}); err == nil {
		t.Fatal("want a parse error")
	}
}

// TestTextMarksNarrowedInFullTree: without --route, a route only the known
// HTTPRoutes narrowed still prints as ambiguous, with the one-candidate note.
func TestTextMarksNarrowedInFullTree(t *testing.T) {
	body := golden(t, "policies.json")
	single := []model.Ref{proxyview.HTTPRouteRef(orders), proxyview.HTTPRouteRef(types.NamespacedName{Namespace: "kgwctl-proxy", Name: "public"})}
	// The note prints on its own line, under the route line.
	marked := "ambiguous: HTTPRoute kgwctl-proxy/orders, HTTPRoute proxy/orders-kgwctl\n│     " + proxyview.OneCandidateNote

	v := build(t, body, proxyGW, single, proxyview.Selection{})
	if n := strings.Count(text(t, v, render.Options{}), marked); n != 2 {
		t.Errorf("full tree marks %d routes, want both colliding routes:\n%s", n, text(t, v, render.Options{}))
	}
	// A caller's Style still sees the origin, and the note follows it.
	styled := text(t, v, render.Options{Style: func(c render.Class, _ *render.FilterRef, s string) string {
		if c == render.ClassOrigin {
			return "<" + s + ">"
		}
		return s
	}})
	if !strings.Contains(styled, "<ambiguous: HTTPRoute kgwctl-proxy/orders, HTTPRoute proxy/orders-kgwctl>\n│     "+proxyview.OneCandidateNote) {
		t.Errorf("styled tree lacks the styled origin and note:\n%s", styled)
	}
	for _, known := range [][]model.Ref{nil, append(single, proxyview.HTTPRouteRef(types.NamespacedName{Namespace: "proxy", Name: "orders-kgwctl"}))} {
		if out := text(t, build(t, body, proxyGW, known, proxyview.Selection{}), render.Options{}); strings.Contains(out, marked) {
			t.Errorf("known=%v: note on the orders routes, which the cluster did not narrow:\n%s", known, out)
		}
	}
}

// copyListener prepends a copy of the dump's listener on port 9090 whose route
// config is a copy named "copy"; strip removes route metadata from the copy.
func copyListener(t *testing.T, body []byte, strip bool) []byte {
	t.Helper()
	return edit(t, body, func(configs []any) []any {
		clone := func(v any) map[string]any {
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatal(err)
			}
			return m
		}
		ls := section(t, configs, ".ListenersConfigDump")
		dl := ls["dynamic_listeners"].([]any)
		l := clone(dl[0])
		l["name"] = "listener~9090"
		lst := l["active_state"].(map[string]any)["listener"].(map[string]any)
		lst["name"] = "listener~9090"
		lst["address"].(map[string]any)["socket_address"].(map[string]any)["port_value"] = 9090
		lst["filter_chains"].([]any)[0].(map[string]any)["filters"].([]any)[0].(map[string]any)["typed_config"].(map[string]any)["rds"].(map[string]any)["route_config_name"] = "copy"
		ls["dynamic_listeners"] = append([]any{l}, dl...)

		rs := section(t, configs, ".RoutesConfigDump")
		drc := rs["dynamic_route_configs"].([]any)
		rc := clone(drc[0])
		rc["route_config"].(map[string]any)["name"] = "copy"
		if strip {
			for _, vh := range rc["route_config"].(map[string]any)["virtual_hosts"].([]any) {
				for _, r := range vh.(map[string]any)["routes"].([]any) {
					delete(r.(map[string]any), "metadata")
				}
			}
		}
		rs["dynamic_route_configs"] = append([]any{rc}, drc...)
		return configs
	})
}

// TestBuildAmbiguousPerRouteConfig: the ambiguous list is per route config and
// route name, so same-named routes in two HCMs keep their own readings.
func TestBuildAmbiguousPerRouteConfig(t *testing.T) {
	// The copy has no metadata (ambiguous); the original has it (exact).
	v := build(t, copyListener(t, golden(t, "collide-meta.json"), true), proxyGW, nil, proxyview.Selection{Route: orders})
	if v.Matched() != 1 {
		t.Errorf("matched %d routes, want the exact one", v.Matched())
	}
	if got := ambiguousNames(v); !slices.Equal(got, collidingRoutes) {
		t.Fatalf("ambiguous %v, want %v", got, collidingRoutes)
	}
	for _, a := range v.Ambiguous {
		if a.RouteConfig != "copy" || len(a.Candidates) != 2 {
			t.Errorf("%s: route config %q, candidates %v; want copy and both readings", a.Name, a.RouteConfig, a.Candidates)
		}
	}

	// No metadata anywhere: each colliding name is listed once per route config.
	v = build(t, copyListener(t, golden(t, "policies.json"), false), proxyGW, nil, proxyview.Selection{Route: orders})
	if len(v.Ambiguous) != 4 {
		t.Fatalf("ambiguous %+v, want 4 entries", v.Ambiguous)
	}
	out := text(t, v, render.Options{})
	for _, want := range []string{collidingRoutes[0] + " · route config copy · ambiguous", collidingRoutes[0] + " · route config listener~8080 · ambiguous"} {
		if !strings.Contains(out, want) {
			t.Errorf("text lacks %q:\n%s", want, out)
		}
	}
}

// TestTextNoListenersAfterNarrowing: a selection that leaves nothing prints
// the header and the no-match line, not "no listeners".
func TestTextNoListenersAfterNarrowing(t *testing.T) {
	v := build(t, golden(t, "collide-meta.json"), proxyGW, nil, proxyview.Selection{Route: types.NamespacedName{Namespace: "kgwctl-proxy", Name: "nope"}})
	out := text(t, v, render.Options{Header: "File x.json"})
	if want := "File x.json\nno route matches HTTPRoute kgwctl-proxy/nope\n"; out != want {
		t.Errorf("text = %q, want %q", out, want)
	}
}

// renameRoute renames the route whose name contains old to name.
func renameRoute(t *testing.T, body []byte, old, name string) []byte {
	t.Helper()
	return edit(t, body, func(configs []any) []any {
		for _, rc := range section(t, configs, ".RoutesConfigDump")["dynamic_route_configs"].([]any) {
			for _, vh := range rc.(map[string]any)["route_config"].(map[string]any)["virtual_hosts"].([]any) {
				for _, r := range vh.(map[string]any)["routes"].([]any) {
					if r := r.(map[string]any); strings.Contains(r["name"].(string), old) {
						r["name"] = name
						return configs
					}
				}
			}
		}
		t.Fatalf("no route named like %s", old)
		return nil
	})
}

// TestBuildUnresolvedNeverMatches: a route name without the matcher suffix
// has no origin, so it is listed as unresolved, never matched by its name.
func TestBuildUnresolvedNeverMatches(t *testing.T) {
	const name = "listener~8080~public_kgwctl_example-route-0-httproute-orders-team-a-0-0"
	body := renameRoute(t, golden(t, "policies.json"), "public_kgwctl", name)
	teamA := types.NamespacedName{Namespace: "team-a", Name: "orders"}
	for _, known := range [][]model.Ref{nil, {proxyview.HTTPRouteRef(teamA)}} {
		v := build(t, body, proxyGW, known, proxyview.Selection{Route: teamA})
		if v.Matched() != 0 {
			t.Errorf("known=%v: matched %d routes, want none", known, v.Matched())
		}
		if len(v.Ambiguous) != 1 || v.Ambiguous[0].Name != name || !v.Ambiguous[0].Unresolved || len(v.Ambiguous[0].Candidates) != 0 {
			t.Fatalf("known=%v: ambiguous %+v, want %s unresolved without candidates", known, v.Ambiguous, name)
		}
		out := text(t, v, render.Options{})
		for _, want := range []string{"no route matches HTTPRoute team-a/orders", name + " · origin unresolved"} {
			if !strings.Contains(out, want) {
				t.Errorf("known=%v: text lacks %q:\n%s", known, want, out)
			}
		}
	}
}

// TestBuildUnresolvedIgnoresVhostPrefix: an HTTPRoute marker inside the vhost
// part of a route name does not list that route as unresolved for the HTTPRoute.
func TestBuildUnresolvedIgnoresVhostPrefix(t *testing.T) {
	const name = "listener~8080~httproute-orders-team-a-x_example-route-0-httproute-payments-team-b-0-0"
	body := renameRoute(t, golden(t, "policies.json"), "public_kgwctl", name)
	teamA := types.NamespacedName{Namespace: "team-a", Name: "orders"}
	if v := build(t, body, proxyGW, nil, proxyview.Selection{Route: teamA}); len(v.Ambiguous) != 0 {
		t.Errorf("team-a/orders: ambiguous %+v, want none", v.Ambiguous)
	}
	payments := types.NamespacedName{Namespace: "team-b", Name: "payments"}
	v := build(t, body, proxyGW, nil, proxyview.Selection{Route: payments})
	if len(v.Ambiguous) != 1 || v.Ambiguous[0].Name != name || !v.Ambiguous[0].Unresolved {
		t.Errorf("team-b/payments: ambiguous %+v, want %s unresolved", v.Ambiguous, name)
	}

	// With --rule, the rule index after the route's marker decides.
	for rule, want := range map[int]int{0: 1, 1: 0} {
		v := build(t, body, proxyGW, nil, proxyview.Selection{Route: payments, Rule: &rule})
		if len(v.Ambiguous) != want || v.Matched() != 0 {
			t.Errorf("team-b/payments rule %d: ambiguous %+v, matched %d; want %d and none", rule, v.Ambiguous, v.Matched(), want)
		}
		if v := build(t, body, proxyGW, nil, proxyview.Selection{Route: teamA, Rule: &rule}); len(v.Ambiguous) != 0 {
			t.Errorf("team-a/orders rule %d: ambiguous %+v, want none", rule, v.Ambiguous)
		}
	}
}

// TestBuildAmbiguousJudgedOnNameReadings: with two of a name's three readings
// in the cluster, the route still concerns the third, deleted HTTPRoute.
func TestBuildAmbiguousJudgedOnNameReadings(t *testing.T) {
	const name = "listener~8080~public_kgwctl_example-route-0-httproute-a-b-c-d-0-0-matcher-0"
	body := renameRoute(t, golden(t, "policies.json"), "public_kgwctl", name)
	known := []model.Ref{
		proxyview.HTTPRouteRef(types.NamespacedName{Namespace: "b-c-d", Name: "a"}),
		proxyview.HTTPRouteRef(types.NamespacedName{Namespace: "c-d", Name: "a-b"}),
	}
	for _, sel := range []types.NamespacedName{{Namespace: "d", Name: "a-b-c"}, {Namespace: "b-c-d", Name: "a"}} {
		v := build(t, body, proxyGW, known, proxyview.Selection{Route: sel})
		if v.Matched() != 0 {
			t.Errorf("%s: matched %d routes, want none", sel, v.Matched())
		}
		if len(v.Ambiguous) != 1 || v.Ambiguous[0].Name != name || len(v.Ambiguous[0].Candidates) != 3 || v.Ambiguous[0].NarrowedByKnown {
			t.Fatalf("%s: ambiguous %+v, want %s with three readings, not narrowed", sel, v.Ambiguous, name)
		}
		if out := text(t, v, render.Options{}); !strings.Contains(out, "HTTPRoute d/a-b-c") {
			t.Errorf("%s: text lacks the deleted reading:\n%s", sel, out)
		}
	}
}
