package proxyview_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/DuncanDoyle/xdsview/pkg/adapter/kgateway"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/model"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/provenance"
	"k8s.io/apimachinery/pkg/types"

	"github.com/DuncanDoyle/kgwctl/internal/proxyview"
	"github.com/DuncanDoyle/kgwctl/internal/proxyview/proxyviewtest"
)

// seamCase is one dump and the inputs proxy routes would pass with it.
type seamCase struct {
	name  string
	body  []byte
	gw    *types.NamespacedName
	known []model.Ref
	sel   proxyview.Selection
}

// seamCases are the golden cases of proxy routes: every recorded and
// hand-built dump, with and without --route, --rule and Known.
func seamCases(t *testing.T) []seamCase {
	t.Helper()
	one := 1
	var out []seamCase
	for _, d := range []struct {
		name  string
		body  []byte
		gw    *types.NamespacedName
		route types.NamespacedName
		known []model.Ref
	}{
		{"demo", golden(t, "demo.json"), demoGW, types.NamespacedName{Namespace: "team-a", Name: "team-a"}, nil},
		{"policies", golden(t, "policies.json"), proxyGW, orders, refs(orders)},
		{"collide-meta", golden(t, "collide-meta.json"), proxyGW, orders, refs(orders, ordersKgwctl)},
		{"levels", handBuilt(t, "levels.json"), nil, shopOrders, nil},
		{"shared-source", handBuilt(t, "shared-source.json"), nil, orders, nil},
		{"ecds-network", handBuilt(t, "ecds-network.json"), nil, orders, nil},
		{"ecds-ambiguous", handBuilt(t, "ecds-ambiguous.json"), nil, orders, nil},
		{"downstream-field", handBuilt(t, "downstream-field.json"), nil, shopOrders, nil},
	} {
		out = append(out,
			seamCase{name: d.name, body: d.body, gw: d.gw},
			seamCase{name: d.name + "/route", body: d.body, gw: d.gw, sel: proxyview.Selection{Route: d.route}},
			seamCase{name: d.name + "/rule-1", body: d.body, sel: proxyview.Selection{Route: d.route, Rule: &one}})
		if d.known != nil {
			out = append(out,
				seamCase{name: d.name + "/known", body: d.body, gw: d.gw, known: d.known},
				seamCase{name: d.name + "/known/route", body: d.body, gw: d.gw, known: d.known, sel: proxyview.Selection{Route: d.route}})
		}
	}
	return out
}

// selectedIDs is what a view selected and chose, as route IDs only.
func selectedIDs(v proxyview.View) [][]model.ID {
	var out [][]model.ID
	if s := v.Selected; s != nil {
		out = append(out, s.Routes, s.Ambiguous, s.Unresolved, narrowedRoutes(s.Narrowed))
	}
	return append(out, narrowedRoutes(v.Chosen), narrowedRoutes(v.Narrowed), v.Inferred)
}

func narrowedRoutes(ns []provenance.Narrowing) []model.ID {
	var out []model.ID
	for _, n := range ns {
		out = append(out, n.Route)
	}
	return out
}

// TestWrappingModuleSameSelection: R11 and R25, a wrapper that adds no claim
// selects and chooses the same routes and gives byte-equal text and JSON.
func TestWrappingModuleSameSelection(t *testing.T) {
	wrapped := []provenance.Module{proxyviewtest.Wrap("example", kgateway.New(), nil)}
	var matched, chosen int
	defer func() {
		// The cases must exercise both a match and a known choice.
		if matched == 0 || chosen == 0 {
			t.Errorf("cases matched %d and chose %d times; want both", matched, chosen)
		}
	}()
	for _, tc := range seamCases(t) {
		for _, s := range strategies {
			if s == proxyview.StrategyKnown && tc.known == nil {
				continue // known needs the cluster's HTTPRoutes
			}
			o := proxyview.Options{Gateway: tc.gw, Known: tc.known, Selection: tc.sel, Strategy: s}
			def := build(t, tc.body, o)
			o.Modules = wrapped
			w := build(t, tc.body, o)
			matched += len(def.Routes())
			chosen += len(def.Chosen)
			if !reflect.DeepEqual(selectedIDs(def), selectedIDs(w)) {
				t.Errorf("%s %s: selection %v, want %v", tc.name, s, selectedIDs(w), selectedIDs(def))
			}
			for _, ro := range []bool{false, true} {
				if got, want := text(t, w, proxyview.TextOptions{ResponseOrder: ro}), text(t, def, proxyview.TextOptions{ResponseOrder: ro}); got != want {
					t.Errorf("%s %s: text differs:\n%s\nwant:\n%s", tc.name, s, got, want)
				}
			}
			if got, want := marshal(t, w.Payload(proxyview.Source{})), marshal(t, def.Payload(proxyview.Source{})); got != want {
				t.Errorf("%s %s: JSON differs", tc.name, s)
			}
		}
	}
}

// addRecord adds a PolicyRecord on the first route, with metadata evidence naming it.
func addRecord(snap *model.Snapshot, _ []provenance.Claim) []provenance.Claim {
	r := snap.Listeners[0].FilterChains[0].HCM.RouteConfig.VirtualHosts[0].Routes[0]
	pol := model.Ref{Group: "example.com", Kind: "ExamplePolicy", Namespace: "shop", Name: "extra"}
	return []provenance.Claim{{Ref: "extra", Subject: r.ID, Certainty: model.Exact,
		Assertion: provenance.PolicyRecord{Policy: pol, Field: "extra", Level: provenance.LevelRoute},
		Evidence:  []provenance.Evidence{{Kind: provenance.EvidenceMetadata, Subject: r.ID, Field: "metadata.filter_metadata.example", Object: &pol}}}}
}

// TestWrappingModuleAddsRecord: R12, a record the wrapper adds is in the
// route's policies and on the policies: line; selection is unchanged.
func TestWrappingModuleAddsRecord(t *testing.T) {
	body := handBuilt(t, "levels.json")
	o := proxyview.Options{Selection: proxyview.Selection{Route: shopOrders}}
	def := build(t, body, o)
	o.Modules = []provenance.Module{proxyviewtest.Wrap("example", kgateway.New(), addRecord)}
	v := build(t, body, o)
	if !reflect.DeepEqual(selectedIDs(def), selectedIDs(v)) {
		t.Errorf("selection %v, want %v", selectedIDs(v), selectedIDs(def))
	}
	route := v.Payload(proxyview.Source{}).Listeners[0].FilterChains[0].HCM.VirtualHosts[0].Routes[0]
	want := proxyview.Policy{Policy: proxyview.Ref{Group: "example.com", Kind: "ExamplePolicy", Namespace: "shop", Name: "extra"}, Field: "extra", Level: "route", Certainty: "exact"}
	if n := len(route.Policies); n != 2 || route.Policies[1] != want {
		t.Errorf("route policies %+v, want route-p then %+v", route.Policies, want)
	}
	if out := text(t, v, proxyview.TextOptions{}); !strings.Contains(out, "policies: TrafficPolicy shop/route-p (cors), ExamplePolicy shop/extra (extra)\n") {
		t.Errorf("policies: line lacks the added record:\n%s", out)
	}
}

// TestWrappingModuleDroppedClaimSameOutput: R12b, one invalid claim under two
// wrapper names gives byte-equal text and JSON, with kgwctl's message.
func TestWrappingModuleDroppedClaimSameOutput(t *testing.T) {
	body := handBuilt(t, "levels.json")
	out := map[string][2]string{}
	for _, name := range []string{"example", "other"} {
		v := build(t, body, proxyview.Options{Modules: []provenance.Module{proxyviewtest.Wrap(name, kgateway.New(), noEvidence(1))}})
		p := v.Payload(proxyview.Source{})
		if len(p.Warnings) != 1 || p.Warnings[0].Code != model.WarnClaimDropped {
			t.Fatalf("%s: warnings %+v, want one claim-dropped", name, p.Warnings)
		}
		out[name] = [2]string{text(t, v, proxyview.TextOptions{}), marshal(t, p)}
		if claimID.MatchString(out[name][1]) {
			t.Errorf("%s: JSON holds a claim ID:\n%s", name, out[name][1])
		}
	}
	// Byte-equal output under two names holds no module name.
	if out["example"] != out["other"] {
		t.Errorf("output differs between wrapper names:\n%s\nvs\n%s", out["example"], out["other"])
	}
}
