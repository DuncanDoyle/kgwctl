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
	// CheckUnavailable is a check that ran and now cannot; CheckAvailable one that runs again.
	CheckUnavailable EventKind = iota
	CheckAvailable
	FindingRaised
	// FindingChanged is a finding whose severity, message or detail changed under the same ID.
	FindingChanged
	FindingCleared
	// FindingUnobserved is a finding gone while a check that decided before did not decide: its state is unknown.
	FindingUnobserved
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
	// ID, Code, Severity and Resource describe a finding event; Severity is the new one.
	ID       string
	Code     string
	Severity string
	Check    string
	Resource *objects.Ref
	// Hold is the new hold, or the last one for HoldEnded.
	Hold  *view.Hold
	Input string
	// Text is the finding message, the hold reason, the input's access error or why a check cannot run.
	// Detail is a finding's detail.
	Text   string
	Detail string
}

// Diff compares two models and returns the events at time at. Findings are
// keyed by install ID and finding ID, holds and checks by install ID and check name,
// inputs by resource. Events are ordered by install ID, kind, then key; input
// events come last. It is pure and never reads a clock.
//
// A finding that disappears while a check that ran in prev does not decide in next
// is FindingUnobserved, not cleared: without provenance, any such check may have raised it.
// A pending check has not decided; CheckAvailable needs a decided check.
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
		pc, nc := checks(pi[id]), checks(ni[id])
		for k, n := range nc {
			p, ok := pc[k]
			switch {
			case n.NotChecked() && (!ok || !p.NotChecked()):
				fe = append(fe, keyed{k, Event{At: at, Kind: CheckUnavailable, Install: id, Check: k, Text: notCheckedReason(n)}})
			case decided(n) && ok && p.NotChecked():
				fe = append(fe, keyed{k, Event{At: at, Kind: CheckAvailable, Install: id, Check: k}})
			}
		}
		_, present := ni[id]
		gone := FindingCleared
		if !present || !decidedStill(ranSet(pi[id]), ni[id]) {
			gone = FindingUnobserved
		}
		pf, nf := findings(pi[id]), findings(ni[id])
		for k, n := range nf {
			p, ok := pf[k]
			switch {
			case !ok:
				fe = append(fe, keyed{k, findingEvent(at, FindingRaised, id, n)})
			case p.Severity != n.Severity || p.Message != n.Message || p.Detail != n.Detail:
				fe = append(fe, keyed{k, findingEvent(at, FindingChanged, id, n)})
			}
		}
		for k, p := range pf {
			if _, ok := nf[k]; !ok {
				fe = append(fe, keyed{k, findingEvent(at, gone, id, p)})
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
	return Event{At: at, Kind: k, Install: install, ID: f.ID, Code: f.Code, Severity: f.Severity, Resource: f.Resource, Text: f.Message, Detail: f.Detail}
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

func checks(ic view.InstallCheck) map[string]view.CheckStatus {
	m := map[string]view.CheckStatus{}
	for _, cs := range ic.Checks {
		m[cs.Name] = cs
	}
	return m
}

// decided reports whether cs ran and decided, neither pending nor holding: only such a check confirms a finding is gone.
func decided(cs view.CheckStatus) bool { return cs.Ran() && !cs.Pending && cs.Hold == nil }

// ranSet is the set of checks of ic that ran, held or pending included: any may have raised a finding.
func ranSet(ic view.InstallCheck) map[string]bool {
	m := map[string]bool{}
	for _, cs := range ic.Checks {
		if cs.Ran() {
			m[cs.Name] = true
		}
	}
	return m
}

// decidedSet is the set of checks of ic that decided.
func decidedSet(ic view.InstallCheck) map[string]bool {
	m := map[string]bool{}
	for _, cs := range ic.Checks {
		if decided(cs) {
			m[cs.Name] = true
		}
	}
	return m
}

// decidedStill reports whether every check in before also decided in ic.
func decidedStill(before map[string]bool, ic view.InstallCheck) bool {
	now := decidedSet(ic)
	for name := range before {
		if !now[name] {
			return false
		}
	}
	return true
}

// notCheckedReason is the engine's reason and the check's error, either may be empty.
func notCheckedReason(cs view.CheckStatus) string {
	if cs.Unavailable != "" && cs.Error != "" {
		return cs.Unavailable + ": " + cs.Error
	}
	return cs.Unavailable + cs.Error
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
// A finding gone while a check that ran during its presence (held or pending too) does not
// decide gets LostAt; it clears once every such check decides without it.
type Tracker struct {
	prev    *view.Check
	entries map[string]*tracked
	// down is the checks, by install and name, not checked since they last decided.
	down map[string]bool
}

type tracked struct {
	f       view.ObservedFinding
	install string
	// transitioned is set once the finding is raised late, cleared, lost, or changes severity, message or detail.
	transitioned bool
	// need is every check that ran while the finding was present; it resets once cleared.
	need map[string]bool
}

// Add records c at time at and returns the events since the previous model.
func (t *Tracker) Add(c view.Check, at time.Time) []Event {
	if t.entries == nil {
		t.entries, t.down = map[string]*tracked{}, map[string]bool{}
	}
	first := t.prev == nil
	var events []Event
	if !first {
		events = Diff(*t.prev, c, at)
	}
	// A check back from not checked is available once it decides, even through pending models.
	events = slices.DeleteFunc(events, func(e Event) bool { return e.Kind == CheckAvailable })
	var extra []Event
	for _, ic := range c.Installs {
		for _, cs := range ic.Checks {
			key := ic.Install.ID + "\x00" + cs.Name
			switch {
			case cs.NotChecked():
				t.down[key] = true
			case decided(cs) && t.down[key]:
				delete(t.down, key)
				extra = append(extra, Event{At: at, Kind: CheckAvailable, Install: ic.Install.ID, Check: cs.Name})
			}
		}
	}
	seen := map[string]bool{}
	for _, ic := range c.Installs {
		d := ranSet(ic)
		for _, f := range ic.Findings {
			key := ic.Install.ID + "\x00" + f.ID
			seen[key] = true
			e := t.entries[key]
			if e == nil {
				e = &tracked{f: view.ObservedFinding{ID: f.ID, Install: ic.Install.ID, Code: f.Code, Severity: f.Severity, Resource: f.Resource,
					Message: f.Message, Detail: f.Detail, FirstSeen: at, Severities: []view.SeverityAt{{Severity: f.Severity, At: at}}}, install: ic.Install.ID, transitioned: !first}
				t.entries[key] = e
			}
			if e.need == nil || e.f.ClearedAt != nil {
				e.need = map[string]bool{}
			}
			for name := range d {
				e.need[name] = true
			}
			e.f.Evidence, e.f.NextSteps, e.f.Fix = f.Evidence, f.NextSteps, f.Fix
			e.f.ClearedAt, e.f.LostAt = nil, nil
			if e.f.Message != f.Message || e.f.Detail != f.Detail {
				e.f.Message, e.f.Detail = f.Message, f.Detail
				e.transitioned = true
			}
			if e.f.Severity != f.Severity {
				e.f.Severity = f.Severity
				e.f.Severities = append(e.f.Severities, view.SeverityAt{Severity: f.Severity, At: at})
				e.transitioned = true
			}
		}
	}
	byID := installs(c)
	// verdict is the tracker's kind for a finding Diff saw disappear: its need spans the whole presence.
	verdict := map[string]EventKind{}
	for key, e := range t.entries {
		if seen[key] || e.f.ClearedAt != nil {
			continue
		}
		ic, ok := byID[e.install]
		switch {
		case ok && decidedStill(e.need, ic):
			verdict[key] = FindingCleared
			// Diff emits no event for a finding already gone: the lost one clears here.
			if e.f.LostAt != nil {
				extra = append(extra, Event{At: at, Kind: FindingCleared, Install: e.install, ID: e.f.ID, Code: e.f.Code,
					Severity: e.f.Severity, Resource: e.f.Resource, Text: e.f.Message, Detail: e.f.Detail})
			}
			e.f.ClearedAt, e.f.LostAt = new(at), nil
			e.transitioned = true
		case e.f.LostAt == nil:
			verdict[key] = FindingUnobserved
			e.f.LostAt = new(at)
			e.transitioned = true
		}
	}
	if first {
		t.prev = &c
		return nil
	}
	for i, e := range events {
		if k, ok := verdict[e.Install+"\x00"+e.ID]; ok && (e.Kind == FindingCleared || e.Kind == FindingUnobserved) {
			events[i].Kind = k
		}
	}
	t.prev = &c
	return mergeEvents(events, extra)
}

// mergeEvents adds the tracker's events to Diff's, keeping install then kind order and input events last.
func mergeEvents(events, extra []Event) []Event {
	slices.SortFunc(extra, func(a, b Event) int {
		return cmp.Or(cmp.Compare(a.Install, b.Install), cmp.Compare(a.Check, b.Check), cmp.Compare(a.ID, b.ID))
	})
	out := append(events, extra...)
	isInput := func(e Event) bool { return e.Kind == InputAvailable || e.Kind == InputUnavailable }
	slices.SortStableFunc(out, func(a, b Event) int {
		if ia, ib := isInput(a), isInput(b); ia != ib {
			if ia {
				return 1
			}
			return -1
		} else if ia {
			return 0
		}
		return cmp.Or(cmp.Compare(a.Install, b.Install), cmp.Compare(a.Kind, b.Kind))
	})
	return out
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
		f.Evidence, f.NextSteps = slices.Clone(f.Evidence), slices.Clone(f.NextSteps)
		out = append(out, f)
	}
	return out
}
