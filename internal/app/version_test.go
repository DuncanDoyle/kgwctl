package app_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DuncanDoyle/kgateway-ui/pkg/extension"
	"github.com/DuncanDoyle/kgateway-ui/pkg/install"

	"github.com/DuncanDoyle/kgwctl/internal/app"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const (
	healthyFixture = "../testenv/testdata/healthy.yaml"
	skewFixture    = "../testenv/testdata/skew.yaml"
	twoInstalls    = "../testenv/testdata/two-installs.yaml"
	crdsResource   = "customresourcedefinitions.apiextensions.k8s.io"
)

var testBuild = app.BuildInfo{Version: "v0.1.0", Commit: "a1b2c3d", GoVersion: "go1.26.0"}

// result is one command run.
type result struct {
	code           int
	stdout, stderr string
	took           time.Duration
}

// runCmd runs the root command with args against env's kubeconfig.
func runCmd(t *testing.T, env *testenv.Env, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	root := app.NewRoot(app.Config{Build: testBuild, Out: &out, Err: &errb, Now: testNow})
	start := time.Now()
	code := run(root, append([]string{"--kubeconfig", env.Kubeconfig}, args...)...)
	return result{code: code, stdout: out.String(), stderr: errb.String(), took: time.Since(start)}
}

// golden compares got with testdata/<dir>/<name>.golden, or rewrites it with -update.
func golden(t *testing.T, dir, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", dir, name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if want := readGolden(t, dir, name); got != want {
		t.Errorf("output differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

// readGolden returns testdata/<dir>/<name>.golden.
func readGolden(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", dir, name+".golden"))
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	return string(b)
}

func TestVersion(t *testing.T) {
	proxySel := testenv.ProxyPodSelector(extension.New())
	ctrlSel := install.UpstreamDetector{}.ControllerPodSelector().String()
	cases := []struct {
		name     string
		fixtures []string
		rules    func(p *testenv.Proxy)
		args     []string
		// warn is a substring stderr must contain; "" means stderr must be empty.
		warn string
		// noRequests means the cluster must see no request.
		noRequests bool
	}{
		{name: "skew", fixtures: []string{healthyFixture, skewFixture}},
		{name: "two-installs", fixtures: []string{twoInstalls}},
		{name: "proxy-pods-delayed", fixtures: []string{healthyFixture, skewFixture}, rules: func(p *testenv.Proxy) {
			p.Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "pods", LabelSelector: proxySel}, testenv.Action{Delay: 300 * time.Millisecond})
		}},
		{name: "proxy-pods-past-deadline", fixtures: []string{healthyFixture, skewFixture}, args: []string{"--request-timeout", "1s"},
			rules: func(p *testenv.Proxy) {
				p.Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "pods", LabelSelector: proxySel}, testenv.Action{Delay: 5 * time.Second})
			}, warn: "Warning: proxy pods: loading"},
		{name: "pods-denied", fixtures: []string{healthyFixture, skewFixture}, rules: func(p *testenv.Proxy) {
			p.Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "pods", LabelSelector: proxySel}, testenv.Action{Status: http.StatusForbidden})
		}, warn: "Warning: proxy pods: denied"},
		{name: "crds-delayed", fixtures: []string{healthyFixture, skewFixture}, rules: func(p *testenv.Proxy) {
			p.Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: crdsResource}, testenv.Action{Delay: 300 * time.Millisecond})
		}},
		{name: "crds-denied", fixtures: []string{healthyFixture, skewFixture}, rules: func(p *testenv.Proxy) {
			p.Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: crdsResource}, testenv.Action{Status: http.StatusForbidden})
		}, warn: "Warning: " + crdsResource + ": denied"},
		{name: "gateways-denied", fixtures: []string{healthyFixture, skewFixture}, rules: func(p *testenv.Proxy) {
			p.Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "gateways.gateway.networking.k8s.io"}, testenv.Action{Status: http.StatusForbidden})
		}, warn: "Warning: gateways.gateway.networking.k8s.io: denied"},
		{name: "controller-pods-denied", fixtures: []string{healthyFixture, skewFixture}, rules: func(p *testenv.Proxy) {
			p.Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "pods", LabelSelector: ctrlSel}, testenv.Action{Status: http.StatusForbidden})
		}, warn: "Warning: pods: denied"},
		{name: "client-only", args: []string{"--client"}, noRequests: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newEnv(t, testenv.Options{Fixtures: tc.fixtures})
			if tc.rules != nil {
				tc.rules(env.Proxies[0])
			}
			for _, format := range []string{"text", "json", "yaml"} {
				t.Run(format, func(t *testing.T) {
					args := append([]string{"version", "-o", format}, tc.args...)
					r := runCmd(t, env, args...)
					if r.code != 0 {
						t.Fatalf("exit %d, stderr: %s", r.code, r.stderr)
					}
					if tc.warn == "" && r.stderr != "" {
						t.Errorf("stderr = %q, want empty", r.stderr)
					}
					if tc.warn != "" && !strings.Contains(r.stderr, tc.warn) {
						t.Errorf("stderr = %q, want it to contain %q", r.stderr, tc.warn)
					}
					// Library log lines never reach the user; only warnings do.
					for _, l := range strings.Split(strings.TrimSuffix(r.stderr, "\n"), "\n") {
						if l != "" && !strings.HasPrefix(l, "Warning: ") {
							t.Errorf("stderr line %q is not a warning", l)
						}
					}
					if strings.Contains(r.stdout, "Warning") {
						t.Errorf("stdout carries a warning: %q", r.stdout)
					}
					if format == "json" {
						var env struct {
							SchemaVersion, Kind string
							Item                json.RawMessage
						}
						if err := json.Unmarshal([]byte(r.stdout), &env); err != nil {
							t.Fatalf("stdout is not JSON: %v", err)
						}
						if env.SchemaVersion != "kgwctl.kgateway.dev/v1alpha1" || env.Kind != "Version" || len(env.Item) == 0 {
							t.Errorf("envelope = %+v", env)
						}
					}
					golden(t, "version", tc.name+"."+format, r.stdout)
				})
			}
			if tc.noRequests {
				if reqs := env.Proxies[0].Requests(); len(reqs) != 0 {
					t.Errorf("--client sent requests: %v", reqs)
				}
			}
		})
	}
}

func TestVersionHangingClusterTimesOut(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindDiscovery, Resource: "*"}, testenv.Action{Delay: 30 * time.Second})
	r := runCmd(t, env, "version", "--request-timeout", "1s")
	if r.code != 1 {
		t.Fatalf("exit %d, want 1; stdout %q", r.code, r.stdout)
	}
	if r.took > 2500*time.Millisecond {
		t.Errorf("took %v, want about 1s", r.took)
	}
	// Discovery is the stalled stage, so the error names it.
	if r.stderr != "Error: timed out during discovery\n" {
		t.Errorf("stderr = %q, want the discovery timeout", r.stderr)
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want empty", r.stdout)
	}
}

func TestVersionUnknownContext(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	r := runCmd(t, env, "version", "--context", "no-such-context")
	if r.code != 1 {
		t.Fatalf("exit %d, want 1", r.code)
	}
	if !strings.Contains(r.stderr, "no-such-context") {
		t.Errorf("stderr = %q, want it to name the context", r.stderr)
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want empty", r.stdout)
	}
}

func TestVersionInvalidOutput(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	r := runCmd(t, env, "version", "--client", "-o", "table")
	if r.code != 1 || !strings.Contains(r.stderr, "table") || r.stdout != "" {
		t.Errorf("exit %d, stderr %q, stdout %q", r.code, r.stderr, r.stdout)
	}
}
