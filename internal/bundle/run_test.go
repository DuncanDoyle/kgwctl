package bundle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/DuncanDoyle/kgwctl/internal/bundle/bundletest"
	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

type fakeCollector struct {
	name    string
	collect func(ctx context.Context, s pb.Sink) error
	called  atomic.Bool
	env     *func() time.Time // receives Env.Now when set
}

func (f *fakeCollector) Name() string { return f.name }
func (f *fakeCollector) Collect(ctx context.Context, env pb.Env, s pb.Sink) error {
	f.called.Store(true)
	if f.env != nil {
		*f.env = env.Now
	}
	return f.collect(ctx, s)
}

func fake(name string, fn func(ctx context.Context, s pb.Sink) error) *fakeCollector {
	return &fakeCollector{name: name, collect: fn}
}

func text(p string, body string) func(context.Context, pb.Sink) error {
	return func(_ context.Context, s pb.Sink) error {
		return s.Text(bg, pb.Path{"x", p}, strings.NewReader(body), pb.Source{})
	}
}

func runEnv() pb.Env { return pb.Env{Now: fixedNow} }

func statusOf(t *testing.T, m Manifest, name string) CollectorEntry {
	t.Helper()
	for _, c := range m.Collectors {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("collector %q not in manifest", name)
	return CollectorEntry{}
}

type failDest struct{ Dest }

func (failDest) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestRunDeadline(t *testing.T) {
	h := newHarness(t, mustLimits(t, MinSize), redact.Options{}, "")
	release := make(chan struct{})
	hung := fake("hung", func(context.Context, pb.Sink) error { <-release; return nil })
	// A deadline in the past ends collection at once.
	m, err := Run(context.Background(), []pb.Collector{hung}, runEnv(), h.w, Manifest{}, RunOptions{Deadline: time.Now().Add(-time.Second)})
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, m, "hung").Status; got != pb.CollectorTimeout {
		t.Errorf("hung = %s", got)
	}
	a := bundletest.Extract(t, bytes.NewReader(h.buf.Bytes()))
	if _, ok := a.File("manifest.json"); !ok {
		t.Error("manifest.json missing")
	}
}

func TestRunDeadlineKeepsFinished(t *testing.T) {
	h := newHarness(t, mustLimits(t, MinSize), redact.Options{}, "")
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	// One worker takes collectors in order, so hung starts after fast is recorded.
	fast := fake("fast", text("a.log", "hello\n"))
	hung := fake("hung", func(context.Context, pb.Sink) error {
		cancel()
		<-release
		return nil
	})
	m, err := Run(ctx, []pb.Collector{fast, hung}, runEnv(), h.w, Manifest{}, RunOptions{Workers: 1})
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, m, "hung").Status; got != pb.CollectorTimeout {
		t.Errorf("hung = %s", got)
	}
	if got := statusOf(t, m, "fast").Status; got != pb.CollectorOK {
		t.Errorf("fast = %s", got)
	}
}

func TestRunReturnAfterSealIsTimeout(t *testing.T) {
	h := newHarness(t, mustLimits(t, MinSize), redact.Options{}, "")
	ctx, cancel := context.WithCancel(context.Background())
	c := fake("swallows", func(ctx context.Context, s pb.Sink) error {
		cancel()
		<-ctx.Done()
		// Wait for the seal, then ignore the error and report success.
		for s.Exclude(schema.GroupKind{Kind: "ConfigMap"}, pb.ExcludeScope) == nil {
			runtime.Gosched()
		}
		return nil
	})
	m, err := Run(ctx, []pb.Collector{c}, runEnv(), h.w, Manifest{}, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, m, "swallows").Status; got != pb.CollectorTimeout {
		t.Errorf("status = %s, want timeout", got)
	}
}

func TestRunFillsEnvNow(t *testing.T) {
	h := newHarness(t, mustLimits(t, MinSize), redact.Options{}, "")
	c := fake("clock", func(ctx context.Context, s pb.Sink) error {
		return text("a.log", "t\n")(ctx, s)
	})
	var got func() time.Time
	c.env = &got
	m, err := Run(context.Background(), []pb.Collector{c}, pb.Env{}, h.w, Manifest{}, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("Env.Now not set for the collector")
	}
	got()
	if s := statusOf(t, m, "clock").Status; s != pb.CollectorOK {
		t.Errorf("status = %s", s)
	}
}

func TestRunCtxCancelActsAsDeadline(t *testing.T) {
	h := newHarness(t, mustLimits(t, MinSize), redact.Options{}, "")
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	waits := fake("waits", func(ctx context.Context, _ pb.Sink) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	go func() { <-started; cancel() }()
	m, err := Run(ctx, []pb.Collector{waits}, runEnv(), h.w, Manifest{}, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, m, "waits").Status; got != pb.CollectorTimeout {
		t.Errorf("waits = %s", got)
	}
}

func TestRunPanic(t *testing.T) {
	const tok = "Bearer kgwctl-canary-panic-token"
	h := newHarness(t, mustLimits(t, MinSize), redact.Options{}, "")
	c := fake("boom", func(context.Context, pb.Sink) error { panic("auth failed: " + tok) })
	m, err := Run(context.Background(), []pb.Collector{c}, runEnv(), h.w, Manifest{}, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	e := statusOf(t, m, "boom")
	if e.Status != pb.CollectorFailed {
		t.Errorf("status = %s", e.Status)
	}
	if !strings.HasPrefix(e.Error, "panic") {
		t.Errorf("Error = %q, want a panic message", e.Error)
	}
	bundletest.AssertNoCanary(t, bundletest.Extract(t, bytes.NewReader(h.buf.Bytes())), []string{"kgwctl-canary-panic-token"})
}

func TestRunSkip(t *testing.T) {
	h := newHarness(t, mustLimits(t, MinSize), redact.Options{}, "")
	logs := fake("logs", text("a.log", "x\n"))
	other := fake("other", text("b.log", "y\n"))
	bad := fake("bad", func(context.Context, pb.Sink) error { return errors.New("broke") })
	m, err := Run(context.Background(), []pb.Collector{logs, other, bad}, runEnv(), h.w, Manifest{}, RunOptions{Skip: map[string]bool{"logs": true}})
	if err != nil {
		t.Fatal(err)
	}
	e := statusOf(t, m, "logs")
	if e.Status != pb.CollectorSkipped || e.Reason != "flag" {
		t.Errorf("logs = %+v", e)
	}
	if logs.called.Load() {
		t.Error("Collect called for skipped collector")
	}
	a := bundletest.Extract(t, bytes.NewReader(h.buf.Bytes()))
	b, ok := a.File("errors.txt")
	if !ok || !strings.Contains(string(b), "bad: failed") {
		t.Fatalf("errors.txt = %q, ok %v", b, ok)
	}
	if strings.Contains(string(b), "logs") {
		t.Errorf("errors.txt names the skipped collector: %q", b)
	}
	if rl, _ := a.File("run.log"); !strings.Contains(string(rl), "collector logs skipped: flag") {
		t.Errorf("run.log = %q", rl)
	}
}

func TestRunCommitOrder(t *testing.T) {
	h := newHarness(t, mustLimits(t, MinSize), redact.Options{}, "")
	gates := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	var cs []pb.Collector
	for i, n := range []string{"zeta", "mid", "alpha"} {
		// Each collector waits for the one after it, so they finish in reverse order.
		cs = append(cs, fake(n, func(ctx context.Context, s pb.Sink) error {
			if i < len(gates)-1 {
				<-gates[i+1]
			}
			defer close(gates[i])
			return s.Text(bg, pb.Path{n, "f.log"}, strings.NewReader(n+"\n"), pb.Source{})
		}))
	}
	m, err := Run(context.Background(), cs, runEnv(), h.w, Manifest{}, RunOptions{Workers: 3})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range m.Collectors {
		got = append(got, c.Name)
	}
	if strings.Join(got, ",") != "zeta,mid,alpha" {
		t.Errorf("manifest order = %v", got)
	}
	var paths []string
	for _, it := range m.Items {
		paths = append(paths, it.Path)
	}
	if len(paths) != 3 || !strings.Contains(paths[0], "f.log") {
		t.Fatalf("items = %v", paths)
	}
	a := bundletest.Extract(t, bytes.NewReader(h.buf.Bytes()))
	var order []string
	for _, mem := range a.Members {
		if strings.HasSuffix(mem.Name, "f.log") {
			order = append(order, strings.TrimSpace(string(mem.Data)))
		}
	}
	if strings.Join(order, ",") != "zeta,mid,alpha" {
		t.Errorf("member order = %v", order)
	}
}

func TestRunCommitError(t *testing.T) {
	l := mustLimits(t, MinSize)
	sp, err := NewSpool(t.TempDir(), l.RAM, l.MaxSize)
	if err != nil {
		t.Fatal(err)
	}
	w := NewWriter(failDest{Stdout(io.Discard)}, sp, DefaultName(t0), l, redact.Options{}, "", fixedNow)
	_, err = Run(context.Background(), []pb.Collector{fake("a", text("a.log", "x\n"))}, runEnv(), w, Manifest{}, RunOptions{})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("err = %v", err)
	}
}

func TestRunPartial(t *testing.T) {
	h := newHarness(t, mustLimits(t, MinSize), redact.Options{}, "")
	c := fake("p", func(_ context.Context, s pb.Sink) error {
		s.Error(pb.Path{"x", "a.json"}, pb.Source{}, errors.New("nope"))
		return nil
	})
	m, err := Run(context.Background(), []pb.Collector{c, fake("ok", text("a.log", "x\n"))}, runEnv(), h.w, Manifest{}, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, m, "p").Status; got != pb.CollectorPartial {
		t.Errorf("p = %s", got)
	}
	if got := statusOf(t, m, "ok").Status; got != pb.CollectorOK {
		t.Errorf("ok = %s", got)
	}
}

func TestRunErrorCanaries(t *testing.T) {
	canaries := []string{"kgwctl-canary-ret-token", "kgwctl-canary-panic-token", "kgwctl-canary-body-token", "kgwctl-canary-long-token"}
	h := newHarness(t, mustLimits(t, MinSize), redact.Options{}, "")
	cs := []pb.Collector{
		fake("returned", func(context.Context, pb.Sink) error {
			return errors.New("Authorization: Bearer " + canaries[0])
		}),
		fake("panics", func(context.Context, pb.Sink) error { panic(fmt.Errorf("token=%s", canaries[1])) }),
		fake("malformed", func(_ context.Context, s pb.Sink) error {
			body := `{"password": "` + canaries[2] + `", "broken": `
			return s.JSON(bg, pb.Path{"x", "a.json"}, strings.NewReader(body), pb.Source{})
		}),
		fake("long", func(context.Context, pb.Sink) error {
			return errors.New("Authorization: Bearer " + canaries[3] + " " + strings.Repeat("e", 2048))
		}),
	}
	if _, err := Run(context.Background(), cs, runEnv(), h.w, Manifest{}, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	bundletest.AssertNoCanary(t, bundletest.Extract(t, bytes.NewReader(h.buf.Bytes())), canaries)
}

func TestRecordFlags(t *testing.T) {
	fs := pflag.NewFlagSet("bundle", pflag.ContinueOnError)
	fs.StringP("namespace", "n", "", "")
	fs.String("install", "", "")
	fs.StringSlice("skip", nil, "")
	fs.StringP("output", "o", "", "")
	fs.String("kubeconfig", "", "")
	fs.String("context", "", "")
	fs.StringSlice("redact-header", nil, "")
	fs.Duration("since", 0, "")
	fs.Int("tail", 0, "")
	fs.BoolP("all-namespaces", "A", false, "")
	fs.Bool("unset", false, "")
	args := []string{"-n", "secret-ns", "--install", "i", "--skip", "logs", "-o", "/tmp/x", "--kubeconfig", "/k", "--context", "c",
		"--redact-header", "X-Secret", "--since", "1h", "--tail", "10", "-A"}
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	got := map[string]*string{}
	for _, f := range RecordFlags(fs) {
		got[f.Name] = f.Value
	}
	for _, n := range []string{"namespace", "install", "skip", "output", "kubeconfig", "context", "redact-header"} {
		if v, ok := got[n]; !ok || v != nil {
			t.Errorf("%s = %v, present %v; want by name only", n, v, ok)
		}
	}
	for n, want := range map[string]string{"since": "1h0m0s", "tail": "10", "all-namespaces": "true"} {
		if v := got[n]; v == nil || *v != want {
			t.Errorf("%s = %v, want %s", n, v, want)
		}
	}
	if _, ok := got["unset"]; ok {
		t.Error("unset flag recorded")
	}
}
