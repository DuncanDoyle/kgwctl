package resolve_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/DuncanDoyle/kgateway-ui/pkg/kinds"

	"github.com/DuncanDoyle/kgwctl/internal/resolve"
)

// servedAll serves every upstream kind at its first version, except the kinds in drop.
func servedAll(drop ...string) []kinds.Served {
	var out []kinds.Served
	for _, d := range kinds.Upstream() {
		if !slices.Contains(drop, d.Kind) {
			out = append(out, kinds.Served{Descriptor: d, Version: d.Versions[0]})
		}
	}
	return out
}

// target is a Target as labels: its title and its kinds.
type target struct {
	label string
	kinds []string
}

func flatten(ts []resolve.Target) []target {
	var out []target
	for _, t := range ts {
		ft := target{label: t.Label}
		for _, k := range t.Kinds {
			ft.kinds = append(ft.kinds, k.Kind)
		}
		out = append(out, ft)
	}
	return out
}

func TestResolve(t *testing.T) {
	var allButClass []target
	for _, k := range []string{"Gateway", "HTTPRoute", "GRPCRoute", "TCPRoute", "TLSRoute", "BackendTLSPolicy", "TrafficPolicy",
		"ListenerPolicy", "HTTPListenerPolicy", "BackendConfigPolicy", "GatewayParameters", "Backend"} {
		allButClass = append(allButClass, target{label: k, kinds: []string{k}})
	}
	// ListenerSet kinds share one table, at the position of the first.
	allButClass = slices.Insert(allButClass, 1, target{label: "ListenerSet", kinds: []string{"ListenerSet", "XListenerSet"}})

	tests := []struct {
		arg     string
		served  []kinds.Served
		want    []target
		unknown string // UnknownKindError name
		notSrv  string // NotServedError name
	}{
		{arg: "gw", want: []target{{"Gateway", []string{"Gateway"}}}},
		{arg: "Gateway", want: []target{{"Gateway", []string{"Gateway"}}}},
		{arg: "GATEWAYS", want: []target{{"Gateway", []string{"Gateway"}}}},
		{arg: "gwc", want: []target{{"GatewayClass", []string{"GatewayClass"}}}},
		{arg: "ls", want: []target{{"ListenerSet", []string{"ListenerSet", "XListenerSet"}}}},
		{arg: "listenerset", want: []target{{"ListenerSet", []string{"ListenerSet", "XListenerSet"}}}},
		{arg: "listenersets", want: []target{{"ListenerSet", []string{"ListenerSet", "XListenerSet"}}}},
		{arg: "ls", served: servedAll("XListenerSet"), want: []target{{"ListenerSet", []string{"ListenerSet"}}}},
		{arg: "xlistenersets", want: []target{{"XListenerSet", []string{"XListenerSet"}}}},
		{arg: "listenersets.gateway.networking.k8s.io", want: []target{{"ListenerSet", []string{"ListenerSet"}}}},
		{arg: "httproutes.gateway.networking.k8s.io", want: []target{{"HTTPRoute", []string{"HTTPRoute"}}}},
		{arg: "HTTPRoute.gateway.networking.k8s.io", want: []target{{"HTTPRoute", []string{"HTTPRoute"}}}},
		{arg: "gateways,hr", want: []target{{"Gateway", []string{"Gateway"}}, {"HTTPRoute", []string{"HTTPRoute"}}}},
		{arg: "gateways,gw", want: []target{{"Gateway", []string{"Gateway"}}}},
		{arg: "tr,tlsr,tp,lp,bcp,be,gwp,btlsp,gr", want: []target{
			{"TCPRoute", []string{"TCPRoute"}}, {"TLSRoute", []string{"TLSRoute"}}, {"TrafficPolicy", []string{"TrafficPolicy"}},
			{"ListenerPolicy", []string{"ListenerPolicy"}}, {"BackendConfigPolicy", []string{"BackendConfigPolicy"}},
			{"Backend", []string{"Backend"}}, {"GatewayParameters", []string{"GatewayParameters"}},
			{"BackendTLSPolicy", []string{"BackendTLSPolicy"}}, {"GRPCRoute", []string{"GRPCRoute"}},
		}},
		{arg: "all", want: allButClass},
		{arg: "directresponses", unknown: "directresponses"},
		{arg: "gw,bogus", unknown: "bogus"},
		{arg: "httproutes.example.com", unknown: "httproutes.example.com"},
		{arg: "tcproutes", served: servedAll("TCPRoute"), notSrv: "tcproutes"},
		{arg: "ls", served: servedAll("ListenerSet", "XListenerSet"), notSrv: "ls"},
	}
	for _, tc := range tests {
		t.Run(tc.arg, func(t *testing.T) {
			served := tc.served
			if served == nil {
				served = servedAll()
			}
			got, err := resolve.Kinds(served, kinds.Upstream(), tc.arg)
			var uk *resolve.UnknownKindError
			var ns *resolve.NotServedError
			switch {
			case tc.unknown != "":
				if !errors.As(err, &uk) || uk.Name != tc.unknown {
					t.Fatalf("err = %v, want UnknownKindError{%q}", err, tc.unknown)
				}
				return
			case tc.notSrv != "":
				if !errors.As(err, &ns) || ns.Name != tc.notSrv {
					t.Fatalf("err = %v, want NotServedError{%q}", err, tc.notSrv)
				}
				return
			case err != nil:
				t.Fatalf("Kinds: %v", err)
			}
			if f := flatten(got); !slices.EqualFunc(f, tc.want, func(a, b target) bool {
				return a.label == b.label && slices.Equal(a.kinds, b.kinds)
			}) {
				t.Fatalf("got %v, want %v", f, tc.want)
			}
		})
	}
}

func TestResolveDownstreamKind(t *testing.T) {
	d := kinds.Descriptor{Group: "example.com", Kind: "WidgetPolicy", Resource: "widgetpolicies", Versions: []string{"v1"},
		Namespaced: true, Category: kinds.CategoryPolicy}
	served := append(servedAll(), kinds.Served{Descriptor: d, Version: "v1"})
	modelled := append(kinds.Upstream(), d)
	for _, arg := range []string{"widgetpolicies", "widgetpolicy", "WidgetPolicy", "widgetpolicies.example.com"} {
		got, err := resolve.Kinds(served, modelled, arg)
		if err != nil || len(got) != 1 || got[0].Label != "WidgetPolicy" {
			t.Errorf("Kinds(%q) = %v, %v; want one WidgetPolicy target", arg, flatten(got), err)
		}
	}
}
