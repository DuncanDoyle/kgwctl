package print_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/objects"

	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/print"
	"github.com/DuncanDoyle/kgwctl/internal/view"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var (
	gen     = time.Date(2026, 11, 10, 14, 2, 10, 0, time.UTC)
	build   = view.BuildInfo{Name: "kgwctl", Version: "v0.1.0-dev"}
	httpRef = func(ns, name string) *objects.Ref {
		return &objects.Ref{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute", Namespace: ns, Name: name}
	}
)

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "check", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if got != string(b) {
		t.Errorf("output differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, got, string(b))
	}
}

func render(t *testing.T, c view.Check, color bool) string {
	t.Helper()
	var sb strings.Builder
	if err := print.CheckTextFor(&sb, c, gen, color, build); err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

func inst(ns, name, state string) view.InstallCheck {
	return view.InstallCheck{
		Install:  view.InstallRef{ID: ns + "/" + name, Namespace: ns, Name: name, Version: "2.5.1"},
		State:    state,
		Findings: []view.CheckFinding{}, Groups: []view.CheckGroup{}, Checks: []view.CheckStatus{},
	}
}

func ok(names ...string) []view.CheckStatus {
	var cs []view.CheckStatus
	for _, n := range names {
		cs = append(cs, view.CheckStatus{Name: n, OK: true})
	}
	return cs
}

func chk(installs ...view.InstallCheck) view.Check {
	return view.Check{Context: "prod", GeneratedAt: gen, Installs: installs}
}

func cleanInstall() view.InstallCheck {
	i := inst("kgateway-system", "kgateway", "healthy")
	i.Checks = ok("a", "b", "c")
	return i
}

func errorsInstall() view.InstallCheck {
	i := inst("kgateway-system", "kgateway", "error")
	since := time.Date(2026, 11, 10, 13, 58, 1, 0, time.UTC)
	i.Findings = []view.CheckFinding{
		{ID: "f1", Code: "KGWUI-001", Severity: "error", Message: "Envoy rejects configuration",
			Resource: &objects.Ref{Group: "gateway.networking.k8s.io", Kind: "Gateway", Namespace: "edge", Name: "public"},
			Detail:   "2 of 3 proxies reject the latest config.", Since: &since,
			NextSteps: []view.NextStep{{Description: "Describe it", Command: "kubectl --context prod -n edge describe gateway public"}}},
		{ID: "f2", Code: "KGWUI-013", Severity: "warning", Group: "g1", Message: "backendRef Service shop/cart-v2 not found",
			Resource: httpRef("shop", "cart"),
			Fix:      &view.Fix{Title: "create Service shop/cart-v2, or change the backendRef"}},
		{ID: "f3", Code: "KGWUI-003", Severity: "warning", Message: "Route not attached to its parent", Resource: httpRef("shop", "web")},
		{ID: "f4", Code: "KGWUI-013", Severity: "warning", Group: "g1", Message: "backendRef Service shop/cart-v2 not found",
			Resource: httpRef("shop", "cart-b")},
		{ID: "f5", Code: "KGWUI-020", Severity: "info", Message: "Listener has no routes", Group: "g2", Resource: httpRef("shop", "x")},
	}
	i.Counts = view.Counts{Error: 1, Warning: 3, Info: 1}
	i.Groups = []view.CheckGroup{
		{ID: "g1", Cause: &objects.Ref{Kind: "Service", Namespace: "shop", Name: "cart-v2"}, Findings: []string{"f2", "f4"}},
		{ID: "g2", Findings: []string{"f5"}},
	}
	i.Checks = ok("a", "b")
	return i
}

func TestCheckText(t *testing.T) {
	hold := func(h int, until time.Time, reason string) *view.Hold {
		return &view.Hold{Until: until, Count: h, Reason: reason}
	}
	holds := inst("kgateway-system", "kgateway", "healthy")
	holds.Checks = append(ok("a", "b"),
		view.CheckStatus{Name: "status progress", OK: true, Hold: hold(12, gen.Add(5*time.Minute), "Waiting to see if status catches up")},
		view.CheckStatus{Name: "proxy ack", OK: true, Hold: hold(1, gen.Add(3*time.Minute), "Waiting for proxies to acknowledge")},
		view.CheckStatus{Name: "warming", Pending: true},
		// Pending or held with an error text is still deciding, never also under NOT CHECKED.
		view.CheckStatus{Name: "resource status", Pending: true, Error: "1 resource is being processed by the controller"},
		view.CheckStatus{Name: "xDS rejects", Pending: true, Error: "Waiting for rejects to return",
			Hold: hold(1, gen.Add(time.Minute), "Waiting to see if rejects return")})

	notChecked := inst("kgateway-system", "kgateway", "healthy")
	notChecked.Checks = append(ok("a"),
		view.CheckStatus{Name: "proxy connection", Unavailable: "denied", Error: `pods "proxy" is forbidden: cannot get pods/proxy`},
		view.CheckStatus{Name: "proxy config", Unavailable: "denied", Error: `pods "proxy" is forbidden: cannot get pods/proxy`},
		view.CheckStatus{Name: "ext auth", Unavailable: "not-on-version", Detail: "not on this kgateway version"},
		view.CheckStatus{Name: "scrape", Error: "scrape timed out"})

	second := inst("other", "gw", "error")
	second.Findings = []view.CheckFinding{{ID: "x", Code: "KGWUI-003", Severity: "error", Message: "Route not attached", Resource: httpRef("o", "r")}}
	second.Counts.Error = 1
	second.Checks = ok("a")

	// OK with a detail ran with partial coverage; findings stay in their own section.
	partial := inst("kgateway-system", "kgateway", "warning")
	partial.Findings = []view.CheckFinding{{ID: "p", Code: "KGWUI-025", Severity: "warning", Message: "Proxy disconnected", Resource: httpRef("edge", "public")}}
	partial.Counts.Warning = 1
	partial.Checks = append(ok("a", "b"),
		view.CheckStatus{Name: "proxy connection", OK: true, Detail: "1 of 2 Gateways have no proxy stats."},
		view.CheckStatus{Name: "version skew", OK: true, Detail: "Versions are compared 10 minutes after a controller rollout ends"},
		// Held with a gap: deciding on one Gateway, blind on another.
		view.CheckStatus{Name: "proxy config warming", OK: true, Detail: "1 of 2 Gateways have no proxy stats.",
			Hold: &view.Hold{Until: gen.Add(2 * time.Minute), Count: 1, Reason: "Waiting for proxies to finish loading config"}},
		view.CheckStatus{Name: "process", OK: true, Unavailable: "off", Detail: "Proxy Pods are not watched, so proxies were not checked"})

	incomplete := cleanInstall()
	c := chk(incomplete)
	c.Incomplete = []load.Input{{Resource: "pods", State: "denied", Reason: "forbidden"}}

	cases := []struct {
		name string
		c    view.Check
	}{
		{"clean", chk(cleanInstall())},
		{"findings", chk(errorsInstall())},
		{"holds", chk(holds)},
		{"not-checked", chk(notChecked)},
		{"partial", chk(partial)},
		{"two-installs", chk(errorsInstall(), second)},
		{"incomplete", c},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := render(t, tc.c, false)
			if strings.Contains(got, "\x1b") {
				t.Errorf("escape code with color=false:\n%q", got)
			}
			golden(t, tc.name, got)
		})
	}
}

func TestCheckTextColor(t *testing.T) {
	got := render(t, chk(errorsInstall()), true)
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("no colour with color=true:\n%q", got)
	}
}

func TestCheckHint(t *testing.T) {
	i := inst("a", "b", "healthy")
	i.Checks = []view.CheckStatus{
		{Name: "x", OK: true, Hold: &view.Hold{Until: gen.Add(4*time.Minute + 48*time.Second), Count: 3}},
		{Name: "y", OK: true, Hold: &view.Hold{Until: gen.Add(9 * time.Minute), Count: 1}},
	}
	j := inst("c", "d", "healthy")
	j.Checks = []view.CheckStatus{{Name: "z", OK: true, Hold: &view.Hold{Until: gen.Add(time.Minute), Count: 1}}}

	want2 := "2 checks are still deciding; the next decision is at 14:06:58 (in 4m48s). Run `kgwctl check --live` to see them decide."
	if got := print.CheckHintFor(chk(i), gen, "kgwctl"); got != want2 {
		t.Errorf("got %q\nwant %q", got, want2)
	}
	one := inst("a", "b", "healthy")
	one.Checks = []view.CheckStatus{{Name: "x", OK: true, Hold: &view.Hold{Until: gen.Add(5 * time.Minute), Count: 1}}}
	want1 := "1 check is still deciding; the next decision is at 14:07:10 (in 5m). Run `vendorctl check --live` to see them decide."
	if got := print.CheckHintFor(chk(one), gen, "vendorctl"); got != want1 {
		t.Errorf("got %q\nwant %q", got, want1)
	}
	// The earliest hold across installs wins.
	if got := print.CheckHintFor(chk(i, j), gen, "kgwctl"); !strings.Contains(got, "14:03:10 (in 1m)") || !strings.HasPrefix(got, "3 checks") {
		t.Errorf("got %q", got)
	}
	if got := print.CheckHint(chk(cleanInstall()), gen); got != "" {
		t.Errorf("clean: got %q", got)
	}
	if got := print.CheckHint(chk(one), gen); !strings.Contains(got, "`kgwctl check --live`") {
		t.Errorf("default name: got %q", got)
	}
}

func TestCheckTextSanitises(t *testing.T) {
	i := inst("kgateway-system", "kgateway", "error")
	i.Findings = []view.CheckFinding{{ID: "f", Code: "KGWUI-001", Severity: "error",
		Message:  "x\nCHECKS RUN: 99\x1b[2J",
		Detail:   "line1\r\nCHECKS RUN: 98\rover\x08\x08write\x1b]0;title\x07",
		Resource: httpRef("a\x1b[31m", "b\nc")}}
	i.Counts.Error = 1
	i.Checks = []view.CheckStatus{
		{Name: "held\x1b[1m", OK: true, Hold: &view.Hold{Until: gen, Count: 1, Reason: "r\x1b[2J\nCHECKS RUN: 97"}},
		{Name: "n", Unavailable: "denied", Error: "forbidden\x1b[2J\nCHECKS RUN: 96"},
		{Name: "p\x1b[1m", OK: true, Detail: "gap\x1b[2J\nCHECKS RUN: 95"},
	}
	got := render(t, chk(i), false)
	if strings.ContainsAny(got, "\x1b\r\x08\x07") {
		t.Errorf("control character in output:\n%q", got)
	}
	for _, l := range strings.Split(got, "\n") {
		if strings.HasPrefix(l, "CHECKS RUN: 9") {
			t.Errorf("forged unindented line %q in\n%s", l, got)
		}
	}
	// The real line is still there: the held and the partial check ran.
	if !strings.Contains(got, "\nCHECKS RUN: 2\n") {
		t.Errorf("missing real CHECKS RUN line:\n%s", got)
	}
}

func TestCheckTextKeepsUnicode(t *testing.T) {
	i := inst("a", "b", "error")
	i.Findings = []view.CheckFinding{{ID: "f", Code: "K-1", Severity: "error", Message: "Ünïcödé 日本語 ✓"}}
	if got := render(t, chk(i), false); !strings.Contains(got, "Ünïcödé 日本語 ✓") {
		t.Errorf("unicode lost:\n%s", got)
	}
}
