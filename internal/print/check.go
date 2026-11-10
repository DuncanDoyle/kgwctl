package print

import (
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/DuncanDoyle/kgwctl/internal/view"
)

const defaultCommand = "kgwctl"

// CheckText writes the text report of c, without the binary name or version in the header.
func CheckText(w io.Writer, c view.Check, now time.Time, color bool) error {
	return CheckTextFor(w, c, now, color, view.BuildInfo{})
}

// CheckTextFor writes the text report of design §3.3. build names the binary in the header;
// color adds ANSI severity colours. now is unused: times come from the view.
func CheckTextFor(w io.Writer, c view.Check, _ time.Time, color bool, build view.BuildInfo) error {
	b := &strings.Builder{}
	hdr := []string{strings.TrimSpace(clean(build.Name) + " check"), "context " + clean(c.Context), stamp(c.GeneratedAt) + " UTC"}
	if v := strings.TrimSpace(clean(build.Name) + " " + clean(build.Version)); build.Version != "" {
		hdr = append(hdr, v)
	}
	b.WriteString(strings.Join(hdr, " · ") + "\n")
	var total view.Counts
	deciding, notChecked, partial := 0, 0, 0
	for _, ic := range c.Installs {
		b.WriteString("\n")
		writeCheckInstall(b, ic, c.GeneratedAt, color)
		total.Error += ic.Counts.Error
		total.Warning += ic.Counts.Warning
		total.Info += ic.Counts.Info
		deciding += len(decidingChecks(ic))
		for _, cs := range ic.Checks {
			if cs.NotChecked() {
				notChecked++
			}
			if cs.Partial() {
				partial++
			}
		}
	}
	if len(c.Incomplete) > 0 {
		b.WriteString("\nINCOMPLETE INPUTS\n")
		for _, in := range c.Incomplete {
			fmt.Fprintf(b, "  %s: %s", clean(in.Resource), clean(string(in.State)))
			if in.Reason != "" {
				fmt.Fprintf(b, ": %s", clean(in.Reason))
			}
			b.WriteString("\n")
		}
	}
	if c.Observation != nil {
		writeObserved(b, *c.Observation, len(c.Installs) > 1)
	}
	b.WriteString("\n" + summary(total, deciding, notChecked, partial) + "\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// writeObserved prints the --live section: every finding with a transition during the run (design §3.2).
// withInstall names each entry's install, for reports of more than one.
func writeObserved(b *strings.Builder, o view.Observation, withInstall bool) {
	fmt.Fprintf(b, "\nOBSERVED DURING THIS RUN (%d) · %s to %s UTC, stopped by %s\n",
		len(o.Observed), utc(o.StartedAt), utc(o.EndedAt), clean(o.StopReason))
	for _, f := range o.Observed {
		code := clean(f.Code)
		head := "  " + code
		if f.Resource != nil {
			head += "  " + clean(f.Resource.Kind) + " " + clean(f.Resource.String())
		}
		if f.Message != "" {
			head += "  " + clean(f.Message)
		}
		if withInstall {
			head += "  (install " + clean(f.Install) + ")"
		}
		b.WriteString(head + "\n")
		pad := "  " + strings.Repeat(" ", len([]rune(code))+2)
		if f.Detail != "" {
			b.WriteString(pad + cleanLines(f.Detail)[0] + "\n")
		}
		seen := "First seen " + utc(f.FirstSeen)
		if f.ClearedAt != nil {
			seen += ", cleared " + utc(*f.ClearedAt)
		} else if f.LostAt != nil {
			seen += ", no longer observed from " + utc(*f.LostAt) + " (kgwctl lost sight of it; not confirmed resolved)"
		} else {
			seen += ", still present"
		}
		b.WriteString(pad + seen + ".\n")
		hist := make([]string, len(f.Severities))
		for i, s := range f.Severities {
			hist[i] = clean(s.Severity) + " from " + utc(s.At)
		}
		b.WriteString(pad + "Severity: " + strings.Join(hist, ", ") + ".\n")
	}
}

// CheckHint is CheckHintFor with the default command name.
func CheckHint(c view.Check, now time.Time) string { return CheckHintFor(c, now, defaultCommand) }

// CheckHintFor returns the §3.1 hint for held checks, naming command, or "" when no check holds.
// It reports only the earliest decision time, never an upper bound.
func CheckHintFor(c view.Check, now time.Time, command string) string {
	var first *time.Time
	n := 0
	for _, ic := range c.Installs {
		for _, cs := range ic.Checks {
			if cs.Hold == nil {
				continue
			}
			n++
			if first == nil || cs.Hold.Until.Before(*first) {
				first = &cs.Hold.Until
			}
		}
	}
	// A --live run already waited for the holds.
	if first == nil || c.Observation != nil {
		return ""
	}
	verb, noun := "are", "checks"
	if n == 1 {
		verb, noun = "is", "check"
	}
	when := utc(*first)
	if d := first.Sub(now); d > 0 {
		when += " (in " + duration(d) + ")"
	}
	return fmt.Sprintf("%d %s %s still deciding; the next decision is at %s. Run `%s check --live` to see them decide.",
		n, noun, verb, when, command)
}

func writeCheckInstall(b *strings.Builder, ic view.InstallCheck, gen time.Time, color bool) {
	fmt.Fprintf(b, "Install %s/%s", clean(ic.Install.Namespace), clean(ic.Install.Name))
	if ic.Install.Version != "" {
		fmt.Fprintf(b, " (%s)", version(clean(ic.Install.Version)))
	}
	fmt.Fprintf(b, " · state: %s\n", clean(ic.State))

	for _, sev := range []struct{ key, title, code string }{
		{"error", "ERRORS", "31"}, {"warning", "WARNINGS", "33"}, {"info", "INFO", "36"},
	} {
		var fs []view.CheckFinding
		for _, f := range ic.Findings {
			if f.Severity == sev.key {
				fs = append(fs, f)
			}
		}
		if len(fs) == 0 {
			continue
		}
		fmt.Fprintf(b, "\n%s\n", paint(fmt.Sprintf("%s (%d)", sev.title, len(fs)), sev.code, color))
		writeFindings(b, fs, ic.Groups, gen)
	}

	if rows := decidingChecks(ic); len(rows) > 0 {
		fmt.Fprintf(b, "\nSTILL DECIDING (%d)\n", len(rows))
		var cells [][]string
		for _, cs := range rows {
			if cs.Hold == nil {
				row := []string{clean(cs.Name), "pending"}
				if why := clean(firstNonEmpty(cs.Error, cs.Detail)); why != "" {
					row = append(row, "", why) // the reason lines up with the held checks' reasons
				}
				cells = append(cells, row)
				continue
			}
			cells = append(cells, []string{clean(cs.Name), fmt.Sprintf("%d held", cs.Hold.Count), "decides " + utc(cs.Hold.Until), clean(cs.Hold.Reason)})
		}
		table(b, "  ", cells)
	}

	writeNotChecked(b, ic.Checks)

	var partial [][]string
	ran := 0
	for _, cs := range ic.Checks {
		if cs.Ran() {
			ran++
		}
		if cs.Partial() {
			partial = append(partial, []string{clean(cs.Name), clean(cs.Detail)})
		}
	}
	if len(partial) > 0 {
		fmt.Fprintf(b, "\nPARTIAL COVERAGE (%d)\n", len(partial))
		table(b, "  ", partial)
	}
	// OK means a check ran, and findings are not linked to checks: none is claimed to pass.
	if len(ic.Checks) > 0 {
		fmt.Fprintf(b, "\nCHECKS RUN: %d\n", ran)
	}
}

// writeFindings prints fs in order; findings of one group with a cause print together under it.
func writeFindings(b *strings.Builder, fs []view.CheckFinding, groups []view.CheckGroup, gen time.Time) {
	causes := map[string]string{}
	for _, g := range groups {
		if g.Cause != nil {
			causes[g.ID] = clean(g.Cause.Kind) + " " + clean(g.Cause.String())
		}
	}
	done := map[string]bool{}
	for _, f := range fs {
		cause, grouped := causes[f.Group]
		if !grouped {
			writeFinding(b, f, "  ", gen)
			continue
		}
		if done[f.Group] {
			continue
		}
		done[f.Group] = true
		fmt.Fprintf(b, "  Likely cause: %s\n", cause)
		for _, m := range fs {
			if m.Group == f.Group {
				writeFinding(b, m, "    ", gen)
			}
		}
	}
}

func writeFinding(b *strings.Builder, f view.CheckFinding, indent string, gen time.Time) {
	code := clean(f.Code)
	head := code
	if f.Resource != nil {
		head += "  " + clean(f.Resource.Kind) + " " + clean(f.Resource.String())
	}
	b.WriteString(indent + head + "  " + clean(f.Message) + "\n")
	pad := indent + strings.Repeat(" ", len([]rune(code))+2)
	// line prints text sanitised, one renderer-indented line per source line.
	line := func(prefix, text string) {
		for i, l := range cleanLines(text) {
			if i > 0 {
				prefix = strings.Repeat(" ", len([]rune(prefix)))
			}
			b.WriteString(pad + prefix + l + "\n")
		}
	}
	if f.Detail != "" {
		line("", f.Detail)
	}
	if f.Since != nil {
		// Same-day times drop the date to stay short.
		at := stamp(*f.Since)
		if f.Since.UTC().YearDay() == gen.UTC().YearDay() && f.Since.UTC().Year() == gen.UTC().Year() {
			at = utc(*f.Since)
		}
		line("", "Since "+at+" UTC.")
	}
	for _, s := range f.NextSteps {
		line("Next: ", s.Command)
		if s.Note != "" {
			line("Note: ", s.Note)
		}
	}
	if f.Fix != nil {
		line("Fix: ", f.Fix.Title)
	}
}

// decidingChecks are the checks with a hold, and the pending ones without.
func decidingChecks(ic view.InstallCheck) []view.CheckStatus {
	var out []view.CheckStatus
	for _, cs := range ic.Checks {
		if cs.Hold != nil || cs.Pending {
			out = append(out, cs)
		}
	}
	return out
}

// writeNotChecked lists the checks that could not run, one line per distinct reason text.
func writeNotChecked(b *strings.Builder, checks []view.CheckStatus) {
	var texts []string
	names := map[string][]string{}
	n := 0
	for _, cs := range checks {
		if !cs.NotChecked() {
			continue
		}
		n++
		t := clean(firstNonEmpty(cs.Error, cs.Detail, cs.Unavailable))
		if strings.Contains(strings.ToLower(t), "forbidden") {
			t += " — see docs/rbac.yaml"
		}
		if names[t] == nil {
			texts = append(texts, t)
		}
		names[t] = append(names[t], clean(cs.Name))
	}
	if n == 0 {
		return
	}
	fmt.Fprintf(b, "\nNOT CHECKED (%d)\n", n)
	for _, t := range texts {
		fmt.Fprintf(b, "  %s  %s\n", strings.Join(names[t], ", "), t)
	}
}

// summary is the closing line: findings by severity, then checks deciding, not checked and partial.
func summary(c view.Counts, deciding, notChecked, partial int) string {
	var parts []string
	if c.Error > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", c.Error, plural(c.Error, "error", "errors")))
	}
	if c.Warning > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", c.Warning, plural(c.Warning, "warning", "warnings")))
	}
	if c.Info > 0 {
		parts = append(parts, fmt.Sprintf("%d info", c.Info))
	}
	if len(parts) == 0 {
		parts = append(parts, "No findings")
	}
	if deciding > 0 {
		parts = append(parts, fmt.Sprintf("%d %s deciding", deciding, plural(deciding, "check", "checks")))
	}
	if notChecked > 0 {
		parts = append(parts, fmt.Sprintf("%d not checked", notChecked))
	}
	if partial > 0 {
		parts = append(parts, fmt.Sprintf("%d with partial coverage", partial))
	}
	return strings.Join(parts, ", ") + "."
}

func paint(s, code string, color bool) string {
	if !color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// utc is HH:MM:SS in UTC.
func utc(t time.Time) string { return t.UTC().Format("15:04:05") }

// stamp is the UTC date and time.
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }

// duration prints d rounded to seconds as "4m48s", "5m" or "1h2m".
func duration(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	var out string
	if h > 0 {
		out += fmt.Sprintf("%dh", h)
	}
	if m > 0 {
		out += fmt.Sprintf("%dm", m)
	}
	if s > 0 || out == "" {
		out += fmt.Sprintf("%ds", s)
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// clean makes cluster-supplied text safe for a terminal: control and format
// characters (ESC, CR, BS, bidi overrides) are dropped, newlines and tabs become spaces.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return ' '
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
}

// cleanLines splits s at newlines and cleans each line, so the renderer owns the indentation.
func cleanLines(s string) []string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, l := range lines {
		lines[i] = clean(l)
	}
	return lines
}
