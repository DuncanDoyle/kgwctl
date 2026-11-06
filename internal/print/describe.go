package print

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/DuncanDoyle/kgateway-ui/pkg/objects"

	"github.com/DuncanDoyle/kgwctl/internal/view"
)

// DescribeText writes v in kubectl-describe style, with the sections of design §3.2.
// Refs print relative to the object's namespace.
func DescribeText(w io.Writer, v *view.DescribeView) error {
	b := &strings.Builder{}
	ns := v.Object.Namespace
	writeObject(b, v.Object)
	if v.Spec != nil {
		writeSpec(b, v.Spec, ns)
	}
	writeConditions(b, v.Conditions, ns)
	writeRelations(b, v.Relations, ns)
	if len(v.Incomplete) > 0 {
		b.WriteString("Incomplete inputs:\n")
		for _, in := range v.Incomplete {
			fmt.Fprintf(b, "  %s: %s", in.Resource, in.State)
			if in.Reason != "" {
				fmt.Fprintf(b, ": %s", in.Reason)
			}
			b.WriteString("\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// table writes rows aligned, each line prefixed by indent.
func table(b *strings.Builder, indent string, rows [][]string) {
	tw := tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintln(tw, indent+strings.Join(r, "\t"))
	}
	_ = tw.Flush() // writes to a strings.Builder, which never fails
}

func writeObject(b *strings.Builder, o view.ObjectInfo) {
	install := orNone(o.Install)
	if o.InstallUnknown != "" {
		install = "unknown (" + o.InstallUnknown + ")"
	}
	observed := "<none>"
	if o.ObservedGeneration != 0 {
		observed = strconv.FormatInt(o.ObservedGeneration, 10)
	}
	rows := [][]string{{"Name:", o.Name}}
	if o.Namespace != "" {
		rows = append(rows, []string{"Namespace:", o.Namespace})
	}
	rows = append(rows, []string{"Kind:", o.Kind + "." + o.Group}, []string{"Install:", install}, []string{"Age:", o.Age},
		[]string{"Generation:", strconv.FormatInt(o.Generation, 10)}, []string{"Observed generation:", observed})
	table(b, "", rows)
}

func writeSpec(b *strings.Builder, s *view.SpecSummary, ns string) {
	b.WriteString("Spec:\n")
	var rows [][]string
	if s.Class != "" {
		rows = append(rows, []string{"Class:", s.Class})
	}
	if len(s.Addresses) > 0 {
		rows = append(rows, []string{"Addresses:", strings.Join(s.Addresses, ", ")})
	}
	if s.Parent != nil {
		rows = append(rows, []string{"Parent:", view.ParentName(*s.Parent, ns)})
	}
	if s.Listeners == nil {
		rows = append(rows, []string{"Hostnames:", or(strings.Join(s.Hostnames, ", "), "*")})
	}
	table(b, "  ", rows)
	if s.Listeners != nil {
		b.WriteString("  Listeners:\n")
		ls := [][]string{{"NAME", "PORT", "PROTOCOL", "HOSTNAME", "TLS", "ALLOWED ROUTES"}}
		for _, l := range s.Listeners {
			ls = append(ls, []string{l.Name, strconv.Itoa(int(l.Port)), l.Protocol, or(l.Hostname, "*"), or(l.TLSMode, "-"), allowedRoutes(l.AllowedRoutes)})
		}
		table(b, "    ", ls)
		return
	}
	if len(s.Rules) == 0 {
		b.WriteString("  Rules:  <none>\n")
		return
	}
	b.WriteString("  Rules:\n")
	for i, r := range s.Rules {
		name := "rule " + strconv.Itoa(i)
		if r.Name != "" {
			name += " (" + r.Name + ")"
		}
		var ms, fs, bs []string
		for _, m := range r.Matches {
			ms = append(ms, match(m))
		}
		for _, f := range r.Filters {
			s := f.Type
			if f.Target != nil {
				s += " " + view.RefName(*f.Target, ns, "")
			}
			fs = append(fs, s)
		}
		for _, br := range r.BackendRefs {
			bs = append(bs, backendRef(br, ns))
		}
		fmt.Fprintf(b, "    %s:\n", name)
		// Matches are ORed, so they are split by "|", not ",".
		table(b, "      ", [][]string{{"Matches:", orNone(strings.Join(ms, " | "))}, {"Filters:", list(fs)}, {"Backends:", list(bs)}})
	}
}

func allowedRoutes(a view.AllowedRoutes) string {
	s := "from " + a.From
	if a.Selector != "" {
		s += " " + a.Selector
	}
	var ks []string
	for _, k := range a.Kinds {
		ks = append(ks, k.Kind)
	}
	if len(ks) > 0 {
		s += ", kinds " + strings.Join(ks, ",")
	}
	return s
}

func match(m view.MatchSpec) string {
	var parts []string
	if m.Path != nil {
		parts = append(parts, m.Path.Type+" "+m.Path.Value)
	}
	for _, h := range m.Headers {
		parts = append(parts, fmt.Sprintf("header %s %s %s", h.Name, h.Type, h.Value))
	}
	for _, q := range m.QueryParams {
		parts = append(parts, fmt.Sprintf("query %s %s %s", q.Name, q.Type, q.Value))
	}
	if m.Method != "" {
		parts = append(parts, "method "+m.Method)
	}
	return strings.Join(parts, "; ")
}

// backendRef is a backend as get prints it, with port and weight, or a delegation.
func backendRef(br view.BackendRefSpec, ns string) string {
	if d := br.Delegation; d != nil {
		// Name mode names a route, wildcard mode takes every route, label mode a label value.
		target := d.Name
		switch d.Mode {
		case "wildcard":
			target = "*"
		case "label":
			target = d.Label
		}
		if d.Namespace != ns {
			target = d.Namespace + "/" + target
		}
		return fmt.Sprintf("delegate (%s) %s", d.Mode, target)
	}
	s := view.RefName(*br.Ref, ns, "Service")
	if br.Port != 0 {
		s += ":" + strconv.Itoa(int(br.Port))
	}
	if br.Weight != nil {
		s += fmt.Sprintf(" weight %d", *br.Weight)
	}
	return s
}

func writeConditions(b *strings.Builder, bs []view.ConditionBlock, ns string) {
	if len(bs) == 0 {
		b.WriteString("Conditions:  <none>\n")
		return
	}
	b.WriteString("Conditions:\n")
	for _, c := range bs {
		head := c.Scope
		switch {
		case c.Listener != "":
			head += " " + c.Listener
		case c.Ref != nil:
			head += " " + view.ParentName(*c.Ref, ns)
		}
		if c.Controller != "" {
			head += " (" + c.Controller + ")"
		}
		if len(c.Conditions) == 0 {
			fmt.Fprintf(b, "  %s:  <no status>\n", head)
			continue
		}
		fmt.Fprintf(b, "  %s:\n", head)
		rows := [][]string{{"TYPE", "STATUS", "REASON", "MESSAGE", "LAST TRANSITION", "OBSERVED GEN"}}
		for _, r := range c.Conditions {
			gen := strconv.FormatInt(r.ObservedGeneration, 10)
			if r.Stale {
				gen += " (stale)"
			}
			rows = append(rows, []string{r.Type, r.Status, or(r.Reason, "-"), or(r.Message, "-"), or(r.LastTransition, "-"), gen})
		}
		table(b, "    ", rows)
	}
}

func writeRelations(b *strings.Builder, r view.Relations, ns string) {
	switch {
	case r.Of == "":
		return
	case r.Of == view.RelationsPolicy:
		b.WriteString("Relations:\n")
		writeParents(b, "Ancestors (from status):", r.Ancestors, ns)
		return
	case r.Unknown != "":
		fmt.Fprintf(b, "Relations unknown (%s)\n", r.Unknown)
		// Parent states come from status, so they still print (Ruling R33).
		if r.Of == view.RelationsRoute {
			writeParents(b, "Parents:", r.Parents, ns)
		}
		return
	case r.Resolved == nil || !*r.Resolved:
		writeDeclared(b, r, ns)
		return
	}
	b.WriteString("Relations:\n")
	if r.Of == view.RelationsRoute {
		writeParents(b, "Parents:", r.Parents, ns)
		var rows [][]string
		if len(r.DuplicateParents) > 0 {
			var ds []string
			for _, p := range r.DuplicateParents {
				ds = append(ds, view.ParentName(p, ns))
			}
			rows = append(rows, []string{"Duplicate parentRefs:", list(ds)})
		}
		var ps, cs []string
		for _, p := range r.DelegationParents {
			ps = append(ps, view.RefName(p, ns, ""))
		}
		for _, c := range r.DelegationChildren {
			s := view.RefName(c.Ref, ns, "")
			if c.Declared {
				s += " (declared)"
			}
			cs = append(cs, s)
		}
		rows = append(rows, []string{"Delegation parents:", list(ps)}, []string{"Delegation children:", list(cs)},
			[]string{"Backends:", list(backends(r.Backends, ns))})
		table(b, "  ", rows)
		return
	}
	if len(r.AttachedRoutes) == 0 {
		b.WriteString("  Attached routes:  <none>\n")
	} else {
		b.WriteString("  Attached routes:\n")
		rows := [][]string{{"LISTENER", "ROUTES"}}
		for _, l := range r.AttachedRoutes {
			var rs []string
			for _, rt := range l.Routes {
				rs = append(rs, view.RefName(rt, ns, ""))
			}
			rows = append(rows, []string{listener(l.Parent, l.Listener, r.Of, ns), strings.Join(rs, ", ")})
		}
		table(b, "    ", rows)
	}
	writeLinks(b, "Not accepted:", r.NotAccepted, ns)
	writeLinks(b, "Pending:", r.Pending, ns)
	if r.Of == view.RelationsGateway {
		var ls []string
		for _, l := range r.ListenerSets {
			ls = append(ls, view.RefName(l, ns, ""))
		}
		table(b, "  ", [][]string{{"ListenerSets:", list(ls)}})
	}
}

// listener names a listener; one of a ListenerSet under a Gateway carries the ListenerSet, an unknown one is "-".
func listener(parent objects.Ref, name, of, ns string) string {
	name = or(name, "-")
	if of == view.RelationsGateway && parent.Kind != "Gateway" {
		return view.RefName(parent, ns, "") + ":" + name
	}
	return name
}

// writeLinks writes route links that are not accepted, with the deciding condition or pending reason.
func writeLinks(b *strings.Builder, heading string, ls []view.RouteLink, ns string) {
	if len(ls) == 0 {
		fmt.Fprintf(b, "  %s  <none>\n", heading)
		return
	}
	fmt.Fprintf(b, "  %s\n", heading)
	rows := [][]string{{"ROUTE", "PARENT REF", "ATTACHMENT", "CONDITION"}}
	for _, l := range ls {
		cond := l.Pending
		if c := l.Condition; c != nil {
			cond = fmt.Sprintf("%s=%s (%s)", c.Type, c.Status, c.Reason)
			if c.Message != "" {
				cond += ": " + c.Message
			}
		}
		rows = append(rows, []string{view.RefName(l.Route, ns, ""), view.ParentName(l.ParentRef, ns), l.Attachment, or(cond, "-")})
	}
	table(b, "    ", rows)
}

// writeDeclared writes an unresolved object's declared references; a Gateway declares none.
func writeDeclared(b *strings.Builder, r view.Relations, ns string) {
	d := r.Declared
	if d == nil {
		b.WriteString("Relations:  not resolved (no owning install)\n")
		return
	}
	b.WriteString("Declared references (not resolved):\n")
	var ps []string
	for _, p := range d.ParentRefs {
		ps = append(ps, view.ParentName(p, ns))
	}
	rows := [][]string{{"Parent refs:", list(ps)}}
	if r.Of == view.RelationsRoute {
		rows = append(rows, []string{"Backend refs:", list(backends(d.BackendRefs, ns))})
	}
	table(b, "  ", rows)
}

// backends formats backends as get does, Service kind left out.
func backends(bs []view.Backend, ns string) []string {
	var out []string
	for _, x := range bs {
		s := view.RefName(x.Ref, ns, "Service")
		if x.Mirror {
			s += " (mirror)"
		}
		out = append(out, s)
	}
	return out
}

// writeParents writes parent or ancestor states under heading, as get counts them.
func writeParents(b *strings.Builder, heading string, ps []view.ParentSummary, ns string) {
	if len(ps) == 0 {
		fmt.Fprintf(b, "  %s  <none>\n", heading)
		return
	}
	fmt.Fprintf(b, "  %s\n", heading)
	var rows [][]string
	for _, p := range ps {
		state := string(p.State)
		switch {
		case p.Reason == "":
		case p.State == view.StateRejected:
			state += ": " + p.Reason
		// An entry with an Unknown condition counts as no status; its reason says why.
		case p.State == view.StateNoStatus:
			state += " (" + p.Reason + ")"
		}
		rows = append(rows, []string{view.ParentName(p.Parent, ns), state, or(p.Controller, "-")})
	}
	table(b, "    ", rows)
}

func list(s []string) string { return orNone(strings.Join(s, ", ")) }

func orNone(s string) string { return or(s, "<none>") }

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
