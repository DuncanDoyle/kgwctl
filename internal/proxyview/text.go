package proxyview

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/DuncanDoyle/xdsview/pkg/envoy/model"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/provenance"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/render"
)

// TextOptions controls Text.
type TextOptions struct {
	Header        string // first line: Gateway, pod and capture time, or the file
	ResponseOrder bool   // also print each route's response order
}

// Text writes v as xdsview's tree with kgwctl's hooks (DD-11), then under
// --route a no-match line and the ambiguous and unresolved routes (DD-5),
// then every warning once (DD-12).
func Text(w io.Writer, v View, o TextOptions) error {
	var b bytes.Buffer
	if v.Selected != nil && len(v.Snapshot.Listeners) == 0 {
		// Selection left nothing: the no-match line says so, not "no listeners".
		if o.Header != "" {
			fmt.Fprintln(&b, clean(o.Header))
		}
	} else {
		// The renderer prints subject warnings inline; the block at the end has them all.
		snap := *v.Snapshot
		snap.Warnings = nil
		opts := render.Options{Header: o.Header, ResponseOrder: o.ResponseOrder, Labels: v.labels(),
			Notes: v.notes(), Trailer: v.trailer()}
		if err := render.Text(&b, &snap, v.Chains, opts); err != nil {
			return err
		}
	}
	v.selectionText(&b)
	for _, warn := range v.Warnings {
		fmt.Fprintln(&b, clean(warningText(v.Full, warn)))
	}
	_, err := w.Write(b.Bytes())
	return err
}

// labels is the Labels hook: provenance.Labels with the chosen routes, plus
// the Gateway kgwctl was given on non-static listener rows, unless a claim names one.
func (v View) labels() func(render.Row) []string {
	base := provenance.Labels(v.Index, v.Chosen)
	return func(row render.Row) []string {
		out := base(row)
		if v.Gateway == nil || row.Route != "" || row.Slot != "" {
			return out
		}
		l, ok := v.Snapshot.Listener(row.Subject)
		if !ok || l.Static {
			return out
		}
		if g := provenance.Resolve[provenance.GatewaySource](v.Index, l.ID); g.Certainty >= model.Inferred {
			return out
		}
		return append(out, "Gateway "+v.Gateway.String()+" "+GatewayContextNote)
	}
}

// recordedAboveLabel labels a route's policy records at its virtual host,
// route config or listener: where they were recorded, not inheritance (D-024).
const recordedAboveLabel = "policies recorded above the route"

// trailer is the Trailer hook: provenance.Notes with kgwctl's label for the
// records above route level.
func (v View) trailer() func(render.Row) []render.Line {
	base := provenance.Notes(v.Index)
	return func(row render.Row) []render.Line {
		out := base(row)
		for i := range out {
			if out[i].Label == xdsviewAboveLabel {
				out[i].Label = recordedAboveLabel
			}
		}
		return out
	}
}

// xdsviewAboveLabel is provenance.Notes' label for the same records.
const xdsviewAboveLabel = "inherited policies"

// notes is the Notes hook: OneCandidateNote under a route Known narrowed to
// one reading that --select known did not choose (DD-6).
func (v View) notes() func(render.Row) []string {
	return func(row render.Row) []string {
		if row.Route == "" || row.Slot != "" || v.chosen(row.Route) {
			return nil
		}
		if n, ok := v.narrowing(row.Route); ok && len(readings(n.Kept)) == 1 {
			return []string{OneCandidateNote}
		}
		return nil
	}
}

// selectionText writes the no-match line and the routes listed, not matched.
// Uses of a shared route config with the same outcome and readings are
// listed once, by source path; different outcomes are each listed (DD-5).
func (v View) selectionText(b *bytes.Buffer) {
	s := v.Selected
	if s == nil {
		return
	}
	if v.Matched() == 0 {
		fmt.Fprintln(b, clean("no route matches "+target(v.Selection)))
	}
	ambiguous, unresolved, inferred := idSet(s.Ambiguous), idSet(s.Unresolved), idSet(v.Inferred)
	for _, n := range v.Chosen {
		delete(ambiguous, n.Route)
	}
	type entry struct {
		rc, name   string
		unres, inf bool
		detail     string // the outcome's text after the name
	}
	// outcome is what one use of a source shows; equal outcomes print once.
	type outcome struct {
		key        model.ID
		detail     string
		unres, inf bool
	}
	routeLabel := provenance.Labels(v.Index, nil)
	var list []entry
	seen := map[outcome]bool{}
	names := map[string]map[model.ID]bool{} // route name to its source keys
	walk(v.Full, func(hcm model.HCMConfig, _ model.VirtualHost, r model.Route) {
		if !ambiguous[r.ID] && !unresolved[r.ID] && !inferred[r.ID] {
			return
		}
		e := entry{name: r.Name, unres: unresolved[r.ID], inf: inferred[r.ID]}
		switch {
		case e.inf:
			// The reading as the tree prints it.
			e.detail = strings.Join(routeLabel(render.Row{Subject: r.ID, Route: r.ID}), " · ")
		case e.unres:
			e.detail = "origin unresolved"
		default:
			e.detail = v.ambiguousText(r.ID)
		}
		key := r.SourcePath
		if key == "" {
			key = r.ID
		}
		o := outcome{key: key, detail: e.detail, unres: e.unres, inf: e.inf}
		if seen[o] {
			return
		}
		seen[o] = true
		if names[r.Name] == nil {
			names[r.Name] = map[model.ID]bool{}
		}
		names[r.Name][key] = true
		e.rc = hcm.RouteConfig.Name
		list = append(list, e)
	})
	line := func(e entry) string {
		out := "  " + e.name
		if len(names[e.name]) > 1 {
			out += " · route config " + e.rc
		}
		if e.detail != "" {
			out += " · " + e.detail
		}
		return clean(out)
	}
	// Inferred routes first.
	for i, e := range slices.DeleteFunc(slices.Clone(list), func(e entry) bool { return !e.inf }) {
		if i == 0 {
			fmt.Fprintln(b, "inferred routes, not matched:")
		}
		fmt.Fprintln(b, line(e))
	}
	list = slices.DeleteFunc(list, func(e entry) bool { return e.inf })
	var amb, unres bool
	for _, e := range list {
		amb, unres = amb || !e.unres, unres || e.unres
	}
	switch {
	case amb && unres:
		fmt.Fprintln(b, "ambiguous or unresolved routes, not matched:")
	case unres:
		fmt.Fprintln(b, "unresolved routes, not matched:")
	case amb:
		fmt.Fprintln(b, "ambiguous routes, not matched:")
	}
	for _, e := range list {
		fmt.Fprintln(b, line(e))
	}
}

// ambiguousText is an ambiguous route's outcome: its readings' HTTPRoutes,
// and OneCandidateNote when Known narrowed it to one.
func (v View) ambiguousText(route model.ID) string {
	var refs []string
	for _, r := range provenance.Resolve[provenance.RouteSource](v.Index, route).Candidates {
		if s := r.Route.String(); len(refs) == 0 || refs[len(refs)-1] != s {
			refs = append(refs, s)
		}
	}
	out := "ambiguous: " + strings.Join(refs, ", ")
	if n, ok := v.narrowing(route); ok && len(readings(n.Kept)) == 1 {
		out += " · " + OneCandidateNote
	}
	return out
}

// target names the selected HTTPRoute and rule, e.g. "HTTPRoute ns/x rule 1".
func target(s Selection) string {
	t := "HTTPRoute " + s.Route.String()
	if s.Rule != nil {
		t += fmt.Sprintf(" rule %d", *s.Rule)
	}
	return t
}

func idSet(ids []model.ID) map[model.ID]bool {
	out := map[model.ID]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out
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
