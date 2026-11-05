package load

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/DuncanDoyle/kgateway-ui/pkg/cache"
	"github.com/DuncanDoyle/kgateway-ui/pkg/engine"
	"github.com/DuncanDoyle/kgateway-ui/pkg/extension"
	"github.com/DuncanDoyle/kgateway-ui/pkg/health"
	"github.com/DuncanDoyle/kgateway-ui/pkg/kinds"
	"github.com/DuncanDoyle/kgateway-ui/pkg/setup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Needs are the inputs a command waits for beyond the core inputs (design §4.1).
type Needs struct {
	// Requested are the kinds the command prints; one still pending at the
	// deadline fails the load with a TimeoutError.
	Requested []schema.GroupVersionResource
	// ProxyPods and CRDs start those optional watches and wait for them.
	ProxyPods, CRDs bool
	// Logger receives the engine's logs; nil discards them.
	Logger *slog.Logger
}

// State is an input's state after the readiness wait (design §6.3).
type State string

// Input states.
const (
	StateOK      State = "ok"
	StateLoading State = "loading"
	StateDenied  State = "denied"
	StateFailed  State = "failed"
)

// Input is one cache input and its state.
type Input struct {
	// Resource names the input, as "httproutes.gateway.networking.k8s.io",
	// "pods" or "proxy pods".
	Resource string                      `json:"input"`
	GVR      schema.GroupVersionResource `json:"-"`
	State    State                       `json:"state"`
	// Reason is the access error of a denied or failed input.
	Reason string `json:"reason,omitempty"`
}

// Result is one load: the model computed once, the served kinds and every input's state.
type Result struct {
	Model  *engine.Model
	Served []kinds.Served
	Inputs map[schema.GroupVersionResource]Input
}

// Unavailable returns the inputs that are not ok, sorted by Resource.
func (r *Result) Unavailable() []Input {
	var out []Input
	for _, in := range r.Inputs {
		if in.State != StateOK {
			out = append(out, in)
		}
	}
	slices.SortFunc(out, func(a, b Input) int { return cmp.Compare(a.Resource, b.Resource) })
	return out
}

// Timeout stages.
const (
	StageDiscovery = "discovery"
	StageLoading   = "loading"
)

// TimeoutError reports that the command context ended during discovery, or
// while requested kinds were still loading.
type TimeoutError struct {
	// Stage is StageDiscovery or StageLoading.
	Stage string
	// Pending are the requested kinds not ready at the deadline, sorted (loading only).
	Pending []string
	cause   error
}

// Error implements error.
func (e *TimeoutError) Error() string {
	if e.Stage == StageLoading {
		return "timed out loading " + strings.Join(e.Pending, ", ")
	}
	return "timed out during " + e.Stage
}

// Unwrap returns the context's cause, such as context.DeadlineExceeded.
func (e *TimeoutError) Unwrap() error { return e.cause }

// pollEvery is how often Load checks readiness.
const pollEvery = 10 * time.Millisecond

// onCoreSettled and onDeadline, if set, run when the core inputs settle and
// when the wait sees ctx end, before the final snapshot (tests).
var (
	onCoreSettled func()
	onDeadline    func(cache.Snapshotter)
)

// Load resolves the served kinds, runs the cache until the core inputs and
// the needed optional inputs settle or ctx ends, then computes the model once.
// The caller owns the deadline on ctx; Load cancels the cache before returning.
func Load(ctx context.Context, c Clients, reg *extension.Registry, needs Needs) (*Result, error) {
	if err := reg.Err(); err != nil {
		return nil, err
	}
	if err := health.ValidateOrder(reg.Checks); err != nil {
		return nil, err
	}
	dc := &memoDiscovery{dc: Discovery(ctx, c.Discovery), seen: map[string]answer{}}
	if _, err := kinds.Resolve(dc, reg.Kinds); err != nil {
		// Classified from ctx: request errors can lose the deadline cause mid-stream.
		if ctx.Err() != nil {
			return nil, &TimeoutError{Stage: StageDiscovery, cause: context.Cause(ctx)}
		}
		return nil, fmt.Errorf("resolve served kinds: %w", err)
	}
	// Build re-resolves from dc's memo, so it sends no request and lists no Catalog.
	cfg, err := setup.Build(setup.Clients{Dynamic: c.Dynamic, Kube: c.Kube, Discovery: dc, Metadata: c.Metadata},
		setup.Options{MetricsInterval: metricsInterval, NoEvents: true, NoCRDs: !needs.CRDs}, reg)
	if err != nil {
		return nil, err
	}
	if !needs.ProxyPods {
		cfg.Cache.ProxyPodSelectors = nil
	}

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ca := cache.New(cfg.Cache)
	ca.Start(cctx)
	snap, pending := wait(ctx, ca, optional(needs))
	cancel()

	inputs := classify(snap.Access, pending)
	var late []string
	for _, gvr := range needs.Requested {
		in, ok := inputs[gvr]
		// A requested kind the cache never watched fails the load (Ruling R12).
		if !ok {
			return nil, fmt.Errorf("requested %s is not among the loaded kinds", qualifiedName(gvr))
		}
		if in.State == StateLoading {
			late = append(late, in.Resource)
		}
	}
	if len(late) > 0 {
		slices.Sort(late)
		return nil, &TimeoutError{Stage: StageLoading, Pending: late, cause: context.Cause(ctx)}
	}
	// Compute does no I/O here, so it runs on what loaded even past the deadline (§4.4).
	cfg.Engine.Snapshots = fixedSnapshot{snap}
	cfg.Engine.Logger = needs.Logger
	if cfg.Engine.Logger == nil {
		cfg.Engine.Logger = slog.New(slog.DiscardHandler)
	}
	m := engine.New(cfg.Engine).Compute()
	return &Result{Model: m, Served: cfg.Engine.Served, Inputs: inputs}, nil
}

// metricsInterval only satisfies setup.Build: no metrics are collected, so the
// staleness and scope-verifier windows it sets are never reached.
const metricsInterval = time.Minute

// memoDiscovery serves each group version's first answer, NotFound included,
// again, so setup.Build repeats Load's discovery without requests. It lists no Catalog.
type memoDiscovery struct {
	dc   kinds.DiscoveryClient
	seen map[string]answer
}

type answer struct {
	l   *metav1.APIResourceList
	err error
}

// ServerResourcesForGroupVersion implements kinds.DiscoveryClient.
func (m *memoDiscovery) ServerResourcesForGroupVersion(gv string) (*metav1.APIResourceList, error) {
	a, ok := m.seen[gv]
	if !ok {
		a.l, a.err = m.dc.ServerResourcesForGroupVersion(gv)
		m.seen[gv] = a
	}
	return a.l, a.err
}

// fixedSnapshot hands Compute the snapshot Load classified its inputs from.
type fixedSnapshot struct{ s *cache.Snapshot }

// Snapshot implements cache.Snapshotter.
func (f fixedSnapshot) Snapshot() *cache.Snapshot { return f.s }

// optional returns the Access keys of the optional inputs needs waits for.
func optional(needs Needs) []schema.GroupVersionResource {
	var out []schema.GroupVersionResource
	if needs.ProxyPods {
		out = append(out, cache.ProxyPodsGVR)
	}
	if needs.CRDs {
		out = append(out, cache.CRDsGVR)
	}
	return out
}

// settled reports whether an input has a final access state.
func settled(a cache.Access) bool { return a.OK || a.Err != "" }

// wait polls ca until the core inputs and opt have settled, or ctx ends, and
// returns the snapshot to compute on and classify. When ctx ended, inputs
// settled in the last snapshot taken before it keep that state; the others
// are pending and read from a fresh snapshot.
func wait(ctx context.Context, ca *cache.Cache, opt []schema.GroupVersionResource) (*cache.Snapshot, map[schema.GroupVersionResource]bool) {
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	var last *cache.Snapshot
	core := false
	for {
		s := ca.Snapshot()
		// A snapshot taken before ctx ended holds no errors caused by its end.
		if ctx.Err() != nil {
			break
		}
		last = s
		// Synced is computed in the same pass as s.Access, so the two agree.
		if s.Synced && !core {
			core = true
			if onCoreSettled != nil {
				onCoreSettled()
			}
		}
		if s.Synced && !slices.ContainsFunc(opt, func(g schema.GroupVersionResource) bool {
			a, running := s.Access[g]
			return running && !settled(a)
		}) {
			return s, nil
		}
		select {
		case <-ctx.Done():
		case <-t.C:
		}
	}
	if onDeadline != nil {
		onDeadline(ca)
	}
	final := ca.Snapshot()
	acc := make(map[schema.GroupVersionResource]cache.Access, len(final.Access))
	pending := map[schema.GroupVersionResource]bool{}
	for gvr, a := range final.Access {
		if last != nil && settled(last.Access[gvr]) {
			// A watch re-opened after ctx ended can record a cancellation error on it.
			acc[gvr] = last.Access[gvr]
			continue
		}
		acc[gvr], pending[gvr] = a, true
	}
	// The engine reads the access states the output reports. No input holds a partial list:
	// client-go's AtomicFIFO gate (default-on beta in v0.36) applies each LIST as one replace.
	snap := *final
	snap.Access = acc
	return &snap, pending
}

// classify maps each Access entry to an Input; pending inputs that are not
// OK are loading, whatever error the deadline left on them.
func classify(acc map[schema.GroupVersionResource]cache.Access, pending map[schema.GroupVersionResource]bool) map[schema.GroupVersionResource]Input {
	out := make(map[schema.GroupVersionResource]Input, len(acc))
	for gvr, a := range acc {
		in := Input{Resource: resourceName(gvr), GVR: gvr}
		switch {
		case a.OK:
			in.State = StateOK
		case pending[gvr] || a.Err == "":
			in.State = StateLoading
		case health.AccessAvailability(a) == health.AvailDenied:
			in.State, in.Reason = StateDenied, a.Err
		default:
			in.State, in.Reason = StateFailed, a.Err
		}
		out[gvr] = in
	}
	return out
}

// resourceName is "<resource>.<group>", the resource alone for the core
// group, and "proxy pods" for the proxy Pod watch.
func resourceName(gvr schema.GroupVersionResource) string {
	if gvr == cache.ProxyPodsGVR {
		return "proxy pods"
	}
	if gvr.Group == "" {
		return gvr.Resource
	}
	return gvr.Resource + "." + gvr.Group
}

// qualifiedName is kubectl's "<resource>.<version>.<group>", or "<resource>.<version>" for the core group.
func qualifiedName(gvr schema.GroupVersionResource) string {
	if gvr.Group == "" {
		return gvr.Resource + "." + gvr.Version
	}
	return gvr.Resource + "." + gvr.Version + "." + gvr.Group
}
