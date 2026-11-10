package observe

import (
	"cmp"
	"slices"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/objects"

	"github.com/DuncanDoyle/kgwctl/internal/view"
)

// EventKind is what changed between two published models.
type EventKind int

// The kinds, in the order Diff emits them within one install.
const (
	FindingRaised EventKind = iota
	// FindingChanged is a finding whose severity or message changed under the same ID.
	FindingChanged
	FindingCleared
	HoldStarted
	HoldChanged
	HoldEnded
	InputAvailable
	InputUnavailable
)

// Event is one progress line of check --live.
type Event struct {
	At   time.Time
	Kind EventKind
	// Install is the install ID; empty for input events.
	Install string
	// Code, Severity and Resource describe a finding event; Severity is the new one.
	Code     string
	Severity string
	Check    string
	Resource *objects.Ref
	// Hold is the new hold, or the last one for HoldEnded.
	Hold  *view.Hold
	Input string
	// Text is the finding message, the hold reason or the input's access error.
	Text string
}

// Diff compares two models and returns the events at time at. Findings are
// keyed by install ID and finding ID, holds by install ID and check name,
// inputs by resource. Events are ordered by install ID, kind, then key; input
// events come last. It is pure and never reads a clock.
func Diff(prev, next view.Check, at time.Time) []Event {
	type keyed struct {
		key string
		e   Event
	}
	pi, ni := installs(prev), installs(next)
	ids := map[string]bool{}
	for id := range pi {
		ids[id] = true
	}
	for id := range ni {
		ids[id] = true
	}
	sorted := make([]string, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	slices.Sort(sorted)

	var out []Event
	for _, id := range sorted {
		var fe []keyed
		pf, nf := findings(pi[id]), findings(ni[id])
		for k, n := range nf {
			p, ok := pf[k]
			switch {
			case !ok:
				fe = append(fe, keyed{k, findingEvent(at, FindingRaised, id, n)})
			case p.Severity != n.Severity || p.Message != n.Message:
				fe = append(fe, keyed{k, findingEvent(at, FindingChanged, id, n)})
			}
		}
		for k, p := range pf {
			if _, ok := nf[k]; !ok {
				fe = append(fe, keyed{k, findingEvent(at, FindingCleared, id, p)})
			}
		}
		ph, nh := holds(pi[id]), holds(ni[id])
		for k, n := range nh {
			p, ok := ph[k]
			switch {
			case !ok:
				fe = append(fe, keyed{k, holdEvent(at, HoldStarted, id, k, n)})
			case p.Count != n.Count || !p.Until.Equal(n.Until) || p.Reason != n.Reason:
				fe = append(fe, keyed{k, holdEvent(at, HoldChanged, id, k, n)})
			}
		}
		for k, p := range ph {
			if _, ok := nh[k]; !ok {
				fe = append(fe, keyed{k, holdEvent(at, HoldEnded, id, k, p)})
			}
		}
		slices.SortFunc(fe, func(a, b keyed) int { return cmp.Or(cmp.Compare(a.e.Kind, b.e.Kind), cmp.Compare(a.key, b.key)) })
		for _, k := range fe {
			out = append(out, k.e)
		}
	}

	pin, nin := inputs(prev), inputs(next)
	var ie []Event
	for r, reason := range nin {
		if _, ok := pin[r]; !ok {
			ie = append(ie, Event{At: at, Kind: InputUnavailable, Input: r, Text: reason})
		}
	}
	for r := range pin {
		if _, ok := nin[r]; !ok {
			ie = append(ie, Event{At: at, Kind: InputAvailable, Input: r})
		}
	}
	slices.SortFunc(ie, func(a, b Event) int { return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Input, b.Input)) })
	return append(out, ie...)
}

func findingEvent(at time.Time, k EventKind, install string, f view.CheckFinding) Event {
	return Event{At: at, Kind: k, Install: install, Code: f.Code, Severity: f.Severity, Resource: f.Resource, Text: f.Message}
}

func holdEvent(at time.Time, k EventKind, install, check string, h view.Hold) Event {
	return Event{At: at, Kind: k, Install: install, Check: check, Hold: &h, Text: h.Reason}
}

func installs(c view.Check) map[string]view.InstallCheck {
	m := map[string]view.InstallCheck{}
	for _, ic := range c.Installs {
		m[ic.Install.ID] = ic
	}
	return m
}

func findings(ic view.InstallCheck) map[string]view.CheckFinding {
	m := map[string]view.CheckFinding{}
	for _, f := range ic.Findings {
		m[f.ID] = f
	}
	return m
}

func holds(ic view.InstallCheck) map[string]view.Hold {
	m := map[string]view.Hold{}
	for _, cs := range ic.Checks {
		if cs.Hold != nil {
			m[cs.Name] = *cs.Hold
		}
	}
	return m
}

// inputs maps each incomplete input's resource to its access error.
func inputs(c view.Check) map[string]string {
	m := map[string]string{}
	for _, in := range c.Incomplete {
		m[in.Resource] = in.Reason
	}
	return m
}

// Tracker follows findings across the models of one run, for the Observed section.
// The first Add is the baseline and raises nothing. A finding that clears and
// returns is ONE entry: it keeps its first FirstSeen, ClearedAt is nil again once
// it returns, and Severities records each severity change with its time.
type Tracker struct {
	prev    *view.Check
	entries map[string]*tracked
}

type tracked struct {
	f       view.ObservedFinding
	install string
	message string
	// transitioned is set once the finding is raised late, cleared or changes severity.
	transitioned bool
}

// Add records c at time at and returns the events since the previous model.
func (t *Tracker) Add(c view.Check, at time.Time) []Event {
	if t.entries == nil {
		t.entries = map[string]*tracked{}
	}
	first := t.prev == nil
	var events []Event
	if !first {
		events = Diff(*t.prev, c, at)
	}
	seen := map[string]bool{}
	for _, ic := range c.Installs {
		for _, f := range ic.Findings {
			key := ic.Install.ID + "\x00" + f.ID
			seen[key] = true
			e := t.entries[key]
			if e == nil {
				t.entries[key] = &tracked{f: view.ObservedFinding{ID: f.ID, Install: ic.Install.ID, Code: f.Code, Severity: f.Severity, Resource: f.Resource,
					Message: f.Message, FirstSeen: at, Severities: []view.SeverityAt{{Severity: f.Severity, At: at}}}, install: ic.Install.ID, message: f.Message, transitioned: !first}
				continue
			}
			e.f.ClearedAt = nil
			if e.message != f.Message {
				e.message, e.f.Message = f.Message, f.Message
				e.transitioned = true
			}
			if e.f.Severity != f.Severity {
				e.f.Severity = f.Severity
				e.f.Severities = append(e.f.Severities, view.SeverityAt{Severity: f.Severity, At: at})
				e.transitioned = true
			}
		}
	}
	for key, e := range t.entries {
		if !seen[key] && e.f.ClearedAt == nil {
			e.f.ClearedAt = new(at)
			e.transitioned = true
		}
	}
	t.prev = &c
	return events
}

// Observed returns every finding with a transition, ordered by first-seen, then
// finding ID, then install ID.
func (t *Tracker) Observed() []view.ObservedFinding {
	var es []*tracked
	for _, e := range t.entries {
		if e.transitioned {
			es = append(es, e)
		}
	}
	slices.SortFunc(es, func(a, b *tracked) int {
		return cmp.Or(a.f.FirstSeen.Compare(b.f.FirstSeen), cmp.Compare(a.f.ID, b.f.ID), cmp.Compare(a.install, b.install))
	})
	out := make([]view.ObservedFinding, 0, len(es))
	for _, e := range es {
		f := e.f
		f.Severities = slices.Clone(f.Severities)
		out = append(out, f)
	}
	return out
}
