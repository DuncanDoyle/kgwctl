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
			Notes: v.notes(), Trailer: provenance.Notes(v.Index)}
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
// A route of a shared route config is listed once, by its source path.
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
		route      model.ID
		unres, inf bool
	}
	var list []entry
	seen := map[model.ID]bool{}
	names := map[string]int{}
	walk(v.Full, func(hcm model.HCMConfig, _ model.VirtualHost, r model.Route) {
		if !ambiguous[r.ID] && !unresolved[r.ID] && !inferred[r.ID] {
			return
		}
		key := r.SourcePath
		if key == "" {
			key = r.ID
		}
		if seen[key] {
			return
		}
		seen[key] = true
		names[r.Name]++
		list = append(list, entry{rc: hcm.RouteConfig.Name, name: r.Name, route: r.ID, unres: unresolved[r.ID], inf: inferred[r.ID]})
	})
	label := func(e entry) string {
		if names[e.name] > 1 {
			return e.name + " · route config " + e.rc
		}
		return e.name
	}
	// Inferred routes first, each with its reading as the tree prints it.
	routeLabel := provenance.Labels(v.Index, nil)
	for i, e := range slices.DeleteFunc(slices.Clone(list), func(e entry) bool { return !e.inf }) {
		if i == 0 {
			fmt.Fprintln(b, "inferred routes, not matched:")
		}
		fmt.Fprintln(b, clean("  "+strings.Join(append([]string{label(e)}, routeLabel(render.Row{Subject: e.route, Route: e.route})...), " · ")))
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
		label := label(e)
		if e.unres {
			fmt.Fprintln(b, clean("  "+label+" · origin unresolved"))
			continue
		}
		var refs []string
		for _, r := range provenance.Resolve[provenance.RouteSource](v.Index, e.route).Candidates {
			if s := r.Route.String(); len(refs) == 0 || refs[len(refs)-1] != s {
				refs = append(refs, s)
			}
		}
		line := "  " + label + " · ambiguous: " + strings.Join(refs, ", ")
		if n, ok := v.narrowing(e.route); ok && len(readings(n.Kept)) == 1 {
			line += " · " + OneCandidateNote
		}
		fmt.Fprintln(b, clean(line))
	}
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
