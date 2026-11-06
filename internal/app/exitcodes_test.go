package app_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/DuncanDoyle/kgwctl/internal/testenv"
)

// TestExitCodeMatrix pins that every output format of a command exits the same way, and prints nothing to stdout on error (§8).
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
	}{
		{name: "get", formats: getFormats, fault: getFault, args: []string{"get", "hr", "-A"}},
		{name: "version", formats: []string{"text", "json", "yaml"}, fault: versionFault, args: []string{"version"}},
		{name: "describe", formats: describeFormats, fault: getFault, args: []string{"describe", "hr", "example", "-n", "default"}},
	}
	cases := []struct {
		name   string
		want   int
		action *testenv.Action
		// args replaces the command's args; nil keeps them. Empty means the case does not apply.
		args    func(command string) []string
		timeout bool
	}{
		{name: "success", want: 0},
		{name: "not-found", want: 1, args: func(c string) []string {
			switch c {
			case "get":
				return []string{"get", "gw", "missing", "-n", "kgateway-system"}
			case "describe":
				return []string{"describe", "gw", "missing", "-n", "kgateway-system"}
			}
			return nil
		}},
		{name: "denied", want: 1, action: &deny},
		{name: "failed", want: 1, action: &fail},
		{name: "timeout", want: 1, action: &stall, timeout: true},
	}
	for _, cmd := range commands {
		for _, tc := range cases {
			t.Run(cmd.name+"/"+tc.name, func(t *testing.T) {
				args := cmd.args
				if tc.args != nil {
					if args = tc.args(cmd.name); args == nil {
						t.Skip("no not-found case for " + cmd.name)
					}
				}
				env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture}})
				if tc.action != nil {
					cmd.fault(*tc.action)(env.Proxies[0])
				}
				if tc.timeout {
					args = append(append([]string{}, args...), timeout...)
				}
				for _, format := range cmd.formats {
					r := runCmd(t, env, append(append([]string{}, args...), "-o", format)...)
					if r.code != tc.want {
						t.Errorf("%s: exit %d, want %d; stderr %q", format, r.code, tc.want, r.stderr)
					}
					if tc.want != 0 && r.stdout != "" {
						t.Errorf("%s: stdout = %q, want empty on error", format, r.stdout)
					}
					if tc.want == 0 && r.stdout == "" {
						t.Errorf("%s: stdout is empty on success", format)
					}
				}
			})
		}
	}
}
