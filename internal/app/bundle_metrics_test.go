package app_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/DuncanDoyle/kgwctl/internal/bundle/bundletest"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
)

// proxyMetricsPath is the metrics item of the proxyPods "http-a" Pod.
const proxyMetricsPath = "proxies/kgateway-system/http-a/metrics.txt"

// isMetricsReq reports whether req is a pods/proxy metrics request.
func isMetricsReq(req string) bool {
	p, _, _ := strings.Cut(req, "?")
	return strings.Contains(p, "/pods/http:") && strings.HasSuffix(p, "/proxy/metrics")
}

// metricsItem returns the status and reason of the manifest item at path.
func metricsItem(t *testing.T, a bundletest.Archive, path string) (status, reason string) {
	t.Helper()
	b, _ := a.File("manifest.json")
	var doc struct {
		Item struct {
			Items []struct{ Path, Status, Reason string } `json:"items"`
		} `json:"item"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	for _, it := range doc.Item.Items {
		if it.Path == path {
			return it.Status, it.Reason
		}
	}
	t.Fatalf("no manifest item at %s", path)
	return "", ""
}

// TestBundleMetrics: through the command's guarded StatusScraper, a proxy
// Pod declaring no 9091 is disabled on a refused connection, denied on a
// 403 and ok on 200, with one pods/proxy GET on 9091.
func TestBundleMetrics(t *testing.T) {
	const exposition = "# TYPE up gauge\nup 1\n"
	for name, tc := range map[string]struct {
		status       int
		body         string
		want, reason string
	}{
		"refused": {http.StatusServiceUnavailable, "error trying to reach service: dial tcp 10.0.0.1:9091: connect: connection refused", "disabled", "no metrics port"},
		"denied":  {http.StatusForbidden, "", "denied", ""},
		"ok":      {http.StatusOK, exposition, "ok", ""},
	} {
		t.Run(name, func(t *testing.T) {
			env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture, proxyPods(t, "http-a")}})
			act := testenv.Action{Status: tc.status}
			if tc.body != "" {
				act.Body = []byte(tc.body)
			}
			env.Proxies[0].Rule(testenv.Match{Resource: "pods", Subresource: "proxy"}, act)
			a := bundleArchive(t, env)

			status, reason := metricsItem(t, a, proxyMetricsPath)
			if status != tc.want || (tc.reason != "" && reason != tc.reason) {
				t.Errorf("item %s (%s), want %s (%s)", status, reason, tc.want, tc.reason)
			}
			data, ok := a.File(proxyMetricsPath)
			if ok != (tc.want == "ok") || (ok && string(data) != exposition) {
				t.Errorf("metrics.txt %q (present %t)", data, ok)
			}
			var got []string
			for _, r := range env.Proxies[0].Requests() {
				if isMetricsReq(r) && strings.Contains(r, "/pods/http:http-a:") {
					got = append(got, r)
				}
			}
			if want := []string{"GET /api/v1/namespaces/kgateway-system/pods/http:http-a:9091/proxy/metrics"}; !slices.Equal(got, want) {
				t.Errorf("requests %v, want %v", got, want)
			}
		})
	}
}

// TestBundleSkipMetrics: --skip controller-metrics,proxy-metrics records
// both collectors skipped with reason flag and sends no metrics request.
func TestBundleSkipMetrics(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture, proxyPods(t, "http-a")}})

	bundleArchive(t, env)
	if !slices.ContainsFunc(env.Proxies[0].Requests(), isMetricsReq) {
		t.Fatal("no metrics request without --skip; the check below proves nothing")
	}
	before := len(env.Proxies[0].Requests())

	a := bundleArchive(t, env, "--skip", "controller-metrics,proxy-metrics")
	seen := map[string]bool{}
	for _, c := range readManifest(t, a).Item.Collectors {
		if c.Name == "controller-metrics" || c.Name == "proxy-metrics" {
			seen[c.Name] = true
			if c.Status != "skipped" || c.Reason != "flag" {
				t.Errorf("%s collector %s (%s), want skipped (flag)", c.Name, c.Status, c.Reason)
			}
		}
	}
	if len(seen) != 2 {
		t.Errorf("collectors in the manifest: %v, want both metrics collectors", seen)
	}
	if ms := membersUnder(a, ""); slices.ContainsFunc(ms, func(m string) bool { return strings.HasSuffix(m, "/metrics.txt") }) {
		t.Errorf("members %v", ms)
	}
	for _, req := range env.Proxies[0].Requests()[before:] {
		if isMetricsReq(req) {
			t.Errorf("request with --skip: %s", req)
		}
	}
}
