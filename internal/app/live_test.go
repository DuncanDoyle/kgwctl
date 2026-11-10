package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/collect"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/yaml"

	"github.com/DuncanDoyle/kgwctl/internal/app"
	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/observe"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
)

const checkRoutesUnstatused = "testdata/check/routes-unstatused.yaml"

// syncBuffer is a bytes.Buffer safe for one writer and a concurrent reader.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// liveRig runs check --live on a fake clock that the test steps.
type liveRig struct {
	t        *testing.T
	fc       *clocktesting.FakeClock
	tracked  chan *engine.Published
	done     chan result
	res      *result
	progress *syncBuffer
}

// liveOpts change a rig: a context to cancel, and whether progress goes to a forced terminal writer.
type liveOpts struct {
	ctx        context.Context
	noProgress bool
	reg        *extension.Registry
	// out replaces the command's stdout; the result's stdout is then empty.
	out io.Writer
}

// startLive runs "check --live args" and returns once the command tracked its
// first model. Until then it steps the clock in small steps, as acquisition waits on its timers.
func startLive(t *testing.T, env *testenv.Env, o liveOpts, h app.CheckTestHooks, args ...string) *liveRig {
	t.Helper()
	r := &liveRig{t: t, fc: clocktesting.NewFakeClock(time.Now()), tracked: make(chan *engine.Published, 100000),
		done: make(chan result, 1), progress: &syncBuffer{}}
	h.Clock, h.Quiet, h.FirstPublishWait = r.fc, 5*time.Millisecond, time.Second
	if h.MetricsInterval == 0 {
		// At most a step, so every step brings a recompute at the new time.
		h.MetricsInterval = 5 * time.Second
	}
	inner := h.Tracked
	h.Tracked = func(p *engine.Published) {
		if inner != nil {
			inner(p)
		}
		r.tracked <- p
	}
	if !o.noProgress {
		h.Progress = r.progress
	}
	reg := o.reg
	if reg == nil {
		reg = statsRegistry(fakeStats{})
	}
	go func() {
		var out, errb bytes.Buffer
		var outW io.Writer = &out
		if o.out != nil {
			outW = o.out
		}
		root := app.NewRootWithCheckHooks(app.Config{Registry: reg, Build: testBuild, Out: outW, Err: &errb, Now: testNow}, h)
		root.SetArgs(append([]string{"--kubeconfig", env.Kubeconfig, "check", "--live"}, args...))
		ctx := o.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		code := 0
		var ee *app.ExitError
		switch err := root.ExecuteContext(ctx); {
		case errors.As(err, &ee):
			code = ee.Code
		case err != nil:
			code = 1
			errb.WriteString("Error: " + err.Error() + "\n")
		}
		r.done <- result{code: code, stdout: out.String(), stderr: errb.String()}
	}()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			r.fc.Step(5 * time.Millisecond)
			time.Sleep(time.Millisecond)
		}
	})
	defer func() { close(stop); wg.Wait() }()
	select {
	case <-r.tracked:
	case res := <-r.done:
		r.res = &res
	case <-time.After(30 * time.Second):
		t.Fatal("no model tracked within 30s")
	}
	return r
}

// step advances the clock by d and waits until the command tracked a model
// computed at the new time. It returns false once the command has returned.
func (r *liveRig) step(d time.Duration) bool {
	r.t.Helper()
	if r.res != nil {
		return false
	}
	r.fc.Step(d)
	at := r.fc.Now()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case p := <-r.tracked:
			if !p.Model.ComputedAt.Before(at) {
				return true
			}
		case res := <-r.done:
			r.res = &res
			return false
		case <-deadline:
			r.t.Fatalf("no model computed at %v tracked within 10s", at)
		}
	}
}

// stepUntilClosed steps the clock 5s at a time until ch is closed.
func (r *liveRig) stepUntilClosed(ch <-chan struct{}) {
	r.t.Helper()
	for range 200 {
		r.fc.Step(5 * time.Second)
		at := r.fc.Now()
		deadline := time.After(10 * time.Second)
		for paced := false; !paced; {
			select {
			case <-ch:
				return
			case p := <-r.tracked:
				paced = !p.Model.ComputedAt.Before(at)
			case <-deadline:
				r.t.Fatalf("neither a model at %v nor the close within 10s", at)
			}
		}
	}
	r.t.Fatal("not closed after 200 steps")
}

// finish steps the clock 5s at a time, the metrics interval, until the command returns.
func (r *liveRig) finish() result {
	r.t.Helper()
	for range 200 {
		if !r.step(5 * time.Second) {
			return *r.res
		}
	}
	r.t.Fatal("check --live still running after 200 steps")
	return result{}
}

// wait returns the command's result once it returns on its own.
func (r *liveRig) wait() result {
	r.t.Helper()
	if r.res != nil {
		return *r.res
	}
	select {
	case res := <-r.done:
		r.res = &res
		return res
	case <-time.After(10 * time.Second):
		r.t.Fatal("check --live did not return within 10s")
		return result{}
	}
}

// liveDoc is the part of a --live JSON report the tests read.
type liveDoc struct {
	Item struct {
		Installs []struct {
			Findings []struct{ ID, Code, Severity string }
		}
		Observation *struct {
			StartedAt, EndedAt time.Time
			StopReason         string
			Observed           []struct {
				ID, Install, Code, Severity string
				FirstSeen                   time.Time
				ClearedAt                   *time.Time
				Severities                  []struct{ Severity string }
			}
		}
	}
}

func decodeLive(t *testing.T, stdout string) liveDoc {
	t.Helper()
	var doc liveDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout)
	}
	if doc.Item.Observation == nil {
		t.Fatalf("no observation in the report:\n%s", stdout)
	}
	return doc
}

// has reports whether the report's findings include code at severity sev ("" for any).
func (d liveDoc) has(code, sev string) bool {
	for _, in := range d.Item.Installs {
		for _, f := range in.Findings {
			if f.Code == code && (sev == "" || f.Severity == sev) {
				return true
			}
		}
	}
	return false
}

func TestLiveTimeout(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController, checkRouteBroken)
	r := startLive(t, env, liveOpts{}, fastHooks(metricsScraper{}), "--timeout", "30s", "-o", "json")
	start := r.fc.Now()
	for i := range 2 {
		if !r.step(5 * time.Second) {
			t.Fatalf("returned after step %d, before the clock passed 30s", i+1)
		}
	}
	res := r.finish()
	if got := r.fc.Now().Sub(start); got < 30*time.Second {
		t.Errorf("returned %v into the run, want at least 30s", got)
	}
	if res.code != 1 {
		t.Errorf("exit %d, want the findings' 1; stderr: %s", res.code, res.stderr)
	}
	doc := decodeLive(t, res.stdout)
	o := doc.Item.Observation
	if o.StopReason != "timeout" || o.EndedAt.Sub(o.StartedAt) < 30*time.Second {
		t.Errorf("observation %s from %v to %v, want timeout after at least 30s", o.StopReason, o.StartedAt, o.EndedAt)
	}
	if !doc.has("KGWUI-013", "") {
		t.Errorf("013 missing from the final report:\n%s", res.stdout)
	}
}

// TestLiveHoldDecides: 003's grace ends and 018's 5m hold matures while kgwctl watches.
func TestLiveHoldDecides(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController, checkRoutesUnstatused)
	r := startLive(t, env, liveOpts{}, fastHooks(metricsScraper{}), "--timeout", "7m", "-o", "json")
	res := r.finish()
	if res.code != 1 {
		t.Errorf("exit %d, want 1; stderr: %s", res.code, res.stderr)
	}
	doc := decodeLive(t, res.stdout)
	if !doc.has("KGWUI-003", "warning") || !doc.has("KGWUI-018", "warning") {
		t.Errorf("final report lacks a 003 or 018 warning:\n%s", res.stdout)
	}
	var changed003, raised018 bool
	for _, f := range doc.Item.Observation.Observed {
		sevs := make([]string, len(f.Severities))
		for i, s := range f.Severities {
			sevs[i] = s.Severity
		}
		switch {
		case f.Code == "KGWUI-003" && strings.Join(sevs, ",") == "info,warning":
			changed003 = true
		case f.Code == "KGWUI-018" && f.ClearedAt == nil && f.FirstSeen.After(doc.Item.Observation.StartedAt):
			raised018 = true
		}
	}
	if !changed003 || !raised018 {
		t.Errorf("observed: 003 info->warning %v, 018 raised %v, want both:\n%s", changed003, raised018, res.stdout)
	}
	p := r.progress.String()
	waiting := strings.Index(p, "WAITING  ")
	changed := strings.Index(p, "CHANGED  KGWUI-003")
	raised := strings.Index(p, "WARNING  KGWUI-018")
	if waiting < 0 || changed < waiting || raised < changed {
		t.Errorf("progress lacks WAITING, then CHANGED 003, then WARNING 018 (at %d, %d, %d):\n%s", waiting, changed, raised, p)
	}
	t.Logf("progress:\n%s", p)
}

var httpRoutesGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}

// fixCart does what fixing route-broken does: create Service shop/cart-v2, and
// the controller then resolving the route's refs (envtest runs no controller).
func fixCart(t *testing.T, env *testenv.Env) {
	t.Helper()
	ctx := context.Background()
	kc := kubernetes.NewForConfigOrDie(env.Admin)
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "cart-v2", Namespace: "shop"},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}}}
	if _, err := kc.CoreV1().Services("shop").Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = kc.CoreV1().Services("shop").Delete(context.Background(), "cart-v2", metav1.DeleteOptions{})
	})
	routes := dynamic.NewForConfigOrDie(env.Admin).Resource(httpRoutesGVR).Namespace("shop")
	u, err := routes.Get(ctx, "cart", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	parents, _, _ := unstructured.NestedSlice(u.Object, "status", "parents")
	p := parents[0].(map[string]any)
	conds := p["conditions"].([]any)
	for _, c := range conds {
		m := c.(map[string]any)
		if m["type"] == "ResolvedRefs" {
			m["status"], m["reason"], m["message"] = "True", "ResolvedRefs", ""
		}
	}
	if err := unstructured.SetNestedSlice(u.Object, parents, "status", "parents"); err != nil {
		t.Fatal(err)
	}
	if _, err := routes.UpdateStatus(ctx, u, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestLiveClears(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController, checkRouteBroken)
	r := startLive(t, env, liveOpts{}, fastHooks(metricsScraper{}), "--timeout", "1m", "-o", "json")
	r.step(5 * time.Second)
	fixCart(t, env)
	res := r.finish()
	doc := decodeLive(t, res.stdout)
	if doc.has("KGWUI-013", "") {
		t.Errorf("013 still in the final report after the fix:\n%s", res.stdout)
	}
	cleared := false
	for _, f := range doc.Item.Observation.Observed {
		if f.Code == "KGWUI-013" && f.ClearedAt != nil && f.Install == "kgateway-system.kgateway" {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("observed does not show 013 cleared:\n%s", res.stdout)
	}
	if !strings.Contains(r.progress.String(), "CLEARED  KGWUI-013  HTTPRoute shop/cart") {
		t.Errorf("progress has no CLEARED line for 013:\n%s", r.progress.String())
	}
}

func TestLiveInterrupt(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController, checkRouteBroken)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := startLive(t, env, liveOpts{ctx: ctx}, fastHooks(metricsScraper{}), "-o", "json")
	r.step(5 * time.Second)
	cancel()
	res := r.wait()
	if res.code != 2 {
		t.Errorf("exit %d, want 2; stderr: %s", res.code, res.stderr)
	}
	doc := decodeLive(t, res.stdout)
	if doc.Item.Observation.StopReason != "interrupted" || !doc.has("KGWUI-013", "") {
		t.Errorf("want an interrupted report with 013:\n%s", res.stdout)
	}
	waitNoWatches(t, env.Proxies[0])
}

// TestLiveInterruptNoInstall: interrupted before any model had an install, nothing is reported.
func TestLiveInterruptNoInstall(t *testing.T) {
	env := checkEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := startLive(t, env, liveOpts{ctx: ctx}, fastHooks(metricsScraper{}))
	r.step(5 * time.Second)
	cancel()
	if res := r.wait(); res.code != 2 || res.stdout != "" {
		t.Errorf("exit %d stdout %q, want 2 and empty", res.code, res.stdout)
	}
}

func TestLiveJSONOneDocument(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController, checkRouteBroken)
	for _, format := range []string{"json", "yaml"} {
		r := startLive(t, env, liveOpts{}, fastHooks(metricsScraper{}), "--timeout", "40s", "-o", format)
		res := r.finish()
		out := []byte(res.stdout)
		if format == "yaml" {
			if strings.Contains(res.stdout, "\n---") {
				t.Errorf("yaml: a document separator:\n%s", res.stdout)
			}
			var err error
			if out, err = yaml.YAMLToJSON(out); err != nil {
				t.Fatalf("yaml: %v", err)
			}
		}
		oneEnvelope(t, out, 1)
		decodeLive(t, string(out))
	}
}

func TestLiveNoANSIOffTerminal(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController, checkRouteBroken)
	r := startLive(t, env, liveOpts{noProgress: true}, fastHooks(metricsScraper{}), "--timeout", "40s")
	res := r.finish()
	if res.code != 1 {
		t.Errorf("exit %d, want 1; stderr: %s", res.code, res.stderr)
	}
	if strings.Contains(res.stdout+res.stderr, "\x1b") {
		t.Error("ANSI escapes off a terminal")
	}
	if res.stderr != "" {
		t.Errorf("stderr = %q, want no progress off a terminal", res.stderr)
	}
	for _, s := range []string{"OBSERVED DURING THIS RUN", "stopped by timeout", "KGWUI-013"} {
		if !strings.Contains(res.stdout, s) {
			t.Errorf("report lacks %q:\n%s", s, res.stdout)
		}
	}
	if strings.Contains(res.stdout, "check --live` to see") {
		t.Errorf("a --live report has the plain-check hint:\n%s", res.stdout)
	}
}

func TestLiveNoLeaks(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController)
	first, firstConns := 0, 0
	for i := range 3 {
		r := startLive(t, env, liveOpts{}, fastHooks(metricsScraper{}), "--timeout", "20s")
		// The fixture's metrics raise 007 once its scope mismatch matures, so 1 is fine too.
		if res := r.finish(); res.code == 2 {
			t.Fatalf("run %d: exit %d, stderr: %s", i, res.code, res.stderr)
		}
		if i == 0 {
			first = runtime.NumGoroutine()
			firstConns = settledConns(env.Proxies[0])
		}
	}
	waitNoWatches(t, env.Proxies[0])
	if n := settledConns(env.Proxies[0]); n > firstConns {
		t.Errorf("open connections after 3 runs = %d, after 1 run = %d", n, firstConns)
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > first+5 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > first+5 {
		t.Errorf("goroutines after 3 runs = %d, after 1 run = %d", n, first)
	}
}

// togglingScraper reports active xDS rejects while reject is set.
type togglingScraper struct{ reject *atomic.Bool }

func (s togglingScraper) Scrape(ctx context.Context, p *corev1.Pod, port int32) (io.ReadCloser, error) {
	if s.reject.Load() {
		return rejecting.Scrape(ctx, p, port)
	}
	return metricsScraper{}.Scrape(ctx, p, port)
}

// has001 reports whether p has an xDS reject finding.
func has001(p *engine.Published) bool {
	for _, im := range p.Model.Installs {
		for _, f := range im.Health.Findings {
			if f.Code == "KGWUI-001" {
				return true
			}
		}
	}
	return false
}

// TestLiveIntermittent: 001 appears and clears while the command is busy
// rendering, so it never sees the latest model in between; Observed still has it.
func TestLiveIntermittent(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController)
	reject := &atomic.Bool{}
	published := make(chan *engine.Published, 100000)
	var gate atomic.Pointer[chan struct{}]
	blocked := make(chan struct{}, 1)
	h := fastHooks(togglingScraper{reject})
	h.Published = func(p observe.Publication) { published <- p.Published }
	h.Tracked = func(*engine.Published) {
		if g := gate.Load(); g != nil {
			blocked <- struct{}{}
			<-*g
		}
	}
	r := startLive(t, env, liveOpts{}, h, "--timeout", "5m", "-o", "json")
	// stepUntil steps the clock until a model for which want holds is published.
	stepUntil := func(want func(*engine.Published) bool, steps int) {
		t.Helper()
		for range steps {
			r.fc.Step(5 * time.Second)
			at := r.fc.Now()
			deadline := time.After(10 * time.Second)
			for paced := false; !paced; {
				select {
				case p := <-published:
					if want(p) {
						return
					}
					paced = !p.Model.ComputedAt.Before(at)
				case <-deadline:
					t.Fatalf("no model at %v within 10s", at)
				}
			}
		}
		t.Fatalf("no model as wanted within %d steps", steps)
	}
	g := make(chan struct{})
	gate.Store(&g)
	// The command blocks after tracking its next model; meanwhile 001 is raised and
	// cleared (after the 60s restart hold of the session's first controller Pods).
	r.fc.Step(5 * time.Second)
	select {
	case <-blocked:
	case <-time.After(10 * time.Second):
		t.Fatal("the command tracked no model within 10s")
	}
	reject.Store(true)
	stepUntil(has001, 6)
	reject.Store(false)
	stepUntil(func(p *engine.Published) bool { return !has001(p) }, 30)
	gate.Store(nil)
	close(g)
	res := r.finish()
	doc := decodeLive(t, res.stdout)
	for _, f := range doc.Item.Observation.Observed {
		if f.Code == "KGWUI-001" && f.ClearedAt != nil {
			return
		}
	}
	t.Errorf("observed lacks 001 raised and cleared:\n%s", res.stdout)
}

// TestLiveInterruptDuringShutdown: a Ctrl-C while the session stops after the timeout still gives exit 2.
func TestLiveInterruptDuringShutdown(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController, checkRouteBroken)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopping, release := make(chan struct{}), make(chan struct{})
	h := fastHooks(metricsScraper{})
	h.Stopping = func() { close(stopping); <-release }
	r := startLive(t, env, liveOpts{ctx: ctx}, h, "--timeout", "30s", "-o", "json")
	r.stepUntilClosed(stopping)
	cancel()
	close(release)
	res := r.wait()
	if res.code != 2 {
		t.Errorf("exit %d, want 2; stderr: %s", res.code, res.stderr)
	}
	if doc := decodeLive(t, res.stdout); doc.Item.Observation.StopReason != "interrupted" {
		t.Errorf("stopReason %q, want interrupted", doc.Item.Observation.StopReason)
	}
}

// gateWriter blocks the first Write until release is closed.
type gateWriter struct {
	entered, release chan struct{}
	once             sync.Once
	buf              syncBuffer
}

func (w *gateWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered); <-w.release })
	return w.buf.Write(p)
}

// TestLiveInterruptDuringOutput: a Ctrl-C while the report is written still gives exit 2.
func TestLiveInterruptDuringOutput(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController, checkRouteBroken)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &gateWriter{entered: make(chan struct{}), release: make(chan struct{})}
	r := startLive(t, env, liveOpts{ctx: ctx, out: w}, fastHooks(metricsScraper{}), "--timeout", "30s", "-o", "json")
	r.stepUntilClosed(w.entered)
	cancel()
	close(w.release)
	if res := r.wait(); res.code != 2 {
		t.Errorf("exit %d, want 2; stderr: %s", res.code, res.stderr)
	}
	oneEnvelope(t, []byte(w.buf.String()), 1)
}

// flakyCollector publishes its one key, then fails (true) or publishes (false) on command.
type flakyCollector struct{ cmds chan bool }

func (flakyCollector) Name() string { return "flaky" }
func (c flakyCollector) Start(ctx context.Context, d extension.Deps) error {
	n := uint64(1)
	d.Publish("value", n, n, 0)
	for {
		select {
		case <-ctx.Done():
			return nil
		case fail := <-c.cmds:
			n++
			if fail {
				d.Fail("value", collect.Failing, "down")
			} else {
				d.Publish("value", n, n, 0)
			}
		}
	}
}

// TestLiveInputTransitions: a collector fails and recovers while the command is
// busy; each publication carries its own inputs, so both INPUT lines print, in order.
func TestLiveInputTransitions(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController)
	flaky := flakyCollector{cmds: make(chan bool)}
	reg := extension.New(extension.WithProxyStatsSource(fakeStats{}), extension.WithCollectors(flaky))
	published := make(chan observe.Publication, 100000)
	var gate atomic.Pointer[chan struct{}]
	blocked := make(chan struct{}, 1)
	h := fastHooks(metricsScraper{})
	h.Published = func(p observe.Publication) { published <- p }
	h.Tracked = func(*engine.Published) {
		if g := gate.Load(); g != nil {
			blocked <- struct{}{}
			<-*g
		}
	}
	r := startLive(t, env, liveOpts{reg: reg}, h, "--timeout", "1m")
	state := func(p observe.Publication) load.State { return p.Inputs[observe.CollectorGVR("flaky")].State }
	// waitState waits for a publication with the flaky input in want. A collector's first
	// failure restarts the quiet period, whose expiry brings the recompute: small steps fire it.
	waitState := func(want load.State) {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case p := <-published:
				if state(p) == want {
					return
				}
			case <-time.After(10 * time.Millisecond):
				r.fc.Step(10 * time.Millisecond)
			case <-deadline:
				t.Fatalf("no publication with flaky %s within 10s", want)
			}
		}
	}
	g := make(chan struct{})
	gate.Store(&g)
	r.fc.Step(5 * time.Second)
	select {
	case <-blocked:
	case <-time.After(10 * time.Second):
		t.Fatal("the command tracked no model within 10s")
	}
	flaky.cmds <- true
	waitState(load.StateFailed)
	flaky.cmds <- false
	waitState(load.StateOK)
	gate.Store(nil)
	close(g)
	r.finish()
	p := r.progress.String()
	down, up := strings.Index(p, "INPUT    flaky: unavailable: down"), strings.Index(p, "INPUT    flaky: available")
	if down < 0 || up < down {
		t.Errorf("progress lacks the flaky input going and coming back, in order (at %d, %d):\n%s", down, up, p)
	}
}

func TestTimeoutWithoutLive(t *testing.T) {
	env := checkEnv(t)
	for _, args := range [][]string{{"check", "--timeout", "1m"}, {"check", "--live", "--timeout", "0s"}} {
		res := runCheck(t, env, statsRegistry(fakeStats{}), fastHooks(metricsScraper{}), args...)
		if res.code != 1 || res.stdout != "" || !strings.HasPrefix(res.stderr, "Error: ") {
			t.Errorf("%v: exit %d stdout %q stderr %q, want a usage error", args, res.code, res.stdout, res.stderr)
		}
	}
}

// TestSecondInterruptKills runs a command that, like check --live, keeps working
// after the first SIGINT; the second must end the process at once.
func TestSecondInterruptKills(t *testing.T) {
	if os.Getenv("KGWCTL_INTERRUPT_HELPER") == "1" {
		interruptHelper()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSecondInterruptKills$")
	cmd.Env = append(os.Environ(), "KGWCTL_INTERRUPT_HELPER=1")
	out := &syncBuffer{}
	cmd.Stdout = out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor := func(s string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !strings.Contains(out.String(), s) {
			if time.Now().After(deadline) {
				_ = cmd.Process.Kill()
				t.Fatalf("helper never printed %q; output %q", s, out.String())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitFor("ready")
	_ = cmd.Process.Signal(os.Interrupt)
	waitFor("restored")
	_ = cmd.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGINT {
			t.Errorf("helper ended with %v, want killed by SIGINT", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the second interrupt did not end the process")
	}
}

// interruptHelper keeps reporting for a minute once the first interrupt restored default handling, as a slow report would.
func interruptHelper() {
	probe := &cobra.Command{Use: "probe", RunE: func(cmd *cobra.Command, _ []string) error {
		restored := make(chan struct{})
		defer app.RestoreOnCancel(cmd.Context(), func() { close(restored) })()
		os.Stdout.WriteString("ready\n")
		<-restored
		os.Stdout.WriteString("restored\n")
		time.Sleep(time.Minute)
		return nil
	}}
	os.Exit(run(app.NewRoot(app.Config{Commands: []*cobra.Command{probe}}), "probe"))
}
