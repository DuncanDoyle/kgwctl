// Package proxyview turns an Envoy config dump into the view `proxy routes`
// prints: xdsview's snapshot, effective chains and claim index, the routes
// selected for one HTTPRoute, and one list of warnings.
package proxyview

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/DuncanDoyle/xdsview/pkg/adapter/kgateway"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/chain"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/model"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/parser"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/provenance"
	"k8s.io/apimachinery/pkg/types"
)

const gatewayGroup = "gateway.networking.k8s.io"

// OneCandidateNote is printed next to an ambiguous route that only the
// HTTPRoutes in the cluster narrowed to one reading.
const OneCandidateNote = "one candidate exists in the cluster"

// GatewayContextNote marks the Gateway kgwctl was given, as opposed to one a claim names.
const GatewayContextNote = "(from the command line)"

// ErrNotConfigDump is Build's error for valid JSON without a configs array.
var ErrNotConfigDump = errors.New("not an Envoy config dump (no configs array)")

// ErrCompose wraps the error of provenance.Compose, such as two modules with one name.
var ErrCompose = errors.New("compose provenance modules")

// Strategy is the route selection rule of --select (design DD-4).
type Strategy string

// Selection strategies.
const (
	StrategyExact  Strategy = "exact"  // a route kgateway.Select selects whose reading is exact
	StrategyStrict Strategy = "strict" // a match is a route kgateway.Select selects, nothing else
	StrategyKnown  Strategy = "known"  // also a route Known narrowed to one matching reading
)

// ParseStrategy parses a --select value: empty is StrategyStrict, and any
// value other than "exact", "strict" or "known" is an error.
func ParseStrategy(s string) (Strategy, error) {
	switch st := Strategy(s); st {
	case "":
		return StrategyStrict, nil
	case StrategyExact, StrategyStrict, StrategyKnown:
		return st, nil
	}
	return "", fmt.Errorf("unknown selection strategy %q: want exact, strict or known", s)
}

// notExact returns the routes whose one reading is not exact; with rule, also
// those whose rule or match index is unknown or not exact (DD-4). It reads certainty only.
func notExact(ix *provenance.Index, routes []model.ID, rule bool) []model.ID {
	var out []model.ID
	for _, id := range routes {
		res := provenance.Resolve[provenance.RouteSource](ix, id)
		ok := res.Certainty == model.Exact && len(res.Candidates) == 1
		if ok && rule {
			// Unset means equal to the reading's certainty: exact.
			r := effective(res)[0]
			ok = (r.IndexCertainty == model.Unknown || r.IndexCertainty == model.Exact) && r.RuleIndex >= 0 && r.MatchIndex >= 0
		}
		if !ok {
			out = append(out, id)
		}
	}
	return out
}

// effective returns res's readings, each with its IndexCertainty replaced by
// indexCertainty. Selection, text and JSON all read these (DD-4).
func effective(res provenance.Resolution[provenance.RouteSource]) []provenance.RouteSource {
	top := topCertainty(res)
	out := slices.Clone(res.Candidates)
	for i, r := range out {
		out[i].IndexCertainty = indexCertainty(res.Claims, r, top)
	}
	return out
}

// indexCertainty is the index certainty of reading r over the claims at
// certainty c: the highest, where unset means the claim's own certainty. It
// stays unset only when that highest value comes from unset claims alone, so
// an explicit marker survives; Resolve's merge prefers unset on a tie (DD-4).
func indexCertainty(claims []provenance.Claim, r provenance.RouteSource, c model.Certainty) model.Certainty {
	ic, explicit := model.Unknown, false
	for _, cl := range claims {
		a, ok := cl.Assertion.(provenance.RouteSource)
		if !ok || cl.Certainty != c || !sameReading(a, r) {
			continue
		}
		e, set := a.IndexCertainty, a.IndexCertainty != model.Unknown
		if !set {
			e = cl.Certainty
		}
		switch {
		case e > ic:
			ic, explicit = e, set
		case e == ic:
			explicit = explicit || set
		}
	}
	if ic == c && !explicit {
		return model.Unknown
	}
	return ic
}

// chooseKnown returns the narrowings of sel whose kept claims name one
// reading, compared as provenance.Resolve merges them, that matches q, each
// normalised (DD-4, DD-6). It is krp's rule over kgateway.Select's result.
func chooseKnown(q kgateway.Query, sel kgateway.Selection) []provenance.Narrowing {
	var out []provenance.Narrowing
	for _, n := range sel.Narrowed {
		if rs := readings(n.Kept); len(rs) == 1 && matches(q, rs[0]) {
			out = append(out, normalise(n))
		}
	}
	return out
}

// matches reports whether reading r names the HTTPRoute and rule of q.
func matches(q kgateway.Query, r provenance.RouteSource) bool {
	return r.Route == q.Route && (q.RuleIndex == nil || r.RuleIndex == *q.RuleIndex) && (q.RuleName == "" || r.RuleName == q.RuleName)
}

// Selection narrows a view to one HTTPRoute.
type Selection struct {
	Route types.NamespacedName // empty Name: no narrowing
	Rule  *int                 // one rule index; nil: every rule
}

// KnownInput says whether the cluster's HTTPRoutes were an input (R24).
type KnownInput string

// Known inputs.
const (
	KnownListed      KnownInput = "listed"      // live: the HTTPRoute list succeeded
	KnownUnavailable KnownInput = "unavailable" // live: the list failed and the command went on without it
	KnownNotRead     KnownInput = "notRead"     // --file: kgwctl calls no Kubernetes API
)

// Options are Build's inputs besides the dump.
type Options struct {
	Gateway *types.NamespacedName // the Gateway kgwctl was given; labels listener rows
	Known   []model.Ref           // the cluster's HTTPRoutes (see HTTPRouteRef); empty with --file
	// KnownInput says how Known was obtained; empty is KnownListed with Known, else KnownNotRead.
	KnownInput KnownInput
	Capture    model.Capture // model.CaptureComplete live; empty with --file
	Selection  Selection
	Strategy   Strategy            // empty is StrategyStrict
	Modules    []provenance.Module // nil is DefaultModules()
}

// DefaultModules returns the provenance modules of a kgwctl build: the kgateway module.
func DefaultModules() []provenance.Module { return []provenance.Module{kgateway.New()} }

// View is a parsed dump with its chains, claim index and selection, all
// computed on the full snapshot; Snapshot is pruned to the selection under
// --route. Snapshots share data with each other; treat them as read-only.
type View struct {
	Full, Snapshot *model.Snapshot
	Chains         map[model.ID]model.EffectiveChain
	Index          *provenance.Index
	Narrowed       []provenance.Narrowing // what Known narrowed: Select's under --route, else Narrow's
	Selected       *kgateway.Selection    // nil without --route
	Chosen         []provenance.Narrowing // normalised narrowings of the routes --select known chose (DD-6)
	Inferred       []model.ID             // Select's routes --select exact left unmatched: a reading below exact
	Strategy       Strategy
	Selection      Selection
	KnownInput     KnownInput // never empty after Build
	Gateway        *types.NamespacedName
	Warnings       []Warning // DD-12: parser, then index warnings, once each
}

// HTTPRouteRef returns the ref xdsview uses for an HTTPRoute, for Options.Known.
func HTTPRouteRef(n types.NamespacedName) model.Ref {
	return model.Ref{Group: gatewayGroup, Kind: "HTTPRoute", Namespace: n.Namespace, Name: n.Name}
}

// Build parses body and runs xdsview's pipeline (design §4.1): chain.Effective,
// provenance.Compose, kgateway.Select under --route or kgateway.Narrow with
// Known, then model.Prune under --route to the matched routes (see Routes).
// Valid JSON without a configs array is ErrNotConfigDump; a Compose error
// wraps ErrCompose.
func Build(body []byte, o Options) (View, error) {
	sel := o.Selection
	if sel.Rule != nil && *sel.Rule < 0 {
		return View{}, errors.New("rule index must not be negative")
	}
	strategy, err := ParseStrategy(string(o.Strategy))
	if err != nil {
		return View{}, err
	}
	if err := checkShape(body); err != nil {
		return View{}, err
	}
	full, err := parser.Parse(bytes.NewReader(body), parser.Options{Capture: o.Capture})
	if err != nil {
		return View{}, err
	}
	modules := o.Modules
	if modules == nil {
		modules = DefaultModules()
	}
	ix, err := provenance.Compose(full, modules...)
	if err != nil {
		return View{}, fmt.Errorf("%w: %w", ErrCompose, err)
	}
	v := View{Full: full, Snapshot: full, Chains: chain.Effective(full), Index: ix, Strategy: strategy,
		Selection: sel, KnownInput: o.KnownInput, Gateway: o.Gateway, Warnings: warningList(full, ix)}
	switch {
	case v.KnownInput != "":
	case len(o.Known) > 0:
		v.KnownInput = KnownListed
	default:
		v.KnownInput = KnownNotRead
	}
	if sel.Route.Name == "" {
		if len(o.Known) > 0 {
			v.Narrowed = kgateway.Narrow(ix, o.Known)
		}
		return v, nil
	}
	q := kgateway.Query{Route: HTTPRouteRef(sel.Route), RuleIndex: sel.Rule, Known: o.Known}
	s := kgateway.Select(ix, full, q)
	v.Selected, v.Narrowed = &s, s.Narrowed
	switch strategy {
	case StrategyKnown:
		v.Chosen = chooseKnown(q, s)
	case StrategyExact:
		v.Inferred = notExact(ix, s.Routes, sel.Rule != nil)
	}
	keep := map[model.ID]bool{}
	for _, id := range v.Routes() {
		keep[id] = true
	}
	v.Snapshot = model.Prune(full, func(id model.ID) bool { return keep[id] })
	return v, nil
}

// Routes returns the matched routes: Select's routes but the inferred ones,
// then the chosen ones. It is empty without --route.
func (v View) Routes() []model.ID {
	if v.Selected == nil {
		return nil
	}
	var out []model.ID
	for _, id := range v.Selected.Routes {
		if !slices.Contains(v.Inferred, id) {
			out = append(out, id)
		}
	}
	for _, n := range v.Chosen {
		out = append(out, n.Route)
	}
	return out
}

// Matched returns the number of routes in Snapshot, counted once per filter chain.
func (v View) Matched() int {
	n := 0
	walk(v.Snapshot, func(model.HCMConfig, model.VirtualHost, model.Route) { n++ })
	return n
}

// checkShape rejects valid JSON that is not an object with a configs array.
// Invalid JSON is left to the parser's error; the parser reads the rest (D-022).
func checkShape(body []byte) error {
	if !json.Valid(body) {
		return nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil || top == nil {
		return ErrNotConfigDump
	}
	if c := bytes.TrimSpace(top["configs"]); len(c) == 0 || c[0] != '[' {
		return ErrNotConfigDump
	}
	return nil
}

// normalise returns n with its kept claims merged into one, as
// provenance.Resolve merges agreeing readings: Incomplete if any is,
// Exhaustive only if all are, and the index certainty of effective (DD-4, DD-6).
func normalise(n provenance.Narrowing) provenance.Narrowing {
	if len(n.Kept) == 0 {
		return n
	}
	c := n.Kept[0]
	merged, ok := c.Assertion.(provenance.RouteSource)
	if !ok {
		return provenance.Narrowing{Route: n.Route, Kept: []provenance.Claim{c}}
	}
	top := model.Unknown
	for _, k := range n.Kept {
		top = max(top, k.Certainty)
	}
	for _, k := range n.Kept[1:] {
		if r, ok := k.Assertion.(provenance.RouteSource); ok {
			merged.Incomplete = merged.Incomplete || r.Incomplete
			merged.Exhaustive = merged.Exhaustive && r.Exhaustive
		}
	}
	merged.IndexCertainty = indexCertainty(n.Kept, merged, top)
	c.Assertion = merged
	return provenance.Narrowing{Route: n.Route, Kept: []provenance.Claim{c}}
}

// readings returns the distinct readings of claims, compared as
// provenance.Resolve compares them: HTTPRoute, rule index, rule name and match index.
func readings(claims []provenance.Claim) []provenance.RouteSource {
	var out []provenance.RouteSource
	for _, c := range claims {
		r, ok := c.Assertion.(provenance.RouteSource)
		if ok && !containsReading(out, r) {
			out = append(out, r)
		}
	}
	return out
}

func containsReading(list []provenance.RouteSource, r provenance.RouteSource) bool {
	for _, o := range list {
		if sameReading(o, r) {
			return true
		}
	}
	return false
}

func sameReading(a, b provenance.RouteSource) bool {
	return a.Route == b.Route && a.RuleIndex == b.RuleIndex && a.RuleName == b.RuleName && a.MatchIndex == b.MatchIndex
}

// narrowing returns the narrowing of route, if Known narrowed it.
func (v View) narrowing(route model.ID) (provenance.Narrowing, bool) {
	for _, n := range v.Narrowed {
		if n.Route == route {
			return n, true
		}
	}
	return provenance.Narrowing{}, false
}

// chosen reports whether --select known chose route.
func (v View) chosen(route model.ID) bool {
	for _, n := range v.Chosen {
		if n.Route == route {
			return true
		}
	}
	return false
}

// walk calls fn for every route in snap, in snapshot order.
func walk(snap *model.Snapshot, fn func(model.HCMConfig, model.VirtualHost, model.Route)) {
	for _, l := range snap.Listeners {
		for _, fc := range l.FilterChains {
			if fc.HCM == nil || fc.HCM.RouteConfig == nil {
				continue
			}
			for _, vh := range fc.HCM.RouteConfig.VirtualHosts {
				for _, r := range vh.Routes {
					fn(*fc.HCM, vh, r)
				}
			}
		}
	}
}
