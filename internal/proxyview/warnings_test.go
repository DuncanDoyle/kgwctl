package proxyview_test

import (
	"strings"
	"testing"

	"github.com/DuncanDoyle/xdsview/pkg/adapter/kgateway"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/model"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/provenance"
	"k8s.io/apimachinery/pkg/types"

	"github.com/DuncanDoyle/kgwctl/internal/proxyview"
	"github.com/DuncanDoyle/kgwctl/internal/proxyview/proxyviewtest"
)

// shopOrders is the HTTPRoute of the hand-built dumps' first route.
var shopOrders = types.NamespacedName{Namespace: "shop", Name: "orders"}

// warningLines returns the "warning:" lines of out and checks they are its last lines.
func warningLines(t *testing.T, out string) []string {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	var got []string
	for i, l := range lines {
		if strings.Contains(l, "warning:") {
			if !strings.HasPrefix(l, "warning: ") || i < len(lines)-countWarnings(lines) {
				t.Errorf("warning line %d %q is not in the block at the end:\n%s", i, l, out)
			}
			got = append(got, l)
		}
	}
	return got
}

func countWarnings(lines []string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, "warning:") {
			n++
		}
	}
	return n
}

// ecdsWarnings returns the ecds-ambiguous entries of ws.
func ecdsWarnings(ws []proxyview.Warning) []proxyview.Warning {
	var out []proxyview.Warning
	for _, w := range ws {
		if w.Code == model.WarnECDSAmbiguous {
			out = append(out, w)
		}
	}
	return out
}

// TestWarningsPrunedHCMSlot: --route keeps the listener but no route of the
// second HCM; its ECDS slot warning is once in JSON and once in the text block.
func TestWarningsPrunedHCMSlot(t *testing.T) {
	v := build(t, handBuilt(t, "ecds-network.json"), proxyview.Options{Selection: proxyview.Selection{Route: shopOrders}})
	if v.Matched() != 1 {
		t.Fatalf("matched %d, want the orders route", v.Matched())
	}
	ws := ecdsWarnings(v.Payload(proxyview.Source{}).Warnings)
	if len(ws) != 1 || ws[0].Subject != "L0/C1/N0/F0" {
		t.Fatalf("warnings %+v, want one on the second HCM's slot", ws)
	}
	lines := warningLines(t, text(t, v, proxyview.TextOptions{}))
	if len(lines) != 1 || !strings.Contains(lines[0], ws[0].Message) || !strings.HasSuffix(lines[0], "(filter ecds/x in listener listener~8080)") {
		t.Errorf("warning lines %q, want the slot warning once with its subject", lines)
	}
}

// TestWarningsSlotUnderTwoRoutes: a slot warning of an HCM with two routes
// prints once, not under each route.
func TestWarningsSlotUnderTwoRoutes(t *testing.T) {
	v := build(t, handBuilt(t, "ecds-network.json"), proxyview.Options{})
	if ws := ecdsWarnings(v.Payload(proxyview.Source{}).Warnings); len(ws) != 1 {
		t.Errorf("warnings %+v, want one", ws)
	}
	out := text(t, v, proxyview.TextOptions{})
	if strings.Count(out, "route /") != 3 {
		t.Fatalf("tree has %d routes, want 3:\n%s", strings.Count(out, "route /"), out)
	}
	if lines := warningLines(t, out); len(lines) != 1 {
		t.Errorf("warning lines %q, want one", lines)
	}
}

// TestWarningsPrunedSubject: a parser warning whose subject --route pruned
// keeps its subject in JSON and its line in the text.
func TestWarningsPrunedSubject(t *testing.T) {
	v := build(t, handBuilt(t, "ecds-ambiguous.json"), proxyview.Options{Selection: proxyview.Selection{Route: types.NamespacedName{Namespace: "shop", Name: "nope"}}})
	if len(v.Snapshot.Listeners) != 0 {
		t.Fatalf("listeners %v, want all pruned", v.Snapshot.Listeners)
	}
	ws := v.Payload(proxyview.Source{}).Warnings
	if len(ws) != 2 || ws[0].Subject != "L0/C0/N0/F0" || ws[1].Subject != "L0/C0/N0/F1" {
		t.Fatalf("warnings %+v, want both slot warnings with their subjects", ws)
	}
	if lines := warningLines(t, text(t, v, proxyview.TextOptions{})); len(lines) != 2 {
		t.Errorf("warning lines %q, want two", lines)
	}
}

// noEvidence adds, on the first route, a claim without evidence, which
// Compose drops; n copies of it give n drops.
func noEvidence(n int) func(*model.Snapshot, []provenance.Claim) []provenance.Claim {
	return func(snap *model.Snapshot, _ []provenance.Claim) []provenance.Claim {
		r := snap.Listeners[0].FilterChains[0].HCM.RouteConfig.VirtualHosts[0].Routes[0]
		var out []provenance.Claim
		for range n {
			out = append(out, provenance.Claim{Subject: r.ID, Assertion: provenance.HostSource{Hostname: "x"}, Certainty: model.Inferred})
		}
		return out
	}
}

// TestWarningsDroppedClaimMessage: a dropped claim gets kgwctl's message and
// the subject's description; no claim ID and no module name (DD-10).
func TestWarningsDroppedClaimMessage(t *testing.T) {
	for _, name := range []string{"wrapmod", "downmod"} {
		v := build(t, handBuilt(t, "levels.json"), proxyview.Options{Modules: []provenance.Module{proxyviewtest.Wrap(name, kgateway.New(), noEvidence(1))}})
		ws := v.Payload(proxyview.Source{}).Warnings
		const want = "a provenance claim about route listener~8080~orders_example-route-0-httproute-orders-shop-0-0-matcher-0 in virtual host listener~8080~orders_example broke a composition rule and was dropped"
		if len(ws) != 1 || ws[0].Code != model.WarnClaimDropped || ws[0].Message != want {
			t.Fatalf("%s: warnings %+v, want one claim-dropped with %q", name, ws, want)
		}
		out := text(t, v, proxyview.TextOptions{})
		for _, s := range []string{ws[0].Message, marshal(t, ws), out} {
			if strings.Contains(s, name) { // a claim ID embeds the module name
				t.Errorf("%s: output names the module or a claim ID: %s", name, s)
			}
		}
		if lines := warningLines(t, out); len(lines) != 1 || !strings.HasPrefix(lines[0], "warning: "+want) {
			t.Errorf("%s: warning lines %q", name, lines)
		}
	}
}

// TestWarningsDedup: two drops of one code on one subject give one entry.
func TestWarningsDedup(t *testing.T) {
	v := build(t, handBuilt(t, "levels.json"), proxyview.Options{Modules: []provenance.Module{proxyviewtest.Wrap("example", kgateway.New(), noEvidence(2))}})
	if got := len(v.Index.Warnings()); got != 2 {
		t.Fatalf("index has %d warnings, want 2 drops", got)
	}
	if ws := v.Payload(proxyview.Source{}).Warnings; len(ws) != 1 {
		t.Errorf("warnings %+v, want one", ws)
	}
}
