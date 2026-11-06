package app_test

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DuncanDoyle/kgateway-ui/pkg/install"

	"github.com/DuncanDoyle/kgwctl/internal/testenv"
)

const (
	orphansFixture      = "../testenv/testdata/orphans.yaml"
	mixedFixture        = "../testenv/testdata/mixed-parents.yaml"
	otherFixture        = "../testenv/testdata/other-controller.yaml"
	listenerSetsFixture = "../testenv/testdata/listenersets.yaml"
	paramsFixture       = "../testenv/testdata/parameters-and-backends.yaml"
	delegationFixture   = "../testenv/testdata/delegation.yaml"
	xListenerSetCRDs    = "../testenv/testdata/crds/xlistenerset"
	httpRoutes          = "httproutes.gateway.networking.k8s.io"
)

// testNow puts AGE ten days after creation, so every fixture object prints "10d".
func testNow() time.Time { return time.Now().Add(240 * time.Hour) }

// newEnv starts an Env with the XListenerSet CRD, so served kinds never depend on test order (R18).
func newEnv(t *testing.T, opts testenv.Options) *testenv.Env {
	t.Helper()
	opts.CRDDirs = append(opts.CRDDirs, xListenerSetCRDs)
	return testenv.New(t, opts)
}

// serverFields are the fields the API server sets differently on every run.
var serverFields = regexp.MustCompile(`((?:creationTimestamp|resourceVersion|uid)"?:\s*)("[^"]*"|\S+)`)

// normalize replaces server-set fields of yaml and json output with a fixed value.
func normalize(format, s string) string {
	if format != "yaml" && format != "json" {
		return s
	}
	return serverFields.ReplaceAllString(s, `${1}"X"`)
}

// onlyWarnings fails unless every stderr line is a warning.
func onlyWarnings(t *testing.T, stderr string) {
	t.Helper()
	for _, l := range strings.Split(strings.TrimSuffix(stderr, "\n"), "\n") {
		if l != "" && !strings.HasPrefix(l, "Warning: ") {
			t.Errorf("stderr line %q is not a warning", l)
		}
	}
}

var getFormats = []string{"table", "wide", "yaml", "json"}

func TestGet(t *testing.T) {
	everything := []string{"get", "gwc,all", "-A"}
	cases := []struct {
		name     string
		fixtures []string
		args     []string
		rules    func(p *testenv.Proxy)
		// warn is a substring stderr must contain; "" means stderr must be empty.
		warn string
		// group means the table output must have a GROUP column.
		group bool
	}{
		{name: "healthy", fixtures: []string{healthyFixture}, args: everything},
		{name: "healthy-gateways", fixtures: []string{healthyFixture}, args: []string{"get", "gw", "-n", "kgateway-system"}},
		{name: "healthy-named", fixtures: []string{healthyFixture}, args: []string{"get", "hr", "example", "-n", "default"}},
		{name: "healthy-selector", fixtures: []string{healthyFixture}, args: []string{"get", "gw,hr", "-A", "-l", "app!=none"}},
		{name: "orphans", fixtures: []string{healthyFixture, orphansFixture}, args: everything},
		{name: "mixed-parents", fixtures: []string{healthyFixture, mixedFixture}, args: []string{"get", "gw,hr", "-n", "mixed"}},
		{name: "two-installs", fixtures: []string{twoInstalls}, args: everything},
		{name: "two-installs-pinned", fixtures: []string{twoInstalls}, args: append(everything, "--install", "kgw-a/kgateway")},
		{name: "other-controller", fixtures: []string{healthyFixture, otherFixture}, args: everything},
		{name: "other-controller-all", fixtures: []string{healthyFixture, otherFixture}, args: append(everything, "--all-controllers")},
		{name: "listenersets", fixtures: []string{healthyFixture, listenerSetsFixture}, args: []string{"get", "ls,hr", "-n", "kgateway-system"}},
		// Overlapping names list each object once, in the alias table.
		{name: "listenersets-overlap", fixtures: []string{healthyFixture, listenerSetsFixture}, args: []string{"get", "ls,xlistenersets", "-n", "kgateway-system"}, group: true},
		{name: "all-overlap", fixtures: []string{healthyFixture, listenerSetsFixture}, args: []string{"get", "all,xlistenersets", "-n", "kgateway-system"}, group: true},
		// Only ListenerSet served: the alias table still has GROUP. The CRD stays installed (R18); discovery hides it.
		{name: "listenersets-one-api", fixtures: []string{healthyFixture, listenerSetsFixture}, args: []string{"get", "ls", "-n", "kgateway-system"}, group: true,
			rules: func(p *testenv.Proxy) {
				p.Rule(testenv.Match{Kind: testenv.KindDiscovery, Resource: "gateway.networking.x-k8s.io/v1alpha1"}, testenv.Action{Status: http.StatusNotFound})
			}},
		// R24: a name selects an object the hiding rule hides.
		{name: "other-controller-named", fixtures: []string{healthyFixture, otherFixture}, args: []string{"get", "gw", "edge", "-n", "other"}},
		{name: "xlistenersets", fixtures: []string{healthyFixture, listenerSetsFixture}, args: []string{"get", "xlistenersets", "-n", "kgateway-system"}},
		{name: "parameters-and-backends", fixtures: []string{healthyFixture, paramsFixture}, args: everything},
		{name: "delegation", fixtures: []string{healthyFixture, delegationFixture}, args: []string{"get", "hr", "-n", "default"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newEnv(t, testenv.Options{Fixtures: tc.fixtures})
			if tc.rules != nil {
				tc.rules(env.Proxies[0])
			}
			for _, format := range getFormats {
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
					if format == "json" {
						checkList(t, r.stdout)
					}
					if tc.group && format == "table" && !strings.Contains(r.stdout, " GROUP ") {
						t.Errorf("header has no GROUP column:\n%s", r.stdout)
					}
					if strings.Contains(r.stdout, "managedFields") {
						t.Error("stdout carries managedFields")
					}
					golden(t, "get", tc.name+"."+format, normalize(format, r.stdout))
				})
			}
		})
	}
}

// checkList fails unless stdout is a v1 List naming each object once.
func checkList(t *testing.T, stdout string) {
	t.Helper()
	var l struct {
		APIVersion, Kind string
		Items            []struct {
			APIVersion, Kind string
			Metadata         struct{ Namespace, Name string }
		}
	}
	if err := json.Unmarshal([]byte(stdout), &l); err != nil || l.APIVersion != "v1" || l.Kind != "List" {
		t.Fatalf("stdout is not a v1 List (%v): %s", err, stdout)
	}
	seen := map[string]bool{}
	for _, it := range l.Items {
		id := it.APIVersion + " " + it.Kind + " " + it.Metadata.Namespace + "/" + it.Metadata.Name
		if seen[id] {
			t.Errorf("List has %s twice", id)
		}
		seen[id] = true
	}
}

// TestGetEmpty prints kubectl's message on stderr and an empty List for yaml.
func TestGetEmpty(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture}})
	r := runCmd(t, env, "get", "hr", "-n", "kgateway-system")
	if r.code != 0 || r.stdout != "" || r.stderr != "No resources found in kgateway-system namespace.\n" {
		t.Errorf("exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
	r = runCmd(t, env, "get", "hr", "-A", "-l", "app=none")
	if r.code != 0 || r.stdout != "" || r.stderr != "No resources found\n" {
		t.Errorf("-A: exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
	r = runCmd(t, env, "get", "hr", "-n", "kgateway-system", "-o", "yaml")
	want := "apiVersion: v1\nitems: []\nkind: List\nmetadata:\n  resourceVersion: \"\"\n"
	if r.code != 0 || r.stdout != want || r.stderr != "" {
		t.Errorf("yaml: exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
}

func TestGetErrors(t *testing.T) {
	deny := func(kind string, status int) func(p *testenv.Proxy) {
		return func(p *testenv.Proxy) {
			p.Rule(testenv.Match{Kind: kind, Resource: httpRoutes}, testenv.Action{Status: status})
		}
	}
	cases := []struct {
		name     string
		fixtures []string
		classic  bool
		rules    func(p *testenv.Proxy)
		args     []string
		// stderr is the error line's prefix, after "Error: ".
		stderr string
	}{
		{name: "httproutes-500", rules: deny(testenv.KindAcquire, http.StatusInternalServerError), args: []string{"get", "hr", "-A"},
			stderr: "cannot read " + httpRoutes + ": failed: "},
		// Classic LIST denied.
		{name: "httproutes-403", classic: true, rules: deny(testenv.KindAcquire, http.StatusForbidden), args: []string{"get", "hr", "-A"},
			stderr: "cannot read " + httpRoutes + ": denied: "},
		// The WatchList request is the acquisition, so its 403 is the watch denied before any data.
		{name: "watch-denied-immediate", rules: deny(testenv.KindAcquire, http.StatusForbidden), args: []string{"get", "gw,hr", "-A"},
			stderr: "cannot read " + httpRoutes + ": denied: "},
		{name: "unknown-kind", args: []string{"get", "directresponses"}, stderr: `unknown kind "directresponses"`},
		{name: "unserved-kind", rules: func(p *testenv.Proxy) {
			p.Rule(testenv.Match{Kind: testenv.KindDiscovery, Resource: "gateway.networking.x-k8s.io/v1alpha1"}, testenv.Action{Status: http.StatusNotFound})
		}, args: []string{"get", "xlistenersets"}, stderr: `kind "xlistenersets" is not served by this cluster`},
		// R28: a name one kind has and another lacks fails the whole get.
		{name: "named-in-one-kind", args: []string{"get", "hr,gw", "example", "-n", "default"}, stderr: `gateways "example" not found`},
		{name: "not-found", args: []string{"get", "gw", "missing", "-n", "kgateway-system"}, stderr: `gateways "missing" not found`},
		{name: "timeout", rules: func(p *testenv.Proxy) {
			p.Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: httpRoutes}, testenv.Action{Delay: 5 * time.Second})
		}, args: []string{"get", "hr", "-A", "--request-timeout", "1s"}, stderr: "timed out loading " + httpRoutes},
		// R16: an --install that names no detected install is an error while ownership is known.
		{name: "install-not-detected", fixtures: []string{twoInstalls}, args: []string{"get", "gw", "-A", "--install", "kgw-x/nope"},
			stderr: `install "kgw-x/nope" not detected`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.classic {
				testenv.ClassicWatch(t)
			}
			fixtures := tc.fixtures
			if fixtures == nil {
				fixtures = []string{healthyFixture}
			}
			env := newEnv(t, testenv.Options{Fixtures: fixtures})
			if tc.rules != nil {
				tc.rules(env.Proxies[0])
			}
			for _, format := range getFormats {
				r := runCmd(t, env, append(tc.args, "-o", format)...)
				if r.code != 1 {
					t.Errorf("%s: exit %d, want 1; stderr %q", format, r.code, r.stderr)
				}
				if r.stdout != "" {
					t.Errorf("%s: stdout = %q, want empty", format, r.stdout)
				}
				if !strings.HasPrefix(r.stderr, "Error: "+tc.stderr) || strings.Count(r.stderr, "\n") != 1 {
					t.Errorf("%s: stderr = %q, want one line starting %q", format, r.stderr, "Error: "+tc.stderr)
				}
			}
		})
	}
}

// TestGetWatchDeniedHeld checks the §6.3 invariant as the plan states it: with
// the httproutes watch denied after LIST sync, each run either prints the full
// table with exit 0 or fails denied with empty stdout. No timing aid.
func TestGetWatchDeniedHeld(t *testing.T) {
	testenv.ClassicWatch(t)
	env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture}})
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindWatch, Resource: httpRoutes}, testenv.Action{HoldWatchThenStatus: http.StatusForbidden})
	want := readGolden(t, "get", "healthy.table")
	var ok, denied int
	for i := range 10 {
		r := runCmd(t, env, "get", "gwc,all", "-A")
		switch {
		case r.code == 0 && r.stdout == want:
			ok++
		case r.code == 1 && r.stdout == "" && strings.Contains(r.stderr, "denied"):
			denied++
		default:
			t.Errorf("run %d breaks the invariant: exit %d, stderr %q, stdout:\n%s", i, r.code, r.stderr, r.stdout)
		}
	}
	t.Logf("%d runs ok, %d denied", ok, denied)
}

// TestGetWatchDeniedAfterSync forces the post-sync 403 into the snapshot:
// Deployments are held for 1s, so Load is still waiting on core inputs when
// the 403 lands 200ms after the httproutes LIST. Every run must fail denied.
func TestGetWatchDeniedAfterSync(t *testing.T) {
	testenv.ClassicWatch(t)
	env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture}})
	p := env.Proxies[0]
	held := p.Rule(testenv.Match{Kind: testenv.KindWatch, Resource: httpRoutes}, testenv.Action{HoldWatchThenStatus: http.StatusForbidden})
	slow := p.Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "deployments.apps"}, testenv.Action{Delay: time.Second})
	want := readGolden(t, "get", "healthy.table")
	const runs = 3
	for i := range runs {
		before := held.N()
		r := runCmd(t, env, "get", "gwc,all", "-A")
		switch {
		case r.code == 1 && r.stdout == "" && strings.Contains(r.stderr, "cannot read "+httpRoutes+": denied"):
		case r.code == 0 && r.stdout == want:
			t.Errorf("run %d: exit 0 with the full table; the 403 did not land before the snapshot", i)
		default:
			t.Errorf("run %d: breaks the invariant: exit %d, stderr %q, stdout:\n%s", i, r.code, r.stderr, r.stdout)
		}
		if held.N() == before {
			t.Errorf("run %d: the held watch rule never applied", i)
		}
	}
	t.Logf("held watch hits %d, delayed deployments hits %d over %d runs", held.N(), slow.N(), runs)
}

// TestGetIndexInputDown: with Namespaces denied, Gateways still list and ROUTES says why it is unknown.
func TestGetIndexInputDown(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture}})
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "namespaces"}, testenv.Action{Status: http.StatusForbidden})
	r := runCmd(t, env, "get", "gateways", "-A", "-o", "wide")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	lines := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], "unknown (namespaces: denied)") {
		t.Errorf("stdout = %q, want one Gateway with ROUTES unknown (namespaces: denied)", r.stdout)
	}
	if strings.Count(lines[1], "unknown (namespaces: denied)") != 2 {
		t.Errorf("ROUTES and LISTENERSETS are not both unknown: %q", lines[1])
	}
	onlyWarnings(t, r.stderr)
	if !strings.Contains(r.stderr, "Warning: relations unknown (namespaces: denied)") {
		t.Errorf("stderr = %q, want the relations warning", r.stderr)
	}
	// GatewayClasses have no index-derived cell, so no relations warning.
	r = runCmd(t, env, "get", "gwc")
	if r.code != 0 || strings.Contains(r.stderr, "relations unknown") {
		t.Errorf("get gwc: exit %d, stderr %q, want no relations warning", r.code, r.stderr)
	}
}

// TestGetOwnershipUnknown: with controller Pods denied, INSTALL is unknown (R17)
// and an undetected --install warns and hides nothing (R16).
func TestGetOwnershipUnknown(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{twoInstalls}})
	ctrlSel := install.UpstreamDetector{}.ControllerPodSelector().String()
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "pods", LabelSelector: ctrlSel}, testenv.Action{Status: http.StatusForbidden})
	r := runCmd(t, env, "get", "gw", "-A", "--install", "kgw-x/nope")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	lines := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
	if len(lines) != 3 || !strings.HasSuffix(lines[0], "INSTALL") {
		t.Fatalf("stdout = %q, want both Gateways with an INSTALL column", r.stdout)
	}
	for _, l := range lines[1:] {
		if !strings.HasSuffix(l, " unknown") {
			t.Errorf("row %q, want INSTALL unknown", l)
		}
	}
	onlyWarnings(t, r.stderr)
	if !strings.Contains(r.stderr, "ownership unknown (pods: denied)") {
		t.Errorf("stderr = %q, want the ownership warning", r.stderr)
	}
}

// TestGetAllControllersKeepsInputWarnings: --all-controllers drops only the
// hiding explanation; an unreadable ownership input still warns.
func TestGetAllControllersKeepsInputWarnings(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture, otherFixture}})
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "deployments.apps"}, testenv.Action{Status: http.StatusForbidden})
	r := runCmd(t, env, "get", "tp", "-A", "--all-controllers")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	onlyWarnings(t, r.stderr)
	if !strings.Contains(r.stderr, "Warning: ownership unknown (deployments: denied)") {
		t.Errorf("stderr = %q, want the ownership warning", r.stderr)
	}
	if strings.Contains(r.stderr, "showing objects of all controllers") {
		t.Errorf("stderr = %q, want no hiding explanation under --all-controllers", r.stderr)
	}
}
