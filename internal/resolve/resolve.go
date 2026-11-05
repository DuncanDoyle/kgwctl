// Package resolve maps the kind argument of a command to the served kinds it
// names (design §3).
package resolve

import (
	"errors"
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
	// Alias marks the ListenerSet alias table, however many of its kinds are served.
	Alias bool
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

// Kinds resolves arg, comma-separated kind names as design §3 defines them,
// to one Target per name, in order. A kind is in at most one Target: names
// that overlap merge into the first, which takes the alias label if either is the alias.
func Kinds(served []kinds.Served, modelled []kinds.Descriptor, arg string) ([]Target, error) {
	modelled = kinds.Dedupe(modelled)
	var out []Target
	named := false
	add := func(t Target) { out = merge(out, served, t) }
	for _, name := range strings.Split(arg, ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		switch name {
		case "":
			continue
		}
		named = true
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
			t.Label, t.Alias = aliasLabel, true
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
	if !named {
		return nil, errors.New("no kind given")
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
				out = append(out, Target{Label: aliasLabel, Alias: true})
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

// merge adds t to ts, folding every target that shares a kind with t into the
// first of them; merged kinds keep served order.
func merge(ts []Target, served []kinds.Served, t Target) []Target {
	first := -1
	for i := 0; i < len(ts); i++ {
		if !slices.ContainsFunc(ts[i].Kinds, func(k kinds.Served) bool { return hasKind(t.Kinds, k) }) {
			continue
		}
		if first < 0 {
			first = i
			continue
		}
		t = union(served, t, ts[i])
		ts = slices.Delete(ts, i, i+1)
		i--
	}
	if first < 0 {
		return append(ts, t)
	}
	ts[first] = union(served, ts[first], t)
	return ts
}

// union is a and b in one target, labelled as the alias if either is.
func union(served []kinds.Served, a, b Target) Target {
	u := Target{Label: a.Label, Alias: a.Alias || b.Alias}
	if u.Alias {
		u.Label = aliasLabel
	}
	for _, s := range served {
		if hasKind(a.Kinds, s) || hasKind(b.Kinds, s) {
			u.Kinds = append(u.Kinds, s)
		}
	}
	return u
}

func hasKind(ks []kinds.Served, k kinds.Served) bool {
	return slices.ContainsFunc(ks, func(x kinds.Served) bool { return x.Group == k.Group && x.Kind == k.Kind })
}
