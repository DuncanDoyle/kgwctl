package app_test

import (
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/DuncanDoyle/kgwctl/internal/testenv"
)

// waitNoWatches fails unless p has no open watch within 2s.
func waitNoWatches(t *testing.T, p *testenv.Proxy) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for p.OpenWatches() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d watches still open 2s after Execute returned", p.OpenWatches())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCancelOnEveryExit(t *testing.T) {
	cases := []struct {
		name     string
		rule     *testenv.Match
		action   testenv.Action
		args     []string
		wantCode int
	}{
		{name: "success", wantCode: 0},
		{name: "error", rule: &testenv.Match{Kind: testenv.KindDiscovery, Resource: "*"}, action: testenv.Action{Status: http.StatusInternalServerError}, wantCode: 1},
		{name: "timeout-discovery", rule: &testenv.Match{Kind: testenv.KindDiscovery, Resource: "*"}, action: testenv.Action{Delay: time.Hour},
			args: []string{"--request-timeout", "1s"}, wantCode: 1},
		// A core input held past the deadline makes version warn, not fail (design §4.1).
		{name: "timeout-core-input", rule: &testenv.Match{Kind: testenv.KindAcquire, Resource: "deployments.apps"}, action: testenv.Action{Delay: time.Hour},
			args: []string{"--request-timeout", "1s"}, wantCode: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := testenv.New(t, testenv.Options{Fixtures: []string{healthyFixture}})
			if tc.rule != nil {
				env.Proxies[0].Rule(*tc.rule, tc.action)
			}
			r := runCmd(t, env, append([]string{"version"}, tc.args...)...)
			if r.code != tc.wantCode {
				t.Fatalf("exit %d, want %d; stderr: %s", r.code, tc.wantCode, r.stderr)
			}
			waitNoWatches(t, env.Proxies[0])
		})
	}
}

// TestReturnAtDeadline holds a request open for an hour; Execute must return
// within 1s of the 1s deadline. Discovery is held, so version fails; a held
// core input only warns (see TestCancelOnEveryExit).
func TestReturnAtDeadline(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthyFixture}})
	hits := env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindDiscovery, Resource: "*"}, testenv.Action{Delay: time.Hour})
	r := runCmd(t, env, "version", "--request-timeout", "1s")
	if r.code != 1 {
		t.Errorf("exit %d, want 1; stderr: %s", r.code, r.stderr)
	}
	if r.took > 2*time.Second {
		t.Errorf("returned after %v, want within 2s of start", r.took)
	}
	if hits.N() == 0 {
		t.Error("the delayed request was never made")
	}
	waitNoWatches(t, env.Proxies[0])
}

func TestNoAccumulation(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthyFixture}})
	first := 0
	for i := range 20 {
		if r := runCmd(t, env, "version"); r.code != 0 {
			t.Fatalf("run %d: exit %d, stderr: %s", i, r.code, r.stderr)
		}
		if i == 0 {
			first = runtime.NumGoroutine()
		}
	}
	waitNoWatches(t, env.Proxies[0])
	// Goroutine count is supplementary evidence; the open-watch count is the assertion.
	if n := runtime.NumGoroutine(); n > first+5 {
		t.Errorf("goroutines after 20 runs = %d, after 1 run = %d", n, first)
	}
}

func TestBinaryDeps(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./cmd/kgwctl", "./examples/downstream")
	cmd.Dir = "../.." // module root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if strings.HasPrefix(pkg, "sigs.k8s.io/controller-runtime") ||
			pkg == "github.com/DuncanDoyle/kgateway-ui/pkg/app" || strings.HasPrefix(pkg, "github.com/DuncanDoyle/kgateway-ui/pkg/app/") {
			t.Errorf("binary depends on %s", pkg)
		}
	}
}
