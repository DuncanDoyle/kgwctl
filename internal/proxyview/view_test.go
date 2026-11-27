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
