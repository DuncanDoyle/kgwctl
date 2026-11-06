package print

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/DuncanDoyle/kgwctl/internal/view"
)

// DescribeText writes v in kubectl-describe style, with the sections of design §3.2.
func DescribeText(w io.Writer, v *view.DescribeView) error {
	b := &strings.Builder{}
	writeObject(b, v.Object)
	if v.Spec != nil {
		writeSpec(b, v.Spec)
	}
	writeConditions(b, v.Conditions)
	writeRelations(b, v.Relations, v.Object.Namespace)
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

func writeSpec(b *strings.Builder, s *view.SpecSummary) {
	b.WriteString("Spec:\n")
	var rows [][]string
	if s.Class != "" {
		rows = append(rows, []string{"Class:", s.Class})
	}
	if len(s.Addresses) > 0 {
		rows = append(rows, []string{"Addresses:", strings.Join(s.Addresses, ", ")})
	}
	if s.Parent != "" {
		rows = append(rows, []string{"Parent:", s.Parent})
	}
	if s.Listeners == nil {
		hosts := "*"
		if len(s.Hostnames) > 0 {
			hosts = strings.Join(s.Hostnames, ", ")
		}
		rows = append(rows, []string{"Hostnames:", hosts})
	}
	table(b, "  ", rows)
	if s.Listeners != nil {
		b.WriteString("  Listeners:\n")
		ls := [][]string{{"NAME", "PORT", "PROTOCOL", "HOSTNAME", "TLS", "ALLOWED ROUTES"}}
		for _, l := range s.Listeners {
			ls = append(ls, []string{l.Name, strconv.Itoa(int(l.Port)), l.Protocol, or(l.Hostname, "*"), or(l.TLS, "-"), l.AllowedRoutes})
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
		fmt.Fprintf(b, "    %s:\n", name)
		table(b, "      ", [][]string{{"Matches:", list(r.Matches)}, {"Filters:", list(r.Filters)}, {"Backends:", list(r.BackendRefs)}})
	}
}

func writeConditions(b *strings.Builder, bs []view.ConditionBlock) {
	if len(bs) == 0 {
		b.WriteString("Conditions:  <none>\n")
		return
	}
	b.WriteString("Conditions:\n")
	for _, c := range bs {
		head := c.Scope
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
	case !r.Resolved:
	case r.Of == view.RelationsGateway:
		b.WriteString("Relations:\n")
		if len(r.AttachedRoutes) == 0 {
			b.WriteString("  Attached routes:  <none>\n")
		} else {
			b.WriteString("  Attached routes:\n")
			var rows [][]string
			for _, l := range r.AttachedRoutes {
				rows = append(rows, []string{"listener " + l.Listener + ":", strings.Join(l.Routes, ", ")})
			}
			table(b, "    ", rows)
		}
		if len(r.NotAccepted) == 0 {
			b.WriteString("  Not accepted:  <none>\n")
		} else {
			b.WriteString("  Not accepted:\n")
			rows := [][]string{{"ROUTE", "LISTENER", "ATTACHMENT", "CONDITION"}}
			for _, n := range r.NotAccepted {
				rows = append(rows, []string{n.Route, or(n.Listener, "-"), n.Attachment, or(n.Condition, "-")})
			}
			table(b, "    ", rows)
		}
		table(b, "  ", [][]string{{"ListenerSets:", list(r.ListenerSets)}})
		return
	default:
		b.WriteString("Relations:\n")
		writeParents(b, "Parents:", r.Parents, ns)
		rows := [][]string{}
		if len(r.DuplicateParents) > 0 {
			rows = append(rows, []string{"Duplicate parentRefs:", list(r.DuplicateParents)})
		}
		rows = append(rows, []string{"Delegation parents:", list(r.DelegationParents)}, []string{"Delegation children:", list(r.DelegationChildren)},
			[]string{"Backends:", list(r.Backends)})
		table(b, "  ", rows)
		return
	}
	if d := r.Declared; d != nil {
		b.WriteString("Declared references (not resolved):\n")
		rows := [][]string{{"Parent refs:", list(d.ParentRefs)}}
		if r.Of == view.RelationsRoute {
			rows = append(rows, []string{"Backend refs:", list(d.BackendRefs)})
		}
		table(b, "  ", rows)
	}
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
		if p.State == view.StateRejected && p.Reason != "" {
			state += ": " + p.Reason
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
