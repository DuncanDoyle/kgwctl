// Package bundle holds the support-bundle manifest and, in later files, the
// writer and runner behind the public contract in pkg/bundle.
package bundle

import (
	"io"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/DuncanDoyle/kgwctl/internal/print"
	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// ManifestKind is the envelope kind of manifest.json.
const ManifestKind = "BundleManifest"

// Bounds that Bound enforces.
const (
	maxSelectors    = 64
	maxExcluded     = 1024
	maxString       = 256
	maxKind         = 320 // group (253) + "/" + kind (63)
	maxMinimalIDs   = 64
	maxMinimalFlags = 32
)

// BuildInfo is the kgwctl build that wrote the bundle.
type BuildInfo struct {
	Version string `json:"version,omitempty"`
	Commit  string `json:"commit,omitempty"`
	Date    string `json:"date,omitempty"`
}

// FlagValue is one flag of the run. Value is nil for flags recorded by name
// only.
type FlagValue struct {
	Name  string  `json:"name"`
	Value *string `json:"value,omitempty"`
}

// CollectorEntry is the outcome of one collector.
type CollectorEntry struct {
	Name   string             `json:"name"`
	Status pb.CollectorStatus `json:"status"`
	// Reason says why a collector was skipped, for example "flag" or
	// "no install".
	Reason     string `json:"reason,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
	Error      string `json:"error,omitempty"`
	// Late counts results that arrived after the seal.
	Late int `json:"late,omitempty"`
}

// Exclusion counts objects left out of the bundle, per kind and reason.
type Exclusion struct {
	// Kind is group/kind, with core as the group of the core API group.
	Kind   string           `json:"kind"`
	Reason pb.ExcludeReason `json:"reason"`
	Count  int              `json:"count"`
}

// Item is one file in the bundle, or one that was meant to be.
type Item struct {
	Path        string        `json:"path"`
	Size        int64         `json:"size,omitempty"`
	ContentType string        `json:"contentType,omitempty"`
	CapturedAt  time.Time     `json:"capturedAt,omitzero"`
	Source      pb.Source     `json:"source,omitzero"`
	Status      pb.ItemStatus `json:"status"`
	Reason      string        `json:"reason,omitempty"`
	Redactions  int           `json:"redactions,omitempty"`
}

// Manifest is the content of manifest.json.
type Manifest struct {
	Build        BuildInfo        `json:"build,omitzero"`
	Context      string           `json:"context,omitempty"`
	Server       string           `json:"server,omitempty"`
	Flags        []FlagValue      `json:"flags,omitempty"`
	Scope        pb.Scope         `json:"scope,omitzero"`
	Window       pb.Window        `json:"window,omitzero"`
	Start        time.Time        `json:"start,omitzero"`
	Sealed       time.Time        `json:"sealed,omitzero"`
	End          time.Time        `json:"end,omitzero"`
	Finalization string           `json:"finalization,omitempty"`
	Replicas     []pb.Replica     `json:"replicas,omitempty"`
	Collectors   []CollectorEntry `json:"collectors,omitempty"`
	Items        []Item           `json:"items,omitempty"`
	Excluded     []Exclusion      `json:"excluded,omitempty"`

	ReplicasOmitted   int  `json:"replicasOmitted,omitempty"`
	CollectorsOmitted int  `json:"collectorsOmitted,omitempty"`
	ItemsOmitted      int  `json:"itemsOmitted,omitempty"`
	Truncated         bool `json:"truncated,omitempty"`
}

// WriteManifest writes m as a JSON envelope of kind BundleManifest.
func WriteManifest(w io.Writer, m Manifest) error {
	return print.Envelope(w, print.FormatJSON, ManifestKind, m)
}

type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

func size(m *Manifest) int64 {
	var c countWriter
	_ = WriteManifest(&c, *m)
	return c.n
}

// cut shortens s to at most n bytes on a rune boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// cutEscaped shortens s until its JSON encoding, where <, > and & take six
// bytes, is at most n bytes.
func cutEscaped(s string, n int) string {
	w := 0
	for i := 0; i < len(s); i++ {
		w++
		if c := s[i]; c == '<' || c == '>' || c == '&' {
			w += 5
		}
		if w > n {
			return cut(s, i)
		}
	}
	return s
}

// Bound limits m so that its encoding is at most max bytes (no limit when max
// is not positive). It drops list entries in this order, recording each drop
// in an omitted count: items, replicas, workload namespaces, unwatched
// namespace names, selectors, installs, collectors. When that is not enough it
// replaces m with a minimal manifest marked Truncated. Every field of the
// minimal manifest is capped, so it stays under 1 MiB, below the smallest
// manifest cap.
func Bound(m *Manifest, max int64) {
	capStrings(m)
	capExcluded(m)
	if max <= 0 || size(m) <= max {
		return
	}

	orig := *m
	items, itemsOm := m.Items, m.ItemsOmitted
	fit(m, max, len(items), func(k int) {
		m.Items, m.ItemsOmitted = items[:k], itemsOm+len(items)-k
	})
	reps, repsOm := m.Replicas, m.ReplicasOmitted
	fit(m, max, len(reps), func(k int) {
		m.Replicas, m.ReplicasOmitted = reps[:k], repsOm+len(reps)-k
	})
	wns, wnsOm := m.Scope.WorkloadNamespaces, m.Scope.WorkloadNamespacesOmitted
	fit(m, max, len(wns), func(k int) {
		m.Scope.WorkloadNamespaces, m.Scope.WorkloadNamespacesOmitted = wns[:k], wnsOm+len(wns)-k
	})
	fitInstallLists(m, max, func(d *pb.DiscoveryScope) (*[]string, *int) { return &d.Unwatched, &d.UnwatchedOmitted })
	fitInstallLists(m, max, func(d *pb.DiscoveryScope) (*[]string, *int) { return &d.Selectors, &d.SelectorsOmitted })
	ins, insOm := m.Scope.Installs, m.Scope.InstallsOmitted
	fit(m, max, len(ins), func(k int) {
		m.Scope.Installs, m.Scope.InstallsOmitted = ins[:k], insOm+len(ins)-k
	})
	cols, colsOm := m.Collectors, m.CollectorsOmitted
	fit(m, max, len(cols), func(k int) {
		m.Collectors, m.CollectorsOmitted = cols[:k], colsOm+len(cols)-k
	})

	if size(m) > max {
		*m = minimal(orig)
	}
}

// fit finds the longest prefix length k of a list of n entries for which m
// fits max, and applies it with set. When even k=0 does not fit, it leaves the
// list empty. It does nothing when m already fits.
func fit(m *Manifest, max int64, n int, set func(k int)) {
	if n == 0 || size(m) <= max {
		return
	}
	set(0)
	if size(m) > max {
		return
	}
	lo, hi := 0, n-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		set(mid)
		if size(m) <= max {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	set(lo)
}

// fitInstallLists caps one per-install list, chosen by field, to the same
// length K on every install, with K as large as still fits max.
func fitInstallLists(m *Manifest, max int64, field func(*pb.DiscoveryScope) (*[]string, *int)) {
	orig := slices.Clone(m.Scope.Installs)
	longest := 0
	for i := range orig {
		l, _ := field(&orig[i].Discovery)
		longest = max2(longest, len(*l))
	}
	fit(m, max, longest, func(k int) {
		ins := slices.Clone(orig)
		for i := range ins {
			l, om := field(&ins[i].Discovery)
			if len(*l) > k {
				*om += len(*l) - k
				*l = (*l)[:k]
			}
		}
		m.Scope.Installs = ins
	})
}

func max2(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// capStrings applies the per-string and per-install caps. Selectors get the
// pattern pass before the cut, since a valid label value can hold a credential.
func capStrings(m *Manifest) {
	m.Build.Version = cut(m.Build.Version, maxString)
	m.Build.Commit = cut(m.Build.Commit, maxString)
	m.Build.Date = cut(m.Build.Date, maxString)
	ins := slices.Clone(m.Scope.Installs)
	for i := range ins {
		d := &ins[i].Discovery
		if len(d.Selectors) > maxSelectors {
			d.SelectorsOmitted += len(d.Selectors) - maxSelectors
			d.Selectors = d.Selectors[:maxSelectors]
		}
		sel := make([]string, len(d.Selectors))
		for j, s := range d.Selectors {
			sel[j] = cut(redact.Pattern(s), maxString)
		}
		if len(sel) > 0 {
			d.Selectors = sel
		}
	}
	if ins != nil {
		m.Scope.Installs = ins
	}
}

// capExcluded keeps Excluded within maxExcluded entries by summing the overflow
// into one "other" entry per reason, so no count is lost. The writer accepts
// two reasons; with more reasons than entries it keeps only "other" entries.
func capExcluded(m *Manifest) {
	if len(m.Excluded) > 0 {
		ex := slices.Clone(m.Excluded)
		for i := range ex {
			ex[i].Kind = cutEscaped(ex[i].Kind, maxKind)
		}
		m.Excluded = ex
	}
	if len(m.Excluded) <= maxExcluded {
		return
	}
	var reasons []pb.ExcludeReason
	for _, e := range m.Excluded {
		if !slices.Contains(reasons, e.Reason) {
			reasons = append(reasons, e.Reason)
		}
	}
	keep := max(maxExcluded-len(reasons), 0)
	other := map[pb.ExcludeReason]int{}
	var order []pb.ExcludeReason
	for _, e := range m.Excluded[keep:] {
		if _, ok := other[e.Reason]; !ok {
			order = append(order, e.Reason)
		}
		other[e.Reason] += e.Count
	}
	out := slices.Clone(m.Excluded[:keep])
	for _, r := range order {
		out = append(out, Exclusion{Kind: "other", Reason: r, Count: other[r]})
	}
	m.Excluded = out
}

// minimal is the fallback manifest: everything that identifies the run and
// accounts for what was left out, with every field capped.
func minimal(m Manifest) Manifest {
	out := Manifest{
		Build:             BuildInfo{Version: cut(m.Build.Version, maxString), Commit: cut(m.Build.Commit, maxString), Date: cut(m.Build.Date, maxString)},
		Start:             m.Start,
		Sealed:            m.Sealed,
		End:               m.End,
		Finalization:      cut(m.Finalization, maxString),
		Excluded:          m.Excluded,
		ReplicasOmitted:   m.ReplicasOmitted + len(m.Replicas),
		CollectorsOmitted: m.CollectorsOmitted + len(m.Collectors),
		ItemsOmitted:      m.ItemsOmitted + len(m.Items),
		Truncated:         true,
	}
	for i, f := range m.Flags {
		if i == maxMinimalFlags {
			break
		}
		fv := FlagValue{Name: cut(f.Name, maxString)}
		if f.Value != nil {
			v := cut(*f.Value, maxString)
			fv.Value = &v
		}
		out.Flags = append(out.Flags, fv)
	}
	out.Scope.Namespace = cut(m.Scope.Namespace, maxString)
	out.Scope.InstallsOmitted = m.Scope.InstallsOmitted
	for i, in := range m.Scope.Installs {
		if i == maxMinimalIDs {
			out.Scope.InstallsOmitted += len(m.Scope.Installs) - maxMinimalIDs
			break
		}
		out.Scope.Installs = append(out.Scope.Installs, pb.Install{
			Namespace:  cut(in.Namespace, maxString),
			Deployment: cut(in.Deployment, maxString),
		})
	}
	return out
}
