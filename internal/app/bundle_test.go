package app_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// writeText is a stub collector that writes one small text item.
func writeText(name string) stubCollector {
	return stubCollector{name: name, fn: func(_ context.Context, s pb.Sink) error {
		return s.Text(pb.Path{"x", "a.txt"}, strings.NewReader("hello\n"), pb.Source{})
	}}
}

var bundleNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// runBundle runs `bundle args` against kubeconfig with extra as downstream collectors.
func runBundle(t *testing.T, kubeconfig string, extra []pb.Collector, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	cfg := app.Config{Build: testBuild, Out: &out, Err: &errb, Now: testNow, Collectors: extra}
	root := app.NewRootWithBundleHooks(cfg, app.BundleTestHooks{Now: func() time.Time { return bundleNow }})
	code := run(root, append([]string{"--kubeconfig", kubeconfig, "bundle"}, args...)...)
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
	r := runBundle(t, env.Kubeconfig, nil, "-o", path)
	if r.code != 2 {
		t.Fatalf("exit %d, stderr %q; want 2", r.code, r.stderr)
	}
	if b, _ := os.ReadFile(path); string(b) != "keep" {
		t.Errorf("existing file changed to %q", b)
	}
	noRequests(t, env)
}

func TestBundleUnwritableDir(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	r := runBundle(t, env.Kubeconfig, nil, "-o", filepath.Join(t.TempDir(), "missing", "out.tar.gz"))
	if r.code != 2 {
		t.Fatalf("exit %d, stderr %q; want 2", r.code, r.stderr)
	}
	noRequests(t, env)
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
	mf, ok := a.File("manifest.json")
	if !ok {
		t.Fatal("no manifest.json")
	}
	// The JSON encoder escapes the angle brackets, so match the word.
	if !bytes.Contains(mf, []byte("apiserver")) || bytes.Contains(mf, []byte(env.Proxies[0].URL())) {
		t.Errorf("manifest server is not the placeholder alone: %s", mf)
	}
}

func TestBundleStdout(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	r := runBundle(t, env.Kubeconfig, []pb.Collector{writeText("alpha")}, "-o", "-")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q; want 0", r.code, r.stderr)
	}
	a := bundletest.Extract(t, strings.NewReader(r.stdout))
	if _, ok := a.File("x/a.txt"); !ok {
		t.Errorf("archive lacks x/a.txt: %v", a.Members)
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
	mf, _ := a.File("manifest.json")
	if !bytes.Contains(mf, []byte(`"reason": "flag"`)) && !bytes.Contains(mf, []byte(`"reason":"flag"`)) {
		t.Errorf("manifest does not record skipped: flag: %s", mf)
	}
}

func TestBundleListCollectors(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	r := runBundle(t, env.Kubeconfig, []pb.Collector{writeText("alpha"), writeText("beta")}, "--list-collectors")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q; want 0", r.code, r.stderr)
	}
	for _, n := range []string{"alpha", "beta"} {
		if !strings.Contains(r.stdout, n+"\n") {
			t.Errorf("stdout %q lacks %q", r.stdout, n)
		}
	}
	noRequests(t, env)
}

func TestBundleUnknownSkip(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	t.Chdir(t.TempDir())
	r := runBundle(t, env.Kubeconfig, []pb.Collector{writeText("alpha")}, "--skip", "nope")
	if r.code != 2 || !strings.Contains(r.stderr, "nope") {
		t.Fatalf("exit %d, stderr %q; want 2 naming nope", r.code, r.stderr)
	}
	if ents, _ := os.ReadDir("."); len(ents) != 0 {
		t.Errorf("an output file was created: %v", ents)
	}
	noRequests(t, env)
}

func TestBundleMinMaxSize(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	t.Chdir(t.TempDir())
	for _, v := range []string{"16Mi", "lots"} {
		r := runBundle(t, env.Kubeconfig, nil, "--max-size", v)
		if r.code != 2 {
			t.Errorf("--max-size %s: exit %d, stderr %q; want 2", v, r.code, r.stderr)
		}
	}
	noRequests(t, env)
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
	root.SetArgs([]string{"--kubeconfig", env.Kubeconfig, "bundle", "-o", out})
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
