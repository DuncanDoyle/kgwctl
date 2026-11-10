package observe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/ctrlmetrics"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/health"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
)

const (
	healthyFixture = "../testenv/testdata/healthy.yaml"
	stuckFixture   = "testdata/stuck-routes.yaml"
)

var httpRoutesGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}

// zeroScraper serves an empty /metrics payload: no rejects.
type zeroScraper struct{}

func (zeroScraper) Scrape(context.Context, *corev1.Pod, int32) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

// hangScraper blocks until the scrape's context ends.
type hangScraper struct{}

func (hangScraper) Scrape(ctx context.Context, _ *corev1.Pod, _ int32) (io.ReadCloser, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// earlyCollector returns at once without publishing.
type earlyCollector struct{}

func (earlyCollector) Name() string                                { return "early" }
func (earlyCollector) Start(context.Context, extension.Deps) error { return nil }

// silentCollector never publishes: it has no targets.
type silentCollector struct{}

func (silentCollector) Name() string { return "silent" }
func (silentCollector) Start(ctx context.Context, _ extension.Deps) error {
	<-ctx.Done()
	return nil
}

// sessionClients builds production clients from env's kubeconfig, bound to ctx.
func sessionClients(t *testing.T, env *testenv.Env, ctx context.Context) load.Clients {
	t.Helper()
	cfg, err := clientcmd.BuildConfigFromFlags("", env.Kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	c, err := load.NewClients(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// start runs a session under its own context and stops it at cleanup.
func start(t *testing.T, env *testenv.Env, reg *extension.Registry, o Options) *Session {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s, err := Start(ctx, sessionClients(t, env, ctx), reg, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s
}

// waitComplete waits on Changed until Complete holds, failing after within.
func waitComplete(t *testing.T, s *Session, within time.Duration) {
	t.Helper()
	deadline := time.NewTimer(within)
	defer deadline.Stop()
	for !s.Complete() {
		select {
		case <-s.Changed():
		case <-deadline.C:
			t.Fatalf("Complete() still false after %v", within)
		}
	}
}

// eventually polls cond every 10ms for up to 10s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// firstInstallModel returns the time the first synced model with an install appeared.
func firstInstallModel(t *testing.T, s *Session) time.Time {
	t.Helper()
	eventually(t, "a synced model with an install", func() bool {
		p := s.Current()
		return p != nil && p.Model.Snapshot.Synced && len(p.Model.Installs) > 0
	})
	return time.Now()
}

// checksReading returns the names of reg's checks that read a key of collector.
func checksReading(reg *extension.Registry, collector string) []string {
	var out []string
	for _, c := range reg.Checks {
		if slices.ContainsFunc(c.Inputs().Collected, func(k string) bool { return strings.HasPrefix(k, collector+"/") }) {
			out = append(out, c.Name())
		}
	}
	return out
}

// checkResult returns the named check's result in the first install's report.
func checkResult(t *testing.T, p *engine.Published, name string) health.CheckResult {
	t.Helper()
	if p == nil || len(p.Model.Installs) == 0 {
		t.Fatal("no model with an install")
	}
	for _, c := range p.Model.Installs[0].Health.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q not in the report", name)
	return health.CheckResult{}
}

func collectorNames(reg *extension.Registry) []string {
	var out []string
	for _, c := range reg.PackCollectors(extension.CollectorEnv{}) {
		out = append(out, c.Name())
	}
	for _, c := range reg.Collectors() {
		out = append(out, c.Name())
	}
	return out
}

// waitNoWatches fails unless p saw at least one watch and has none open within 2s.
func waitNoWatches(t *testing.T, p *testenv.Proxy) {
	t.Helper()
	if p.WatchesSeen() == 0 {
		t.Fatal("no watch was ever opened, so the cleanup check proves nothing")
	}
	deadline := time.Now().Add(2 * time.Second)
	for p.OpenWatches() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d watches still open 2s after Stop returned", p.OpenWatches())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitGoroutines polls for up to 5s until the goroutine count is at most limit.
func waitGoroutines(t *testing.T, limit int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := runtime.NumGoroutine()
		if n <= limit {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			t.Fatalf("goroutines = %d, want at most %d\n%s", n, limit, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestObserveCompleteHealthy(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthyFixture}})
	reg := extension.New()
	s := start(t, env, reg, Options{Scraper: zeroScraper{}})
	waitComplete(t, s, 10*time.Second)

	if n := len(s.Current().Model.Installs); n != 1 {
		t.Fatalf("installs = %d, want 1", n)
	}
	in := s.Inputs()
	for _, name := range collectorNames(reg) {
		got, ok := in[CollectorGVR(name)]
		if !ok || got.State != load.StateOK {
			t.Errorf("collector %s input = %+v (present %v), want ok", name, got, ok)
		}
	}
	for gvr, i := range in {
		if i.State == load.StateLoading {
			t.Errorf("input %v still loading at Complete", gvr)
		}
	}
}

func TestObserveNoInstall(t *testing.T) {
	env := testenv.New(t, testenv.Options{})
	s := start(t, env, extension.New(), Options{Scraper: zeroScraper{}})
	waitComplete(t, s, 10*time.Second)
	if n := len(s.Current().Model.Installs); n != 0 {
		t.Fatalf("installs = %d, want 0", n)
	}
}

func TestObservePodsProxyDenied(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthyFixture}})
	env.Proxies[0].Rule(testenv.Match{Resource: "pods", Subresource: "proxy"}, testenv.Action{Status: http.StatusForbidden})
	reg := extension.New()
	s := start(t, env, reg, Options{})
	waitComplete(t, s, 15*time.Second)

	names := checksReading(reg, ctrlmetrics.CollectorName)
	if len(names) == 0 {
		t.Fatal("no registered check reads the controller metrics")
	}
	for _, name := range names {
		// The refused scrape reaches the user through the check's result, never as a pass.
		if r := checkResult(t, s.Current(), name); r.OK || !strings.Contains(r.Error+r.Detail, "403") {
			t.Errorf("check %q = %+v; want not ok with the 403 text", name, r)
		}
	}
	for _, f := range s.Current().Model.Installs[0].Health.Findings {
		if f.Code == health.CodeXDSRejected || f.Code == health.CodeConfigWithheld {
			t.Errorf("finding %s raised from metrics that were never read", f.Code)
		}
	}
}

func TestObserveScrapeHangs(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthyFixture}})
	reg := extension.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := Start(ctx, sessionClients(t, env, ctx), reg, Options{Scraper: hangScraper{}, FirstPublishWait: time.Second, Quiet: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	t0 := firstInstallModel(t, s)
	for time.Since(t0) < 800*time.Millisecond {
		if s.Complete() {
			t.Fatalf("Complete() true %v after the first install model, before the 1s first-publish wait", time.Since(t0))
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitComplete(t, s, 3*time.Second)
	for _, name := range checksReading(reg, ctrlmetrics.CollectorName) {
		if r := checkResult(t, s.Current(), name); r.OK {
			t.Errorf("check %q passed without metrics", name)
		}
	}
	stopped := make(chan struct{})
	go func() { s.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return within 2s while a scrape hangs")
	}
}

func TestObserveStopNoLeaks(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthyFixture}})
	reg := extension.New()
	run := func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s, err := Start(ctx, sessionClients(t, env, ctx), reg, Options{Scraper: zeroScraper{}, Quiet: 200 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		waitComplete(t, s, 10*time.Second)
		s.Stop()
		s.Stop() // idempotent
		if _, open := <-s.Models(); open {
			t.Fatal("Models() still open after Stop")
		}
	}
	// The first run leaves the shared transport's idle connections behind.
	run()
	waitNoWatches(t, env.Proxies[0])
	base := runtime.NumGoroutine()
	run()
	waitNoWatches(t, env.Proxies[0])
	waitGoroutines(t, base+2)
}

// loopLog records the engine's loop events for the clock-stepping tests.
type loopLog struct {
	mu      sync.Mutex
	last    engine.LoopEventKind
	running bool
	seen    bool
	events  []engine.LoopEvent
}

func (l *loopLog) record(ev engine.LoopEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.last, l.seen = ev.Kind, true
	l.events = append(l.events, ev)
	switch ev.Kind {
	case engine.EvRunStarted:
		l.running = true
	case engine.EvPublished:
		l.running = false
	}
}

// idle reports whether the loop has armed its timer with no run in flight.
func (l *loopLog) idle() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seen && !l.running && l.last == engine.EvArmed
}

func hookLoop(t *testing.T) *loopLog {
	l := &loopLog{}
	testLoop = l.record
	t.Cleanup(func() { testLoop = nil })
	return l
}

// step advances fc by d once the loop waits, then waits for a model computed at the new time.
func step(t *testing.T, s *Session, l *loopLog, fc *clocktesting.FakeClock, d time.Duration) *engine.Published {
	t.Helper()
	eventually(t, "the engine loop to wait", l.idle)
	fc.Step(d)
	now := fc.Now()
	eventually(t, "a model computed at "+now.String(), func() bool {
		p := s.Current()
		return p != nil && !p.Model.ComputedAt.Before(now)
	})
	return s.Current()
}

func warnings003(p *engine.Published) int {
	n := 0
	for _, f := range p.Model.Installs[0].Health.Findings {
		if f.Code == health.CodeRouteUnattached && f.Severity == health.SeverityWarning {
			n++
		}
	}
	return n
}

func has018(p *engine.Published) bool {
	return slices.ContainsFunc(p.Model.Installs[0].Health.Findings, func(f health.Finding) bool {
		return f.Code == health.CodeStatusNotProgressing
	})
}

func TestObserveHoldMatures(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthyFixture, stuckFixture}})
	l := hookLoop(t)
	fc := clocktesting.NewFakeClock(time.Now())
	s := start(t, env, extension.New(), Options{Clock: fc, Scraper: zeroScraper{}, MetricsInterval: 10 * time.Second})
	eventually(t, "the cache to settle", s.ca.Settled)

	p := step(t, s, l, fc, time.Second)
	for !p.Model.Snapshot.Synced || len(p.Model.Installs) == 0 {
		p = step(t, s, l, fc, time.Second)
	}
	start := fc.Now()
	if n := warnings003(p); n != 0 {
		t.Fatalf("%d KGWUI-003 warnings inside the grace period", n)
	}
	for warnings003(p) < 10 {
		if fc.Since(start) > 2*time.Minute {
			t.Fatalf("KGWUI-003 warnings = %d after %v, want 10", warnings003(p), fc.Since(start))
		}
		p = step(t, s, l, fc, 10*time.Second)
	}
	if fc.Since(start) < health.Grace {
		t.Fatalf("KGWUI-003 warnings raised after %v, before the %v grace", fc.Since(start), health.Grace)
	}
	hold := checkResult(t, p, "status progress").Hold
	if hold == nil || hold.Count < 10 {
		t.Fatalf("status progress hold = %+v, want one with count >= 10", hold)
	}
	t.Logf("%d KGWUI-003 warnings %v after the first synced model; hold %+v", warnings003(p), fc.Since(start), *hold)
	holdStart := fc.Now()
	for !has018(p) {
		if fc.Since(holdStart) > 7*time.Minute {
			t.Fatalf("no KGWUI-018 %v after the hold started", fc.Since(holdStart))
		}
		p = step(t, s, l, fc, 10*time.Second)
	}
	t.Logf("KGWUI-018 raised %v after the hold started", fc.Since(holdStart))
	if fc.Since(holdStart) < 5*time.Minute {
		t.Fatalf("KGWUI-018 raised %v after the hold started, before 5m", fc.Since(holdStart))
	}
}

func TestObserveModelsInOrder(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthyFixture}})
	l := hookLoop(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// A short interval makes the safety timer publish a model every 50ms.
	s, err := Start(ctx, sessionClients(t, env, ctx), extension.New(), Options{Scraper: zeroScraper{}, MetricsInterval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	var gotMu sync.Mutex
	var got []uint64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for p := range s.Models() {
			gotMu.Lock()
			got = append(got, p.Model.Seq)
			gotMu.Unlock()
			time.Sleep(20 * time.Millisecond) // a slow reader
		}
	}()
	eventually(t, "20 published models", func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		n := 0
		for _, ev := range l.events {
			if ev.Kind == engine.EvPublished {
				n++
			}
		}
		return n >= 20
	})
	eventually(t, "the slow reader to receive 20 models", func() bool {
		gotMu.Lock()
		defer gotMu.Unlock()
		return len(got) >= 20
	})
	s.Stop()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Models() not closed after Stop")
	}
	var want []uint64
	for _, ev := range l.events {
		if ev.Kind == engine.EvPublished {
			want = append(want, ev.Published.Model.Seq)
		}
	}
	// Models not yet received at Stop are dropped, so got is a prefix of want.
	if len(got) < 20 || !slices.Equal(got, want[:len(got)]) {
		t.Fatalf("Models() delivered seqs %v, published %v", got, want)
	}
	if !slices.IsSorted(got) {
		t.Fatalf("seqs out of order: %v", got)
	}
}

func TestObserveCollectorReturnsEarly(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthyFixture}})
	s := start(t, env, extension.New(extension.WithCollectors(earlyCollector{})),
		Options{Scraper: zeroScraper{}, FirstPublishWait: time.Hour, Quiet: 200 * time.Millisecond})
	waitComplete(t, s, 10*time.Second)
	if got := s.Inputs()[CollectorGVR("early")]; got.State != load.StateOK {
		t.Fatalf("early collector input = %+v, want ok", got)
	}
}

func TestObserveSilentCollector(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthyFixture}})
	s := start(t, env, extension.New(extension.WithCollectors(silentCollector{})),
		Options{Scraper: zeroScraper{}, FirstPublishWait: time.Second, Quiet: 200 * time.Millisecond})
	t0 := firstInstallModel(t, s)
	waitComplete(t, s, 10*time.Second)
	if d := time.Since(t0); d > time.Second+time.Second {
		t.Fatalf("Complete() %v after the first install model, want at most the 1s wait plus slack", d)
	}
	if got := s.Inputs()[CollectorGVR("silent")]; got.State != load.StateLoading {
		t.Fatalf("silent collector input = %+v, want loading", got)
	}
}

func TestObserveQuietRecompute(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthyFixture}})
	l := hookLoop(t)
	var mu sync.Mutex
	var quietAt []int // index into l.events when the quiet timer fired
	testQuiet = func() {
		l.mu.Lock()
		n := len(l.events)
		l.mu.Unlock()
		mu.Lock()
		quietAt = append(quietAt, n)
		mu.Unlock()
	}
	t.Cleanup(func() { testQuiet = nil })
	s := start(t, env, extension.New(), Options{Scraper: zeroScraper{}, Quiet: 500 * time.Millisecond})
	waitComplete(t, s, 10*time.Second)

	mu.Lock()
	last := quietAt[len(quietAt)-1]
	mu.Unlock()
	l.mu.Lock()
	evs := slices.Clone(l.events)
	l.mu.Unlock()
	var before *engine.Published
	for _, ev := range evs[:last] {
		if ev.Kind == engine.EvPublished {
			before = ev.Published
		}
	}
	var after *engine.Published
	for _, ev := range evs[last:] {
		if ev.Kind == engine.EvPublished {
			after = ev.Published
			break
		}
	}
	if before == nil || after == nil {
		t.Fatalf("no model published around the quiet expiry (before %v, after %v)", before != nil, after != nil)
	}
	// Same source: nothing changed, so only the quiet expiry caused the recompute.
	if !after.Source.Equal(before.Source) {
		t.Fatalf("source changed across the quiet recompute: %+v -> %+v", before.Source, after.Source)
	}
	if after.Model.Seq <= before.Model.Seq {
		t.Fatalf("quiet recompute seq %d not after %d", after.Model.Seq, before.Model.Seq)
	}
}

func TestObserveDiscoverySlow(t *testing.T) {
	env := testenv.New(t, testenv.Options{})
	release := make(chan struct{})
	t.Cleanup(sync.OnceFunc(func() { close(release) }))
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindDiscovery, Resource: "*"}, testenv.Action{Until: release})
	base := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := sessionClients(t, env, ctx)
	type result struct {
		s   *Session
		err error
	}
	res := make(chan result, 1)
	begin := time.Now()
	go func() {
		s, err := Start(ctx, c, extension.New(), Options{Scraper: zeroScraper{}})
		res <- result{s, err}
	}()
	// The caller's acquisition deadline: stop waiting and cancel the session.
	select {
	case r := <-res:
		t.Fatalf("Start returned before discovery was released: %v", r.err)
	case <-time.After(500 * time.Millisecond):
	}
	cancel()
	if d := time.Since(begin); d > time.Second {
		t.Fatalf("the caller's wait took %v, want it to end at the 500ms deadline", d)
	}
	select {
	case r := <-res:
		if r.err == nil {
			r.s.Stop()
			t.Fatal("Start succeeded after its context was cancelled")
		}
		if !errors.Is(r.err, context.Canceled) {
			t.Logf("Start error: %v", r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return within 2s of cancellation")
	}
	waitGoroutines(t, base+2)
}

func TestObserveOutlivesRequestTimeout(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthyFixture}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The acquisition deadline bounds only the wait, never the clients.
	acq, acqCancel := context.WithTimeout(context.Background(), time.Second)
	defer acqCancel()
	s, err := Start(ctx, sessionClients(t, env, ctx), extension.New(), Options{Scraper: zeroScraper{}, Quiet: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	eventually(t, "a first model", func() bool { return s.Current() != nil })
	if acq.Err() != nil {
		t.Fatal("no model within the 1s acquisition deadline")
	}
	<-acq.Done()
	time.Sleep(2 * time.Second) // the session outlives the deadline by 2s

	// A route created now reaches a later model through the session's watches.
	dyn := dynamic.NewForConfigOrDie(env.Admin)
	route := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
		"metadata": map[string]any{"name": "late", "namespace": "default"},
		"spec":     map[string]any{"parentRefs": []any{map[string]any{"name": "http", "namespace": "kgateway-system"}}},
	}}
	if _, err := dyn.Resource(httpRoutesGVR).Namespace("default").Create(t.Context(), route, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = dyn.Resource(httpRoutesGVR).Namespace("default").Delete(context.Background(), "late", metav1.DeleteOptions{})
	})
	eventually(t, "a model with the late route", func() bool {
		p := s.Current()
		return p != nil && slices.ContainsFunc(p.Model.Snapshot.Objects[httpRoutesGVR], func(u *unstructured.Unstructured) bool {
			return u.GetName() == "late"
		})
	})
}

func TestObserveStopWithUnreadModels(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthyFixture}})
	l := hookLoop(t)
	published := func() int {
		l.mu.Lock()
		defer l.mu.Unlock()
		n := 0
		for _, ev := range l.events {
			if ev.Kind == engine.EvPublished {
				n++
			}
		}
		return n
	}
	run := func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// A short interval publishes a model every 50ms; nobody reads Models().
		s, err := Start(ctx, sessionClients(t, env, ctx), extension.New(), Options{Scraper: zeroScraper{}, MetricsInterval: 50 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		from := published()
		eventually(t, "10 unread models", func() bool { return published()-from >= 10 })
		stopped := make(chan struct{})
		go func() { s.Stop(); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			t.Fatal("Stop() did not return within 2s with unread models")
		}
		if _, open := <-s.Models(); open {
			t.Fatal("Models() delivered a model after Stop")
		}
	}
	// The first run leaves the shared transport's idle connections behind.
	run()
	waitNoWatches(t, env.Proxies[0])
	base := runtime.NumGoroutine()
	run()
	waitNoWatches(t, env.Proxies[0])
	waitGoroutines(t, base+2)
}
