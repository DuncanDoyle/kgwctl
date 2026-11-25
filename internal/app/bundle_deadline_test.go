package app_test

import (
	"encoding/json"
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
		Collectors []struct{ Name, Status string } `json:"collectors"`
		Items      []struct{ Path, Status string } `json:"items"`
	} `json:"item"`
}

// timedBundle runs bundle to stdout, fails past limit, and returns the
// manifest and stderr.
func timedBundle(t *testing.T, env *testenv.Env, limit time.Duration, args ...string) (deadlineManifest, string) {
	t.Helper()
	start := time.Now()
	r := runBundle(t, env.Kubeconfig, nil, append([]string{"-o", "-"}, args...)...)
	if el := time.Since(start); el > limit {
		t.Errorf("run took %v, want at most %v", el.Round(time.Millisecond), limit)
	}
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
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
// archive; detection and inventory are recorded as timeout.
func TestBundleTimeoutCoversDetection(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	held := releaseAfter(t, 20*time.Second)
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "*"}, testenv.Action{Until: held})

	m, _ := timedBundle(t, env, 8*time.Second, "--timeout", "1s")
	for path, want := range map[string]string{"inventory/detection": "timeout", "proxies": "timeout"} {
		if got := itemStatus(m, path); got != want {
			t.Errorf("item %s: status %q, want %q", path, got, want)
		}
	}
	for _, c := range m.Item.Collectors {
		if c.Status != "timeout" && c.Status != "skipped" {
			t.Errorf("collector %s: status %q, want timeout", c.Name, c.Status)
		}
	}
}

// TestBundleTimeoutCoversInventory: with --request-timeout=0, a blocked
// inventory read still ends at --timeout.
func TestBundleTimeoutCoversInventory(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	held := releaseAfter(t, 20*time.Second)
	// Only the inventory lists proxy Pods by this selector; detection's adds a component label.
	hits := env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "pods", LabelSelector: "kgateway=kube-gateway"}, testenv.Action{Until: held})

	m, _ := timedBundle(t, env, 10*time.Second, "--timeout", "4s", "--request-timeout", "0")
	// The read was sent and held, so the timeout below is the inventory's own.
	if hits.N() != 1 {
		t.Fatalf("inventory proxy list held %d times, want 1", hits.N())
	}
	if got := itemStatus(m, "proxies"); got != "timeout" {
		t.Errorf("item proxies: status %q, want timeout", got)
	}
}

// TestBundleTimeoutDuringDetectionDiscovery: --timeout ends while detection
// is held, in discovery or while listing. The cluster was reached, so the
// archive is still sealed: detection is timeout, every collector timeout,
// exit 0. With --install, the cut detection is not reported as an unknown install.
func TestBundleTimeoutDuringDetectionDiscovery(t *testing.T) {
	// Other group versions answer before heldGV, so the cluster is reached.
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

			m, stderr := timedBundle(t, env, 10*time.Second, append([]string{"--timeout", "2s"}, tc.args...)...)
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
			if strings.Contains(stderr, "no kgateway install detected") {
				t.Errorf("stderr %q claims no install", stderr)
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
