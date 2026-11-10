package print_test

import (
	"strings"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/objects"

	"github.com/DuncanDoyle/kgwctl/internal/observe"
	"github.com/DuncanDoyle/kgwctl/internal/print"
	"github.com/DuncanDoyle/kgwctl/internal/view"
)

func progressLine(t *testing.T, e observe.Event, color bool) string {
	t.Helper()
	var sb strings.Builder
	if err := print.Progress(&sb, e, color); err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

func TestProgressLines(t *testing.T) {
	gw := &objects.Ref{Group: "gateway.networking.k8s.io", Kind: "Gateway", Namespace: "edge", Name: "public"}
	hold := &view.Hold{Until: gen.Add(5 * time.Minute), Count: 12, Reason: "Waiting to see if status catches up"}
	cases := []struct {
		e    observe.Event
		want string
	}{
		{observe.Event{At: gen, Kind: observe.FindingRaised, Code: "KGWUI-001", Severity: "error", Resource: gw, Text: "Envoy rejects configuration"},
			"14:02:10  ERROR    KGWUI-001  Gateway edge/public: Envoy rejects configuration\n"},
		{observe.Event{At: gen, Kind: observe.FindingRaised, Code: "KGWUI-018", Severity: "warning", Text: "Status is not progressing"},
			"14:02:10  WARNING  KGWUI-018  Status is not progressing\n"},
		{observe.Event{At: gen, Kind: observe.FindingChanged, Code: "KGWUI-003", Severity: "warning", Resource: httpRef("shop", "web"), Text: "Not yet processed"},
			"14:02:10  CHANGED  KGWUI-003  HTTPRoute shop/web: now warning: Not yet processed\n"},
		{observe.Event{At: gen, Kind: observe.FindingChanged, Code: "KGWUI-013", Severity: "error", Resource: httpRef("live", "r"), Text: "Backend not found",
			Detail: "Service `live/x` has no port 8080 (ports: 80).\nSecond line"},
			"14:02:10  CHANGED  KGWUI-013  HTTPRoute live/r: now error: Backend not found; Service `live/x` has no port 8080 (ports: 80).\n"},
		{observe.Event{At: gen, Kind: observe.FindingCleared, Code: "KGWUI-003", Severity: "warning", Resource: httpRef("shop", "cart"), Text: "not attached"},
			"14:02:10  CLEARED  KGWUI-003  HTTPRoute shop/cart: not attached\n"},
		{observe.Event{At: gen, Kind: observe.HoldStarted, Check: "status progress", Hold: hold, Text: hold.Reason},
			"14:02:10  WAITING  status progress  12 held; decides 14:07:10 (Waiting to see if status catches up)\n"},
		{observe.Event{At: gen, Kind: observe.HoldChanged, Check: "status progress", Hold: hold, Text: hold.Reason},
			"14:02:10  WAITING  status progress  12 held; decides 14:07:10 (Waiting to see if status catches up)\n"},
		{observe.Event{At: gen, Kind: observe.HoldEnded, Check: "status progress", Hold: hold},
			"14:02:10  DECIDED  status progress\n"},
		{observe.Event{At: gen, Kind: observe.InputUnavailable, Input: "pods", Text: "forbidden"},
			"14:02:10  INPUT    pods: unavailable: forbidden\n"},
		{observe.Event{At: gen, Kind: observe.InputAvailable, Input: "pods"},
			"14:02:10  INPUT    pods: available\n"},
	}
	for _, tc := range cases {
		if got := progressLine(t, tc.e, false); got != tc.want {
			t.Errorf("kind %d:\n got %q\nwant %q", tc.e.Kind, got, tc.want)
		}
	}
}

// TestProgressSanitises: cluster text in a progress line cannot move the cursor or recolour the terminal.
func TestProgressSanitises(t *testing.T) {
	e := observe.Event{At: gen, Kind: observe.FindingRaised, Code: "KGWUI-001\x1b[2J", Severity: "error",
		Resource: httpRef("shop\x1b[31m", "web\r"), Text: "evil\x1b]0;title\x07\nnext"}
	got := progressLine(t, e, false)
	if strings.ContainsAny(got, "\x1b\r\x07") || strings.Count(got, "\n") != 1 {
		t.Errorf("unsanitised progress line %q", got)
	}
	h := observe.Event{At: gen, Kind: observe.HoldStarted, Check: "x\x1b[1m", Hold: &view.Hold{Until: gen, Count: 1, Reason: "r\x1b[0m"}}
	if got := progressLine(t, h, false); strings.Contains(got, "\x1b") {
		t.Errorf("unsanitised hold line %q", got)
	}
}

func TestProgressColor(t *testing.T) {
	e := observe.Event{At: gen, Kind: observe.FindingRaised, Code: "KGWUI-001", Severity: "error", Text: "x"}
	if got := progressLine(t, e, true); !strings.Contains(got, "\x1b[31mERROR\x1b[0m    KGWUI-001") {
		t.Errorf("coloured line %q, want a red ERROR padded after the colour", got)
	}
	if got := progressLine(t, e, false); strings.Contains(got, "\x1b") {
		t.Errorf("uncoloured line has ANSI: %q", got)
	}
}

func TestHeartbeat(t *testing.T) {
	c := view.Check{Installs: []view.InstallCheck{{
		Counts: view.Counts{Error: 1, Warning: 2},
		Checks: []view.CheckStatus{{Name: "a", Hold: &view.Hold{Count: 1}}, {Name: "b", Pending: true}, {Name: "c", OK: true}},
	}}}
	var sb strings.Builder
	if err := print.Heartbeat(&sb, gen, time.Minute, 6*time.Minute, c); err != nil {
		t.Fatal(err)
	}
	if want := "14:02:10  1:00 / 6:00  1 error, 2 warnings, 2 checks deciding\n"; sb.String() != want {
		t.Errorf("heartbeat %q, want %q", sb.String(), want)
	}
	sb.Reset()
	if err := print.Heartbeat(&sb, gen, 90*time.Second, 10*time.Minute, view.Check{}); err != nil {
		t.Fatal(err)
	}
	if want := "14:02:10  1:30 / 10:00  0 errors, 0 warnings, 0 checks deciding\n"; sb.String() != want {
		t.Errorf("heartbeat %q, want %q", sb.String(), want)
	}
}

func TestProgressStart(t *testing.T) {
	var sb strings.Builder
	if err := print.ProgressStart(&sb, gen, "prod\x1b[1m", []string{"kgateway-system/kgateway"}, 6*time.Minute); err != nil {
		t.Fatal(err)
	}
	if want := "14:02:10  observing prod[1m: kgateway-system/kgateway for 6m (Ctrl-C: stop and report)\n"; sb.String() != want {
		t.Errorf("start %q, want %q", sb.String(), want)
	}
}

func TestCheckTextObserved(t *testing.T) {
	cleared := gen.Add(3 * time.Minute)
	c := view.Check{Context: "prod", GeneratedAt: gen.Add(6 * time.Minute), Installs: []view.InstallCheck{{
		Install: view.InstallRef{ID: "kgateway-system.kgateway", Namespace: "kgateway-system", Name: "kgateway"}, State: "warning",
	}}, Observation: &view.Observation{StartedAt: gen, EndedAt: gen.Add(6 * time.Minute), StopReason: "timeout",
		Observed: []view.ObservedFinding{
			{ID: "a", Install: "kgateway-system.kgateway", Code: "KGWUI-003", Severity: "warning", Resource: httpRef("shop", "web"), Message: "Not yet processed", FirstSeen: gen,
				Severities: []view.SeverityAt{{Severity: "info", At: gen}, {Severity: "warning", At: gen.Add(time.Minute)}}},
			{ID: "b", Install: "kgateway-system.kgateway", Code: "KGWUI-013", Severity: "warning", Resource: httpRef("shop", "cart\x1b[1m"), Message: "Backend not found\x1b[2J", Detail: "Service `shop/cart-v2` has no port 8080 (ports: 80).", FirstSeen: gen,
				ClearedAt: &cleared, Severities: []view.SeverityAt{{Severity: "warning", At: gen}}},
		}}}
	got := render(t, c, false)
	if strings.Contains(got, "\x1b") {
		t.Error("observed section is not sanitised")
	}
	golden(t, "observed", got)
	c.Observation.Observed = []view.ObservedFinding{}
	if got := render(t, c, false); !strings.Contains(got, "OBSERVED DURING THIS RUN (0)") {
		t.Errorf("an empty observation is not shown:\n%s", got)
	}
}
