package app_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DuncanDoyle/kgwctl/internal/bundle/bundletest"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
)

// releaseAfter returns a channel closed after d or at the test's end,
// whichever comes first; a request held on it outlasts any bound under test.
func releaseAfter(t *testing.T, d time.Duration) <-chan struct{} {
	t.Helper()
	ch := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(ch) }) }
	tm := time.AfterFunc(d, release)
	t.Cleanup(func() { tm.Stop(); release() })
	return ch
}

// deadlineManifest is the part of manifest.json the deadline tests read.
type deadlineManifest struct {
	Item struct {
		Collectors []struct{ Name, Status, Reason, Error string } `json:"collectors"`
		Items      []struct{ Path, Status, Reason string }        `json:"items"`
	} `json:"item"`
}

// timedBundle runs bundle to stdout, wants exit 0, fails past limit, and
// returns the manifest and stderr.
func timedBundle(t *testing.T, env *testenv.Env, limit time.Duration, args ...string) (deadlineManifest, string) {
	t.Helper()
	return timedBundleExit(t, env, limit, 0, args...)
}

// timedBundleExit is timedBundle wanting exit code. Exit 2 is a sealed
// archive without cluster data (D-030).
func timedBundleExit(t *testing.T, env *testenv.Env, limit time.Duration, code int, args ...string) (deadlineManifest, string) {
	t.Helper()
	start := time.Now()
	r := runBundle(t, env.Kubeconfig, nil, append([]string{"-o", "-"}, args...)...)
	if el := time.Since(start); el > limit {
		t.Errorf("run took %v, want at most %v", el.Round(time.Millisecond), limit)
	}
	if r.code != code {
		t.Fatalf("exit %d, stderr %q; want %d", r.code, r.stderr, code)
	}
	a := bundletest.Extract(t, strings.NewReader(r.stdout))
	b, ok := a.File("manifest.json")
	if !ok {
		t.Fatal("no manifest.json: archive not sealed")
	}
	var m deadlineManifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m, r.stderr
}

// itemStatus returns the status of the manifest item at path, "" if absent.
func itemStatus(m deadlineManifest, path string) string {
	for _, it := range m.Item.Items {
		if it.Path == path {
			return it.Status
		}
	}
	return ""
}

// collectorStatus returns the status of collector name, "" if absent.
func collectorStatus(m deadlineManifest, name string) string {
	for _, c := range m.Item.Collectors {
		if c.Name == name {
			return c.Status
		}
	}
	return ""
}

// TestBundleTimeoutCoversDetection: --timeout starts before install
// detection. With every list held, the run ends near --timeout with a sealed
// archive; detection and inventory are recorded as timeout. The model exists,
// but its absence of installs is unknown: no "no install" warning or skip.
// With only routes held, the ownership inputs load, so the filter's own
// warning is tested too. No collector writes cluster data: exit 2 (D-030).
func TestBundleTimeoutCoversDetection(t *testing.T) {
	everything := map[string]string{"inventory/detection": "timeout", "proxies": "timeout"}
	routes := map[string]string{"inventory/detection": "timeout"}
	for name, tc := range map[string]struct {
		hold, timeout string
		args          []string
		items         map[string]string
	}{
		"default":                {hold: "*", timeout: "1s", items: everything},
		"all-controllers":        {hold: "*", timeout: "1s", args: []string{"--all-controllers"}, items: everything},
		"routes-held":            {hold: httpRoutes, timeout: "4s", items: routes},
		"routes-all-controllers": {hold: httpRoutes, timeout: "4s", args: []string{"--all-controllers"}, items: routes},
	} {
		t.Run(name, func(t *testing.T) {
			env := newEnv(t, testenv.Options{})
			held := releaseAfter(t, 20*time.Second)
			env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: tc.hold}, testenv.Action{Until: held})

			m, stderr := timedBundleExit(t, env, 12*time.Second, 2, append([]string{"--timeout", tc.timeout}, tc.args...)...)
			if !strings.Contains(stderr, "no cluster data") {
				t.Errorf("stderr %q does not say the bundle holds no cluster data", stderr)
			}
			for path, want := range tc.items {
				if got := itemStatus(m, path); got != want {
					t.Errorf("item %s: status %q, want %q", path, got, want)
				}
			}
			// version and check are skipped by flag in runBundle.
			for _, c := range m.Item.Collectors {
				if c.Status != "timeout" && (c.Status != "skipped" || c.Reason != "flag") {
					t.Errorf("collector %s: %s (%s), want timeout", c.Name, c.Status, c.Reason)
				}
			}
			if strings.Contains(stderr, "no kgateway install detected") {
				t.Errorf("stderr %q claims no install", stderr)
			}
			// Held routes must leave ownership known, or the filter's branch goes untested.
			if tc.hold == httpRoutes && strings.Contains(stderr, "ownership unknown") {
				t.Errorf("stderr %q: ownership inputs did not load; the variant tests nothing", stderr)
			}
		})
	}
}

// TestBundleDetectionDenied: with Deployments denied, detection completes
// without seeing installs, so their absence is unknown. Nothing records
// no install, and stderr does not claim it, also with --all-controllers.
func TestBundleDetectionDenied(t *testing.T) {
	for name, args := range map[string][]string{"default": nil, "all-controllers": {"--all-controllers"}} {
		t.Run(name, func(t *testing.T) {
			env := newEnv(t, testenv.Options{})
			hits := env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "deployments.apps"}, testenv.Action{Status: http.StatusForbidden})
			m, stderr := timedBundle(t, env, time.Minute, args...)
			if hits.N() == 0 {
				t.Fatal("the rule never applied")
			}
			if strings.Contains(stderr, "no kgateway install detected") || !strings.Contains(stderr, "ownership unknown") {
				t.Errorf("stderr %q, want ownership unknown and no claim of no install", stderr)
			}
			for _, c := range m.Item.Collectors {
				if c.Reason == "no install" {
					t.Errorf("collector %s: %s (no install)", c.Name, c.Status)
				}
			}
			for _, it := range m.Item.Items {
				if it.Reason == "no install" {
					t.Errorf("item %s: %s (no install)", it.Path, it.Status)
				}
			}
		})
	}
}

// TestBundleNoInstall: with no install, also with --all-controllers, stderr
// warns, controller-metrics is skipped with reason no install, and the
// controller parts of logs and install are omissions with that reason.
func TestBundleNoInstall(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		warn string
	}{
		"default":         {warn: "Warning: no kgateway install detected; showing objects of all controllers\n"},
		"all-controllers": {args: []string{"--all-controllers"}, warn: "Warning: no kgateway install detected; controller metrics, logs and Deployment are skipped\n"},
	} {
		t.Run(name, func(t *testing.T) {
			env := newEnv(t, testenv.Options{})
			m, stderr := timedBundle(t, env, time.Minute, tc.args...)
			if !strings.Contains(stderr, tc.warn) {
				t.Errorf("stderr %q, want %q", stderr, tc.warn)
			}
			for _, c := range m.Item.Collectors {
				if c.Name == "controller-metrics" && (c.Status != "skipped" || c.Reason != "no install") {
					t.Errorf("collector %s: %s (%s), want skipped (no install)", c.Name, c.Status, c.Reason)
				}
			}
			for _, p := range []string{"controller", "install"} {
				found := false
				for _, it := range m.Item.Items {
					found = found || it.Path == p && it.Status == "skipped" && it.Reason == "no install"
				}
				if !found {
					t.Errorf("no omission %s skipped (no install) in %+v", p, m.Item.Items)
				}
			}
		})
	}
}

// TestBundleTimeoutCoversInventory: with --request-timeout=0, a blocked
// inventory read still ends at --timeout. No collector runs: exit 2 (D-030).
func TestBundleTimeoutCoversInventory(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	held := releaseAfter(t, 20*time.Second)
	// Only the inventory lists proxy Pods by this selector; detection's adds a component label.
	hits := env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "pods", LabelSelector: "kgateway=kube-gateway"}, testenv.Action{Until: held})

	m, _ := timedBundleExit(t, env, 10*time.Second, 2, "--timeout", "4s", "--request-timeout", "0")
	// The read was sent and held, so the timeout below is the inventory's own.
	if hits.N() != 1 {
		t.Fatalf("inventory proxy list held %d times, want 1", hits.N())
	}
	if got := itemStatus(m, "proxies"); got != "timeout" {
		t.Errorf("item proxies: status %q, want timeout", got)
	}
}

// TestBundleTimeoutDuringDetectionDiscovery: --timeout ends while detection
// is held, in discovery or while listing. The archive is still sealed with
// no cluster data: detection is timeout, every collector timeout, exit 2
// (D-030). With --install, the cut detection is not reported as an unknown install.
func TestBundleTimeoutDuringDetectionDiscovery(t *testing.T) {
	// Other group versions answer before heldGV.
	discovery := testenv.Match{Kind: testenv.KindDiscovery, Resource: heldGV}
	lists := testenv.Match{Kind: testenv.KindAcquire, Resource: "*"}
	install := []string{"--install", "kgateway-system/kgateway"}
	for _, tc := range []struct {
		name string
		hold testenv.Match
		args []string
	}{
		{name: "discovery", hold: discovery},
		{name: "discovery-install-flag", hold: discovery, args: install},
		{name: "lists-install-flag", hold: lists, args: install},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newEnv(t, testenv.Options{})
			held := releaseAfter(t, 20*time.Second)
			hits := env.Proxies[0].Rule(tc.hold, testenv.Action{Until: held})

			m, stderr := timedBundleExit(t, env, 10*time.Second, 2, append([]string{"--timeout", "2s"}, tc.args...)...)
			if hits.N() == 0 {
				t.Fatal("detection's discovery was not held")
			}
			if got := itemStatus(m, "inventory/detection"); got != "timeout" {
				t.Errorf("item inventory/detection: status %q, want timeout", got)
			}
			for _, c := range m.Item.Collectors {
				if c.Status != "timeout" && c.Status != "skipped" {
					t.Errorf("collector %s: status %q, want timeout", c.Name, c.Status)
				}
			}
			if !strings.Contains(stderr, "install detection timed out") {
				t.Errorf("stderr %q does not say detection timed out", stderr)
			}
			if strings.Contains(stderr, "no kgateway install detected") || strings.Contains(stderr, "no such install") {
				t.Errorf("stderr %q claims no install", stderr)
			}
		})
	}
}

// TestBundleRequestTimeoutCutsDetection: --request-timeout cuts detection's
// discovery while --timeout has time left. Collectors are timeout, and their
// error says detection timed out, not that the collection deadline passed.
// The archive has no cluster data: exit 2 (D-030).
func TestBundleRequestTimeoutCutsDetection(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	held := releaseAfter(t, 30*time.Second)
	hits := env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindDiscovery, Resource: heldGV}, testenv.Action{Until: held})

	m, stderr := timedBundleExit(t, env, 12*time.Second, 2, "--request-timeout", "2s", "--timeout", "5m")
	if hits.N() == 0 {
		t.Fatal("detection's discovery was not held")
	}
	if got := itemStatus(m, "inventory/detection"); got != "timeout" {
		t.Errorf("item inventory/detection: status %q, want timeout", got)
	}
	for _, c := range m.Item.Collectors {
		if c.Status == "skipped" {
			continue
		}
		if c.Status != "timeout" || c.Error != "install detection timed out" {
			t.Errorf("collector %s: %s %q, want timeout %q", c.Name, c.Status, c.Error, "install detection timed out")
		}
	}
	if !strings.Contains(stderr, "install detection timed out") {
		t.Errorf("stderr %q does not say detection timed out", stderr)
	}
}

// TestBundleDeadlineBeforeFirstResponse: the API server accepts detection's
// first request and never answers. Either deadline still seals an archive
// with detection timeout and every collector timeout, exit 2 (D-030).
func TestBundleDeadlineBeforeFirstResponse(t *testing.T) {
	for name, args := range map[string][]string{
		"timeout":         {"--timeout", "2s"},
		"request-timeout": {"--request-timeout", "2s", "--timeout", "5m"},
	} {
		t.Run(name, func(t *testing.T) {
			env := newEnv(t, testenv.Options{})
			held := releaseAfter(t, 30*time.Second)
			// Detection starts with discovery, so no request gets a response.
			hits := env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindDiscovery, Resource: "*"}, testenv.Action{Until: held})

			m, stderr := timedBundleExit(t, env, 12*time.Second, 2, args...)
			if hits.N() == 0 {
				t.Fatal("detection's discovery was not held")
			}
			if got := itemStatus(m, "inventory/detection"); got != "timeout" {
				t.Errorf("item inventory/detection: status %q, want timeout", got)
			}
			for _, c := range m.Item.Collectors {
				if c.Status != "timeout" && c.Status != "skipped" {
					t.Errorf("collector %s: status %q, want timeout", c.Name, c.Status)
				}
			}
			if !strings.Contains(stderr, "install detection timed out") {
				t.Errorf("stderr %q does not say detection timed out", stderr)
			}
		})
	}
}

// heldGV is a group version detection reads after others have answered.
const heldGV = "gateway.kgateway.dev/v1alpha1"

// TestBundleDiscoveryRequestTimeout: blocked discovery in the resources and
// install collectors gives up after --request-timeout, not the run budget.
func TestBundleDiscoveryRequestTimeout(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	held := releaseAfter(t, 60*time.Second)
	// /api and /apis: only the collectors' group discovery; detection reads group versions.
	hits := env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindDiscovery, Resource: ""}, testenv.Action{Until: held})

	m, _ := timedBundle(t, env, 15*time.Second, "--request-timeout", "3s", "--timeout", "2m")
	if hits.N() == 0 {
		t.Fatal("no discovery request was held")
	}
	if got := collectorStatus(m, "resources"); got != "failed" {
		t.Errorf("collector resources: status %q, want failed", got)
	}
	if got := itemStatus(m, "install/_cluster"); got != "timeout" {
		t.Errorf("item install/_cluster: status %q, want timeout", got)
	}
}
