package observe

import (
	"reflect"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/objects"

	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/view"
)

var t0 = time.Date(2026, 11, 10, 14, 0, 0, 0, time.UTC)

func finding(id, sev, msg string) view.CheckFinding {
	return view.CheckFinding{ID: id, Code: "KGWUI-" + id, Severity: sev, Message: msg,
		Resource: &objects.Ref{Namespace: "shop", Name: id}}
}

func model(fs []view.CheckFinding, checks []view.CheckStatus, inc ...load.Input) view.Check {
	return view.Check{
		Installs:   []view.InstallCheck{{Install: view.InstallRef{ID: "i1"}, Findings: fs, Checks: checks}},
		Incomplete: inc,
	}
}

type reqT struct{}

var require reqT

func (reqT) Equal(t *testing.T, want, got any) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("want %#v, got %#v", want, got)
	}
}

func (reqT) Nil(t *testing.T, v any) {
	t.Helper()
	if v != nil && !reflect.ValueOf(v).IsNil() {
		t.Fatalf("want nil, got %#v", v)
	}
}

func (reqT) Len(t *testing.T, v any, n int) {
	t.Helper()
	if l := reflect.ValueOf(v).Len(); l != n {
		t.Fatalf("want len %d, got %d", n, l)
	}
}

func (reqT) Empty(t *testing.T, v any) {
	t.Helper()
	if reflect.ValueOf(v).Len() != 0 {
		t.Fatalf("want empty, got %#v", v)
	}
}

func kinds(es []Event) []EventKind {
	out := []EventKind{}
	for _, e := range es {
		out = append(out, e.Kind)
	}
	return out
}

func TestDiffEvents(t *testing.T) {
	hold := func(n int, until time.Time, reason string) []view.CheckStatus {
		return []view.CheckStatus{{Name: "status", Hold: &view.Hold{Count: n, Until: until, Reason: reason}}}
	}
	u1, u2 := t0.Add(time.Minute), t0.Add(2*time.Minute)
	tests := []struct {
		name       string
		prev, next view.Check
		want       []EventKind
	}{
		{"no change", model([]view.CheckFinding{finding("a", "error", "m")}, nil), model([]view.CheckFinding{finding("a", "error", "m")}, nil), []EventKind{}},
		{"raised", model(nil, nil), model([]view.CheckFinding{finding("a", "error", "m")}, nil), []EventKind{FindingRaised}},
		{"cleared", model([]view.CheckFinding{finding("a", "error", "m")}, nil), model(nil, nil), []EventKind{FindingCleared}},
		{"severity change", model([]view.CheckFinding{finding("a", "info", "m")}, nil), model([]view.CheckFinding{finding("a", "warning", "m")}, nil), []EventKind{FindingChanged}},
		{"message change", model([]view.CheckFinding{finding("a", "info", "m")}, nil), model([]view.CheckFinding{finding("a", "info", "m2")}, nil), []EventKind{FindingChanged}},
		{"hold started", model(nil, nil), model(nil, hold(3, u1, "r")), []EventKind{HoldStarted}},
		{"hold count", model(nil, hold(3, u1, "r")), model(nil, hold(2, u1, "r")), []EventKind{HoldChanged}},
		{"hold until", model(nil, hold(3, u1, "r")), model(nil, hold(3, u2, "r")), []EventKind{HoldChanged}},
		{"hold same", model(nil, hold(3, u1, "r")), model(nil, hold(3, u1, "r")), []EventKind{}},
		{"hold ended", model(nil, hold(3, u1, "r")), model(nil, []view.CheckStatus{{Name: "status"}}), []EventKind{HoldEnded}},
		{"input unavailable", model(nil, nil), model(nil, nil, load.Input{Resource: "pods"}), []EventKind{InputUnavailable}},
		{"input available", model(nil, nil, load.Input{Resource: "pods"}), model(nil, nil), []EventKind{InputAvailable}},
		{"order", model([]view.CheckFinding{finding("b", "error", "m")}, hold(1, u1, "r")),
			model([]view.CheckFinding{finding("a", "error", "m")}, nil, load.Input{Resource: "pods"}),
			[]EventKind{FindingRaised, FindingCleared, HoldEnded, InputUnavailable}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, kinds(Diff(tc.prev, tc.next, t0)))
		})
	}
}

func TestDiffEventFields(t *testing.T) {
	es := Diff(model([]view.CheckFinding{finding("a", "info", "m")}, nil),
		model([]view.CheckFinding{finding("a", "warning", "m")}, nil), t0)
	require.Len(t, es, 1)
	e := es[0]
	require.Equal(t, "i1", e.Install)
	require.Equal(t, "KGWUI-a", e.Code)
	require.Equal(t, "warning", e.Severity)
	require.Equal(t, "m", e.Text)
	require.Equal(t, t0, e.At)
	require.Equal(t, "a", e.Resource.Name)
}

func TestTrackerBaselineRaisesNothing(t *testing.T) {
	tr := &Tracker{}
	require.Empty(t, tr.Add(model([]view.CheckFinding{finding("a", "error", "m")}, nil), t0))
	require.Empty(t, tr.Observed())
}

func TestTrackerTransitions(t *testing.T) {
	tr := &Tracker{}
	tr.Add(model([]view.CheckFinding{finding("a", "info", "m"), finding("s", "error", "m")}, nil), t0)
	t1, t2, t3 := t0.Add(time.Second), t0.Add(2*time.Second), t0.Add(3*time.Second)
	// a: severity change from the start; b raised; s stays steady.
	es := tr.Add(model([]view.CheckFinding{finding("a", "warning", "m"), finding("b", "error", "m"), finding("s", "error", "m")}, nil), t1)
	require.Equal(t, []EventKind{FindingRaised, FindingChanged}, kinds(es))
	tr.Add(model([]view.CheckFinding{finding("a", "warning", "m"), finding("s", "error", "m")}, nil), t2)
	obs := tr.Observed()
	require.Len(t, obs, 2)
	a, b := obs[0], obs[1]
	require.Equal(t, "a", a.ID)
	require.Equal(t, "warning", a.Severity)
	require.Equal(t, t0, a.FirstSeen)
	require.Nil(t, a.ClearedAt)
	require.Equal(t, []view.SeverityAt{{Severity: "info", At: t0}, {Severity: "warning", At: t1}}, a.Severities)
	require.Equal(t, "b", b.ID)
	require.Equal(t, t1, b.FirstSeen)
	require.Equal(t, &t2, b.ClearedAt)
	require.Equal(t, []view.SeverityAt{{Severity: "error", At: t1}}, b.Severities)
	_ = t3
}

func TestTrackerClearAndReturn(t *testing.T) {
	tr := &Tracker{}
	tr.Add(model(nil, nil), t0)
	t1, t2, t3, t4 := t0.Add(1*time.Second), t0.Add(2*time.Second), t0.Add(3*time.Second), t0.Add(4*time.Second)
	f := func(sev string) []view.CheckFinding { return []view.CheckFinding{finding("a", sev, "m")} }
	tr.Add(model(f("error"), nil), t1)
	tr.Add(model(nil, nil), t2)
	tr.Add(model(f("warning"), nil), t3)
	obs := tr.Observed()
	require.Len(t, obs, 1)
	require.Equal(t, t1, obs[0].FirstSeen)
	require.Nil(t, obs[0].ClearedAt)
	require.Equal(t, []view.SeverityAt{{Severity: "error", At: t1}, {Severity: "warning", At: t3}}, obs[0].Severities)
	tr.Add(model(nil, nil), t4)
	require.Equal(t, &t4, tr.Observed()[0].ClearedAt)
	require.Equal(t, "warning", tr.Observed()[0].Severity)
}
