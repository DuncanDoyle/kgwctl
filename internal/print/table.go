package print

import (
	"io"
	"strings"
	"text/tabwriter"

	"github.com/DuncanDoyle/kgwctl/internal/view"
)

// Get output formats.
const (
	FormatTable = "table"
	FormatWide  = "wide"
)

// Tables writes the tables that have rows, kubectl-style, with a blank line
// between them. It reports whether it wrote any; the caller says so on stderr when not.
func Tables(w io.Writer, ts []view.Table) (bool, error) {
	b := &strings.Builder{}
	wrote := false
	for _, t := range ts {
		if len(t.Rows) == 0 {
			continue
		}
		if wrote {
			b.WriteString("\n")
		}
		wrote = true
		// kubectl's tabwriter settings.
		tw := tabwriter.NewWriter(b, 6, 4, 3, ' ', 0)
		_, _ = io.WriteString(tw, strings.Join(t.Headers, "\t")+"\n")
		for _, r := range t.Rows {
			_, _ = io.WriteString(tw, strings.Join(r, "\t")+"\n")
		}
		_ = tw.Flush() // writes to a strings.Builder, which never fails
	}
	// An empty last cell, such as INSTALL without an owner, leaves padding behind.
	lines := strings.Split(b.String(), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	_, err := io.WriteString(w, strings.Join(lines, "\n"))
	return wrote, err
}

// NoResources is kubectl's message for an empty get in namespace ns; "" means all namespaces.
func NoResources(ns string) string {
	if ns == "" {
		return "No resources found"
	}
	return "No resources found in " + ns + " namespace."
}
