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
	"sync"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/cache"
	"github.com/DuncanDoyle/snellius/pkg/collect"
	"github.com/DuncanDoyle/snellius/pkg/ctrlmetrics"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/extension"
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
	// collector that has not published is treated as having nothing to report;
	// after it, such a silent collector is left out of Inputs.
	FirstPublishWait time.Duration
	// Clock drives the engine, the collectors and the session's timers; nil is the real clock.
	Clock clock.WithTicker
	// Scraper replaces the controller metrics scraper; nil uses pods/proxy.
	Scraper ctrlmetrics.Scraper
	// Logger receives the engine's and collectors' logs; nil discards them.
	Logger *slog.Logger
	// Published, if set, is called on the engine goroutine with each published model; it must not block (tests).
	Published func(*engine.Published)
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

// Test hooks, if set: each loop event, each quiet expiry, each quiet re-arm
// and the first-publish wait's expiry, after the session's state is updated.
var (
	testLoop  func(engine.LoopEvent)
	testQuiet func()
	testArmed func()
	testWait  func()
	// testStored runs between a collector's store write and its record (tests).
	testStored func(collector string)
)

// CollectorGVR is the Inputs key of the collector named name; it never
// collides with a served kind.
func CollectorGVR(name string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Resource: "collector#" + name}
}

// collectorState is what one collector has done so far.
type collectorState struct {
	published bool
	reported  bool               // published or failed at least once
	failing   map[string]failure // key -> its latest Fail, until it publishes again
	terminal  bool               // has failed or returned at least once
	returned  bool
	err       string // Start's error or panic
}

// failure is one Fail's state and reason.
type failure struct {
	state  collect.State
	reason string
}

// effect is what a collector event does to completion.
type effect int

const (
	// noEffect: nothing the engine reads changed.
	noEffect effect = iota
	// resettle: a model computed after this event is needed.
	resettle
	// requiet: as resettle, and the quiet period restarts.
	requiet
)

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
	stopped    chan struct{} // closed when no more models can come
	forwarded  chan struct{} // closed when the forwarder has exited
	rearm      chan struct{}
	discovered chan struct{}
	changed    chan struct{}
	out        chan *engine.Published

	mu   sync.Mutex
	cond *sync.Cond // signals queue growth and ended
	cols map[string]*collectorState
	// queue holds published models not yet sent on out; ended is set once no more can come.
	queue []*engine.Published
	ended bool
	// discoveredClosed and waitOver track the first-publish wait.
	discoveredClosed, waitOver bool
	// gen counts quiet-restarting events; epoch counts every event that needs a
	// new model. A run settles only if quiet has fired and epoch is unchanged.
	gen, epoch          uint64
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
		done: make(chan struct{}), stopped: make(chan struct{}), forwarded: make(chan struct{}), rearm: make(chan struct{}, 1), discovered: make(chan struct{}),
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
		s.cols[name] = &collectorState{failing: map[string]failure{}}
	}

	s.ca.Start(sctx)
	for _, col := range cols {
		s.runCollector(sctx, col, cfg.Collected)
	}
	s.wg.Go(func() { s.timers(sctx) })
	s.wg.Go(func() { s.eng.Run(sctx, o.MetricsInterval) })
	go s.send()
	go func() {
		<-sctx.Done()
		s.ca.Wait()
		s.wg.Wait()
		s.mu.Lock()
		s.ended = true
		s.cond.Broadcast()
		s.mu.Unlock()
		close(s.stopped)
		<-s.forwarded
		close(s.done)
	}()
	return s, nil
}

// runCollector runs col in a goroutine Stop joins, its Publish and Fail
// writing "<name>/<key>" to store as Registry.RunCollectors does.
func (s *Session) runCollector(ctx context.Context, col extension.Collector, store *collect.Store) {
	name := col.Name()
	log := s.o.Logger.With("collector", name)
	d := extension.Deps{Current: s.eng.Current, Logger: log,
		Publish: func(key string, v any, fp uint64, ttl time.Duration) {
			store.Publish(name+"/"+key, v, fp, ttl)
			s.record(name, func(st *collectorState) effect {
				st.published, st.reported = true, true
				delete(st.failing, key)
				return requiet
			})
		},
		Fail: func(key string, state collect.State, reason string) {
			store.Fail(name+"/"+key, state, reason)
			if testStored != nil {
				testStored(name)
			}
			s.record(name, func(st *collectorState) effect {
				f := failure{state, reason}
				if prev, ok := st.failing[key]; ok && prev == f {
					return noEffect // the store ignores it too
				}
				st.reported, st.failing[key] = true, f
				return st.firstTerminal()
			})
		},
	}
	s.wg.Go(func() {
		var errText string
		defer func() {
			store.Stop(name)
			s.record(name, func(st *collectorState) effect {
				st.returned, st.err = true, errText
				return st.firstTerminal()
			})
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

// firstTerminal restarts the quiet period on a collector's first Fail or
// return; a later one only needs a new model.
func (st *collectorState) firstTerminal() effect {
	if st.terminal {
		return resettle
	}
	st.terminal = true
	return requiet
}

// record applies f to collector name's state and then its effect: publishes
// and first terminal outcomes restart the quiet period, every change voids the
// settled model. The store notified the engine before record ran, so a
// compute may already have consumed that notify; resettle asks for another.
func (s *Session) record(name string, f func(*collectorState) effect) {
	s.mu.Lock()
	e := f(s.cols[name])
	if e != noEffect {
		s.epoch++
		s.settled, s.runAfterQuiet = false, false
	}
	if e == requiet {
		s.gen++
		s.quietFired = false
	}
	s.mu.Unlock()
	switch e {
	case requiet:
		select {
		case s.rearm <- struct{}{}:
		default:
		}
	case resettle:
		s.changes.Notify()
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
			if testArmed != nil {
				testArmed()
			}
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
			// Collectors that have not reported are now treated as having nothing to
			// report; a model computed after this boundary is needed.
			waitC = nil
			s.mu.Lock()
			s.waitOver = true
			s.epoch++
			s.settled, s.runAfterQuiet = false, false
			s.mu.Unlock()
			if testWait != nil {
				testWait()
			}
			s.changes.Notify()
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
		s.runAfterQuiet, s.runGen = s.quietFired, s.epoch
		s.mu.Unlock()
	case engine.EvPublished:
		p := ev.Published
		s.mu.Lock()
		// Runs are serial, so this publication is the run started last.
		if s.runAfterQuiet && s.quietFired && s.runGen == s.epoch {
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
		if s.o.Published != nil {
			s.o.Published(p)
		}
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

// Models returns every published model in publication order. Stop closes it
// without waiting for a reader; a consumer that needs every model calls Drain
// after Stop for the ones Models did not deliver.
func (s *Session) Models() <-chan *engine.Published { return s.out }

// Drain waits for Stop to finish and returns, in order, every published model
// Models did not deliver; empty when it delivered all. Models are kept for
// Drain until it is called, whether or not Models is read.
func (s *Session) Drain() []*engine.Published {
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.queue
	s.queue = nil
	return out
}

// send moves queued models to out until the session ends. A model is removed
// from the queue only once delivered, so Drain returns the rest.
func (s *Session) send() {
	defer close(s.forwarded)
	defer close(s.out)
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.ended {
			s.cond.Wait()
		}
		if s.ended {
			s.mu.Unlock()
			return
		}
		p := s.queue[0]
		s.mu.Unlock()
		select {
		case s.out <- p:
		case <-s.stopped:
			return
		}
		s.mu.Lock()
		s.queue[0] = nil
		s.queue = s.queue[1:]
		s.mu.Unlock()
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
// model, plus one input per collector keyed by CollectorGVR. Once the
// first-publish wait has passed, a collector that has not published, failed or
// returned is silent and left out; the checks that read it report their own state.
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
		in := collectorInput(name, s.cols[name])
		if in.State == load.StateLoading && s.waitOver {
			continue
		}
		out[CollectorGVR(name)] = in
	}
	return out
}

// collectorInput is a collector's state from what its runner saw: failed after
// a Fail or an error return, ok after a publish or a nil return, loading before.
// A value that carries its own error, such as a refused scrape, reads ok here;
// its checks report it.
func collectorInput(name string, st *collectorState) load.Input {
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
		in.State, in.Reason = load.StateFailed, st.failing[keys[0]].reason
	case st.published, st.returned:
		in.State = load.StateOK
	}
	return in
}

// Stop cancels the session and returns once the cache, the collectors and
// the engine loop have exited and Models is closed. A compute in flight finishes on its own and is
// dropped. Stop is idempotent.
func (s *Session) Stop() {
	s.cancel()
	<-s.done
}
