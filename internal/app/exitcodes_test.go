package app_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/DuncanDoyle/kgwctl/internal/testenv"
)

// TestExitCodeMatrix pins that every output format of a command exits the same way, and prints nothing to stdout on error (§8).
// check exits 2 when it could not check, with the report on stdout (Phase 2 design §3.4).
func TestExitCodeMatrix(t *testing.T) {
	// version needs discovery, so its error cases fault discovery; get and describe fault the httproutes input.
	getFault := func(a testenv.Action) func(p *testenv.Proxy) {
		return func(p *testenv.Proxy) {
			p.Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: httpRoutes}, a)
		}
	}
	versionFault := func(a testenv.Action) func(p *testenv.Proxy) {
		return func(p *testenv.Proxy) {
			p.Rule(testenv.Match{Kind: testenv.KindDiscovery, Resource: "*"}, a)
		}
	}
	deny := testenv.Action{Status: http.StatusForbidden}
	fail := testenv.Action{Status: http.StatusInternalServerError}
	stall := testenv.Action{Delay: 5 * time.Second}
	timeout := []string{"--request-timeout", "1s"}

	commands := []struct {
		name    string
		formats []string
		fault   func(a testenv.Action) func(p *testenv.Proxy)
		args    []string
		check   bool
	}{
		{name: "get", formats: getFormats, fault: getFault, args: []string{"get", "hr", "-A"}},
		{name: "version", formats: []string{"text", "json", "yaml"}, fault: versionFault, args: []string{"version"}},
		{name: "describe", formats: describeFormats, fault: getFault, args: []string{"describe", "hr", "example", "-n", "default"}},
		{name: "check", formats: []string{"text", "json", "yaml"}, fault: getFault, args: []string{"check"}, check: true},
	}
	cases := []struct {
		name   string
		want   int
		action *testenv.Action
		// args replaces the command's args; nil keeps them. Empty means the case does not apply.
		args    func(command string) []string
		timeout bool
		// checkWant is check's exit code; checkReport says whether check prints its report.
		checkWant   int
		checkReport bool
	}{
		{name: "success", want: 0, checkWant: 0, checkReport: true},
		{name: "not-found", want: 1, args: func(c string) []string {
			switch c {
			case "get":
				return []string{"get", "gw", "missing", "-n", "kgateway-system"}
			case "describe":
				return []string{"describe", "gw", "missing", "-n", "kgateway-system"}
			}
			return nil
		}},
		{name: "usage", want: 1, checkWant: 1, args: func(c string) []string {
			if c == "check" {
				return []string{"check", "--install", "a/b/c"}
			}
			return nil
		}},
		{name: "denied", want: 1, action: &deny, checkWant: 2, checkReport: true},
		{name: "failed", want: 1, action: &fail, checkWant: 2, checkReport: true},
		{name: "timeout", want: 1, action: &stall, timeout: true, checkWant: 2, checkReport: true},
	}
	for _, cmd := range commands {
		for _, tc := range cases {
			t.Run(cmd.name+"/"+tc.name, func(t *testing.T) {
				args := cmd.args
				if tc.args != nil {
					if args = tc.args(cmd.name); args == nil {
						t.Skip("no " + tc.name + " case for " + cmd.name)
					}
				}
				want, report := tc.want, tc.want == 0
				var env *testenv.Env
				if cmd.check {
					want, report = tc.checkWant, tc.checkReport
					env = checkEnv(t, checkInstall, checkController)
				} else {
					env = newEnv(t, testenv.Options{Fixtures: []string{healthyFixture}})
				}
				if tc.action != nil {
					cmd.fault(*tc.action)(env.Proxies[0])
				}
				if tc.timeout {
					args = append(append([]string{}, args...), timeout...)
				}
				for _, format := range cmd.formats {
					fargs := append(append([]string{}, args...), "-o", format)
					var r result
					if cmd.check {
						r = runCheck(t, env, statsRegistry(fakeStats{}), fastHooks(metricsScraper{}), fargs...)
					} else {
						r = runCmd(t, env, fargs...)
					}
					if r.code != want {
						t.Errorf("%s: exit %d, want %d; stderr %q", format, r.code, want, r.stderr)
					}
					if !report && r.stdout != "" {
						t.Errorf("%s: stdout = %q, want empty", format, r.stdout)
					}
					if report && r.stdout == "" {
						t.Errorf("%s: stdout is empty, want the output", format)
					}
				}
			})
		}
	}
}
