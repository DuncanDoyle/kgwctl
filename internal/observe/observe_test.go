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

	"github.com/DuncanDoyle/snellius/pkg/collect"
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

func (l *loopLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.events)
}

func (l *loopLog) countKind(k engine.LoopEventKind) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, ev := range l.events {
		if ev.Kind == k {
			n++
		}
	}
	return n
}

func (l *loopLog) published() int { return l.countKind(engine.EvPublished) }

// seqs returns the Seq of every published model, in order.
func (l *loopLog) seqs() []uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []uint64
	for _, ev := range l.events {
		if ev.Kind == engine.EvPublished {
			out = append(out, ev.Published.Model.Seq)
		}
	}
	return out
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
	// The reader takes one model, then waits at the gate while a backlog builds.
	gate := make(chan struct{})
	var got []uint64
	done := make(chan struct{})
	go func() {
		defer close(done)
		first := true
		for p := range s.Models() {
			got = append(got, p.Model.Seq)
			if first {
				first = false
				<-gate
			}
		}
	}()
	eventually(t, "a backlog of 10 published models", func() bool { return l.published() >= 11 })
	s.Stop()
	close(gate)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Models() not closed after Stop")
	}
	rest := s.Drain()
	if len(rest) == 0 {
		t.Fatal("Drain returned nothing, so the backlog never formed")
	}
	for _, p := range rest {
		got = append(got, p.Model.Seq)
	}
	if want := l.seqs(); !slices.Equal(got, want) {
		t.Fatalf("Models() + Drain() = %v, published %v", got, want)
	}
	if again := s.Drain(); len(again) != 0 {
		t.Fatalf("second Drain returned %d models", len(again))
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

// gateCheck blocks its Run while armed, so a test holds a compute in flight.
type gateCheck struct {
	mu      sync.Mutex
	gate    chan struct{}
	entered chan struct{}
}

func (*gateCheck) Name() string { return "test gate" }

// Inputs reads the clock, so the check is never memoized and runs on every compute.
func (*gateCheck) Inputs() health.InputSet { return health.InputSet{Clock: true} }

func (c *gateCheck) Run(ctx health.RunContext) ([]health.Finding, health.CheckResult) {
	c.mu.Lock()
	g := c.gate
	c.gate = nil
	c.mu.Unlock()
	if g != nil {
		c.entered <- struct{}{}
		<-g
	}
	return nil, health.CheckResult{Name: c.Name(), OK: true, CollectedAt: ctx.Now}
}

// arm makes the next Run block until the returned func is called.
func (c *gateCheck) arm() (release func()) {
	g := make(chan struct{})
	c.mu.Lock()
	c.gate = g
	c.mu.Unlock()
	return sync.OnceFunc(func() { close(g) })
}

// pushCollector publishes a new value each time the test calls push.
type pushCollector struct{ ch chan struct{} }

func (*pushCollector) Name() string { return "push" }
func (c *pushCollector) Start(ctx context.Context, d extension.Deps) error {
	var n uint64
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.ch:
			n++
			d.Publish("value", n, n, 0)
		}
	}
}

// signals counts hook calls and lets a test wait for the next one.
type signals struct {
	mu sync.Mutex
	n  int
}

func (g *signals) hit() { g.mu.Lock(); g.n++; g.mu.Unlock() }
func (g *signals) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.n
}

// settle steps fc by d (at least the 1s confirm tick, so the loop always
// wakes) and waits until the loop is idle again.
func settle(t *testing.T, l *loopLog, fc *clocktesting.FakeClock, d time.Duration) {
	t.Helper()
	eventually(t, "the engine loop to wait", l.idle)
	n := l.count()
	fc.Step(d)
	eventually(t, "the engine loop to wake and wait again", func() bool { return l.count() > n && l.idle() })
}

// TestObserveQuietBarrier drives the quiet barrier with a fake clock, a pushed
// collector and a check that holds a compute in flight.
func TestObserveQuietBarrier(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthyFixture}})
	l := hookLoop(t)
	var quiet, armed signals
	testQuiet, testArmed = quiet.hit, armed.hit
	t.Cleanup(func() { testQuiet, testArmed = nil, nil })
	gc := &gateCheck{entered: make(chan struct{}, 1)}
	push := &pushCollector{ch: make(chan struct{})}
	reg := extension.New(extension.WithChecks("TEST-", gc), extension.WithCollectors(push))
	if err := reg.Err(); err != nil {
		t.Fatal(err)
	}
	fc := clocktesting.NewFakeClock(time.Now())
	// An hour-long interval and wait keep the safety timer, the collectors' ticks and the wait out of the way.
	s := start(t, env, reg, Options{Clock: fc, Scraper: zeroScraper{}, MetricsInterval: time.Hour, FirstPublishWait: time.Hour, Quiet: 2 * time.Second})
	eventually(t, "the cache to settle", s.ca.Settled)

	// publish pushes a value and waits until it has re-armed the quiet timer and reached the engine.
	publish := func() {
		a, trig := armed.count(), l.countKind(engine.EvTriggered)
		push.ch <- struct{}{}
		eventually(t, "the quiet timer to re-arm", func() bool { return armed.count() > a })
		eventually(t, "the engine to see the publish", func() bool { return l.countKind(engine.EvTriggered) > trig })
	}
	// await waits for the gate check to be entered: a compute is in flight.
	await := func() {
		select {
		case <-gc.entered:
		case <-time.After(10 * time.Second):
			t.Fatal("no compute reached the gate check")
		}
	}
	// published waits for the in-flight compute to publish after release.
	finish := func(release func()) {
		n := l.published()
		release()
		eventually(t, "the held compute to publish", func() bool { return l.published() > n })
		eventually(t, "the engine loop to wait", l.idle)
	}

	// Baseline: every collector reports and the session completes once.
	publish()
	for i := 0; !s.Complete(); i++ {
		if i == 20 {
			t.Fatal("no baseline completion after 20s of fake time")
		}
		settle(t, l, fc, time.Second)
	}

	// 1. A compute that starts before the quiet expiry does not complete the session.
	publish()
	if s.Complete() {
		t.Fatal("Complete() true right after a publish")
	}
	release := gc.arm()
	q := quiet.count()
	fc.Step(time.Second) // the publish's debounce is due: a compute starts
	await()
	fc.Step(time.Second) // the quiet period ends while that compute is held
	eventually(t, "the quiet timer to fire", func() bool { return quiet.count() > q })
	finish(release)
	if s.Complete() {
		t.Fatal("Complete() true from a compute that started before the quiet expiry")
	}

	// 2. A publish during a compute that started after the expiry voids it.
	release = gc.arm()
	fc.Step(time.Second) // the quiet expiry's trigger is due: a qualifying compute starts
	await()
	publish()
	finish(release)
	if s.Complete() {
		t.Fatal("Complete() true after a publish during the qualifying compute")
	}

	// 3. Only a compute started after the next quiet expiry completes the session.
	q = quiet.count()
	for i := 0; ; i++ {
		if s.Complete() {
			if quiet.count() == q {
				t.Fatal("Complete() true before the quiet timer fired after the last publish")
			}
			break
		}
		if i == 10 {
			t.Fatal("not complete 10s of fake time after the last publish")
		}
		settle(t, l, fc, time.Second)
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
	begin := time.Now()
	// The acquisition deadline is independent of the session context and its clients.
	acq, acqCancel := context.WithTimeout(context.Background(), time.Second)
	defer acqCancel()
	s, err := Start(ctx, sessionClients(t, env, ctx), extension.New(), Options{Scraper: zeroScraper{}, Quiet: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	waitComplete(t, s, 10*time.Second)
	<-acq.Done()
	// Keep the session for 3s in all, well past the deadline.
	if d := 3*time.Second - time.Since(begin); d > 0 {
		time.Sleep(d)
	}

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
	published := l.published
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
		if n := len(s.Drain()); n < 10 {
			t.Fatalf("Drain returned %d unread models, want at least 10", n)
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

// failingCollector reports the same failure every 200ms and never publishes.
type failingCollector struct{}

func (failingCollector) Name() string { return "failing" }
func (failingCollector) Start(ctx context.Context, d extension.Deps) error {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		d.Fail("value", collect.Failing, "target unreachable")
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func TestObserveRepeatedFailure(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthyFixture}})
	// A 500ms quiet period would never end if each repeated Fail restarted it.
	s := start(t, env, extension.New(extension.WithCollectors(failingCollector{})),
		Options{Scraper: zeroScraper{}, Quiet: 500 * time.Millisecond, FirstPublishWait: time.Hour})
	waitComplete(t, s, 10*time.Second)
	got := s.Inputs()[CollectorGVR("failing")]
	if got.State != load.StateFailed || got.Reason != "target unreachable" {
		t.Fatalf("failing collector input = %+v, want failed with its reason", got)
	}
}

func TestObserveFirstPublishWaitRecompute(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthyFixture}})
	l := hookLoop(t)
	var expired signals
	testWait = expired.hit
	t.Cleanup(func() { testWait = nil })
	fc := clocktesting.NewFakeClock(time.Now())
	s := start(t, env, extension.New(extension.WithCollectors(silentCollector{})),
		Options{Clock: fc, Scraper: zeroScraper{}, MetricsInterval: time.Hour, FirstPublishWait: 5 * time.Second, Quiet: time.Second})
	eventually(t, "the cache to settle", s.ca.Settled)

	for i := 0; expired.count() == 0; i++ {
		if i == 20 {
			t.Fatal("the first-publish wait never expired")
		}
		if s.Complete() {
			t.Fatal("Complete() true while the silent collector's wait runs")
		}
		settle(t, l, fc, time.Second)
	}
	// The expiry needs a new model: the one computed before it does not count.
	eventually(t, "the engine loop to wait", l.idle)
	if s.Complete() {
		t.Fatal("Complete() true at the wait's expiry, before a compute started after it")
	}
	n := l.countKind(engine.EvRunStarted)
	for i := 0; !s.Complete(); i++ {
		if i == 5 {
			t.Fatal("not complete 5s of fake time after the wait expired")
		}
		settle(t, l, fc, time.Second)
	}
	if l.countKind(engine.EvRunStarted) == n {
		t.Fatal("complete without a compute started after the wait expired")
	}
}

// flipCollector fails with each reason the test sends; the first one is sent at once.
type flipCollector struct{ ch chan string }

func (*flipCollector) Name() string { return "flip" }
func (c *flipCollector) Start(ctx context.Context, d extension.Deps) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case r := <-c.ch:
			d.Fail("value", collect.Failing, r)
		}
	}
}

// TestObserveResettleRecomputes holds a changed Fail between its store write
// and its record, lets a compute consume the store's notify, then checks the
// session still completes without the safety timer.
func TestObserveResettleRecomputes(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthyFixture}})
	l := hookLoop(t)
	flip := &flipCollector{ch: make(chan string, 1)}
	flip.ch <- "first"
	fc := clocktesting.NewFakeClock(time.Now())
	// An hour-long interval and wait: only a trigger can start a compute.
	s := start(t, env, extension.New(extension.WithCollectors(flip)),
		Options{Clock: fc, Scraper: zeroScraper{}, MetricsInterval: time.Hour, FirstPublishWait: time.Hour, Quiet: time.Second})
	eventually(t, "the cache to settle", s.ca.Settled)
	for i := 0; !s.Complete(); i++ {
		if i == 20 {
			t.Fatal("no baseline completion after 20s of fake time")
		}
		settle(t, l, fc, time.Second)
	}

	stored, release := make(chan struct{}), make(chan struct{})
	testStored = func(name string) {
		if name == "flip" {
			close(stored)
			<-release
		}
	}
	t.Cleanup(func() { testStored = nil })
	trig := l.countKind(engine.EvTriggered)
	flip.ch <- "second"
	<-stored
	eventually(t, "the engine to see the store's notify", func() bool { return l.countKind(engine.EvTriggered) > trig })
	n := l.published()
	settle(t, l, fc, time.Second) // a compute consumes the store's notify before the record
	if l.published() == n {
		t.Fatal("no compute ran on the store's notify")
	}
	close(release)
	eventually(t, "the changed failure to be recorded", func() bool { return s.Inputs()[CollectorGVR("flip")].Reason == "second" })
	if s.Complete() {
		t.Fatal("Complete() true from a compute that ran before the changed failure was recorded")
	}
	for i := 0; !s.Complete(); i++ {
		if i == 5 {
			t.Fatal("not complete 5s of fake time after the changed failure; it waits for the safety timer")
		}
		settle(t, l, fc, time.Second)
	}
}
