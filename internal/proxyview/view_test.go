package proxyview_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/DuncanDoyle/xdsview/pkg/adapter/kgateway"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/model"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/provenance"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/render"
	"k8s.io/apimachinery/pkg/types"

	"github.com/DuncanDoyle/kgwctl/internal/proxyview"
	"github.com/DuncanDoyle/kgwctl/internal/proxyview/proxyviewtest"
)

var (
	proxyGW = &types.NamespacedName{Namespace: "kgwctl-proxy", Name: "proxy"}
	demoGW  = &types.NamespacedName{Namespace: "team-a", Name: "team-a"}
	orders  = types.NamespacedName{Namespace: "kgwctl-proxy", Name: "orders"}
	// ordersKgwctl is the other reading of the colliding route names.
	ordersKgwctl = types.NamespacedName{Namespace: "proxy", Name: "orders-kgwctl"}

	// The two route names that both read as kgwctl-proxy/orders and proxy/orders-kgwctl.
	collidingRoutes = []string{
		"listener~8080~orders-kgwctl_proxy_example-route-0-httproute-orders-kgwctl-proxy-0-0-matcher-0",
		"listener~8080~orders_kgwctl_example-route-0-httproute-orders-kgwctl-proxy-0-0-matcher-0",
	}
)

// golden reads a recorded dump.
func golden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/dumps/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// handBuilt reads a dump of testdata/.
func handBuilt(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
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

func build(t *testing.T, body []byte, o proxyview.Options) proxyview.View {
	t.Helper()
	v, err := proxyview.Build(body, o)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func text(t *testing.T, v proxyview.View, o proxyview.TextOptions) string {
	t.Helper()
	var b bytes.Buffer
	if err := proxyview.Text(&b, v, o); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func refs(ns ...types.NamespacedName) []model.Ref {
	var out []model.Ref
	for _, n := range ns {
		out = append(out, proxyview.HTTPRouteRef(n))
	}
	return out
}

// routes returns every route in snap by the domains of its virtual host.
func routes(snap *model.Snapshot) map[string]model.Route {
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

// names returns the sorted names of the routes ids of v's full snapshot.
func names(v proxyview.View, ids []model.ID) []string {
	var out []string
	for _, id := range ids {
		r, ok := v.Full.Route(id)
		if !ok {
			continue
		}
		out = append(out, r.Name)
	}
	slices.Sort(out)
	return out
}

func TestBuildExactSelection(t *testing.T) {
	body := golden(t, "collide-meta.json")
	for _, known := range [][]model.Ref{nil, refs(orders, ordersKgwctl)} {
		v := build(t, body, proxyview.Options{Gateway: proxyGW, Known: known, Selection: proxyview.Selection{Route: orders}})
		got := routes(v.Snapshot)
		if _, ok := got["orders.kgwctl.example"]; !ok || len(got) != 1 {
			t.Fatalf("known=%v: routes %v, want only orders.kgwctl.example", known, got)
		}
		if ids := v.Routes(); len(ids) != 1 || v.Matched() != 1 || len(v.Selected.Ambiguous) != 0 {
			t.Errorf("known=%v: routes %v, matched %d, ambiguous %v; want one, 1 and none", known, ids, v.Matched(), v.Selected.Ambiguous)
		}
		res := provenance.Resolve[provenance.RouteSource](v.Index, v.Routes()[0])
		if res.Certainty != model.Exact || res.Candidates[0].Route != proxyview.HTTPRouteRef(orders) {
			t.Errorf("origin %+v, want exact kgwctl-proxy/orders", res)
		}
	}
}

func TestBuildAmbiguousWithoutMetadata(t *testing.T) {
	body := golden(t, "policies.json")
	for _, known := range [][]model.Ref{nil, refs(orders, ordersKgwctl)} {
		v := build(t, body, proxyview.Options{Gateway: proxyGW, Known: known, Selection: proxyview.Selection{Route: orders}})
		if v.Matched() != 0 || len(v.Routes()) != 0 {
			t.Errorf("known=%v: matched %d routes, want none", known, v.Matched())
		}
		if got := names(v, v.Selected.Ambiguous); !slices.Equal(got, collidingRoutes) {
			t.Errorf("known=%v: ambiguous %v, want %v", known, got, collidingRoutes)
		}
		if len(v.Narrowed) != 0 {
			t.Errorf("known=%v: narrowed %v, want none", known, v.Narrowed)
		}
		out := text(t, v, proxyview.TextOptions{})
		for _, want := range []string{"no route matches HTTPRoute kgwctl-proxy/orders", collidingRoutes[0], "ambiguous: HTTPRoute kgwctl-proxy/orders, HTTPRoute proxy/orders-kgwctl"} {
			if !strings.Contains(out, want) {
				t.Errorf("known=%v: text lacks %q:\n%s", known, want, out)
			}
		}
		if strings.Contains(out, proxyview.OneCandidateNote) {
			t.Errorf("known=%v: text claims one candidate exists:\n%s", known, out)
		}
	}
}

func TestBuildSingleSurvivorIsNotAMatch(t *testing.T) {
	body := golden(t, "policies.json")
	// proxy/orders-kgwctl is deleted; Envoy may still serve its route.
	known := refs(orders, types.NamespacedName{Namespace: "kgwctl-proxy", Name: "public"})

	v := build(t, body, proxyview.Options{Gateway: proxyGW, Known: known, Selection: proxyview.Selection{Route: orders}})
	if v.Matched() != 0 || len(v.Routes()) != 0 {
		t.Errorf("matched %d routes %v, want none", v.Matched(), v.Routes())
	}
	if got := names(v, v.Selected.Ambiguous); !slices.Equal(got, collidingRoutes) {
		t.Errorf("ambiguous %v, want %v", got, collidingRoutes)
	}
	var narrowed []model.ID
	for _, n := range v.Narrowed {
		narrowed = append(narrowed, n.Route)
		if len(n.Kept) != 1 || n.Kept[0].Assertion.(provenance.RouteSource).Route != proxyview.HTTPRouteRef(orders) {
			t.Errorf("%s: kept %+v, want the one kgwctl-proxy/orders reading", n.Route, n.Kept)
		}
	}
	if got := names(v, narrowed); !slices.Equal(got, collidingRoutes) {
		t.Errorf("narrowed %v, want %v", got, collidingRoutes)
	}
	if out := text(t, v, proxyview.TextOptions{}); !strings.Contains(out, proxyview.OneCandidateNote) {
		t.Errorf("text lacks the one-candidate note:\n%s", out)
	}

	// The full tree never shows the route as kgwctl-proxy/orders either.
	full := build(t, body, proxyview.Options{Gateway: proxyGW, Known: known})
	out := text(t, full, proxyview.TextOptions{})
	for _, bad := range []string{"chosen from known HTTPRoutes", "HTTPRoute kgwctl-proxy/orders · rule"} {
		if strings.Contains(out, bad) {
			t.Errorf("full tree shows %q:\n%s", bad, out)
		}
	}
}

func TestBuildWarmingListenerSurvivesNarrowing(t *testing.T) {
	body := edit(t, golden(t, "demo.json"), func(configs []any) []any {
		l := section(t, configs, ".ListenersConfigDump")["dynamic_listeners"].([]any)[0].(map[string]any)
		l["warming_state"] = l["active_state"]
		delete(l, "active_state")
		return configs
	})
	v := build(t, body, proxyview.Options{Gateway: demoGW, Selection: proxyview.Selection{Route: types.NamespacedName{Namespace: "team-a", Name: "nope"}}})
	if len(v.Snapshot.Listeners) != 1 || v.Snapshot.Listeners[0].State.Status != model.ListenerWarming {
		t.Fatalf("listeners %+v, want listener~80 warming", v.Snapshot.Listeners)
	}
	if v.Matched() != 0 {
		t.Errorf("matched %d routes, want none", v.Matched())
	}
	if out := text(t, v, proxyview.TextOptions{}); !strings.Contains(out, "listener~80") || !strings.Contains(out, "warming") {
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
	v := build(t, body, proxyview.Options{Gateway: demoGW})
	if len(v.Warnings) != 1 || v.Warnings[0].Code != model.WarnRouteConfigSkipped || !strings.Contains(v.Warnings[0].Message, "skipped route config") {
		t.Fatalf("warnings %+v, want one for the malformed route config", v.Warnings)
	}
	if got := len(routes(v.Snapshot)); got != 6 {
		t.Errorf("%d domain sets with routes, want the 5 that survive and the static listeners' *", got)
	}
	out := text(t, v, proxyview.TextOptions{})
	if !strings.HasSuffix(out, "warning: "+v.Warnings[0].Message+"\n") || !strings.Contains(out, "shop.team-a.example") {
		t.Errorf("text lacks the warning at the end or the surviving routes:\n%s", out)
	}
	p := v.Payload(proxyview.Source{})
	if !slices.Equal(p.Warnings, v.Warnings) || len(p.Listeners) != 3 || p.Listeners[0].Static {
		t.Errorf("payload warnings %+v, listeners %d; want the warning next to listener~80 and the two static listeners", p.Warnings, len(p.Listeners))
	}
}

// entry returns the chain entry named name of route r.
func entry(t *testing.T, v proxyview.View, r model.Route, name string) model.ChainEntry {
	t.Helper()
	for _, e := range v.Chains[r.ID].Filters {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("%s has no filter %s", r.Name, name)
	return model.ChainEntry{}
}

func TestBuildPolicyChain(t *testing.T) {
	v := build(t, golden(t, "policies.json"), proxyview.Options{Gateway: proxyGW})
	rs := routes(v.Snapshot)

	ord := rs["orders.kgwctl.example"]
	authz := entry(t, v, ord, "ext_auth/kgwctl-proxy/authz")
	if authz.Runs != model.TriYes || authz.State != model.FilterEnabled || authz.Level != model.LevelRoute {
		t.Errorf("orders ext_authz %+v, want enabled at route level", authz)
	}
	want := proxyview.Policy{Policy: proxyview.Ref{Group: "gateway.kgateway.dev", Kind: "TrafficPolicy", Namespace: "kgwctl-proxy", Name: "orders-extauth"},
		Field: "extAuth", Level: "route", Certainty: "inferred"}
	if f := payloadFilter(t, v, ord.ID, authz.Slot); !slices.Contains(f.Policies, want) {
		t.Errorf("orders ext_authz policies %+v lack %+v", f.Policies, want)
	}

	// kgateway disables ext_authz for a route by enabling its global_disable
	// filter; the ext_authz filter itself stays off, as the HCM default.
	pub := rs["public.kgwctl.example"]
	if e := entry(t, v, pub, "ext_auth/kgwctl-proxy/authz"); e.Runs != model.TriNo || e.State != model.FilterOff {
		t.Errorf("public ext_authz %+v, want off", e)
	}
	if e := entry(t, v, pub, "global_disable/ext_auth"); e.Runs != model.TriYes || e.State != model.FilterEnabled || e.Level != model.LevelRoute {
		t.Errorf("public global_disable/ext_auth %+v, want enabled at route level", e)
	}

	// Response order: reversed, without the request-only router.
	var resp []string
	for _, e := range v.Chains[ord.ID].ResponseOrder() {
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
	v := build(t, body, proxyview.Options{Gateway: proxyGW})
	out := text(t, v, proxyview.TextOptions{ResponseOrder: true})
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
		v := build(t, body, proxyview.Options{Gateway: proxyGW, Selection: proxyview.Selection{Route: orders, Rule: &rule}})
		if v.Matched() != want {
			t.Errorf("rule %d: matched %d routes, want %d", rule, v.Matched(), want)
		}
	}
	neg := -1
	if _, err := proxyview.Build(body, proxyview.Options{Selection: proxyview.Selection{Route: orders, Rule: &neg}}); err == nil {
		t.Error("rule -1: want an error")
	}
}

func TestBuildParseError(t *testing.T) {
	if _, err := proxyview.Build([]byte("{"), proxyview.Options{}); err == nil {
		t.Fatal("want a parse error")
	}
}

// TestTextMarksNarrowedInFullTree: without --route, a route only the known
// HTTPRoutes narrowed still prints as ambiguous, with the one-candidate note.
func TestTextMarksNarrowedInFullTree(t *testing.T) {
	body := golden(t, "policies.json")
	single := refs(orders, types.NamespacedName{Namespace: "kgwctl-proxy", Name: "public"})
	// The note prints on its own line, under the route line.
	marked := "ambiguous: HTTPRoute kgwctl-proxy/orders, HTTPRoute proxy/orders-kgwctl\n│     " + proxyview.OneCandidateNote

	out := text(t, build(t, body, proxyview.Options{Gateway: proxyGW, Known: single}), proxyview.TextOptions{})
	if n := strings.Count(out, marked); n != 2 {
		t.Errorf("full tree marks %d routes, want both colliding routes:\n%s", n, out)
	}
	if strings.Contains(out, "chosen from known") {
		t.Errorf("full tree marks a route as chosen:\n%s", out)
	}
	for _, known := range [][]model.Ref{nil, append(single, proxyview.HTTPRouteRef(ordersKgwctl))} {
		if out := text(t, build(t, body, proxyview.Options{Gateway: proxyGW, Known: known}), proxyview.TextOptions{}); strings.Contains(out, marked) {
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

// TestBuildAmbiguousPerRouteConfig: the ambiguous list is per route occurrence,
// so same-named routes in two route configs keep their own readings.
func TestBuildAmbiguousPerRouteConfig(t *testing.T) {
	// The copy has no metadata (ambiguous); the original has it (exact).
	v := build(t, copyListener(t, golden(t, "collide-meta.json"), true), proxyview.Options{Gateway: proxyGW, Selection: proxyview.Selection{Route: orders}})
	if v.Matched() != 1 {
		t.Errorf("matched %d routes, want the exact one", v.Matched())
	}
	if got := names(v, v.Selected.Ambiguous); !slices.Equal(got, collidingRoutes) {
		t.Fatalf("ambiguous %v, want %v", got, collidingRoutes)
	}
	sel := v.Payload(proxyview.Source{}).Selection
	for _, a := range sel.Ambiguous {
		if a.RouteConfig != "copy" || len(a.Readings) != 2 {
			t.Errorf("%s: route config %q, readings %v; want copy and both readings", a.Name, a.RouteConfig, a.Readings)
		}
	}

	// No metadata anywhere: each colliding name is listed once per route config.
	v = build(t, copyListener(t, golden(t, "policies.json"), false), proxyview.Options{Gateway: proxyGW, Selection: proxyview.Selection{Route: orders}})
	if len(v.Selected.Ambiguous) != 4 {
		t.Fatalf("ambiguous %v, want 4 entries", v.Selected.Ambiguous)
	}
	out := text(t, v, proxyview.TextOptions{})
	for _, want := range []string{collidingRoutes[0] + " · route config copy · ambiguous", collidingRoutes[0] + " · route config listener~8080 · ambiguous"} {
		if !strings.Contains(out, want) {
			t.Errorf("text lacks %q:\n%s", want, out)
		}
	}
}

// TestTextNoListenersAfterNarrowing: a selection that leaves nothing prints
// the header and the no-match line, not "no listeners".
func TestTextNoListenersAfterNarrowing(t *testing.T) {
	v := build(t, golden(t, "collide-meta.json"), proxyview.Options{Gateway: proxyGW, Selection: proxyview.Selection{Route: types.NamespacedName{Namespace: "kgwctl-proxy", Name: "nope"}}})
	out := text(t, v, proxyview.TextOptions{Header: "File x.json"})
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
// has no reading, so it is listed as unresolved, never matched by its name.
func TestBuildUnresolvedNeverMatches(t *testing.T) {
	const name = "listener~8080~public_kgwctl_example-route-0-httproute-orders-team-a-0-0"
	body := renameRoute(t, golden(t, "policies.json"), "public_kgwctl", name)
	teamA := types.NamespacedName{Namespace: "team-a", Name: "orders"}
	for _, known := range [][]model.Ref{nil, refs(teamA)} {
		v := build(t, body, proxyview.Options{Gateway: proxyGW, Known: known, Selection: proxyview.Selection{Route: teamA}})
		if v.Matched() != 0 {
			t.Errorf("known=%v: matched %d routes, want none", known, v.Matched())
		}
		if got := names(v, v.Selected.Unresolved); !slices.Equal(got, []string{name}) || len(v.Selected.Ambiguous) != 0 {
			t.Fatalf("known=%v: unresolved %v, ambiguous %v; want %s unresolved only", known, got, v.Selected.Ambiguous, name)
		}
		out := text(t, v, proxyview.TextOptions{})
		for _, want := range []string{"no route matches HTTPRoute team-a/orders", "unresolved routes, not matched:", name + " · origin unresolved"} {
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
	if v := build(t, body, proxyview.Options{Selection: proxyview.Selection{Route: teamA}}); len(v.Selected.Unresolved)+len(v.Selected.Ambiguous) != 0 {
		t.Errorf("team-a/orders: unresolved %v, ambiguous %v; want none", v.Selected.Unresolved, v.Selected.Ambiguous)
	}
	payments := types.NamespacedName{Namespace: "team-b", Name: "payments"}
	v := build(t, body, proxyview.Options{Selection: proxyview.Selection{Route: payments}})
	if got := names(v, v.Selected.Unresolved); !slices.Equal(got, []string{name}) {
		t.Errorf("team-b/payments: unresolved %v, want %s", got, name)
	}

	// With --rule, the rule index after the route's marker decides.
	for rule, want := range map[int]int{0: 1, 1: 0} {
		v := build(t, body, proxyview.Options{Selection: proxyview.Selection{Route: payments, Rule: &rule}})
		if len(v.Selected.Unresolved) != want || v.Matched() != 0 {
			t.Errorf("team-b/payments rule %d: unresolved %v, matched %d; want %d and none", rule, v.Selected.Unresolved, v.Matched(), want)
		}
		if v := build(t, body, proxyview.Options{Selection: proxyview.Selection{Route: teamA, Rule: &rule}}); len(v.Selected.Unresolved) != 0 {
			t.Errorf("team-a/orders rule %d: unresolved %v, want none", rule, v.Selected.Unresolved)
		}
	}
}

// TestBuildAmbiguousJudgedOnNameReadings: with two of a name's three readings
// in the cluster, the route still concerns the third, deleted HTTPRoute.
func TestBuildAmbiguousJudgedOnNameReadings(t *testing.T) {
	const name = "listener~8080~public_kgwctl_example-route-0-httproute-a-b-c-d-0-0-matcher-0"
	body := renameRoute(t, golden(t, "policies.json"), "public_kgwctl", name)
	known := refs(types.NamespacedName{Namespace: "b-c-d", Name: "a"}, types.NamespacedName{Namespace: "c-d", Name: "a-b"})
	for _, sel := range []types.NamespacedName{{Namespace: "d", Name: "a-b-c"}, {Namespace: "b-c-d", Name: "a"}} {
		v := build(t, body, proxyview.Options{Gateway: proxyGW, Known: known, Selection: proxyview.Selection{Route: sel}})
		if v.Matched() != 0 {
			t.Errorf("%s: matched %d routes, want none", sel, v.Matched())
		}
		amb := v.Payload(proxyview.Source{}).Selection.Ambiguous
		if len(amb) != 1 || amb[0].Name != name || len(amb[0].Readings) != 3 || !amb[0].Narrowed {
			t.Fatalf("%s: ambiguous %+v, want %s with three readings, narrowed", sel, amb, name)
		}
		// Two readings kept: no one-candidate note.
		if out := text(t, v, proxyview.TextOptions{}); !strings.Contains(out, "HTTPRoute d/a-b-c") || strings.Contains(out, proxyview.OneCandidateNote) {
			t.Errorf("%s: text lacks the deleted reading or has the note:\n%s", sel, out)
		}
	}
}

// TestBuildNoKnownWithFile: with --file there is no Known, so nothing is narrowed.
func TestBuildNoKnownWithFile(t *testing.T) {
	for _, sel := range []proxyview.Selection{{}, {Route: orders}} {
		v := build(t, golden(t, "policies.json"), proxyview.Options{Gateway: proxyGW, Selection: sel})
		if len(v.Narrowed) != 0 || len(v.Chosen) != 0 {
			t.Errorf("selection %v: narrowed %v, chosen %v; want none", sel, v.Narrowed, v.Chosen)
		}
		if out := text(t, v, proxyview.TextOptions{}); strings.Contains(out, proxyview.OneCandidateNote) {
			t.Errorf("selection %v: text has the one-candidate note:\n%s", sel, out)
		}
	}
}

// TestBuildCapture: the capture is unverified unless the caller says complete (DD-2).
func TestBuildCapture(t *testing.T) {
	for capture, want := range map[model.Capture]string{"": "unverified", model.CaptureComplete: "complete"} {
		v := build(t, golden(t, "demo.json"), proxyview.Options{Capture: capture})
		if got := v.Payload(proxyview.Source{}).Capture; got != want {
			t.Errorf("capture %q: payload capture %q, want %q", capture, got, want)
		}
	}
}

// gatewayModule claims a Gateway on every non-static listener at certainty c.
type gatewayModule struct{ c model.Certainty }

func (gatewayModule) Name() string { return "gateway-claims" }
func (m gatewayModule) Claims(snap *model.Snapshot) []provenance.Claim {
	gw := model.Ref{Group: "gateway.networking.k8s.io", Kind: "Gateway", Namespace: "claimed", Name: "gw"}
	var out []provenance.Claim
	for _, l := range snap.Listeners {
		if !l.Static {
			out = append(out, provenance.Claim{Subject: l.ID, Assertion: provenance.GatewaySource{Gateway: gw}, Certainty: m.c,
				Evidence: []provenance.Evidence{{Kind: provenance.EvidenceMetadata, Subject: l.ID, Field: "metadata", Object: &gw}}})
		}
	}
	return out
}

// TestTextGatewayContext: every non-static listener row ends with the Gateway
// kgwctl was given; static rows get none, and a Gateway claim at inferred or
// better replaces it.
func TestTextGatewayContext(t *testing.T) {
	body := golden(t, "policies.json")
	label := "Gateway kgwctl-proxy/proxy " + proxyview.GatewayContextNote
	out := text(t, build(t, body, proxyview.Options{Gateway: proxyGW}), proxyview.TextOptions{})
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "listener~8080 "):
			if !strings.HasSuffix(line, " · "+label) {
				t.Errorf("listener row %q does not end with %q", line, label)
			}
		case strings.Contains(line, "· static"):
			if strings.Contains(line, label) {
				t.Errorf("static row %q has the context label", line)
			}
		}
	}
	for c, want := range map[model.Certainty]bool{model.Inferred: false, model.Ambiguous: true} {
		mods := append(proxyview.DefaultModules(), gatewayModule{c: c})
		out := text(t, build(t, body, proxyview.Options{Gateway: proxyGW, Modules: mods}), proxyview.TextOptions{})
		if got := strings.Contains(out, label); got != want {
			t.Errorf("Gateway claim at %s: context label %v, want %v:\n%s", c, got, want, out)
		}
	}
}

// TestBuildDuplicateModules: two modules with one name are a Compose error.
func TestBuildDuplicateModules(t *testing.T) {
	_, err := proxyview.Build(golden(t, "demo.json"), proxyview.Options{Modules: []provenance.Module{kgateway.New(), kgateway.New()}})
	if !errors.Is(err, proxyview.ErrCompose) || !strings.HasPrefix(err.Error(), "compose provenance modules: ") {
		t.Fatalf("err = %v, want a compose error", err)
	}
	if got := proxyview.DefaultModules(); len(got) != 1 || got[0].Name() != kgateway.ModuleName {
		t.Errorf("default modules %v, want the kgateway module", got)
	}
}

// TestNormaliseMergesReading: kept claims that differ only in IndexCertainty,
// Incomplete and Exhaustive merge into one reading, which Labels marks as chosen.
func TestNormaliseMergesReading(t *testing.T) {
	v := build(t, golden(t, "policies.json"), proxyview.Options{})
	route := v.Index.About(v.Full.Listeners[0].FilterChains[0].HCM.RouteConfig.VirtualHosts[1].Routes[0].ID)[0]
	base := route.Assertion.(provenance.RouteSource)
	a, b := base, base
	a.IndexCertainty, a.Incomplete, a.Exhaustive = model.Inferred, false, true
	b.IndexCertainty, b.Incomplete, b.Exhaustive = model.Exact, true, false
	ca, cb := route, route
	ca.Assertion, cb.Assertion = a, b

	n := proxyview.Normalise(provenance.Narrowing{Route: route.Subject, Kept: []provenance.Claim{ca, cb}})
	if len(n.Kept) != 1 {
		t.Fatalf("kept %+v, want one claim", n.Kept)
	}
	got := n.Kept[0].Assertion.(provenance.RouteSource)
	if got.IndexCertainty != model.Exact || !got.Incomplete || got.Exhaustive {
		t.Errorf("merged %+v, want exact, incomplete, not exhaustive", got)
	}
	labels := provenance.Labels(v.Index, []provenance.Narrowing{n})(render.Row{Subject: route.Subject, Route: route.Subject})
	if len(labels) != 1 || !strings.Contains(labels[0], "(chosen from known HTTPRoutes)") || strings.Contains(labels[0], "ambiguous:") {
		t.Errorf("labels %q, want one reading chosen from known HTTPRoutes", labels)
	}
}

// TestParseStrategy: empty is strict; only strict and known are valid.
func TestParseStrategy(t *testing.T) {
	for in, want := range map[string]proxyview.Strategy{"": proxyview.StrategyStrict, "exact": proxyview.StrategyExact, "strict": proxyview.StrategyStrict, "known": proxyview.StrategyKnown} {
		if got, err := proxyview.ParseStrategy(in); err != nil || got != want {
			t.Errorf("ParseStrategy(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"bogus", "Known", " strict"} {
		if _, err := proxyview.ParseStrategy(in); err == nil {
			t.Errorf("ParseStrategy(%q): want an error", in)
		}
	}
	// Build checks the strategy before it parses the dump.
	if _, err := proxyview.Build([]byte("{"), proxyview.Options{Strategy: "bogus"}); err == nil || !strings.Contains(err.Error(), "selection strategy") {
		t.Errorf("Build with strategy bogus: err %v, want the strategy error", err)
	}
}

// TestChooseKnownRule: a narrowed route is chosen only when its kept claims
// name one reading that matches the query; the result is normalised.
func TestChooseKnownRule(t *testing.T) {
	v := build(t, golden(t, "policies.json"), proxyview.Options{Known: refs(orders), Selection: proxyview.Selection{Route: orders}})
	n := v.Narrowed[0]
	keep, drop := n.Kept[0], n.Dropped[0]
	// a and b differ from keep only in IndexCertainty, Incomplete and Exhaustive.
	a, b := keep, keep
	ra, rb := keep.Assertion.(provenance.RouteSource), keep.Assertion.(provenance.RouteSource)
	ra.IndexCertainty, ra.Incomplete, ra.Exhaustive = model.Inferred, false, true
	rb.IndexCertainty, rb.Incomplete, rb.Exhaustive = model.Exact, true, false
	a.Assertion, b.Assertion = ra, rb
	q := kgateway.Query{Route: proxyview.HTTPRouteRef(orders)}
	rule1 := 1
	// merged is a and b merged: exact (the higher), incomplete (b is), not exhaustive (b is not).
	one, merged := keep.Assertion.(provenance.RouteSource), ra
	merged.IndexCertainty, merged.Incomplete, merged.Exhaustive = model.Exact, true, false
	for _, tc := range []struct {
		name string
		q    kgateway.Query
		kept []provenance.Claim
		want *provenance.RouteSource // nil: not chosen
	}{
		{name: "one-reading", q: q, kept: []provenance.Claim{keep}, want: &one},
		{name: "two-readings", q: q, kept: []provenance.Claim{keep, drop}},
		{name: "other-rule", q: kgateway.Query{Route: q.Route, RuleIndex: &rule1}, kept: []provenance.Claim{keep}},
		{name: "differ-only-in-flags", q: q, kept: []provenance.Claim{a, b}, want: &merged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := proxyview.ChooseKnown(tc.q, kgateway.Selection{Narrowed: []provenance.Narrowing{{Route: n.Route, Kept: tc.kept}}})
			if tc.want == nil {
				if len(got) != 0 {
					t.Fatalf("chosen %+v, want none", got)
				}
				return
			}
			if len(got) != 1 || got[0].Route != n.Route || len(got[0].Kept) != 1 {
				t.Fatalf("chosen %+v, want %s with one kept claim", got, n.Route)
			}
			if got[0].Kept[0].Assertion != *tc.want {
				t.Errorf("kept %+v, want %+v", got[0].Kept[0].Assertion, *tc.want)
			}
		})
	}
}

// chosenRow is the route row text of a route --select known chose.
const chosenRow = "HTTPRoute kgwctl-proxy/orders · rule 0 · match 0 (chosen from known HTTPRoutes)"

// TestBuildStrategySameCapture: R3/R19 and R2/R20 on one capture.
func TestBuildStrategySameCapture(t *testing.T) {
	body := golden(t, "policies.json")
	public := types.NamespacedName{Namespace: "kgwctl-proxy", Name: "public"}
	opts := func(known []model.Ref, s proxyview.Strategy) proxyview.Options {
		return proxyview.Options{Gateway: proxyGW, Known: known, Selection: proxyview.Selection{Route: orders}, Strategy: s}
	}
	// proxy/orders-kgwctl is deleted.
	single := refs(orders, public)
	strict := build(t, body, opts(single, proxyview.StrategyStrict))
	if len(strict.Routes()) != 0 || strict.Chosen != nil {
		t.Errorf("strict: routes %v, chosen %v; want none", strict.Routes(), strict.Chosen)
	}
	known := build(t, body, opts(single, proxyview.StrategyKnown))
	var chosen []model.ID
	for _, n := range known.Chosen {
		chosen = append(chosen, n.Route)
	}
	if got := names(known, known.Routes()); !slices.Equal(got, collidingRoutes) || !slices.Equal(names(known, chosen), collidingRoutes) {
		t.Fatalf("known: routes %v, chosen %v; want both colliding routes", got, names(known, chosen))
	}
	out := text(t, known, proxyview.TextOptions{})
	if n := strings.Count(out, chosenRow+"\n"); n != 2 || known.Matched() != 2 {
		t.Errorf("known: %d rows end with the marker, matched %d; want 2 and 2:\n%s", n, known.Matched(), out)
	}
	for _, bad := range []string{"ambiguous", "no route matches", proxyview.OneCandidateNote} {
		if strings.Contains(out, bad) {
			t.Errorf("known: text has %q:\n%s", bad, out)
		}
	}

	// Both HTTPRoutes exist: Known narrows nothing, so the strategies agree.
	both := append(single, proxyview.HTTPRouteRef(ordersKgwctl))
	s, k := build(t, body, opts(both, proxyview.StrategyStrict)), build(t, body, opts(both, proxyview.StrategyKnown))
	if len(s.Routes()) != 0 || len(k.Routes()) != 0 {
		t.Errorf("both: routes %v and %v, want none", s.Routes(), k.Routes())
	}
	if a, b := text(t, s, proxyview.TextOptions{}), text(t, k, proxyview.TextOptions{}); a != b {
		t.Errorf("both: text differs:\nstrict:\n%s\nknown:\n%s", a, b)
	}
}

// flagCopies is a wrapper's extra: for each kgwctl-proxy/orders reading, a
// claim that differs only in IndexCertainty, Incomplete and Exhaustive.
func flagCopies(_ *model.Snapshot, inner []provenance.Claim) []provenance.Claim {
	var out []provenance.Claim
	for _, c := range inner {
		r, ok := c.Assertion.(provenance.RouteSource)
		if !ok || r.Route != proxyview.HTTPRouteRef(orders) {
			continue
		}
		r.IndexCertainty = model.Exact
		if r.IndexCertainty == c.Assertion.(provenance.RouteSource).IndexCertainty {
			r.IndexCertainty = model.Inferred
		}
		r.Incomplete, r.Exhaustive = !r.Incomplete, !r.Exhaustive
		c.Assertion, c.Ref = r, ""
		out = append(out, c)
	}
	return out
}

// TestKnownMarkerTextAndJSON: R26, a chosen route whose kept claims differ
// only in IndexCertainty, Incomplete and Exhaustive prints one reading.
func TestKnownMarkerTextAndJSON(t *testing.T) {
	body := golden(t, "policies.json")
	known := refs(orders, types.NamespacedName{Namespace: "kgwctl-proxy", Name: "public"})
	var first string
	for _, name := range []string{"example", "other"} {
		mods := []provenance.Module{proxyviewtest.Wrap(name, kgateway.New(), flagCopies)}
		v := build(t, body, proxyview.Options{Gateway: proxyGW, Known: known, Selection: proxyview.Selection{Route: orders}, Strategy: proxyview.StrategyKnown, Modules: mods})
		if len(v.Warnings) != 0 {
			t.Fatalf("%s: warnings %+v, want none", name, v.Warnings)
		}
		for _, n := range v.Narrowed {
			if len(n.Kept) != 2 {
				t.Fatalf("%s: %s kept %d claims, want the original and its copy", name, n.Route, len(n.Kept))
			}
		}
		out := text(t, v, proxyview.TextOptions{})
		if n := strings.Count(out, chosenRow+"\n"); n != 2 || strings.Contains(out, "ambiguous") {
			t.Errorf("%s: %d marked rows, want 2 and no ambiguous:\n%s", name, n, out)
		}
		p := v.Payload(proxyview.Source{})
		if len(p.Selection.Routes) != 2 || len(p.Selection.Ambiguous) != 0 {
			t.Fatalf("%s: selection %+v, want two routes", name, p.Selection)
		}
		for _, m := range p.Selection.Routes {
			if m.ChosenBy != "known" {
				t.Errorf("%s: %s chosenBy %q, want known", name, m.Name, m.ChosenBy)
			}
		}
		if got := out + marshal(t, p); first == "" {
			first = got
		} else if got != first {
			t.Errorf("%s: output differs from the first wrapper name", name)
		}
	}
}

// TestStrategyNoEffectWithoutRoute: R23 at the view, the same text and payload.
func TestStrategyNoEffectWithoutRoute(t *testing.T) {
	body := golden(t, "policies.json")
	known := refs(orders, types.NamespacedName{Namespace: "kgwctl-proxy", Name: "public"})
	var outs []string
	for _, s := range strategies {
		v := build(t, body, proxyview.Options{Gateway: proxyGW, Known: known, Strategy: s})
		outs = append(outs, text(t, v, proxyview.TextOptions{})+marshal(t, v.Payload(proxyview.Source{})))
	}
	for i, s := range strategies[1:] {
		if outs[i+1] != outs[0] {
			t.Errorf("%s differs from exact without --route:\n%s\n---\n%s", s, outs[0], outs[i+1])
		}
	}
}

var strategies = []proxyview.Strategy{proxyview.StrategyExact, proxyview.StrategyStrict, proxyview.StrategyKnown}

// withoutStrategy blanks the selection strategy of a payload, to compare the rest.
func withoutStrategy(p proxyview.ProxyRoutes) proxyview.ProxyRoutes {
	if p.Selection != nil {
		s := *p.Selection
		s.Strategy = ""
		p.Selection = &s
	}
	return p
}

// TestBuildExactSameCapture: R27 and R28, one capture with and one without
// route metadata under all three strategies.
func TestBuildExactSameCapture(t *testing.T) {
	public := types.NamespacedName{Namespace: "kgwctl-proxy", Name: "public"}
	for _, tc := range []struct {
		dump  string
		known []model.Ref
		want  map[proxyview.Strategy][]string // matched route names
	}{
		// Metadata: the exact reading matches under every strategy.
		{dump: "collide-meta.json", known: refs(orders, ordersKgwctl, public), want: map[proxyview.Strategy][]string{
			proxyview.StrategyExact: collidingRoutes[1:], proxyview.StrategyStrict: collidingRoutes[1:], proxyview.StrategyKnown: collidingRoutes[1:]}},
		// No metadata: nothing is exact, so exact matches what strict does: nothing.
		{dump: "policies.json", known: refs(orders, public), want: map[proxyview.Strategy][]string{
			proxyview.StrategyExact: nil, proxyview.StrategyStrict: nil, proxyview.StrategyKnown: collidingRoutes}},
	} {
		body := golden(t, tc.dump)
		views := map[proxyview.Strategy]proxyview.View{}
		for _, s := range strategies {
			v := build(t, body, proxyview.Options{Gateway: proxyGW, Known: tc.known, Selection: proxyview.Selection{Route: orders}, Strategy: s})
			if got := names(v, v.Routes()); !slices.Equal(got, tc.want[s]) || len(v.Inferred) != 0 {
				t.Errorf("%s %s: matched %v, inferred %v; want %v and none", tc.dump, s, got, v.Inferred, tc.want[s])
			}
			views[s] = v
		}
		e, s := views[proxyview.StrategyExact], views[proxyview.StrategyStrict]
		if text(t, e, proxyview.TextOptions{}) != text(t, s, proxyview.TextOptions{}) {
			t.Errorf("%s: exact and strict text differ", tc.dump)
		}
		ep, sp := e.Payload(proxyview.Source{}), s.Payload(proxyview.Source{})
		if ep.Selection.Strategy != "exact" || marshal(t, withoutStrategy(ep)) != marshal(t, withoutStrategy(sp)) {
			t.Errorf("%s: exact payload differs from strict other than in strategy %q", tc.dump, ep.Selection.Strategy)
		}
	}
}

// TestBuildExactListsInferred: R29, a route strict matches on a name reading
// is listed as inferred under exact, in the text and the payload.
func TestBuildExactListsInferred(t *testing.T) {
	body := handBuilt(t, "levels.json")
	strict := build(t, body, proxyview.Options{Selection: proxyview.Selection{Route: shopOrders}})
	if len(strict.Routes()) != 1 {
		t.Fatalf("strict: routes %v, want the inferred shop/orders route", strict.Routes())
	}
	id := strict.Routes()[0]
	r, _ := strict.Full.Route(id)
	for _, s := range []proxyview.Strategy{proxyview.StrategyStrict, proxyview.StrategyKnown} {
		if v := build(t, body, proxyview.Options{Selection: proxyview.Selection{Route: shopOrders}, Strategy: s}); text(t, v, proxyview.TextOptions{}) != text(t, strict, proxyview.TextOptions{}) {
			t.Errorf("%s: text differs from strict", s)
		}
	}

	v := build(t, body, proxyview.Options{Selection: proxyview.Selection{Route: shopOrders}, Strategy: proxyview.StrategyExact})
	if len(v.Routes()) != 0 || !slices.Equal(v.Inferred, []model.ID{id}) || v.Matched() != 0 {
		t.Fatalf("exact: routes %v, inferred %v, matched %d; want none, %s and 0", v.Routes(), v.Inferred, v.Matched(), id)
	}
	want := "no route matches HTTPRoute shop/orders\ninferred routes, not matched:\n  " + r.Name + " · HTTPRoute shop/orders · rule 0 · match 0 (inferred)\n"
	if out := text(t, v, proxyview.TextOptions{}); out != want {
		t.Errorf("exact text:\n%s\nwant:\n%s", out, want)
	}
	sel := v.Payload(proxyview.Source{}).Selection
	if len(sel.Routes) != 0 || len(sel.Ambiguous) != 0 || len(sel.Inferred) != 1 {
		t.Fatalf("exact selection %+v, want one inferred route only", sel)
	}
	if in := sel.Inferred[0]; in.ID != string(id) || len(in.Readings) != 1 || in.Readings[0].Certainty != "inferred" {
		t.Errorf("inferred entry %+v, want %s with its one inferred reading", in, id)
	}
	if s := marshal(t, strict.Payload(proxyview.Source{})); !strings.Contains(s, `"inferred":[]`) {
		t.Errorf("strict payload lacks an empty inferred list: %s", s)
	}
}

// stripIndices removes the rule and match of kgwctl-proxy/orders' route
// metadata, so the module reads them from the route name.
func stripIndices(t *testing.T, body []byte) []byte {
	t.Helper()
	return edit(t, body, func(configs []any) []any {
		for _, rc := range section(t, configs, ".RoutesConfigDump")["dynamic_route_configs"].([]any) {
			for _, vh := range rc.(map[string]any)["route_config"].(map[string]any)["virtual_hosts"].([]any) {
				for _, r := range vh.(map[string]any)["routes"].([]any) {
					md, _ := r.(map[string]any)["metadata"].(map[string]any)
					fm, _ := md["filter_metadata"].(map[string]any)
					if src, ok := fm["dev.kgateway.route_source"].(map[string]any); ok && src["name"] == "orders" && src["namespace"] == "kgwctl-proxy" {
						delete(src, "rule")
						delete(src, "match")
						return configs
					}
				}
			}
		}
		t.Fatal("no kgwctl-proxy/orders route metadata")
		return nil
	})
}

// TestBuildExactInferredIndex: with --rule, exact also needs the rule index
// to be exact; without --rule, the exact HTTPRoute is enough.
func TestBuildExactInferredIndex(t *testing.T) {
	body := stripIndices(t, golden(t, "collide-meta.json"))
	rule := 0
	for _, tc := range []struct {
		s       proxyview.Strategy
		rule    *int
		matched int
	}{
		{proxyview.StrategyExact, nil, 1},
		{proxyview.StrategyExact, &rule, 0},
		{proxyview.StrategyStrict, &rule, 1},
	} {
		v := build(t, body, proxyview.Options{Selection: proxyview.Selection{Route: orders, Rule: tc.rule}, Strategy: tc.s})
		if v.Matched() != tc.matched || len(v.Inferred) != 1-tc.matched {
			t.Errorf("%s rule %v: matched %d, inferred %v; want %d", tc.s, tc.rule != nil, v.Matched(), v.Inferred, tc.matched)
		}
		if tc.matched == 0 {
			if out := text(t, v, proxyview.TextOptions{}); !strings.Contains(out, "inferred routes, not matched:\n  "+collidingRoutes[1]+" · HTTPRoute kgwctl-proxy/orders · rule 0 · match 0 (partly from name)\n") {
				t.Errorf("%s rule: text lacks the inferred route:\n%s", tc.s, out)
			}
		}
	}
}
