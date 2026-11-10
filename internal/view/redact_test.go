package view_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/DuncanDoyle/snellius/pkg/health"

	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/view"
)

// scrapeErr is a transport error as http.Client.Do returns it through pods/proxy.
const scrapeErr = `Get "https://10.0.0.1:6443/api/v1/namespaces/x/pods/y:9092/proxy/metrics": dial tcp 10.0.0.1:6443: connect: connection refused`

func TestRedact(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{scrapeErr, `Get "https://<redacted>": dial tcp <redacted>: connect: connection refused`},
		{"read tcp 10.0.0.2:5555->[fd00::1]:6443: read: connection reset", "read tcp <redacted>: read: connection reset"},
		{"see http://kube.internal:8080/x?y=1 now", "see http://<redacted> now"},
		{"no url here: 1 of 2 Gateways", "no url here: 1 of 2 Gateways"},
		{`{"err":"Get \"https://h/a\""}`, `{"err":"Get \"https://<redacted>\""}`},
	} {
		if got := view.Redact(tc.in); got != tc.want {
			t.Errorf("Redact(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
	in := view.RedactInput(load.Input{Resource: "pods", State: load.StateFailed, Reason: scrapeErr})
	if strings.Contains(in.Reason, "10.0.0.1") || in.Resource != "pods" {
		t.Errorf("RedactInput = %+v", in)
	}
}

// errEvidence is evidence quoting an error.
type errEvidence struct {
	Error string `json:"error"`
}

func (errEvidence) EvidenceKind() string { return "test" }

// Every cluster-derived string BuildCheck maps loses its URLs and addresses.
func TestBuildCheckRedacts(t *testing.T) {
	f := finding("f", "X-8", health.SeverityInfo, nil)
	f.Message, f.Detail = "m "+scrapeErr, scrapeErr
	f.Facts = errEvidence{Error: scrapeErr}
	rep := health.Report{Findings: []health.Finding{f}, Checks: []health.CheckResult{
		{Name: "a", Error: scrapeErr},
		{Name: "b", OK: true, Detail: scrapeErr},
		{Name: "c", OK: true, Hold: &health.Hold{Until: computedAt, Count: 1, Reason: scrapeErr}},
	}}
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(build(checkModel(rep, nil), view.CheckSelect{})); err != nil {
		t.Fatal(err)
	}
	if s := buf.String(); strings.Contains(s, "10.0.0.1") || strings.Contains(s, "/api/v1") || strings.Count(s, "https://<redacted>") != 6 {
		t.Errorf("not redacted:\n%s", s)
	}
}
