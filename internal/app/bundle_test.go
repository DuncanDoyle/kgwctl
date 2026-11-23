package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
