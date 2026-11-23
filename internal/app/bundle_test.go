package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/DuncanDoyle/kgwctl/internal/app"
	"github.com/DuncanDoyle/kgwctl/internal/bundle/bundletest"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// stubCollector is a downstream collector driven by fn.
type stubCollector struct {
	name string
	fn   func(ctx context.Context, s pb.Sink) error
}

func (c stubCollector) Name() string { return c.name }
func (c stubCollector) Collect(ctx context.Context, _ pb.Env, s pb.Sink) error {
	return c.fn(ctx, s)
}

// writeText is a stub collector that writes one small text item at x/<name>.txt.
func writeText(name string) stubCollector {
	return stubCollector{name: name, fn: func(ctx context.Context, s pb.Sink) error {
		return s.Text(ctx, pb.Path{"x", name + ".txt"}, strings.NewReader("hello\n"), pb.Source{})
	}}
}

// probe is a collector that makes one API request, so an empty request
// recorder proves the command returned before running collectors.
type probe struct{ ran atomic.Bool }

func (*probe) Name() string { return "probe" }
func (p *probe) Collect(_ context.Context, env pb.Env, _ pb.Sink) error {
	p.ran.Store(true)
	_, err := env.Discovery.ServerVersion()
	return err
}

// assertNoRun fails unless the probe never ran and the API saw no request.
func assertNoRun(t *testing.T, env *testenv.Env, p *probe) {
	t.Helper()
	if p.ran.Load() {
		t.Error("a collector ran")
	}
	noRequests(t, env)
}

// manifestDoc is the part of manifest.json the tests read.
type manifestDoc struct {
	Item struct {
		Server     string `json:"server"`
		Collectors []struct {
			Name, Status, Reason string
		} `json:"collectors"`
		Items []struct{ Path string } `json:"items"`
	} `json:"item"`
}

func readManifest(t *testing.T, a bundletest.Archive) manifestDoc {
	t.Helper()
	b, ok := a.File("manifest.json")
	if !ok {
		t.Fatal("no manifest.json")
	}
	var m manifestDoc
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

var bundleNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// runBundle runs `bundle args` against kubeconfig with extra as downstream
// collectors. The version and check collectors are skipped; their tests use runBundleWith.
func runBundle(t *testing.T, kubeconfig string, extra []pb.Collector, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	cfg := app.Config{Build: testBuild, Out: &out, Err: &errb, Now: testNow, Collectors: extra}
	root := app.NewRootWithBundleHooks(cfg, app.BundleTestHooks{Now: func() time.Time { return bundleNow }})
	code := run(root, append([]string{"--kubeconfig", kubeconfig, "bundle", "--skip", "version,check"}, args...)...)
	return result{code: code, stdout: out.String(), stderr: errb.String()}
}

func noRequests(t *testing.T, env *testenv.Env) {
	t.Helper()
	if reqs := env.Proxies[0].Requests(); len(reqs) != 0 {
		t.Errorf("got %d requests, want none: %v", len(reqs), reqs)
	}
}

func TestBundleOutputExists(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	path := filepath.Join(t.TempDir(), "out.tar.gz")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &probe{}
	r := runBundle(t, env.Kubeconfig, []pb.Collector{p}, "-o", path)
	if r.code != 2 {
		t.Fatalf("exit %d, stderr %q; want 2", r.code, r.stderr)
	}
	if b, _ := os.ReadFile(path); string(b) != "keep" {
		t.Errorf("existing file changed to %q", b)
	}
	assertNoRun(t, env, p)
}

func TestBundleUnwritableDir(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	p := &probe{}
	r := runBundle(t, env.Kubeconfig, []pb.Collector{p}, "-o", filepath.Join(t.TempDir(), "missing", "out.tar.gz"))
	if r.code != 2 {
		t.Fatalf("exit %d, stderr %q; want 2", r.code, r.stderr)
	}
	assertNoRun(t, env, p)
}

func TestBundleDefaultName(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	t.Chdir(t.TempDir())
	r := runBundle(t, env.Kubeconfig, []pb.Collector{writeText("alpha")})
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q; want 0", r.code, r.stderr)
	}
	const root = "kgwctl-bundle-20260102T030405Z"
	fi, err := os.Stat(root + ".tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
	f, err := os.Open(root + ".tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	a := bundletest.Extract(t, f)
	if a.Root != root {
		t.Errorf("archive root %q, want %q", a.Root, root)
	}
	for _, m := range a.Members {
		if strings.Contains(m.Name, testenv.ContextName(0)) {
			t.Errorf("member %q names the context", m.Name)
		}
	}
	if got := readManifest(t, a).Item.Server; got != "<apiserver>" {
		t.Errorf("manifest server = %q, want <apiserver>", got)
	}
	if mf, _ := a.File("manifest.json"); bytes.Contains(mf, []byte(env.Proxies[0].URL())) {
		t.Error("manifest names the API server host")
	}
}

func TestBundleStdout(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	p := &probe{}
	r := runBundle(t, env.Kubeconfig, []pb.Collector{writeText("alpha"), p}, "-o", "-")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q; want 0", r.code, r.stderr)
	}
	a := bundletest.Extract(t, strings.NewReader(r.stdout))
	if _, ok := a.File("x/alpha.txt"); !ok {
		t.Errorf("archive lacks x/alpha.txt: %v", a.Members)
	}
	// The probe shows the recorder sees collector requests.
	if !p.ran.Load() || len(env.Proxies[0].Requests()) == 0 {
		t.Error("the probe collector made no recorded request")
	}
	if !strings.Contains(r.stderr, "alpha") || !strings.Contains(r.stderr, "files") {
		t.Errorf("stderr lacks the summary: %q", r.stderr)
	}
}

func TestBundleSkip(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	r := runBundle(t, env.Kubeconfig, []pb.Collector{writeText("alpha"), writeText("beta")}, "-o", "-", "--skip", "alpha")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q; want 0", r.code, r.stderr)
	}
	a := bundletest.Extract(t, strings.NewReader(r.stdout))
	m := readManifest(t, a)
	status := map[string]string{}
	for _, c := range m.Item.Collectors {
		status[c.Name] = c.Status + "/" + c.Reason
	}
	if status["alpha"] != "skipped/flag" || status["beta"] != "ok/" {
		t.Errorf("collector statuses = %v, want alpha skipped/flag and beta ok", status)
	}
	if _, ok := a.File("x/alpha.txt"); ok {
		t.Error("skipped collector alpha wrote a member")
	}
	if _, ok := a.File("x/beta.txt"); !ok {
		t.Error("collector beta did not write its member")
	}
}

func TestBundleListCollectors(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	p := &probe{}
	r := runBundle(t, env.Kubeconfig, []pb.Collector{writeText("alpha"), p}, "--list-collectors")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q; want 0", r.code, r.stderr)
	}
	for _, n := range []string{"alpha", "probe"} {
		if !strings.Contains(r.stdout, n+"\n") {
			t.Errorf("stdout %q lacks %q", r.stdout, n)
		}
	}
	assertNoRun(t, env, p)
}

func TestBundleUnknownSkip(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	t.Chdir(t.TempDir())
	p := &probe{}
	r := runBundle(t, env.Kubeconfig, []pb.Collector{p}, "--skip", "nope")
	if r.code != 2 || !strings.Contains(r.stderr, "nope") {
		t.Fatalf("exit %d, stderr %q; want 2 naming nope", r.code, r.stderr)
	}
	if ents, _ := os.ReadDir("."); len(ents) != 0 {
		t.Errorf("an output file was created: %v", ents)
	}
	assertNoRun(t, env, p)
}

func TestBundleMinMaxSize(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	t.Chdir(t.TempDir())
	p := &probe{}
	for _, v := range []string{"16Mi", "lots"} {
		r := runBundle(t, env.Kubeconfig, []pb.Collector{p}, "--max-size", v)
		if r.code != 2 {
			t.Errorf("--max-size %s: exit %d, stderr %q; want 2", v, r.code, r.stderr)
		}
	}
	assertNoRun(t, env, p)
}

// TestBundleUnreachable: with no reachable API server no bundle is written.
func TestBundleUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // the port now refuses connections
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["c"] = &clientcmdapi.Cluster{Server: srv.URL}
	cfg.AuthInfos["u"] = &clientcmdapi.AuthInfo{}
	cfg.Contexts["ctx"] = &clientcmdapi.Context{Cluster: "c", AuthInfo: "u"}
	cfg.CurrentContext = "ctx"
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*cfg, kubeconfig); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out.tar.gz")
	p := &probe{}
	r := runBundle(t, kubeconfig, []pb.Collector{p}, "-o", out, "--request-timeout", "5s")
	if r.code != 2 || !strings.Contains(r.stderr, "no reachable cluster") {
		t.Fatalf("exit %d, stderr %q; want 2 naming no reachable cluster", r.code, r.stderr)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("output left behind: %v", err)
	}
	if p.ran.Load() {
		t.Error("a collector ran")
	}
	if host := strings.TrimPrefix(srv.URL, "http://"); strings.Contains(r.stderr, host) {
		t.Errorf("stderr names the API host: %q", r.stderr)
	}

	// Ctrl-C during detection is reported as such, not as an unreachable cluster.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfgApp := app.Config{Build: testBuild, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Now: testNow}
	root := app.NewRootWithBundleHooks(cfgApp, app.BundleTestHooks{Now: func() time.Time { return bundleNow }})
	root.SetArgs([]string{"--kubeconfig", kubeconfig, "bundle", "-o", out})
	err := root.ExecuteContext(ctx)
	var ee *app.ExitError
	if !errors.As(err, &ee) || ee.Code != 2 || !strings.Contains(err.Error(), "interrupted") {
		t.Errorf("cancelled: err %v, want exit 2 naming the interruption", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("cancelled: output left behind: %v", err)
	}
}

// TestBundleGuardsClients: Env's clients refuse Secrets and ConfigMaps unsent.
func TestBundleGuardsClients(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	g := &guardProbe{}
	r := runBundle(t, env.Kubeconfig, []pb.Collector{g}, "-o", filepath.Join(t.TempDir(), "out.tar.gz"))
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	if len(g.errs) != 4 {
		t.Fatalf("got %d results, want 4", len(g.errs))
	}
	for i, err := range g.errs {
		if !errors.Is(err, pb.ErrForbiddenKind) {
			t.Errorf("request %d: err %v, want ErrForbiddenKind", i, err)
		}
	}
	for _, req := range env.Proxies[0].Requests() {
		if strings.Contains(req, "/secrets") || strings.Contains(req, "/configmaps") {
			t.Errorf("request sent: %s", req)
		}
	}
}

// guardProbe lists Secrets and ConfigMaps through every Env client kind.
type guardProbe struct{ errs []error }

func (*guardProbe) Name() string { return "guard-probe" }
func (g *guardProbe) Collect(ctx context.Context, env pb.Env, _ pb.Sink) error {
	_, e1 := env.Kube.CoreV1().Secrets("").List(ctx, metav1.ListOptions{})
	_, e2 := env.Kube.CoreV1().ConfigMaps("default").Get(ctx, "c", metav1.GetOptions{})
	_, e3 := env.Dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}).Namespace("default").List(ctx, metav1.ListOptions{})
	e4 := env.Discovery.RESTClient().Get().AbsPath("/api/v1/configmaps").Do(ctx).Error()
	g.errs = []error{e1, e2, e3, e4}
	return nil
}

// TestBundleInterrupt cancels the command while a collector blocks: the bundle
// is still written, the collector is timeout and no spool directory remains.
func TestBundleInterrupt(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	out := filepath.Join(t.TempDir(), "out.tar.gz")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	blocker := stubCollector{name: "blocker", fn: func(ctx context.Context, _ pb.Sink) error {
		cancel() // the run is interrupted once the collector is running
		<-ctx.Done()
		return ctx.Err()
	}}
	var errb bytes.Buffer
	cfg := app.Config{Build: testBuild, Out: &bytes.Buffer{}, Err: &errb, Now: testNow, Collectors: []pb.Collector{blocker}}
	root := app.NewRootWithBundleHooks(cfg, app.BundleTestHooks{Now: func() time.Time { return bundleNow }})
	root.SetArgs([]string{"--kubeconfig", env.Kubeconfig, "bundle", "--skip", "version,check", "-o", out})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("err %v, stderr %q; want success", err, errb.String())
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	a := bundletest.Extract(t, f)
	mf, _ := a.File("manifest.json")
	if !bytes.Contains(mf, []byte(`"timeout"`)) {
		t.Errorf("manifest does not record the collector as timeout: %s", mf)
	}
	if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
		t.Errorf("spool left behind: %v", ents)
	}
}

// grantLines returns the lines of errors.txt that name a grant to add.
func grantLines(t *testing.T, a bundletest.Archive) (all, grants []string) {
	t.Helper()
	b, ok := a.File("errors.txt")
	if !ok {
		t.Fatal("archive lacks errors.txt")
	}
	all = strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	for _, l := range all {
		if strings.HasPrefix(l, "grant: ") {
			grants = append(grants, l)
		}
	}
	return all, grants
}

// TestBundleReaderOnly: pods/log and pods/portforward are denied through the
// real log source and port-forwarder. The run exits 0, every log and admin
// item is denied, and errors.txt ends with one grant line per grant and
// namespace, though version and proxy-admin both hit pods/portforward.
// Metrics need a kubelet, so the scraper is a seam: the reader may use pods/proxy.
func TestBundleReaderOnly(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture, proxyPods(t, "http-a")}})
	logHits := env.Proxies[0].Rule(testenv.Match{Resource: "pods", Subresource: "log"}, testenv.Action{Status: http.StatusForbidden})
	pfHits := env.Proxies[0].Rule(testenv.Match{Resource: "pods", Subresource: "portforward"}, testenv.Action{Status: http.StatusForbidden})
	a := archiveWith(t, env.Kubeconfig, nil, app.BundleTestHooks{Check: fastHooks(metricsScraper{}), Scraper: metricsScraper{}})
	if logHits.N() == 0 || pfHits.N() == 0 {
		t.Fatalf("rule hits: pods/log %d, pods/portforward %d; want both", logHits.N(), pfHits.N())
	}

	seen := map[string]int{}
	for _, it := range readItems(t, a).Item.Items {
		switch op := it.Source.Operation; op {
		case "log", "previous-log", "server-info", "config-dump-eds", "stats", "certs", "clusters":
			seen[op]++
			if it.Status != "denied" {
				t.Errorf("%s: %s, want denied", it.Path, it.Status)
			}
		}
	}
	// The controller and the proxy each have one container; one proxy replica.
	if want := map[string]int{"log": 2, "server-info": 1, "config-dump-eds": 1, "stats": 1, "certs": 1, "clusters": 1}; !reflect.DeepEqual(seen, want) {
		t.Errorf("denied items per operation %v, want %v", seen, want)
	}
	for _, o := range readVersion(t, a).Observed {
		if o.Status != "denied" || o.Version != "" {
			t.Errorf("observed %+v, want denied without a version", o)
		}
	}

	all, grants := grantLines(t, a)
	want := []string{
		"grant: create pods/portforward in namespace kgateway-system: see the kgwctl-proxy-portforward Role in docs/rbac.yaml",
		"grant: get pods/log in namespace kgateway-system: see the kgwctl-bundle-logs Role in docs/rbac.yaml",
	}
	if !slices.Equal(grants, want) || !slices.Equal(all[len(all)-len(want):], want) {
		t.Errorf("errors.txt:\n%s\nwant it to end with exactly\n%s", strings.Join(all, "\n"), strings.Join(want, "\n"))
	}
	assertMembers(t, a, []string{
		gwDir + "Gateway/kgateway-system/http.yaml",
		"workloads/kgateway-system/pods.yaml",
		"install/kgateway-system/kgateway.yaml",
		"cluster/version.json",
		"check/check.json",
		proxyMetricsPath,
	}, nil)
	if ms := membersUnder(a, ""); slices.ContainsFunc(ms, func(m string) bool { return strings.Contains(m, "/logs/") }) {
		t.Errorf("a denied log has a member: %v", ms)
	}
}

// TestBundleDeniedListGrant: a 403 on HTTPRoute lists gives the item a
// denied status and a reader grant line per namespace of the -n bound.
func TestBundleDeniedListGrant(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture}})
	hits := env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: httpRoutes}, testenv.Action{Status: http.StatusForbidden})
	a := bundleArchive(t, env, "-n", "default")
	if hits.N() == 0 {
		t.Fatal("the rule never applied")
	}
	fileHas(t, a, "errors.txt", "resources/gateway.networking.k8s.io/HTTPRoute", "denied")
	_, grants := grantLines(t, a)
	want := []string{
		"grant: get, list and watch in namespace default: see the kgwctl-reader ClusterRole in docs/rbac.yaml",
		"grant: get, list and watch in namespace kgateway-system: see the kgwctl-reader ClusterRole in docs/rbac.yaml",
	}
	if !slices.Equal(grants, want) {
		t.Errorf("grant lines %q, want %q", grants, want)
	}
}

// mixedLogs serves every log stream but http-b's, which hangs until its
// request times out.
type mixedLogs struct{}

func (mixedLogs) Stream(ctx context.Context, pod types.NamespacedName, _ *corev1.PodLogOptions) (io.ReadCloser, error) {
	if pod.Name == "http-b" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return io.NopCloser(strings.NewReader("2026-01-02T03:04:05Z ready\n")), nil
}

// TestBundleMixedOutcomes: one replica's log is ok and another's times out
// (log source seam); its admin capture is denied through the real
// port-forwarder. The run exits 0 with each status on its item.
func TestBundleMixedOutcomes(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture, proxyPods(t, "http-a", "http-b")}})
	pfHits := env.Proxies[0].Rule(testenv.Match{Resource: "pods", Subresource: "portforward"}, testenv.Action{Status: http.StatusForbidden})
	a := archiveWith(t, env.Kubeconfig, nil, app.BundleTestHooks{Logs: mixedLogs{}},
		"--skip", "check,controller-metrics,proxy-metrics", "--request-timeout", "1s")
	if pfHits.N() == 0 {
		t.Fatal("no port-forward request reached the API server")
	}
	const dir = "proxies/kgateway-system/"
	want := map[string]string{
		dir + "http-a/logs/kgateway-proxy.log": "ok",
		dir + "http-b/logs/kgateway-proxy.log": "timeout",
		dir + "http-a/stats.txt":               "denied",
	}
	got := map[string]string{}
	for _, it := range readItems(t, a).Item.Items {
		if _, ok := want[it.Path]; ok {
			got[it.Path] = it.Status
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("item statuses %v, want %v", got, want)
	}
	fileHas(t, a, dir+"http-a/logs/kgateway-proxy.log", "ready")
}

// requestKeys returns the distinct "METHOD path" of reqs; a watch keeps its
// marker, and every other query is dropped.
func requestKeys(reqs []string) map[string]bool {
	out := map[string]bool{}
	for _, r := range reqs {
		k, q, _ := strings.Cut(r, "?")
		if strings.Contains("&"+q, "&watch=true") || strings.Contains("&"+q, "&watch=1") {
			k += " watch"
		}
		out[k] = true
	}
	return out
}

// nsOf returns the namespace a request path names, or "" for a cluster-wide one.
func nsOf(key string) string {
	_, rest, ok := strings.Cut(key, "/namespaces/")
	if !ok {
		return ""
	}
	ns, _, _ := strings.Cut(rest, "/")
	if strings.HasSuffix(ns, " watch") || !strings.Contains(rest, "/") {
		return "" // the Namespace resource itself
	}
	return ns
}

// TestNamespacedRequestSet: with -n team-a and every collector on its real
// adapter, the requests match design §3.2. Cluster-wide lists and watches are
// those of install detection and `check -n team-a`, plus discovery and the
// cluster-scoped GatewayClass list; collectors read only team-a and the
// install namespace, and resources list each kind in both.
func TestNamespacedRequestSet(t *testing.T) {
	extra := filepath.Join(t.TempDir(), "pods.yaml")
	ctrl := "apiVersion: v1\nkind: Pod\nmetadata:\n  name: kgateway-0\n  namespace: kgateway-system\n" +
		"  labels: {kgateway: kgateway, app.kubernetes.io/name: kgateway, app.kubernetes.io/component: controller}\n" +
		"spec: {containers: [{name: kgateway, image: cr.kgateway.dev/kgateway-dev/kgateway:v2.5.0}]}\nstatus: {phase: Running}\n"
	proxy := strings.NewReplacer("namespace: kgateway-system", "namespace: team-a", "gateway-full-name: http", "gateway-full-name: gw",
		"gateway-name: http", "gateway-name: gw").Replace(proxyPodYAML("gw-0"))
	if err := os.WriteFile(extra, []byte(ctrl+"---\n"+proxy), 0o600); err != nil {
		t.Fatal(err)
	}
	env := newEnv(t, testenv.Options{Fixtures: []string{ownershipFixture(t), extra}})
	hooks := app.BundleTestHooks{Check: fastHooks(metricsScraper{})}
	p := env.Proxies[0]

	// Install detection and the inventory alone, then check alone, then the full run.
	all := "resources,workloads,install,version,check,logs,controller-metrics,proxy-metrics,proxy-admin"
	archiveWith(t, env.Kubeconfig, nil, hooks, "-n", "team-a", "--skip", all)
	boot := requestKeys(p.Requests())
	n := len(p.Requests())
	if r := runCheck(t, env, nil, hooks.Check, "check", "-n", "team-a"); r.stdout == "" {
		t.Fatalf("check: exit %d, stderr %q", r.code, r.stderr)
	}
	chk := requestKeys(p.Requests()[n:])
	n = len(p.Requests())
	archiveWith(t, env.Kubeconfig, nil, hooks, "-n", "team-a")
	full := requestKeys(p.Requests()[n:])

	want := maps.Clone(boot)
	maps.Copy(want, chk)
	const sys, api = "/api/v1/namespaces/kgateway-system/", "/api/v1/namespaces/team-a/"
	for _, k := range []string{
		"GET /api", "GET /apis", "GET /version", // discovery
		"GET /apis/gateway.networking.k8s.io/v1/gatewayclasses",             // resources and install: cluster-scoped
		"GET /apis/apps/v1/namespaces/kgateway-system/deployments/kgateway", // install
		"GET " + sys + "events", "GET " + sys + "pods", "GET " + sys + "services", "GET /apis/apps/v1/namespaces/kgateway-system/deployments",
		"GET " + api + "events", "GET " + api + "pods", "GET " + api + "services", "GET /apis/apps/v1/namespaces/team-a/deployments",
		"GET " + sys + "pods/kgateway-0/log", "GET " + api + "pods/gw-0/log", // logs
		"GET " + sys + "pods/http:kgateway-0:9092/proxy/metrics", "GET " + api + "pods/http:gw-0:9091/proxy/metrics",
		"POST " + api + "pods/gw-0/portforward",                      // version and proxy-admin
		"GET " + sys + "pods/kgateway-0", "GET " + api + "pods/gw-0", // the Sink's re-reads
	} {
		want[k] = true
	}
	// resources lists each namespaced kind in both namespaces, and nowhere else.
	kinds := map[string][]string{}
	for k := range full {
		// Only plain lists: a collector never watches.
		if ns := nsOf(k); strings.HasPrefix(k, "GET /apis/gateway.") && ns != "" && !strings.HasSuffix(k, " watch") {
			kind := strings.Replace(k, "/namespaces/"+ns+"/", "/", 1)
			kinds[kind] = append(kinds[kind], ns)
			want[k] = true
		}
	}
	for kind, nss := range kinds {
		if slices.Sort(nss); !slices.Equal(nss, []string{"kgateway-system", "team-a"}) {
			t.Errorf("%s listed in %v, want kgateway-system and team-a", kind, nss)
		}
	}
	if !slices.ContainsFunc(slices.Collect(maps.Keys(kinds)), func(k string) bool { return strings.HasSuffix(k, "/httproutes") }) {
		t.Errorf("resources listed no HTTPRoutes: %v", kinds)
	}

	got, wantKeys := slices.Sorted(maps.Keys(full)), slices.Sorted(maps.Keys(want))
	if !slices.Equal(got, wantKeys) {
		for _, k := range got {
			if !want[k] {
				t.Errorf("unexpected request %s", k)
			}
		}
		for _, k := range wantKeys {
			if !full[k] {
				t.Errorf("missing request %s", k)
			}
		}
	}
	for k := range full {
		if ns := nsOf(k); ns != "" && ns != "team-a" && ns != "kgateway-system" {
			t.Errorf("request outside -n team-a and the install namespace: %s", k)
		}
	}
}

// TestBundleNoForbiddenRequests: a full run, every collector on its real
// adapter, sends no secrets or configmaps request, and no collector tries one
// that the guard refused.
func TestBundleNoForbiddenRequests(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture, bundleCanaries, proxyPods(t, "http-a")}})
	a := archiveWith(t, env.Kubeconfig, nil, app.BundleTestHooks{Check: fastHooks(metricsScraper{})})
	for _, f := range []string{"errors.txt", "manifest.json"} {
		if b, _ := a.File(f); strings.Contains(string(b), "forbidden kind") {
			t.Errorf("%s records a refused Secret or ConfigMap request:\n%s", f, b)
		}
	}
	reqs := env.Proxies[0].Requests()
	// Each real adapter reached the API server, so the scan below covers it.
	for _, sub := range []string{"/log?", "/portforward", "/proxy/metrics"} {
		if !slices.ContainsFunc(reqs, func(r string) bool { return strings.Contains(r, sub) }) {
			t.Errorf("no %s request in a full run", sub)
		}
	}
	for _, r := range reqs {
		if strings.Contains(r, "/secrets") || strings.Contains(r, "/configmaps") {
			t.Errorf("request sent: %s", r)
		}
	}
}
