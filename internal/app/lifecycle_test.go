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

// waitNoWatches fails unless p saw at least one watch and has none open within 2s.
func waitNoWatches(t *testing.T, p *testenv.Proxy) {
	t.Helper()
	if p.WatchesSeen() == 0 {
		t.Fatal("no watch was ever opened, so the cleanup check proves nothing")
	}
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
		// noWatches is set where the run fails before any watch can open.
		noWatches bool
		// classic turns WatchList off, so a plain WATCH follows the LIST.
		classic bool
		// bounded means Execute must return within 2s of the 1s deadline.
		bounded bool
	}{
		{name: "success", wantCode: 0},
		// A watch fails after the LIST succeeded: version warns and exits 0.
		{name: "failure-after-watches-open", classic: true, rule: &testenv.Match{Kind: testenv.KindWatch, Resource: "deployments.apps"}, action: testenv.Action{HoldWatchThenStatus: http.StatusInternalServerError}, wantCode: 0},
		{name: "error", noWatches: true, rule: &testenv.Match{Kind: testenv.KindDiscovery, Resource: "*"}, action: testenv.Action{Status: http.StatusInternalServerError}, wantCode: 1},
		{name: "timeout-discovery", noWatches: true, bounded: true, rule: &testenv.Match{Kind: testenv.KindDiscovery, Resource: "*"}, action: testenv.Action{Delay: time.Hour},
			args: []string{"--request-timeout", "1s"}, wantCode: 1},
		// A core input held past the deadline makes version warn, not fail (design §4.1).
		{name: "timeout-core-input", rule: &testenv.Match{Kind: testenv.KindAcquire, Resource: "deployments.apps"}, action: testenv.Action{Delay: time.Hour},
			args: []string{"--request-timeout", "1s"}, wantCode: 0, bounded: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.classic {
				testenv.ClassicWatch(t)
			}
			env := testenv.New(t, testenv.Options{Fixtures: []string{healthyFixture}})
			if tc.rule != nil {
				env.Proxies[0].Rule(*tc.rule, tc.action)
			}
			r := runCmd(t, env, append([]string{"version"}, tc.args...)...)
			if r.code != tc.wantCode {
				t.Fatalf("exit %d, want %d; stderr: %s", r.code, tc.wantCode, r.stderr)
			}
			if tc.bounded && r.took > 2*time.Second {
				t.Errorf("returned after %v, want within 2s of start", r.took)
			}
			if tc.noWatches {
				if n := env.Proxies[0].WatchesSeen(); n != 0 {
					t.Errorf("%d watches opened, want none", n)
				}
				return
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
}

// settledConns returns the proxy's open connection count once it stops
// changing for 300ms, so requests still finishing do not skew it.
func settledConns(p *testenv.Proxy) int {
	last, since := p.OpenConns(), time.Now()
	for time.Since(since) < 300*time.Millisecond {
		time.Sleep(20 * time.Millisecond)
		if n := p.OpenConns(); n != last {
			last, since = n, time.Now()
		}
	}
	return last
}

func TestNoAccumulation(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthyFixture}})
	first, firstConns := 0, 0
	for i := range 20 {
		if r := runCmd(t, env, "version"); r.code != 0 {
			t.Fatalf("run %d: exit %d, stderr: %s", i, r.code, r.stderr)
		}
		if i == 0 {
			first = runtime.NumGoroutine()
			firstConns = settledConns(env.Proxies[0])
		}
	}
	waitNoWatches(t, env.Proxies[0])
	t.Logf("open connections: after 1 run %d, after 20 runs %d", firstConns, env.Proxies[0].OpenConns())
	if n := settledConns(env.Proxies[0]); n > firstConns {
		t.Errorf("open connections after 20 runs = %d, after 1 run = %d", n, firstConns)
	}
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
