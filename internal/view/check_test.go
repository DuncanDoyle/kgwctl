package view_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/diagnose"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/DuncanDoyle/snellius/pkg/objects"

	"github.com/DuncanDoyle/kgwctl/internal/view"
)

var computedAt = time.Date(2026, 11, 10, 14, 2, 10, 0, time.UTC)

func ref(kind, ns, name string) *objects.Ref {
	return &objects.Ref{Group: "gateway.networking.k8s.io", Kind: kind, Namespace: ns, Name: name}
}

func finding(id, code string, sev health.Severity, res *objects.Ref) health.Finding {
	return health.Finding{ID: id, Code: code, Slug: "slug-" + id, Severity: sev, Resource: res, Message: "m " + id}
}

// checkModel is a one-install model over rep and d.
func checkModel(rep health.Report, d *diagnose.Diagnosis) *engine.Model {
	res := install.Resolved{Install: install.Install{Namespace: "kgateway-system", Name: "kgateway", Version: "v2.5.1"}}
	return &engine.Model{ComputedAt: computedAt, Installs: []engine.InstallModel{{Resolved: res, Health: rep, Diagnosis: d}}}
}

func build(m *engine.Model, sel view.CheckSelect) view.Check {
	return view.BuildCheck(m, extension.New(), sel)
}

func ids(fs []view.CheckFinding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.ID)
	}
	return out
}

func TestBuildCheckOrdersBySeverity(t *testing.T) {
	rep := health.Report{State: health.StateError, Findings: []health.Finding{
		finding("i1", "X-1", health.SeverityInfo, nil),
		finding("w1", "X-2", health.SeverityWarning, nil),
		finding("e1", "X-3", health.SeverityError, nil),
		finding("w2", "X-4", health.SeverityWarning, nil),
	}, Checks: []health.CheckResult{{Name: "b", OK: true}, {Name: "a", OK: true}}}
	// Diagnosis order puts w2 before w1.
	d := &diagnose.Diagnosis{Rank: []int{3, 0, 1, 2}}
	c := build(checkModel(rep, d), view.CheckSelect{Context: "prod"})
	if len(c.Installs) != 1 {
		t.Fatalf("installs = %d", len(c.Installs))
	}
	ic := c.Installs[0]
	if got := strings.Join(ids(ic.Findings), ","); got != "e1,w2,w1,i1" {
		t.Errorf("order = %s", got)
	}
	if ic.Counts != (view.Counts{Error: 1, Warning: 2, Info: 1}) || ic.State != "error" {
		t.Errorf("counts/state = %+v %s", ic.Counts, ic.State)
	}
	if ic.Checks[0].Name != "a" || ic.Checks[1].Name != "b" {
		t.Errorf("checks not sorted: %+v", ic.Checks)
	}
	if c.Context != "prod" || !c.GeneratedAt.Equal(computedAt) || ic.Install.ID != "kgateway-system.kgateway" || ic.Install.Version != "v2.5.1" {
		t.Errorf("header = %+v", c)
	}
	if c.ExitCode() != 1 {
		t.Errorf("exit = %d", c.ExitCode())
	}
}

func TestBuildCheckNamespaceFilter(t *testing.T) {
	rep := health.Report{State: health.StateWarning, Findings: []health.Finding{
		finding("a", "X-1", health.SeverityWarning, ref("HTTPRoute", "shop", "r")),
		finding("b", "X-2", health.SeverityInfo, ref("HTTPRoute", "other", "r")),
		finding("c", "X-3", health.SeverityInfo, nil),
		finding("d", "X-4", health.SeverityInfo, &objects.Ref{Group: "gateway.networking.k8s.io", Kind: "GatewayClass", Name: "kgw"}),
	}}
	ic := build(checkModel(rep, nil), view.CheckSelect{Namespace: "other"}).Installs[0]
	if got := strings.Join(ids(ic.Findings), ","); got != "b,c,d" {
		t.Errorf("kept = %s", got)
	}
	if ic.Counts != (view.Counts{Info: 3}) || ic.State != "warning" {
		t.Errorf("counts/state = %+v %s", ic.Counts, ic.State)
	}
	c := build(checkModel(rep, nil), view.CheckSelect{Namespace: "other"})
	if c.ExitCode() != 0 {
		t.Errorf("exit with only info shown = %d", c.ExitCode())
	}
}

func TestBuildCheckHold(t *testing.T) {
	until := time.Date(2026, 11, 10, 15, 7, 12, 0, time.FixedZone("x", 3600))
	rep := health.Report{Checks: []health.CheckResult{
		{Name: "status progress", OK: true, Hold: &health.Hold{Until: until, Count: 12, Reason: "Waiting"}},
		{Name: "plain", OK: true},
	}}
	cs := build(checkModel(rep, nil), view.CheckSelect{}).Installs[0].Checks
	h := cs[1].Hold // sorted: "plain", "status progress"
	if cs[0].Hold != nil || h == nil || h.Count != 12 || h.Reason != "Waiting" || h.Until.Location() != time.UTC || !h.Until.Equal(until) {
		t.Fatalf("hold = %+v", cs)
	}
	if cs[1].Passed() {
		t.Error("a held check must not count as passed")
	}
	if !cs[0].Passed() {
		t.Error("plain OK check should pass")
	}
}

func TestBuildCheckUnavailable(t *testing.T) {
	rep := health.Report{Checks: []health.CheckResult{
		{Name: "a", Unavailable: health.AvailDenied, Error: "denied: get pods/proxy"},
		{Name: "b", OK: false, Error: "403 forbidden"},
		{Name: "c", OK: false, Pending: true},
		{Name: "d", OK: true},
		// Pending or held with an error text is still deciding, not "not checked".
		{Name: "e", OK: false, Pending: true, Error: "1 resource is being processed by the controller"},
		{Name: "f", OK: false, Error: "waiting", Hold: &health.Hold{Until: computedAt, Count: 1, Reason: "r"}},
	}}
	cs := build(checkModel(rep, nil), view.CheckSelect{}).Installs[0].Checks
	if cs[0].Unavailable != "denied" || cs[0].Error == "" {
		t.Errorf("raw fields not kept: %+v", cs[0])
	}
	for i, want := range []struct{ notChecked, passed bool }{{true, false}, {true, false}, {false, false}, {false, true}, {false, false}, {false, false}} {
		if cs[i].NotChecked() != want.notChecked || cs[i].Passed() != want.passed {
			t.Errorf("%s: NotChecked=%v Passed=%v", cs[i].Name, cs[i].NotChecked(), cs[i].Passed())
		}
	}
}

func TestBuildCheckGroups(t *testing.T) {
	svc := &objects.Ref{Kind: "Service", Namespace: "shop", Name: "cart-v2"}
	// Indexes 0 and 2 are filtered out of "shop"; g2 loses all its members.
	rep := health.Report{Findings: []health.Finding{
		finding("f0", "X-1", health.SeverityInfo, ref("HTTPRoute", "other", "a")),
		finding("f1", "X-2", health.SeverityInfo, ref("HTTPRoute", "shop", "b")),
		finding("f2", "X-3", health.SeverityWarning, ref("HTTPRoute", "other", "c")),
		finding("f3", "X-4", health.SeverityError, ref("HTTPRoute", "shop", "d")),
		finding("f4", "X-5", health.SeverityWarning, ref("HTTPRoute", "shop", "e")),
	}}
	for i, g := range []string{"g2", "g1", "g2", "g1", "g1"} {
		rep.Findings[i].Group = g
	}
	d := &diagnose.Diagnosis{Groups: []diagnose.Group{
		{ID: "g1", Cause: svc, Members: []int{1, 3, 4}},
		{ID: "g2", Members: []int{0, 2}},
	}, Rank: []int{4, 3, 1, 0, 2}}
	ic := build(checkModel(rep, d), view.CheckSelect{Namespace: "shop"}).Installs[0]
	// Severity first (f3 error, f4 warning, f1 info).
	if got := strings.Join(ids(ic.Findings), ","); got != "f3,f4,f1" {
		t.Errorf("findings = %s", got)
	}
	if len(ic.Groups) != 1 {
		t.Fatalf("groups = %+v", ic.Groups)
	}
	g := ic.Groups[0]
	// Members stay in the group's own order.
	if g.ID != "g1" || g.Cause == nil || g.Cause.Name != "cart-v2" || strings.Join(g.Findings, ",") != "f1,f3,f4" {
		t.Errorf("group = %+v", g)
	}
	for _, f := range ic.Findings {
		if f.Group != "g1" {
			t.Errorf("%s group = %q", f.ID, f.Group)
		}
	}
	// A namespace that keeps nothing of g1 drops it.
	ic = build(checkModel(rep, d), view.CheckSelect{Namespace: "none"}).Installs[0]
	if got := strings.Join(ids(ic.Findings), ","); got != "" || len(ic.Groups) != 0 {
		t.Errorf("findings = %s, groups = %+v", got, ic.Groups)
	}
}

func TestBuildCheckFix(t *testing.T) {
	core := ""
	f := finding("f", health.CodeRefUnresolved, health.SeverityWarning, ref("HTTPRoute", "shop", "r"))
	f.Facts = &health.RefsEvidence{Explanation: health.RefExplainKindNotServed, Attributed: true, Group: &core,
		Candidates: []health.RefLocation{{Path: "spec.rules[0].backendRefs[0]", Ref: objects.Ref{Kind: "Service", Namespace: "shop", Name: "cart"}}}}
	other := finding("g", "X-1", health.SeverityInfo, nil)
	ic := build(checkModel(health.Report{Findings: []health.Finding{f, other}}, nil), view.CheckSelect{}).Installs[0]
	var withFix, without view.CheckFinding
	for _, cf := range ic.Findings {
		if cf.ID == "f" {
			withFix = cf
		} else {
			without = cf
		}
	}
	if withFix.Fix == nil || withFix.Fix.Title == "" || withFix.Fix.Path != "spec.rules[0].backendRefs[0]" || withFix.Fix.YAML == "" {
		t.Errorf("fix = %+v", withFix.Fix)
	}
	if without.Fix != nil {
		t.Errorf("unexpected fix %+v", without.Fix)
	}
	if len(withFix.Evidence) == 0 || !strings.Contains(string(withFix.Evidence), `"kind":"refs"`) {
		t.Errorf("evidence = %s", withFix.Evidence)
	}
	if without.Evidence != nil {
		t.Errorf("evidence should be omitted: %s", without.Evidence)
	}
}

// A code and check name no table knows still map and marshal.
func TestBuildCheckUnknownCode(t *testing.T) {
	since := time.Date(2026, 11, 10, 15, 0, 0, 0, time.FixedZone("x", 3600))
	f := finding("v1", "EXAMPLE-001", health.SeverityWarning, ref("Gateway", "edge", "public"))
	f.Detail, f.Since = "detail", &since
	rep := health.Report{Findings: []health.Finding{f}, Checks: []health.CheckResult{{Name: "vendor thing", OK: true,
		Hold: &health.Hold{Until: computedAt, Count: 2, Reason: "r"}}}}
	c := build(checkModel(rep, nil), view.CheckSelect{})
	cf := c.Installs[0].Findings[0]
	if cf.Code != "EXAMPLE-001" || cf.Slug != "slug-v1" || cf.Detail != "detail" || cf.Since == nil || cf.Since.Location() != time.UTC {
		t.Fatalf("finding = %+v", cf)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"code":"EXAMPLE-001"`, `"since":"2026-11-10T14:00:00Z"`, `"name":"vendor thing"`, `"generatedAt":"2026-11-10T14:02:10Z"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("json lacks %s: %s", want, b)
		}
	}
	for _, absent := range []string{"evidence", "fix", "nextSteps", "observation", "incomplete"} {
		if strings.Contains(string(b), absent) {
			t.Errorf("json has %s: %s", absent, b)
		}
	}
}

func TestBuildCheckNextStepsContext(t *testing.T) {
	f := finding("s", "KGWUI-018", health.SeverityError, &objects.Ref{Group: "apps", Kind: "Deployment", Namespace: "kgateway-system", Name: "kgateway"})
	f.Facts = &health.StatusProgressEvidence{}
	m := checkModel(health.Report{Findings: []health.Finding{f}}, nil)
	steps := build(m, view.CheckSelect{Context: "prod"}).Installs[0].Findings[0].NextSteps
	if len(steps) == 0 || !strings.HasPrefix(steps[0].Command, "kubectl --context prod logs ") || steps[0].Description == "" {
		t.Fatalf("steps = %+v", steps)
	}
	steps = build(m, view.CheckSelect{}).Installs[0].Findings[0].NextSteps
	if len(steps) == 0 || strings.Contains(steps[0].Command, "--context") {
		t.Errorf("no context set, steps = %+v", steps)
	}
}

func TestBuildCheckInstallSelect(t *testing.T) {
	m := checkModel(health.Report{}, nil)
	if n := len(build(m, view.CheckSelect{Install: "kgateway-system/kgateway"}).Installs); n != 1 {
		t.Errorf("namespace/name select = %d", n)
	}
	if n := len(build(m, view.CheckSelect{Install: "kgateway-system.kgateway"}).Installs); n != 1 {
		t.Errorf("id select = %d", n)
	}
	if n := len(build(m, view.CheckSelect{Install: "x/y"}).Installs); n != 0 {
		t.Errorf("other install = %d", n)
	}
	for _, bad := range []string{"a/b/c", "/x", "x/", "bad id"} {
		sel := view.CheckSelect{Install: bad}
		if n := len(build(m, sel).Installs); n != 0 {
			t.Errorf("%q selected %d installs", bad, n)
		}
		if sel.Validate() == nil {
			t.Errorf("%q: Validate returned nil", bad)
		}
	}
	if err := (view.CheckSelect{Install: "kgateway-system/kgateway"}).Validate(); err != nil {
		t.Errorf("valid selector: %v", err)
	}
}

func TestCheckExitCode(t *testing.T) {
	rep := health.Report{Findings: []health.Finding{finding("i", "X-1", health.SeverityInfo, nil)}}
	if c := build(checkModel(rep, nil), view.CheckSelect{}); c.ExitCode() != 0 {
		t.Errorf("info only = %d", c.ExitCode())
	}
	rep.Findings = append(rep.Findings, finding("w", "X-2", health.SeverityWarning, nil))
	if c := build(checkModel(rep, nil), view.CheckSelect{}); c.ExitCode() != 1 {
		t.Errorf("warning = %d", c.ExitCode())
	}
}

// Optional fields are absent, and required empty values are present, in JSON.
func TestCheckJSONOmission(t *testing.T) {
	rep := health.Report{Findings: []health.Finding{finding("f", "X-1", health.SeverityInfo, nil)},
		Checks: []health.CheckResult{{Name: "c"}}}
	b, err := json.Marshal(build(checkModel(rep, nil), view.CheckSelect{}))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	ic := doc["installs"].([]any)[0].(map[string]any)
	f := ic["findings"].([]any)[0].(map[string]any)
	for _, k := range []string{"resource", "detail", "since", "group", "evidence", "nextSteps", "fix"} {
		if _, ok := f[k]; ok {
			t.Errorf("finding has %q", k)
		}
	}
	c := ic["checks"].([]any)[0].(map[string]any)
	for _, k := range []string{"ok", "pending", "unavailable"} {
		if v, ok := c[k]; !ok {
			t.Errorf("check lacks %q", k)
		} else if v != false && v != "" {
			t.Errorf("check %q = %v", k, v)
		}
	}
	for _, k := range []string{"error", "detail", "hold"} {
		if _, ok := c[k]; ok {
			t.Errorf("check has %q", k)
		}
	}
	for _, k := range []string{"incomplete", "observation"} {
		if _, ok := doc[k]; ok {
			t.Errorf("check has %q", k)
		}
	}
	// Empty arrays are [] not null.
	empty, _ := json.Marshal(build(checkModel(health.Report{}, nil), view.CheckSelect{Install: "a/b/c"}))
	if !strings.Contains(string(empty), `"installs":[]`) {
		t.Errorf("empty = %s", empty)
	}
	b, _ = json.Marshal(build(checkModel(health.Report{}, nil), view.CheckSelect{}).Installs[0])
	for _, want := range []string{`"findings":[]`, `"groups":[]`, `"checks":[]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("install lacks %s: %s", want, b)
		}
	}
}

// A since not before ObservedFrom is only when the engine first looked, so it is dropped.
func TestBuildCheckSinceBeforeObserving(t *testing.T) {
	from := time.Date(2026, 11, 10, 14, 0, 0, 0, time.UTC)
	before, at, after := from.Add(-time.Hour), from, from.Add(3*time.Second)
	var fs []health.Finding
	for i, s := range []time.Time{before, at, after} {
		f := finding(fmt.Sprint("f", i), "X-1", health.SeverityWarning, ref("HTTPRoute", "shop", fmt.Sprint("r", i)))
		f.Since = &s
		fs = append(fs, f)
	}
	rep := health.Report{Findings: fs}
	got := build(checkModel(rep, nil), view.CheckSelect{ObservedFrom: from}).Installs[0].Findings
	if len(got) != 3 || got[0].Since == nil || !got[0].Since.Equal(before) || got[1].Since != nil || got[2].Since != nil {
		t.Fatalf("since = %v %v %v, want only the one before observing", got[0].Since, got[1].Since, got[2].Since)
	}
	b, err := json.Marshal(got[2])
	if err != nil || strings.Contains(string(b), "since") {
		t.Errorf("json keeps since: %s (%v)", b, err)
	}
	all := build(checkModel(rep, nil), view.CheckSelect{}).Installs[0].Findings
	if all[1].Since == nil || all[2].Since == nil {
		t.Error("a zero ObservedFrom must keep every since")
	}
}
