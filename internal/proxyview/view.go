// Package proxyview turns an Envoy config dump into the view `proxy routes`
// prints: the snapshot annotated with origins and effective filter chains,
// narrowed to one HTTPRoute, with the routes that could not be attributed.
package proxyview

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/DuncanDoyle/xdsview/pkg/envoy/chain"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/filter"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/model"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/origin"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/parser"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/render"
	"k8s.io/apimachinery/pkg/types"
)

const gatewayGroup = "gateway.networking.k8s.io"

// OneCandidateNote is printed next to an ambiguous route that only the
// HTTPRoutes in the cluster narrowed to one candidate.
const OneCandidateNote = "one candidate exists in the cluster"

// ErrNotConfigDump is Build's error for valid JSON without a configs array.
var ErrNotConfigDump = errors.New("not an Envoy config dump (no configs array)")

// Selection narrows a view to one HTTPRoute.
type Selection struct {
	Route types.NamespacedName // empty Name: no narrowing
	Rule  *int                 // one rule index; nil: every rule
}

// AmbiguousRoute is an Envoy route listed rather than matched: its name reads
// as the selected HTTPRoute and at least one other, or it has no reading but
// names the selected HTTPRoute (Unresolved).
type AmbiguousRoute struct {
	RouteConfig string              `json:"routeConfig"`
	Name        string              `json:"name"`
	Candidates  []model.RouteOrigin `json:"candidates"`
	// NarrowedByKnown is set when one candidate exists in the cluster.
	NarrowedByKnown bool `json:"narrowedByKnown,omitempty"`
	// Unresolved is set for a route whose name has no reading; it has no candidates.
	Unresolved bool `json:"unresolved,omitempty"`
}

// View is a parsed, annotated and narrowed config dump. Snapshot shares data
// with the parsed dump; treat it as read-only.
type View struct {
	Snapshot  *model.EnvoySnapshot `json:"snapshot"`
	Warnings  []string             `json:"warnings,omitempty"`  // sections the parser skipped
	Ambiguous []AmbiguousRoute     `json:"ambiguous,omitempty"` // set only with a Selection
	Selection Selection            `json:"-"`
}

// Source says where a config dump came from: a live replica (Gateway, Pod and
// CapturedAt) or a file (File, and Gateway when --gateway named it).
type Source struct {
	Gateway    *model.Ref `json:"gateway,omitempty"`
	Pod        *model.Ref `json:"pod,omitempty"`
	CapturedAt *time.Time `json:"capturedAt,omitempty"`
	File       string     `json:"file,omitempty"`
}

// Item is the `item` of the ProxyRoutes envelope: the source and the view.
type Item struct {
	Source Source `json:"source"`
	View
}

// HTTPRouteRef returns the ref xdsview uses for an HTTPRoute, for Build's known list.
func HTTPRouteRef(n types.NamespacedName) model.Ref {
	return model.Ref{Group: gatewayGroup, Kind: "HTTPRoute", Namespace: n.Namespace, Name: n.Name}
}

// Build parses body, annotates origins and filter chains, and narrows to sel;
// gw labels origins and known lists existing HTTPRoutes (see HTTPRouteRef).
// An origin that only known narrowed to one candidate stays ambiguous, since
// Envoy may still serve a deleted HTTPRoute's route.
// Valid JSON without a configs array is ErrNotConfigDump.
func Build(body []byte, gw *types.NamespacedName, known []model.Ref, sel Selection) (View, error) {
	if sel.Rule != nil && *sel.Rule < 0 {
		return View{}, errors.New("rule index must not be negative")
	}
	if err := checkShape(body); err != nil {
		return View{}, err
	}
	res, err := parser.Parse(bytes.NewReader(body))
	if err != nil {
		return View{}, err
	}
	var gwRef *model.Ref
	if gw != nil {
		gwRef = &model.Ref{Group: gatewayGroup, Kind: "Gateway", Namespace: gw.Namespace, Name: gw.Name}
	}
	m := origin.Kgateway(origin.KgatewayOptions{Gateway: gwRef, Known: known})
	nameOnly := origin.Kgateway(origin.KgatewayOptions{Gateway: gwRef})
	snap := demote(origin.Annotate(res.Snapshot, m), nameOnly)
	snap = chain.Annotate(snap, m)

	v := View{Snapshot: emptyListeners(snap), Warnings: res.Warnings, Selection: sel}
	if sel.Route.Name == "" {
		return v, nil
	}
	opts := filter.Options{HTTPRouteName: sel.Route.Name, HTTPRouteNamespace: sel.Route.Namespace, RuleIndex: filter.AllRules}
	if sel.Rule != nil {
		opts.RuleIndex = *sel.Rule
	}
	// filter.Filter matches a route without an origin by name substring; guard
	// keeps those out, and they are listed as unresolved instead.
	unresolved := map[[2]string]bool{}
	walk(filter.Filter(snap, opts), func(rc *model.RouteConfig, r *model.Route) {
		if !resolved(r.Origin) && sel.named(r.Name) {
			unresolved[[2]string{rc.Name, r.Name}] = true
		}
	})
	v.Snapshot = emptyListeners(filter.Filter(guard(snap), opts))

	// Ambiguity is judged on the name-only readings: Known may drop a reading
	// whose HTTPRoute was deleted but whose route Envoy still serves.
	// filter.Ambiguous keys by route name alone; two route configs can share one.
	seen := map[[2]string]bool{}
	walk(snap, func(rc *model.RouteConfig, r *model.Route) {
		key := [2]string{rc.Name, r.Name}
		if seen[key] {
			return
		}
		if unresolved[key] {
			seen[key] = true
			v.Ambiguous = append(v.Ambiguous, AmbiguousRoute{RouteConfig: rc.Name, Name: r.Name, Candidates: []model.RouteOrigin{}, Unresolved: true})
			return
		}
		if r.Origin != nil && r.Origin.Certainty == model.Exact {
			return
		}
		a := nameOnly.Route(*r)
		if a.Certainty != model.Ambiguous || !slices.ContainsFunc(a.Candidates, sel.matches) {
			return
		}
		seen[key] = true
		v.Ambiguous = append(v.Ambiguous, AmbiguousRoute{RouteConfig: rc.Name, Name: r.Name, Candidates: a.Candidates, NarrowedByKnown: r.Origin != nil && r.Origin.NarrowedByKnown})
	})
	return v, nil
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

// resolved reports whether a can drive a match (design §4.2): Exact, or
// Inferred from the route name alone.
func resolved(a *model.RouteAnnotation) bool {
	return a != nil && (a.Certainty == model.Exact || a.Certainty == model.Inferred && !a.NarrowedByKnown)
}

// guard gives every route whose origin is not resolved an empty Unknown
// origin, which filter.Filter never selects. It copies snap.
func guard(snap *model.EnvoySnapshot) *model.EnvoySnapshot {
	return rewriteRoutes(snap, func(r *model.Route) {
		if !resolved(r.Origin) {
			r.Origin = &model.RouteAnnotation{}
		}
	})
}

// emptyListeners gives a snapshot without listeners an empty slice, so JSON
// prints [] and not null. It copies a snapshot it changes.
func emptyListeners(s *model.EnvoySnapshot) *model.EnvoySnapshot {
	if s.Listeners != nil {
		return s
	}
	out := *s
	out.Listeners = []model.Listener{}
	return &out
}

// matches reports whether o reads as the selected HTTPRoute and rule.
func (s Selection) matches(o model.RouteOrigin) bool {
	return o.Route.Kind == "HTTPRoute" && o.Route.Namespace == s.Route.Namespace && o.Route.Name == s.Route.Name &&
		(s.Rule == nil || o.RuleIndex == *s.Rule)
}

// named reports whether a route name carries the selected HTTPRoute's marker
// right after a `-route-<i>-` segment, so a marker inside the vhost part doesn't count.
func (s Selection) named(name string) bool {
	marker := "-httproute-" + s.Route.Name + "-" + s.Route.Namespace + "-"
	for off := 0; ; {
		k := strings.Index(name[off:], marker)
		if k < 0 {
			return false
		}
		i := off + k
		j := strings.LastIndex(name[:i], "-route-")
		ruleOK := s.Rule == nil || strings.HasPrefix(name[i+len(marker):], fmt.Sprintf("%d-", *s.Rule))
		if j >= 0 && isDigits(name[j+len("-route-"):i]) && ruleOK {
			return true
		}
		off = i + 1
	}
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// demote replaces each origin narrowed by known with its name-only reading,
// keeping NarrowedByKnown. It copies what it changes; snap stays unchanged.
func demote(snap *model.EnvoySnapshot, nameOnly origin.Mapper) *model.EnvoySnapshot {
	narrowed := false
	walk(snap, func(_ *model.RouteConfig, r *model.Route) {
		narrowed = narrowed || (r.Origin != nil && r.Origin.NarrowedByKnown)
	})
	if !narrowed {
		return snap
	}
	return rewriteRoutes(snap, func(r *model.Route) {
		if r.Origin == nil || !r.Origin.NarrowedByKnown {
			return
		}
		a := nameOnly.Route(*r)
		a.NarrowedByKnown = true
		r.Origin = &a
	})
}

// rewriteRoutes returns a copy of snap down to its routes, with fn applied to
// each route; snap stays unchanged.
func rewriteRoutes(snap *model.EnvoySnapshot, fn func(*model.Route)) *model.EnvoySnapshot {
	out := &model.EnvoySnapshot{Listeners: make([]model.Listener, len(snap.Listeners))}
	for i, l := range snap.Listeners {
		l.FilterChains = append([]model.NetworkFilterChain(nil), l.FilterChains...)
		for j, fc := range l.FilterChains {
			if fc.HCM == nil || fc.HCM.RouteConfig == nil {
				continue
			}
			hcm, rc := *fc.HCM, *fc.HCM.RouteConfig
			rc.VirtualHosts = append([]model.VirtualHost(nil), rc.VirtualHosts...)
			for k := range rc.VirtualHosts {
				vh := &rc.VirtualHosts[k]
				vh.Routes = append([]model.Route(nil), vh.Routes...)
				for n := range vh.Routes {
					fn(&vh.Routes[n])
				}
			}
			hcm.RouteConfig = &rc
			l.FilterChains[j].HCM = &hcm
		}
		out.Listeners[i] = l
	}
	return out
}

// walk calls fn for every route in snap, in snapshot order.
func walk(snap *model.EnvoySnapshot, fn func(*model.RouteConfig, *model.Route)) {
	for _, l := range snap.Listeners {
		for _, fc := range l.FilterChains {
			if fc.HCM == nil || fc.HCM.RouteConfig == nil {
				continue
			}
			for _, vh := range fc.HCM.RouteConfig.VirtualHosts {
				for i := range vh.Routes {
					fn(fc.HCM.RouteConfig, &vh.Routes[i])
				}
			}
		}
	}
}

// Matched returns the number of routes in the view, counted once per filter chain.
func (v View) Matched() int {
	n := 0
	walk(v.Snapshot, func(*model.RouteConfig, *model.Route) { n++ })
	return n
}

// Text writes v as xdsview's tree, then, under a Selection, a no-match line
// and the ambiguous and unresolved routes, then the parser warnings. A route only the known
// HTTPRoutes narrowed carries OneCandidateNote in the tree too.
func Text(w io.Writer, v View, opts render.Options) error {
	var b bytes.Buffer
	switch {
	case v.Selection.Route.Name != "" && len(v.Snapshot.Listeners) == 0:
		// Narrowing left nothing: the no-match line says so, not "no listeners".
		if opts.Header != "" {
			fmt.Fprintln(&b, clean(opts.Header))
		}
	default:
		tree := opts
		tree.Style = noteNarrowed(v.Snapshot, opts.Style)
		if err := render.Text(&b, v.Snapshot, tree); err != nil {
			return err
		}
	}
	style := func(c render.Class, s string) string {
		s = clean(s)
		if opts.Style == nil {
			return s
		}
		return opts.Style(c, nil, s)
	}
	if sel := v.Selection; sel.Route.Name != "" && v.Matched() == 0 {
		target := "HTTPRoute " + sel.Route.String()
		if sel.Rule != nil {
			target += fmt.Sprintf(" rule %d", *sel.Rule)
		}
		fmt.Fprintln(&b, style(render.ClassWarning, "no route matches "+target))
	}
	if h := listHeading(v.Ambiguous); h != "" {
		fmt.Fprintln(&b, h)
	}
	names := map[string]int{}
	for _, a := range v.Ambiguous {
		names[a.Name]++
	}
	for _, a := range v.Ambiguous {
		label := clean(a.Name)
		if names[a.Name] > 1 {
			label += " · route config " + clean(a.RouteConfig)
		}
		if a.Unresolved {
			fmt.Fprintln(&b, "  "+label+" · "+style(render.ClassOrigin, "origin unresolved"))
			continue
		}
		var refs []string
		for _, c := range a.Candidates {
			if s := c.Route.String(); len(refs) == 0 || refs[len(refs)-1] != s {
				refs = append(refs, s)
			}
		}
		line := "  " + label + " · " + style(render.ClassOrigin, "ambiguous: "+strings.Join(refs, ", "))
		if a.NarrowedByKnown {
			line += " · " + OneCandidateNote
		}
		fmt.Fprintln(&b, line)
	}
	for _, warn := range v.Warnings {
		fmt.Fprintln(&b, style(render.ClassWarning, "warning: "+warn))
	}
	_, err := w.Write(b.Bytes())
	return err
}

// listHeading names the kinds of routes in list, or is empty for none.
func listHeading(list []AmbiguousRoute) string {
	var amb, unres bool
	for _, a := range list {
		amb, unres = amb || !a.Unresolved, unres || a.Unresolved
	}
	switch {
	case amb && unres:
		return "ambiguous or unresolved routes, not matched:"
	case unres:
		return "unresolved routes, not matched:"
	case amb:
		return "ambiguous routes, not matched:"
	}
	return ""
}

// noteNarrowed returns style, extended to add OneCandidateNote after a narrowed
// route's origin. render styles each route's match, then its origin.
func noteNarrowed(snap *model.EnvoySnapshot, style func(render.Class, *render.FilterRef, string) string) func(render.Class, *render.FilterRef, string) string {
	var origins []*model.RouteAnnotation
	narrowed := false
	walk(snap, func(_ *model.RouteConfig, r *model.Route) {
		origins = append(origins, r.Origin)
		narrowed = narrowed || (r.Origin != nil && r.Origin.NarrowedByKnown)
	})
	if !narrowed {
		return style
	}
	route := -1
	return func(c render.Class, ref *render.FilterRef, s string) string {
		if c == render.ClassRoute {
			route++
		}
		out := s
		if style != nil {
			out = style(c, ref, s)
		}
		if c == render.ClassOrigin && strings.HasPrefix(s, "ambiguous: ") && route >= 0 && route < len(origins) &&
			origins[route] != nil && origins[route].NarrowedByKnown {
			out += " · " + OneCandidateNote
		}
		return out
	}
}

// clean escapes control characters in dump strings, as render does, so they
// can't move the cursor or change colours.
func clean(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			fmt.Fprintf(&b, `\x%02x`, r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
