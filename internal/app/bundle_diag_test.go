package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/go-logr/logr/funcr"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"

	"github.com/DuncanDoyle/kgwctl/internal/app"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// Pattern-free canaries: no redaction pattern matches them, so only keeping them out works.
const (
	selectorCanary = "kgwctlCanarySelectorValue!"
	ownerCanary    = "kgwctl canary owner"
	detectorCanary = "kgwctl canary detector"
	warningCanary  = "kgwctl-canary-warning-header"
)

// writeFixture writes body to a temporary YAML file and returns its path.
func writeFixture(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestBundleManifestKeepsClusterTextOut: an invalid discovery selector is
// recorded as a fixed reason, and under --all-controllers a proxy whose
// full-name annotation is no known Gateway gets no owner.
func TestBundleManifestKeepsClusterTextOut(t *testing.T) {
	healthy, err := os.ReadFile(healthyFixture)
	if err != nil {
		t.Fatal(err)
	}
	// The selector env goes on the Deployment template and the controller Pod, which scope reads first.
	withEnv := func(indent string) (string, string) {
		image := indent + "image: cr.kgateway.dev/kgateway-dev/kgateway:v2.5.0\n" + indent + "ports:\n"
		return image, strings.Replace(image, indent+"ports:", indent+"env:\n"+indent+"  - name: KGW_DISCOVERY_NAMESPACE_SELECTORS\n"+
			indent+"    value: '[{\"matchLabels\":{\"team\":\""+selectorCanary+"\"}}]'\n"+indent+"ports:", 1)
	}
	d0, d1 := withEnv("          ")
	p0, p1 := withEnv("      ")
	if bytes.Count(healthy, []byte(d0)) != 1 || bytes.Count(healthy, []byte(p0)) != 1 {
		t.Fatal("healthy fixture: want one Deployment and one Pod controller container")
	}
	inst := writeFixture(t, "install.yaml", strings.NewReplacer(d0, d1, p0, p1).Replace(string(healthy)))
	bogus := strings.NewReplacer("name: http-a\n", "name: bogus\n",
		"gateway-full-name: http}", "gateway-full-name: '"+ownerCanary+"'}").Replace(proxyPodYAML("http-a"))
	proxies := writeFixture(t, "proxies.yaml", proxyPodYAML("http-a")+"---\n"+bogus)

	e := newEnv(t, testenv.Options{Fixtures: []string{inst, proxies}})
	a := bundleArchive(t, e, "--all-controllers")
	b, _ := a.File("manifest.json")
	for _, c := range []string{selectorCanary, ownerCanary} {
		if bytes.Contains(b, []byte(c)) {
			t.Errorf("manifest.json holds canary %q", c)
		}
	}
	var doc struct {
		Item struct {
			Scope    pb.Scope     `json:"scope"`
			Replicas []pb.Replica `json:"replicas"`
		} `json:"item"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Item.Scope.Installs) != 1 {
		t.Fatalf("installs %+v, want one", doc.Item.Scope.Installs)
	}
	if d := doc.Item.Scope.Installs[0].Discovery; d.Mode != "unknown" || d.Reason != "invalid selector" {
		t.Errorf("discovery %+v, want unknown (invalid selector)", d)
	}
	owners := map[string]string{}
	for _, r := range doc.Item.Replicas {
		if r.Role == pb.RoleProxy {
			owners[r.Pod] = r.Owner
		}
	}
	if owners["http-a"] != "kgateway-system/http" {
		t.Errorf("http-a owner %q, want kgateway-system/http", owners["http-a"])
	}
	if o, ok := owners["bogus"]; !ok || o != "" {
		t.Errorf("bogus owner %q (listed %t), want listed with no owner", o, ok)
	}
}

// leakyDetector detects app=leaky-controller Deployments with an install ID
// that fails ValidateID and carries a canary.
type leakyDetector struct{}

func (leakyDetector) Name() string { return "leaky" }
func (leakyDetector) ControllerSelector() labels.Selector {
	return labels.SelectorFromSet(labels.Set{"app": "leaky-controller"})
}
func (d leakyDetector) ControllerPodSelector() labels.Selector { return d.ControllerSelector() }
func (leakyDetector) ProxySelector() labels.Selector {
	return labels.SelectorFromSet(labels.Set{"app": "leaky-proxy"})
}
func (leakyDetector) Detect(d *appsv1.Deployment) (install.Install, bool) {
	return install.Install{CustomID: detectorCanary, Namespace: d.Namespace, Name: d.Name,
		ControllerNames: []string{"example.com/leaky"}, Deployment: d}, true
}

// TestBundleDetectorErrorStderr: a rejected install's error never reaches stderr.
func TestBundleDetectorErrorStderr(t *testing.T) {
	const dep = "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: leaky, namespace: default, labels: {app: leaky-controller}}\n" +
		"spec:\n  selector: {matchLabels: {app: leaky-controller}}\n  template:\n    metadata: {labels: {app: leaky-controller}}\n" +
		"    spec: {containers: [{name: c, image: example.com/leaky:v1}]}\n"
	e := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture, writeFixture(t, "leaky.yaml", dep)}})
	var out, errb bytes.Buffer
	cfg := app.Config{Build: testBuild, Out: &out, Err: &errb, Now: testNow,
		Registry: extension.New(extension.WithDetectors(leakyDetector{}))}
	root := app.NewRootWithBundleHooks(cfg, app.BundleTestHooks{Now: func() time.Time { return bundleNow }})
	code := run(root, "--kubeconfig", e.Kubeconfig, "bundle", "--skip", "version,check", "-o", filepath.Join(t.TempDir(), "out.tar.gz"))
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
	if strings.Contains(errb.String(), detectorCanary) {
		t.Errorf("stderr holds the detector canary: %q", errb.String())
	}
}

// versionProbe GETs /version under the run's context; discovery's
// ServerVersion would use context.TODO() and lose the context's logger.
type versionProbe struct{}

func (versionProbe) Name() string { return "version-probe" }
func (versionProbe) Collect(ctx context.Context, env pb.Env, _ pb.Sink) error {
	return env.Discovery.RESTClient().Get().AbsPath("/version").Do(ctx).Error()
}

// TestBundleWarningHeaders: a server Warning header never reaches stderr, even
// when the command's context carries a logger that writes there.
func TestBundleWarningHeaders(t *testing.T) {
	e := newEnv(t, testenv.Options{})
	body := []byte(`{"major":"1","minor":"36","gitVersion":"v1.36.0"}`)
	hits := e.Proxies[0].Rule(testenv.Match{Kind: testenv.KindDiscovery, Resource: "version"},
		testenv.Action{Status: http.StatusOK, Body: body, Header: http.Header{"Warning": {`299 - "` + warningCanary + `"`}}})
	var errb syncBuffer
	logger := funcr.New(func(prefix, args string) { fmt.Fprintln(&errb, prefix, args) }, funcr.Options{})
	cfg := app.Config{Build: testBuild, Out: &bytes.Buffer{}, Err: &errb, Now: testNow, Collectors: []pb.Collector{versionProbe{}}}
	root := app.NewRootWithBundleHooks(cfg, app.BundleTestHooks{Now: func() time.Time { return bundleNow }})
	root.SetArgs([]string{"--kubeconfig", e.Kubeconfig, "bundle", "--skip", "version,check", "-o", filepath.Join(t.TempDir(), "out.tar.gz")})
	if err := root.ExecuteContext(klog.NewContext(context.Background(), logger)); err != nil {
		t.Fatalf("bundle: %v, stderr %q", err, errb.String())
	}
	if hits.N() == 0 {
		t.Fatal("no request got the Warning header")
	}
	if strings.Contains(errb.String(), warningCanary) {
		t.Errorf("stderr holds the Warning canary: %q", errb.String())
	}
}
