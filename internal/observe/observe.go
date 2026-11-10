// Package observe keeps one Snellius cache, engine and the registry's
// collectors running for one command, and says when the model is complete
// enough to report from (design §4.1).
package observe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/cache"
	"github.com/DuncanDoyle/snellius/pkg/collect"
	"github.com/DuncanDoyle/snellius/pkg/ctrlmetrics"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/setup"
	"github.com/DuncanDoyle/snellius/pkg/trigger"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/clock"

	"github.com/DuncanDoyle/kgwctl/internal/load"
)

// Option defaults.
const (
	DefaultMetricsInterval  = 10 * time.Second
	DefaultQuiet            = 2 * time.Second
	DefaultFirstPublishWait = 6 * time.Second
)

// Options configures a Session. Zero durations take the defaults.
type Options struct {
	// MetricsInterval is the collection interval and the engine's safety timer.
	MetricsInterval time.Duration
	// Quiet is how long no collector may publish before the final recompute.
	Quiet time.Duration
	// FirstPublishWait is how long after the first model with an install a
	// collector that has not published is treated as having nothing to report.
	FirstPublishWait time.Duration
	// Clock drives the engine, the collectors and the session's timers; nil is the real clock.
	Clock clock.WithTicker
	// Scraper replaces the controller metrics scraper; nil uses pods/proxy.
	Scraper ctrlmetrics.Scraper
	// Logger receives the engine's and collectors' logs; nil discards them.
	Logger *slog.Logger
}

func (o Options) withDefaults() Options {
	if o.MetricsInterval <= 0 {
		o.MetricsInterval = DefaultMetricsInterval
	}
	if o.Quiet <= 0 {
		o.Quiet = DefaultQuiet
	}
	if o.FirstPublishWait <= 0 {
		o.FirstPublishWait = DefaultFirstPublishWait
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	return o
}

// testLoop and testQuiet, if set, see each loop event and each quiet expiry (tests).
var (
	testLoop  func(engine.LoopEvent)
	testQuiet func()
)

// CollectorGVR is the Inputs key of the collector named name; it never
// collides with a served kind.
func CollectorGVR(name string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Resource: "collector#" + name}
}

// collectorState is what one collector has done so far.
type collectorState struct {
	published bool
	reported  bool              // published or failed at least once
	failing   map[string]string // key -> reason of its latest Failing Fail
	returned  bool
	err       string // Start's error or panic
	// checks are the checks that read only this collector's keys.
	checks []string
}

// Session is one running cache, engine and collector set.
type Session struct {
	cancel  context.CancelFunc
	ca      *cache.Cache
	eng     *engine.Engine
	changes *trigger.Signal
	clk     clock.WithTicker
	o       Options
	names   []string

	wg         sync.WaitGroup
	done       chan struct{}
	rearm      chan struct{}
	discovered chan struct{}
	changed    chan struct{}
	out        chan *engine.Published
	forward    sync.Once

	mu   sync.Mutex
	cond *sync.Cond // signals queue growth and ended
	cols map[string]*collectorState
	// queue holds published models not yet sent on out; ended is set once no more can come.
	queue []*engine.Published
	ended bool
	// discoveredClosed and waitOver track the first-publish wait.
	discoveredClosed, waitOver bool
	// gen counts collector store changes; the quiet and settled flags reset on each.
	gen                 uint64
	quietFired, settled bool
	runAfterQuiet       bool
	runGen              uint64
}

// Start builds the cache and engine from reg, starts them and the
// collectors under ctx, and returns. It blocks on discovery inside
// setup.Build; the caller bounds that wait and cancels ctx at its deadline.
func Start(ctx context.Context, c load.Clients, reg *extension.Registry, o Options) (*Session, error) {
	o = o.withDefaults()
	sctx, cancel := context.WithCancel(ctx)
	cfg, err := setup.Build(setup.Clients{Dynamic: c.Dynamic, Kube: c.Kube, Discovery: load.MemoDiscovery(sctx, c.Discovery), Metadata: c.Metadata},
		setup.Options{MetricsInterval: o.MetricsInterval, Clock: o.Clock}, reg)
	if err == nil && sctx.Err() != nil {
		err = context.Cause(sctx)
	}
	if err != nil {
		cancel()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("discovery: %w", context.Cause(ctx))
		}
		return nil, err
	}
	s := &Session{cancel: cancel, changes: cfg.Changes, clk: o.Clock, o: o, cols: map[string]*collectorState{},
		done: make(chan struct{}), rearm: make(chan struct{}, 1), discovered: make(chan struct{}),
		changed: make(chan struct{}, 1), out: make(chan *engine.Published)}
	if s.clk == nil {
		s.clk = clock.RealClock{}
	}
	s.cond = sync.NewCond(&s.mu)
	s.ca = cache.New(cfg.Cache)
	ecfg := cfg.Engine
	ecfg.Snapshots, ecfg.Logger, ecfg.OnLoop = s.ca, o.Logger, s.onLoop
	s.eng = engine.New(ecfg)

	env := extension.CollectorEnv{Kube: c.Kube, Scraper: o.Scraper, Interval: o.MetricsInterval,
		MaxAge: setup.MetricsMaxAge(o.MetricsInterval), Clock: o.Clock, Installs: s.eng.Installs, Current: s.eng.Current,
		Discovered: s.discovered, Collected: cfg.Collected}
	cols := append(reg.PackCollectors(env), reg.Collectors()...)
	for _, col := range cols {
		name := col.Name()
		s.names = append(s.names, name)
		s.cols[name] = &collectorState{failing: map[string]string{}, checks: ownChecks(reg.Checks, name)}
	}

	s.ca.Start(sctx)
	for _, col := range cols {
		s.runCollector(sctx, col, cfg.Collected)
	}
	s.wg.Go(func() { s.timers(sctx) })
	s.wg.Go(func() { s.eng.Run(sctx, o.MetricsInterval) })
	go func() {
		<-sctx.Done()
		s.ca.Wait()
		s.wg.Wait()
		s.mu.Lock()
		s.ended = true
		s.cond.Broadcast()
		s.mu.Unlock()
		close(s.done)
	}()
	return s, nil
}

// ownChecks returns the checks whose collector keys all belong to collector name.
func ownChecks(checks []health.Check, name string) []string {
	var out []string
	for _, c := range checks {
		keys := c.Inputs().Collected
		if len(keys) > 0 && !slices.ContainsFunc(keys, func(k string) bool { return !strings.HasPrefix(k, name+"/") }) {
			out = append(out, c.Name())
		}
	}
	return out
}

// runCollector runs col in a goroutine Stop joins, its Publish and Fail
// writing "<name>/<key>" to store as Registry.RunCollectors does.
func (s *Session) runCollector(ctx context.Context, col extension.Collector, store *collect.Store) {
	name := col.Name()
	log := s.o.Logger.With("collector", name)
	d := extension.Deps{Current: s.eng.Current, Logger: log,
		Publish: func(key string, v any, fp uint64, ttl time.Duration) {
			store.Publish(name+"/"+key, v, fp, ttl)
			s.record(name, func(st *collectorState) {
				st.published, st.reported = true, true
				delete(st.failing, key)
			})
		},
		Fail: func(key string, state collect.State, reason string) {
			store.Fail(name+"/"+key, state, reason)
			s.record(name, func(st *collectorState) {
				st.reported = true
				if state == collect.Failing {
					st.failing[key] = reason
				} else {
					delete(st.failing, key)
				}
			})
		},
	}
	s.wg.Go(func() {
		var errText string
		defer func() {
			store.Stop(name)
			s.record(name, func(st *collectorState) { st.returned, st.err = true, errText })
		}()
		defer func() {
			if p := recover(); p != nil {
				log.Error("collector panicked", "panic", p, "stack", string(debug.Stack()))
				errText = fmt.Sprint("panic: ", p)
			}
		}()
		// A return caused by the cancelled ctx is a normal stop.
		if err := col.Start(ctx, d); err != nil && (ctx.Err() == nil || !errors.Is(err, context.Canceled)) {
			log.Error("collector stopped", "err", err)
			errText = err.Error()
		}
	})
}

// record applies f to collector name's state. Every store change re-arms the
// quiet timer and voids the settled model.
func (s *Session) record(name string, f func(*collectorState)) {
	s.mu.Lock()
	f(s.cols[name])
	s.gen++
	s.quietFired, s.settled, s.runAfterQuiet = false, false, false
	s.mu.Unlock()
	select {
	case s.rearm <- struct{}{}:
	default:
	}
	s.signal()
}

// timers runs the quiet timer and the first-publish wait on the session clock.
func (s *Session) timers(ctx context.Context) {
	s.mu.Lock()
	armed := s.gen
	s.mu.Unlock()
	quiet := s.clk.NewTimer(s.o.Quiet)
	defer func() { quiet.Stop() }()
	disc := (<-chan struct{})(s.discovered)
	var wait clock.Timer
	var waitC <-chan time.Time
	defer func() {
		if wait != nil {
			wait.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.rearm:
			quiet.Stop()
			s.mu.Lock()
			armed = s.gen
			s.mu.Unlock()
			quiet = s.clk.NewTimer(s.o.Quiet)
		case <-quiet.C():
			s.mu.Lock()
			fired := s.gen == armed
			s.quietFired = s.quietFired || fired
			s.mu.Unlock()
			// A publish since arming has a re-arm pending, which restarts the timer.
			if fired {
				if testQuiet != nil {
					testQuiet()
				}
				s.changes.Notify()
				s.signal()
			}
		case <-disc:
			disc = nil
			wait = s.clk.NewTimer(s.o.FirstPublishWait)
			waitC = wait.C()
		case <-waitC:
			waitC = nil
			s.mu.Lock()
			s.waitOver = true
			s.mu.Unlock()
			s.signal()
		}
	}
}

// onLoop runs on the engine's Run goroutine, so it never blocks.
func (s *Session) onLoop(ev engine.LoopEvent) {
	if testLoop != nil {
		testLoop(ev)
	}
	switch ev.Kind {
	case engine.EvRunStarted:
		s.mu.Lock()
		s.runAfterQuiet, s.runGen = s.quietFired, s.gen
		s.mu.Unlock()
	case engine.EvPublished:
		p := ev.Published
		s.mu.Lock()
		// Runs are serial, so this publication is the run started last.
		if s.runAfterQuiet && s.quietFired && s.runGen == s.gen {
			s.settled = true
		}
		s.runAfterQuiet = false
		s.queue = append(s.queue, p)
		s.cond.Broadcast()
		if !s.discoveredClosed && p.Model.Snapshot != nil && p.Model.Snapshot.Synced && len(p.Model.Installs) > 0 {
			s.discoveredClosed = true
			close(s.discovered)
		}
		s.mu.Unlock()
		s.signal()
	}
}

func (s *Session) signal() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

// Current returns the latest published model, nil before the first.
func (s *Session) Current() *engine.Published { return s.eng.Current() }

// Models returns every published model in publication order. It is closed
// after Stop once drained; a reader must drain it. Models are queued from
// Start on, whether or not Models is called.
func (s *Session) Models() <-chan *engine.Published {
	s.forward.Do(func() { go s.send() })
	return s.out
}

// send moves queued models to out until the queue is empty and ended.
func (s *Session) send() {
	defer close(s.out)
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.ended {
			s.cond.Wait()
		}
		if len(s.queue) == 0 {
			s.mu.Unlock()
			return
		}
		p := s.queue[0]
		s.queue[0] = nil
		s.queue = s.queue[1:]
		s.mu.Unlock()
		s.out <- p
	}
}

// Changed returns a coalesced notification: a model was published or
// completion may have changed.
func (s *Session) Changed() <-chan struct{} { return s.changed }

// Complete reports whether the current model is complete (design §4.1 item
// 4): the cache is synced and every watch has settled; every collector has
// published, failed or returned, or the first-publish wait has passed, or the
// model has no install; and a model computed after the quiet period has been published.
func (s *Session) Complete() bool {
	p := s.Current()
	if p == nil || p.Model.Snapshot == nil || !p.Model.Snapshot.Synced {
		return false
	}
	for _, a := range p.Model.Snapshot.Access {
		if !a.OK && a.Err == "" {
			return false
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.settled {
		return false
	}
	if len(p.Model.Installs) == 0 || s.waitOver {
		return true
	}
	for _, st := range s.cols {
		if !st.reported && !st.returned {
			return false
		}
	}
	return true
}

// Inputs returns the access state of every watched input in the current
// model, plus one input per collector keyed by CollectorGVR.
func (s *Session) Inputs() map[schema.GroupVersionResource]load.Input {
	p := s.Current()
	var out map[schema.GroupVersionResource]load.Input
	if p != nil && p.Model.Snapshot != nil {
		out = load.Classify(p.Model.Snapshot.Access)
	} else {
		out = map[schema.GroupVersionResource]load.Input{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range s.names {
		out[CollectorGVR(name)] = collectorInput(name, s.cols[name], p)
	}
	return out
}

// collectorInput is a collector's state: failed on an error, a Failing key or
// its own checks reporting the input denied or failing; ok once it published,
// failed or returned; loading before.
func collectorInput(name string, st *collectorState, p *engine.Published) load.Input {
	in := load.Input{Resource: name, GVR: CollectorGVR(name), State: load.StateLoading}
	switch {
	case st.err != "":
		in.State, in.Reason = load.StateFailed, st.err
	case len(st.failing) > 0:
		keys := make([]string, 0, len(st.failing))
		for k := range st.failing {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		in.State, in.Reason = load.StateFailed, st.failing[keys[0]]
	case st.published:
		in.State = load.StateOK
		// A value can carry its own error, such as a scrape refused; its checks report it.
		if reason := checksFailing(st.checks, p); reason != "" {
			in.State, in.Reason = load.StateFailed, reason
		}
	case st.reported, st.returned:
		in.State = load.StateOK
	}
	return in
}

// checksFailing returns the error of the first of checks, over p's installs,
// whose input is denied or failing; "" when none is.
func checksFailing(checks []string, p *engine.Published) string {
	if p == nil || len(checks) == 0 {
		return ""
	}
	for _, im := range p.Model.Installs {
		for _, r := range im.Health.Checks {
			if slices.Contains(checks, r.Name) && (r.Unavailable == health.AvailDenied || r.Unavailable == health.AvailFailing) {
				return r.Error
			}
		}
	}
	return ""
}

// Stop cancels the session and returns once the cache, the collectors and
// the engine loop have exited. A compute in flight finishes on its own and is
// dropped. Stop is idempotent.
func (s *Session) Stop() {
	s.cancel()
	<-s.done
}
