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

// The API server's own host is redacted wherever it appears, not only in URLs.
func TestRedactorServerHost(t *testing.T) {
	for _, tc := range []struct{ server, in, want string }{
		{"https://private-api:6443", `Get "https://private-api:6443/api/v1/x": dial tcp: lookup private-api on 10.96.0.10:53: no such host`,
			`Get "https://<redacted>": dial tcp: lookup <apiserver> on <redacted>: no such host`},
		{"private-api:6443", "x509: certificate is valid for other, not Private-API", "x509: certificate is valid for other, not <apiserver>"},
		{"https://private-api", "private-api-2 and api.private-api.example stay", "private-api-2 and api.private-api.example stay"},
		{"https://10.0.0.1:6443/prefix", "x509: certificate is valid for 10.0.0.2, 110.0.0.12, not 10.0.0.1.", "x509: certificate is valid for 10.0.0.2, 110.0.0.12, not <apiserver>."},
		{"https://[fd00::1]:6443", "valid for fd00::2, not fd00::1", "valid for fd00::2, not <apiserver>"},
		{"", "lookup x on [fd00::a]:53: no such host", "lookup x on <redacted>: no such host"},
	} {
		if got := view.NewRedactor(tc.server).Redact(tc.in); got != tc.want {
			t.Errorf("server %q: Redact(%q)\n got %q\nwant %q", tc.server, tc.in, got, tc.want)
		}
	}
}

// BuildCheck redacts the server host given by CheckSelect.Server.
func TestBuildCheckRedactsServerHost(t *testing.T) {
	rep := health.Report{Checks: []health.CheckResult{{Name: "a", Error: "x509: certificate is valid for other, not private-api"}}}
	c := build(checkModel(rep, nil), view.CheckSelect{Server: "https://private-api:6443"})
	if got := c.Installs[0].Checks[0].Error; got != "x509: certificate is valid for other, not <apiserver>" {
		t.Errorf("error = %q", got)
	}
}
