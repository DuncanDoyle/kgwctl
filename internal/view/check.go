package view

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/diagnose"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/DuncanDoyle/snellius/pkg/objects"

	"github.com/DuncanDoyle/kgwctl/internal/load"
)

// Check is the output of check (design §3.3, §7). It is kgwctl's own view; no Snellius type is marshalled.
type Check struct {
	Context     string         `json:"context"`
	GeneratedAt time.Time      `json:"generatedAt"`
	Installs    []InstallCheck `json:"installs"`
	// Incomplete are the inputs the report depends on that are not ok (design §6.3).
	Incomplete []load.Input `json:"incomplete,omitempty"`
	// Observation is set by --live only.
	Observation *Observation `json:"observation,omitempty"`
}

// InstallRef identifies one install.
type InstallRef struct {
	ID        string `json:"id"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Version   string `json:"version,omitempty"`
}

// Counts is the number of shown findings per severity.
type Counts struct {
	Error   int `json:"error"`
	Warning int `json:"warning"`
	Info    int `json:"info"`
}

// InstallCheck is the report of one install.
type InstallCheck struct {
	Install InstallRef `json:"install"`
	// State is the engine's roll-up and ignores any namespace filter.
	State    string         `json:"state"`
	Counts   Counts         `json:"counts"`
	Findings []CheckFinding `json:"findings"`
	Groups   []CheckGroup   `json:"groups"`
	Checks   []CheckStatus  `json:"checks"`
}

// CheckGroup is a diagnosis group as the library built it, with the IDs of its shown findings.
type CheckGroup struct {
	ID       string       `json:"id"`
	Cause    *objects.Ref `json:"cause,omitempty"`
	Findings []string     `json:"findings"`
}

// CheckFinding is one finding of an install.
type CheckFinding struct {
	ID       string       `json:"id"`
	Code     string       `json:"code"`
	Slug     string       `json:"slug"`
	Severity string       `json:"severity"`
	Resource *objects.Ref `json:"resource,omitempty"`
	Message  string       `json:"message"`
	Detail   string       `json:"detail,omitempty"`
	Since    *time.Time   `json:"since,omitempty"`
	Group    string       `json:"group,omitempty"`
	// Evidence is the library's typed evidence, passed through as JSON.
	Evidence  json.RawMessage `json:"evidence,omitempty"`
	NextSteps []NextStep      `json:"nextSteps,omitempty"`
	Fix       *Fix            `json:"fix,omitempty"`
}

// NextStep is a command that helps to learn more about a finding.
type NextStep struct {
	Description string `json:"description"`
	Command     string `json:"command"`
	// Note is a caution about the command's output.
	Note string `json:"note,omitempty"`
}

// Fix is the one change the finding's evidence leaves: a new object, or a fragment at Path of Target.
type Fix struct {
	Title  string      `json:"title"`
	Target objects.Ref `json:"target"`
	Create bool        `json:"create,omitempty"`
	Path   string      `json:"path,omitempty"`
	YAML   string      `json:"yaml"`
	Basis  string      `json:"basis"`
}

// CheckStatus is whether one check could collect its input.
type CheckStatus struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Pending bool   `json:"pending"`
	// Unavailable is the engine's reason: off, denied, failing, not-on-version or not-observed.
	Unavailable string `json:"unavailable"`
	Error       string `json:"error,omitempty"`
	Detail      string `json:"detail,omitempty"`
	Hold        *Hold  `json:"hold,omitempty"`
}

// NotChecked reports whether the check could not run: its input is unavailable,
// or it failed with an error (a 403 from a scrape is OK=false with Error and no Unavailable).
// A pending or holding check is still deciding, never not checked.
func (c CheckStatus) NotChecked() bool {
	if c.Pending || c.Hold != nil {
		return false
	}
	return c.Unavailable != "" || !c.OK && c.Error != ""
}

// Passed reports whether the check is OK, holds nothing and was checked.
func (c CheckStatus) Passed() bool { return c.OK && c.Hold == nil && !c.NotChecked() }

// Hold says a check is still deciding on some items.
type Hold struct {
	Until  time.Time `json:"until"`
	Count  int       `json:"count"`
	Reason string    `json:"reason"`
}

// Observation is what --live saw during the run.
type Observation struct {
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
	// StopReason is timeout or interrupted.
	StopReason string            `json:"stopReason"`
	Observed   []ObservedFinding `json:"observed"`
}

// ObservedFinding is a finding seen during a --live run, cleared or not.
type ObservedFinding struct {
	ID         string       `json:"id"`
	Code       string       `json:"code"`
	Severity   string       `json:"severity"`
	Resource   *objects.Ref `json:"resource,omitempty"`
	FirstSeen  time.Time    `json:"firstSeen"`
	ClearedAt  *time.Time   `json:"clearedAt,omitempty"`
	Severities []SeverityAt `json:"severities"`
}

// SeverityAt is the severity a finding had from At on.
type SeverityAt struct {
	Severity string    `json:"severity"`
	At       time.Time `json:"at"`
}

// CheckSelect is what check selects (design §6).
type CheckSelect struct {
	// Context is the kube context name, put into next-step commands.
	Context string
	// Namespace keeps findings on that namespace's objects, and those without a namespace; "" keeps all.
	Namespace string
	// Install is the --install value: an install ID or namespace/name; "" keeps all.
	Install string
	// AllControllers is carried for the command; every detected install is shown either way.
	AllControllers bool
	// ObservedFrom is when the command started observing. A since not before it is only
	// when kgwctl first looked, so it is left out; zero keeps every since.
	ObservedFrom time.Time
}

// Validate returns the error of a malformed Install selector, for the command to report as a usage error.
func (s CheckSelect) Validate() error {
	_, err := install.ParsePinned(s.Install)
	return err
}

// ExitCode is 1 when a shown finding is an error or warning, else 0. The
// command decides exit 2 (design §3.4).
func (c Check) ExitCode() int {
	for _, ic := range c.Installs {
		if ic.Counts.Error > 0 || ic.Counts.Warning > 0 {
			return 1
		}
	}
	return 0
}

// BuildCheck maps m to the check view. Findings are ordered by severity, then
// diagnosis order; next steps come from the install's registry hooks.
func BuildCheck(m *engine.Model, reg *extension.Registry, sel CheckSelect) Check {
	c := Check{Context: sel.Context, GeneratedAt: m.ComputedAt.UTC(), Installs: []InstallCheck{}}
	pinned, err := install.ParsePinned(sel.Install)
	if err != nil {
		// A malformed selector selects nothing; the command reports it via Validate.
		return c
	}
	for i := range m.Installs {
		im := &m.Installs[i]
		if pinned == "" || im.Resolved.ID() == pinned {
			c.Installs = append(c.Installs, installCheck(m, im, reg, sel))
		}
	}
	slices.SortFunc(c.Installs, func(a, b InstallCheck) int { return cmp.Compare(a.Install.ID, b.Install.ID) })
	return c
}

// sevRank orders severities worst first; an unknown severity sorts last.
func sevRank(s health.Severity) int {
	switch s {
	case health.SeverityError:
		return 0
	case health.SeverityWarning:
		return 1
	case health.SeverityInfo:
		return 2
	}
	return 3
}

func installCheck(m *engine.Model, im *engine.InstallModel, reg *extension.Registry, sel CheckSelect) InstallCheck {
	r := im.Resolved
	ic := InstallCheck{
		Install:  InstallRef{ID: r.ID(), Namespace: r.Namespace, Name: r.Name, Version: r.Version},
		State:    string(im.Health.State),
		Findings: []CheckFinding{},
		Groups:   []CheckGroup{},
		Checks:   []CheckStatus{},
	}
	fs := im.Health.Findings
	// pos is a finding's place in diagnosis order; groups index fs, so resolve them before filtering.
	pos := make([]int, len(fs))
	for i := range pos {
		pos[i] = i
	}
	if d := im.Diagnosis; d != nil {
		for p, i := range d.Rank {
			if i >= 0 && i < len(pos) {
				pos[i] = p
			}
		}
	}
	var cmds diagnose.Commands
	if reg != nil {
		cmds = reg.ImplFor(r.Install).Commands
	}
	if cmds != nil && sel.Context != "" {
		cmds = contextCommands{cmds, sel.Context}
	}
	in := diagnose.Input{Install: r, Owned: im.Owned, Snapshot: m.Snapshot, Served: m.Served}

	kept := make([]int, 0, len(fs))
	for i := range fs {
		if inNamespace(fs[i], sel.Namespace) {
			kept = append(kept, i)
		}
	}
	slices.SortStableFunc(kept, func(a, b int) int {
		return cmp.Or(cmp.Compare(sevRank(fs[a].Severity), sevRank(fs[b].Severity)), cmp.Compare(pos[a], pos[b]))
	})
	for _, i := range kept {
		f := &fs[i]
		ic.Findings = append(ic.Findings, checkFinding(f, in, cmds, sel.ObservedFrom))
		switch f.Severity {
		case health.SeverityError:
			ic.Counts.Error++
		case health.SeverityWarning:
			ic.Counts.Warning++
		case health.SeverityInfo:
			ic.Counts.Info++
		}
	}
	if d := im.Diagnosis; d != nil {
		keep := map[int]bool{}
		for _, i := range kept {
			keep[i] = true
		}
		for _, g := range d.Groups {
			cg := CheckGroup{ID: g.ID, Cause: g.Cause, Findings: []string{}}
			for _, i := range g.Members {
				if keep[i] {
					cg.Findings = append(cg.Findings, fs[i].ID)
				}
			}
			if len(cg.Findings) > 0 {
				ic.Groups = append(ic.Groups, cg)
			}
		}
	}
	for _, cr := range im.Health.Checks {
		ic.Checks = append(ic.Checks, checkStatus(cr))
	}
	slices.SortStableFunc(ic.Checks, func(a, b CheckStatus) int { return cmp.Compare(a.Name, b.Name) })
	return ic
}

// inNamespace keeps a finding without a namespaced resource, and one in ns; "" keeps all.
func inNamespace(f health.Finding, ns string) bool {
	return ns == "" || f.Resource == nil || f.Resource.Namespace == "" || f.Resource.Namespace == ns
}

// checkFinding maps f; in and cmds build its next steps. A since not before from is dropped.
func checkFinding(f *health.Finding, in diagnose.Input, cmds diagnose.Commands, from time.Time) CheckFinding {
	cf := CheckFinding{ID: f.ID, Code: f.Code, Slug: f.Slug, Severity: string(f.Severity), Resource: f.Resource,
		Message: f.Message, Detail: f.Detail, Group: f.Group}
	if f.Since != nil && (from.IsZero() || f.Since.Before(from)) {
		cf.Since = new(f.Since.UTC())
	}
	if f.Facts != nil {
		// An evidence that cannot be marshalled is left out rather than failing the report.
		if b, err := json.Marshal(f.Facts); err == nil && string(b) != "null" && string(b) != "{}" {
			cf.Evidence = b
		}
	}
	if cmds != nil {
		fs := []health.Finding{*f}
		// Steps reads members by index: pass this finding alone at index 0.
		for _, s := range diagnose.Steps(diagnose.Group{Members: []int{0}}, fs, in, cmds, nil) {
			cf.NextSteps = append(cf.NextSteps, NextStep{Description: s.Title, Command: s.Command, Note: s.Note})
		}
	}
	if fx := diagnose.Fix(f); fx != nil {
		cf.Fix = &Fix{Title: fx.Title, Target: fx.Target, Create: fx.Create, Path: fx.Path, YAML: fx.YAML, Basis: fx.Basis}
	}
	return cf
}

func checkStatus(cr health.CheckResult) CheckStatus {
	cs := CheckStatus{Name: cr.Name, OK: cr.OK, Pending: cr.Pending, Unavailable: string(cr.Unavailable), Error: cr.Error, Detail: cr.Detail}
	if h := cr.Hold; h != nil {
		cs.Hold = &Hold{Until: h.Until.UTC(), Count: h.Count, Reason: h.Reason}
	}
	return cs
}

// contextCommands passes the kube context to every kubectl command of the wrapped Commands.
type contextCommands struct {
	diagnose.Commands
	context string
}

func (c contextCommands) ControllerLog(pod objects.Ref, since time.Duration, grep ...string) string {
	return c.with(c.Commands.ControllerLog(pod, since, grep...))
}

func (c contextCommands) ProxyLog(proxy objects.Ref, since time.Duration, grep string) string {
	return c.with(c.Commands.ProxyLog(proxy, since, grep))
}

func (c contextCommands) ProxyCounters(proxy objects.Ref) string {
	return c.with(c.Commands.ProxyCounters(proxy))
}

func (c contextCommands) PreviousLog(pod objects.Ref, container string) string {
	return c.with(c.Commands.PreviousLog(pod, container))
}

// with adds --context after each leading "kubectl" word, one command per line.
func (c contextCommands) with(cmd string) string {
	lines := strings.Split(cmd, "\n")
	for i, l := range lines {
		if rest, ok := strings.CutPrefix(l, "kubectl "); ok {
			lines[i] = "kubectl --context " + diagnose.Quote(c.context) + " " + rest
		}
	}
	return strings.Join(lines, "\n")
}
