package app_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/DuncanDoyle/kgwctl/internal/testenv"
)

const (
	describeFixture     = "../testenv/testdata/describe.yaml"
	delegationModes     = "../testenv/testdata/delegation-modes.yaml"
	sameNameLSFixture   = "../testenv/testdata/listenersets-same-name.yaml"
	describeEnvelopeTag = "kgwctl.kgateway.dev/v1alpha1"
)

var describeFormats = []string{"text", "json", "yaml"}

func TestDescribe(t *testing.T) {
	deny := func(resource string) func(p *testenv.Proxy) {
		return func(p *testenv.Proxy) {
			p.Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: resource}, testenv.Action{Status: http.StatusForbidden})
		}
	}
	base := []string{healthyFixture, listenerSetsFixture, describeFixture}
	cases := []struct {
		name     string
		fixtures []string
		args     []string
		rules    func(p *testenv.Proxy)
		// warn is a substring stderr must contain; "" means stderr must be empty.
		warn string
		// want are substrings the text output must contain; absent must not appear.
		want, absent []string
	}{
		{name: "gateway", fixtures: base, args: []string{"describe", "gw", "http", "-n", "kgateway-system"},
			want: []string{"Listeners:", "Attached routes:", "HTTPRoute/default/example", "Not accepted:", "NotAllowedByListeners", "Pending:", "ListenerSets:"}},
		{name: "listenerset", fixtures: base, args: []string{"describe", "ls", "extra", "-n", "kgateway-system"},
			want: []string{"Parent:", "Pending:", "HTTPRoute/via-extra"}, absent: []string{"ListenerSets:"}},
		{name: "route-mixed-parents", fixtures: []string{healthyFixture, mixedFixture}, args: []string{"describe", "hr", "mixed", "-n", "mixed"},
			want: []string{"example.com/other", "no status", "stale", "other"}},
		{name: "route-missing-parent", fixtures: []string{healthyFixture, orphansFixture}, args: []string{"describe", "hr", "to-missing", "-n", "default"},
			want: []string{"Declared references (not resolved):", "Gateway/kgateway-system/missing"}},
		{name: "delegation-child", fixtures: []string{healthyFixture, delegationFixture}, args: []string{"describe", "hr", "child-a", "-n", "default"},
			want: []string{"Delegation parents:", "HTTPRoute/parent"}},
		{name: "delegation-parent", fixtures: []string{healthyFixture, delegationFixture}, args: []string{"describe", "hr", "parent", "-n", "default"},
			want: []string{"delegate (name) child-a", "Delegation children:  HTTPRoute/child-a", "Backends:             <none>"}},
		{name: "route-rich", fixtures: base, args: []string{"describe", "hr", "rejected", "-n", "default"},
			want: []string{"PathPrefix /api; header x-team Exact a; query version RegularExpression v[12]; method GET", "RequestMirror Service/api-shadow", "api:8080 weight 90"}},
		{name: "delegation-wildcard", fixtures: []string{healthyFixture, delegationModes}, args: []string{"describe", "hr", "wildcard-parent", "-n", "default"},
			want: []string{"delegate (wildcard) team-a/*"}},
		{name: "delegation-label", fixtures: []string{healthyFixture, delegationModes}, args: []string{"describe", "hr", "label-parent", "-n", "default"},
			want: []string{"delegate (label) team-b"}},
		{name: "policy-ancestors", fixtures: base, args: []string{"describe", "tp", "route-policy", "-n", "default"},
			want: []string{"ancestor Gateway/kgateway-system/http", "Ancestors (from status):"}},
		{name: "policy-none", fixtures: []string{healthyFixture, orphansFixture}, args: []string{"describe", "tp", "no-ancestors", "-n", "default"},
			want: []string{"Ancestors (from status):  <none>"}},
		{name: "gateway-parameters", fixtures: []string{healthyFixture, orphansFixture}, args: []string{"describe", "gwp", "params", "-n", "default"},
			want: []string{"Conditions:"}},
		{name: "other-controller-all", fixtures: []string{healthyFixture, otherFixture}, args: []string{"describe", "gw", "edge", "-n", "other", "--all-controllers"},
			want: []string{"Relations:  not resolved (no owning install)"}},
		{name: "pods-denied", fixtures: base, args: []string{"describe", "hr", "example", "-n", "default"}, rules: deny("pods"),
			warn: "Warning: pods: denied", want: []string{"Incomplete inputs:", "pods: denied", "Parents:"}},
		{name: "namespaces-denied", fixtures: base, args: []string{"describe", "gw", "http", "-n", "kgateway-system"}, rules: deny("namespaces"),
			warn: "Warning: namespaces: denied", want: []string{"Relations unknown (namespaces: denied)", "Conditions:"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newEnv(t, testenv.Options{Fixtures: tc.fixtures})
			if tc.rules != nil {
				tc.rules(env.Proxies[0])
			}
			for _, format := range describeFormats {
				t.Run(format, func(t *testing.T) {
					r := runCmd(t, env, append(tc.args, "-o", format)...)
					if r.code != 0 {
						t.Fatalf("exit %d, stderr: %s", r.code, r.stderr)
					}
					if tc.warn == "" && r.stderr != "" {
						t.Errorf("stderr = %q, want empty", r.stderr)
					}
					if tc.warn != "" && !strings.Contains(r.stderr, tc.warn) {
						t.Errorf("stderr = %q, want it to contain %q", r.stderr, tc.warn)
					}
					onlyWarnings(t, r.stderr)
					switch format {
					case "text":
						for _, w := range tc.want {
							if !strings.Contains(r.stdout, w) {
								t.Errorf("text output lacks %q:\n%s", w, r.stdout)
							}
						}
						for _, a := range tc.absent {
							if strings.Contains(r.stdout, a) {
								t.Errorf("text output has %q:\n%s", a, r.stdout)
							}
						}
					case "json":
						var e struct {
							SchemaVersion, Kind string
							Item                json.RawMessage
						}
						if err := json.Unmarshal([]byte(r.stdout), &e); err != nil {
							t.Fatalf("stdout is not JSON: %v", err)
						}
						if e.SchemaVersion != describeEnvelopeTag || e.Kind != "Describe" || len(e.Item) == 0 {
							t.Errorf("envelope = %+v", e)
						}
					}
					golden(t, "describe", tc.name+"."+format, normalize(format, r.stdout))
				})
			}
		})
	}
}

func TestDescribeErrors(t *testing.T) {
	cases := []struct {
		name     string
		fixtures []string
		rules    func(p *testenv.Proxy)
		args     []string
		// stderr is the whole error line after "Error: ", or its prefix when prefix is set.
		stderr string
		prefix bool
	}{
		{name: "ambiguous-ls", fixtures: []string{healthyFixture, sameNameLSFixture}, args: []string{"describe", "ls", "same", "-n", "kgateway-system"},
			stderr: "ambiguous: name exists as ListenerSet and XListenerSet; use listenersets.<group>"},
		{name: "not-found", args: []string{"describe", "gw", "missing", "-n", "kgateway-system"}, stderr: `gateways "missing" not found`},
		{name: "ls-not-found", args: []string{"describe", "ls", "missing", "-n", "kgateway-system"}, stderr: `listenersets "missing" not found`},
		{name: "unknown-kind", args: []string{"describe", "directresponses", "x"}, stderr: `unknown kind "directresponses"`},
		{name: "unserved-kind", rules: func(p *testenv.Proxy) {
			p.Rule(testenv.Match{Kind: testenv.KindDiscovery, Resource: "gateway.networking.x-k8s.io/v1alpha1"}, testenv.Action{Status: http.StatusNotFound})
		}, args: []string{"describe", "xlistenersets", "x"}, stderr: `kind "xlistenersets" is not served by this cluster`},
		{name: "kind-denied", rules: func(p *testenv.Proxy) {
			p.Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: httpRoutes}, testenv.Action{Status: http.StatusForbidden})
		}, args: []string{"describe", "hr", "example", "-n", "default"}, stderr: "cannot read " + httpRoutes + ": denied: ", prefix: true},
		{name: "two-kinds", args: []string{"describe", "gw,hr", "example", "-n", "default"}, stderr: "describe takes one kind, got 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixtures := tc.fixtures
			if fixtures == nil {
				fixtures = []string{healthyFixture}
			}
			env := newEnv(t, testenv.Options{Fixtures: fixtures})
			if tc.rules != nil {
				tc.rules(env.Proxies[0])
			}
			for _, format := range describeFormats {
				r := runCmd(t, env, append(tc.args, "-o", format)...)
				if r.code != 1 || r.stdout != "" {
					t.Errorf("%s: exit %d, stdout %q; want 1 and empty", format, r.code, r.stdout)
				}
				want := "Error: " + tc.stderr
				if !tc.prefix {
					want += "\n"
				}
				if !strings.HasPrefix(r.stderr, want) || strings.Count(r.stderr, "\n") != 1 {
					t.Errorf("%s: stderr = %q, want %q", format, r.stderr, want)
				}
			}
		})
	}
}

// Ownership inputs reach incomplete and stderr for every attributed kind, not only through relations.
func TestDescribeOwnershipDown(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture, listenerSetsFixture, describeFixture}})
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "namespaces"}, testenv.Action{Status: http.StatusForbidden})
	for _, args := range [][]string{{"describe", "tp", "route-policy", "-n", "default"}, {"describe", "gwc", "kgateway"}} {
		r := runCmd(t, env, append(args, "-o", "json")...)
		if r.code != 0 {
			t.Fatalf("%v: exit %d, stderr %q", args, r.code, r.stderr)
		}
		if !strings.Contains(r.stderr, "Warning: namespaces: denied") {
			t.Errorf("%v: stderr = %q, want the namespaces warning", args, r.stderr)
		}
		var e struct {
			Item struct {
				Incomplete []struct{ Input, State string }
			}
		}
		if err := json.Unmarshal([]byte(r.stdout), &e); err != nil {
			t.Fatal(err)
		}
		if len(e.Item.Incomplete) != 1 || e.Item.Incomplete[0].Input != "namespaces" || e.Item.Incomplete[0].State != "denied" {
			t.Errorf("%v: incomplete = %+v, want namespaces denied", args, e.Item.Incomplete)
		}
	}
}

// TestUnknownNeverObservedNotStale: get, describe relations and describe
// conditions apply one stale rule to an Unknown parent condition (D-012).
func TestUnknownNeverObservedNotStale(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture, "../testenv/testdata/unknown-parent.yaml"}})
	r := runCmd(t, env, "get", "hr", "pending", "-n", "pending")
	if r.code != 0 || strings.Contains(r.stdout, "stale") {
		t.Errorf("get: exit %d, stdout %q; want no stale parent", r.code, r.stdout)
	}
	r = runCmd(t, env, "describe", "hr", "pending", "-n", "pending", "-o", "json")
	if r.code != 0 {
		t.Fatalf("describe: exit %d, stderr %q", r.code, r.stderr)
	}
	var v struct {
		Item struct {
			Conditions []struct {
				Scope      string
				Conditions []struct {
					Type, Status, Reason string
					Stale                bool
				}
			}
			Relations struct {
				Parents []struct{ State, Reason string }
			}
		}
	}
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil {
		t.Fatal(err)
	}
	if ps := v.Item.Relations.Parents; len(ps) != 1 || ps[0].State == "stale" || ps[0].Reason != "Pending" {
		t.Errorf("relations.parents = %+v, want one non-stale parent with reason Pending", ps)
	}
	for _, b := range v.Item.Conditions {
		for _, c := range b.Conditions {
			if b.Scope == "parent" && c.Type == "Accepted" && (c.Stale || c.Status != "Unknown" || c.Reason != "Pending") {
				t.Errorf("parent Accepted condition = %+v, want Unknown (Pending), not stale", c)
			}
		}
	}
}
