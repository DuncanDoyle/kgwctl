package print

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/objects"

	"github.com/DuncanDoyle/kgwctl/internal/observe"
	"github.com/DuncanDoyle/kgwctl/internal/view"
)

// ProgressStart writes the first progress line of check --live: what is observed and for how long.
func ProgressStart(w io.Writer, at time.Time, context string, installs []string, timeout time.Duration) error {
	what := "no install yet"
	if len(installs) > 0 {
		cleaned := make([]string, len(installs))
		for i, in := range installs {
			cleaned[i] = clean(in)
		}
		what = strings.Join(cleaned, ", ")
	}
	_, err := fmt.Fprintf(w, "%s  observing %s: %s for %s (Ctrl-C: stop and report)\n", utc(at), clean(context), what, duration(timeout))
	return err
}

// Progress writes one timestamped line for e (design §3.2). Cluster text is
// sanitised; color paints the label.
func Progress(w io.Writer, e observe.Event, color bool) error {
	var label, code, text string
	switch e.Kind {
	case observe.FindingRaised:
		label, text = strings.ToUpper(e.Severity), findingText(e.Resource, e.Text)
		code = map[string]string{"error": "31", "warning": "33", "info": "36"}[e.Severity]
		text = clean(e.Code) + "  " + text
	case observe.FindingChanged:
		label, text = "CHANGED", clean(e.Code)+"  "+findingText(e.Resource, "now "+e.Severity+": "+e.Text)
		// The detail says what changed when severity and message did not.
		if d := cleanLines(e.Detail)[0]; d != "" {
			text += "; " + d
		}
	case observe.FindingCleared:
		label, code, text = "CLEARED", "32", clean(e.Code)+"  "+findingText(e.Resource, e.Text)
	case observe.HoldStarted, observe.HoldChanged:
		label, text = "WAITING", clean(e.Check)
		if e.Hold != nil {
			text += fmt.Sprintf("  %d held; decides %s (%s)", e.Hold.Count, utc(e.Hold.Until), clean(e.Hold.Reason))
		}
	case observe.HoldEnded:
		label, text = "DECIDED", clean(e.Check)
	case observe.InputUnavailable:
		label, text = "INPUT", clean(e.Input)+": unavailable"
		if e.Text != "" {
			text += ": " + clean(e.Text)
		}
	case observe.InputAvailable:
		label, text = "INPUT", clean(e.Input)+": available"
	default:
		return nil
	}
	// Pad outside the colour so columns line up either way.
	pad := strings.Repeat(" ", max(1, 9-len(label)))
	_, err := fmt.Fprintf(w, "%s  %s%s%s\n", utc(e.At), paint(clean(label), code, color && code != ""), pad, text)
	return err
}

// findingText is "Kind ns/name: text", or text alone without a resource.
func findingText(r *objects.Ref, text string) string {
	if r == nil {
		return clean(text)
	}
	return clean(r.Kind) + " " + clean(r.String()) + ": " + clean(text)
}

// Heartbeat writes the 30s progress line: time observed of the total, and the shown counts.
func Heartbeat(w io.Writer, at time.Time, elapsed, total time.Duration, c view.Check) error {
	var n view.Counts
	deciding := 0
	for _, ic := range c.Installs {
		n.Error += ic.Counts.Error
		n.Warning += ic.Counts.Warning
		deciding += len(decidingChecks(ic))
	}
	_, err := fmt.Fprintf(w, "%s  %s / %s  %d %s, %d %s, %d %s deciding\n", utc(at), clockTime(elapsed), clockTime(total),
		n.Error, plural(n.Error, "error", "errors"), n.Warning, plural(n.Warning, "warning", "warnings"),
		deciding, plural(deciding, "check", "checks"))
	return err
}

// clockTime is d as m:ss.
func clockTime(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
