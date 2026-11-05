// Package resolve maps the kind argument of a command to the served kinds it
// names (design §3).
package resolve

import (
	"fmt"
	"slices"
	"strings"

	"github.com/DuncanDoyle/kgateway-ui/pkg/kinds"
)

// Target is one table of output: the kinds it lists under one title.
type Target struct {
	// Label is the table title: the kind, or "ListenerSet" for the alias.
	Label string
	Kinds []kinds.Served
}

// UnknownKindError reports a name that matches no modelled kind.
type UnknownKindError struct{ Name string }

// Error implements error.
func (e *UnknownKindError) Error() string { return fmt.Sprintf("unknown kind %q", e.Name) }

// NotServedError reports a modelled kind this cluster does not serve.
type NotServedError struct{ Name string }

// Error implements error.
func (e *NotServedError) Error() string {
	return fmt.Sprintf("kind %q is not served by this cluster", e.Name)
}

const (
	gwGroup  = "gateway.networking.k8s.io"
	gwxGroup = "gateway.networking.x-k8s.io"
)

// groupKind keys the short names and the ListenerSet alias.
type groupKind struct{ group, kind string }

// shortNames are the short names of design §3.
var shortNames = map[string]groupKind{
	"gwc":   {gwGroup, "GatewayClass"},
	"gw":    {gwGroup, "Gateway"},
	"hr":    {gwGroup, "HTTPRoute"},
	"gr":    {gwGroup, "GRPCRoute"},
	"tr":    {gwGroup, "TCPRoute"},
	"tlsr":  {gwGroup, "TLSRoute"},
	"btlsp": {gwGroup, "BackendTLSPolicy"},
	"tp":    {kinds.KgatewayGroup, "TrafficPolicy"},
	"lp":    {kinds.KgatewayGroup, "ListenerPolicy"},
	"bcp":   {kinds.KgatewayGroup, "BackendConfigPolicy"},
	"gwp":   {kinds.KgatewayGroup, "GatewayParameters"},
	"be":    {kinds.KgatewayGroup, "Backend"},
}

// listenerSets are the kinds the ListenerSet alias lists in one table.
var listenerSets = []groupKind{{gwGroup, "ListenerSet"}, {gwxGroup, "XListenerSet"}}

// aliasLabel is the title of the ListenerSet alias table.
const aliasLabel = "ListenerSet"

// isAlias reports whether name is the ListenerSet alias.
func isAlias(name string) bool {
	return name == "listenersets" || name == "listenerset" || name == "ls"
}

// Kinds resolves arg, a comma-separated list of kind names, to one Target
// per name, in order and without duplicates. A name is a plural, singular,
// kind or short name, optionally qualified as "<name>.<group>", matched
// case-insensitively against modelled. "all" is every served kind except
// GatewayClass. A modelled kind the cluster does not serve is a NotServedError.
func Kinds(served []kinds.Served, modelled []kinds.Descriptor, arg string) ([]Target, error) {
	modelled = kinds.Dedupe(modelled)
	var out []Target
	add := func(t Target) {
		if !slices.ContainsFunc(out, func(o Target) bool { return o.Label == t.Label && sameKinds(o.Kinds, t.Kinds) }) {
			out = append(out, t)
		}
	}
	for _, name := range strings.Split(arg, ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "all" {
			for _, t := range all(served) {
				add(t)
			}
			continue
		}
		descs := match(modelled, name)
		if len(descs) == 0 {
			return nil, &UnknownKindError{Name: name}
		}
		t := Target{Label: descs[0].Kind}
		if isAlias(name) {
			t.Label = aliasLabel
		}
		for _, s := range served {
			if slices.ContainsFunc(descs, func(d kinds.Descriptor) bool { return d.Group == s.Group && d.Kind == s.Kind }) {
				t.Kinds = append(t.Kinds, s)
			}
		}
		if len(t.Kinds) == 0 {
			return nil, &NotServedError{Name: name}
		}
		add(t)
	}
	return out, nil
}

// all is one Target per served kind except GatewayClass, with the
// ListenerSet alias kinds in one Target at the position of the first.
func all(served []kinds.Served) []Target {
	var out []Target
	alias := -1
	for _, s := range served {
		switch {
		case s.Category == kinds.CategoryGatewayClass:
		case slices.Contains(listenerSets, groupKind{s.Group, s.Kind}):
			if alias < 0 {
				alias = len(out)
				out = append(out, Target{Label: aliasLabel})
			}
			out[alias].Kinds = append(out[alias].Kinds, s)
		default:
			out = append(out, Target{Label: s.Kind, Kinds: []kinds.Served{s}})
		}
	}
	return out
}

// match returns the modelled descriptors name selects; name is lower case.
func match(modelled []kinds.Descriptor, name string) []kinds.Descriptor {
	keep := func(f func(groupKind) bool) []kinds.Descriptor {
		return slices.DeleteFunc(slices.Clone(modelled), func(d kinds.Descriptor) bool { return !f(groupKind{d.Group, d.Kind}) })
	}
	if isAlias(name) {
		return keep(func(gk groupKind) bool { return slices.Contains(listenerSets, gk) })
	}
	if short, ok := shortNames[name]; ok {
		return keep(func(gk groupKind) bool { return gk == short })
	}
	for _, d := range modelled {
		if names(d, name) {
			return []kinds.Descriptor{d}
		}
	}
	return nil
}

// names reports whether name is d's plural, singular or kind, bare or
// qualified with d's group. The singular is the lower-case kind.
func names(d kinds.Descriptor, name string) bool {
	for _, n := range []string{strings.ToLower(d.Resource), strings.ToLower(d.Kind)} {
		if name == n || name == n+"."+strings.ToLower(d.Group) {
			return true
		}
	}
	return false
}

func sameKinds(a, b []kinds.Served) bool {
	return slices.EqualFunc(a, b, func(x, y kinds.Served) bool { return x.Group == y.Group && x.Kind == y.Kind })
}
