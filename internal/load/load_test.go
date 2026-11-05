package load_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DuncanDoyle/kgateway-ui/pkg/cache"
	"github.com/DuncanDoyle/kgateway-ui/pkg/extension"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
)

const healthy = "../testenv/testdata/healthy.yaml"

var httpRoutesGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}

// runLoad loads env's first context under a deadline of d, with clients bound to it.
func runLoad(t *testing.T, env *testenv.Env, d time.Duration, needs load.Needs) (*load.Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), d)
	defer cancel()
	c, err := load.NewClients(ctx, restConfig(t, env))
	if err != nil {
		t.Fatal(err)
	}
	return load.Load(ctx, c, extension.New(), needs)
}

// mustLoad is runLoad that fails the test on an error or a nil Model.
func mustLoad(t *testing.T, env *testenv.Env, d time.Duration, needs load.Needs) *load.Result {
	t.Helper()
	r, err := runLoad(t, env, d, needs)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r == nil || r.Model == nil {
		t.Fatal("Load returned no Model")
	}
	return r
}

func input(t *testing.T, r *load.Result, gvr schema.GroupVersionResource) load.Input {
	t.Helper()
	in, ok := r.Inputs[gvr]
	if !ok {
		t.Fatalf("Inputs has no %v; have %v", gvr, r.Inputs)
	}
	return in
}

func TestLoadHealthy(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthy}})
	r := mustLoad(t, env, 10*time.Second, load.Needs{Requested: []schema.GroupVersionResource{httpRoutesGVR}, ProxyPods: true, CRDs: true})

	if n := len(r.Model.Installs); n != 1 {
		t.Fatalf("got %d installs, want 1", n)
	}
	// The fixture's controller is labelled 2.5.0 (Ruling R6).
	if v := r.Model.Installs[0].Resolved.Version; v != "2.5.0" {
		t.Fatalf("install version = %q, want 2.5.0", v)
	}
	for _, gvr := range []schema.GroupVersionResource{httpRoutesGVR, cache.PodsGVR, cache.ProxyPodsGVR, cache.CRDsGVR} {
		input(t, r, gvr)
	}
	for gvr, in := range r.Inputs {
		if in.State != load.StateOK {
			t.Errorf("input %v = %+v, want ok", gvr, in)
		}
	}
	if u := r.Unavailable(); len(u) != 0 {
		t.Errorf("Unavailable() = %v, want none", u)
	}
	if len(r.Served) == 0 {
		t.Error("Served is empty")
	}
}

func TestLoadDeniedKind(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthy}})
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "pods"}, testenv.Action{Status: http.StatusForbidden})
	r := mustLoad(t, env, 10*time.Second, load.Needs{})

	if in := input(t, r, cache.PodsGVR); in.State != load.StateDenied {
		t.Fatalf("pods = %+v, want denied", in)
	}
	u := r.Unavailable()
	if !slices.ContainsFunc(u, func(in load.Input) bool { return in.Resource == "pods" }) {
		t.Fatalf("Unavailable() = %v, want pods listed", u)
	}
}

func TestLoadFailedKind(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthy}})
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "httproutes.gateway.networking.k8s.io"}, testenv.Action{Status: http.StatusInternalServerError})
	r := mustLoad(t, env, 10*time.Second, load.Needs{Requested: []schema.GroupVersionResource{httpRoutesGVR}})

	in := input(t, r, httpRoutesGVR)
	if in.State != load.StateFailed || in.Reason == "" {
		t.Fatalf("httproutes = %+v, want failed with a reason", in)
	}
	if in.Resource != "httproutes.gateway.networking.k8s.io" {
		t.Fatalf("Resource = %q", in.Resource)
	}
}

func TestLoadWatchDenied(t *testing.T) {
	t.Run("classic watch", func(t *testing.T) {
		testenv.ClassicWatch(t)
		env := testenv.New(t, testenv.Options{Fixtures: []string{healthy}})
		env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindWatch, Resource: "httproutes.gateway.networking.k8s.io"}, testenv.Action{Status: http.StatusForbidden})
		r := mustLoad(t, env, 10*time.Second, load.Needs{Requested: []schema.GroupVersionResource{httpRoutesGVR}})
		// The LIST may win the race with the 403 (design §6.3): ok is allowed only with complete data.
		switch in := input(t, r, httpRoutesGVR); in.State {
		case load.StateDenied:
		case load.StateOK:
			objs := r.Model.Snapshot.Objects[httpRoutesGVR]
			if len(objs) != 1 || objs[0].GetNamespace() != "default" || objs[0].GetName() != "example" {
				t.Fatalf("httproutes ok with %d objects, want the fixture's default/example", len(objs))
			}
		default:
			t.Fatalf("httproutes = %+v, want denied, or ok with complete data", in)
		}
	})
	t.Run("default gates", func(t *testing.T) {
		env := testenv.New(t, testenv.Options{Fixtures: []string{healthy}})
		env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "httproutes.gateway.networking.k8s.io"}, testenv.Action{Status: http.StatusForbidden})
		r := mustLoad(t, env, 10*time.Second, load.Needs{Requested: []schema.GroupVersionResource{httpRoutesGVR}})
		if in := input(t, r, httpRoutesGVR); in.State == load.StateOK {
			t.Fatalf("httproutes = %+v, want not ok", in)
		}
	})
}

func TestLoadWaitsForProxyPods(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthy}})
	sel := testenv.ProxyPodSelector(extension.New())
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "pods", LabelSelector: sel}, testenv.Action{Delay: 300 * time.Millisecond})
	var settledAt time.Duration
	start := time.Now()
	t.Cleanup(load.SetCoreSettledHook(func() { settledAt = time.Since(start) }))

	r := mustLoad(t, env, 10*time.Second, load.Needs{ProxyPods: true})
	took := time.Since(start)
	if in := input(t, r, cache.ProxyPodsGVR); in.State != load.StateOK {
		t.Fatalf("proxy pods = %+v, want ok", in)
	}
	if settledAt == 0 || settledAt >= 300*time.Millisecond {
		t.Fatalf("core inputs settled at %v, want < 300ms", settledAt)
	}
	if took < 300*time.Millisecond {
		t.Fatalf("Load returned after %v, before the 300ms proxy Pod delay", took)
	}
}

func TestLoadOptionalPastDeadline(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthy}})
	sel := testenv.ProxyPodSelector(extension.New())
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "pods", LabelSelector: sel}, testenv.Action{Delay: 2 * time.Second})
	r := mustLoad(t, env, 500*time.Millisecond, load.Needs{ProxyPods: true})

	if in := input(t, r, cache.ProxyPodsGVR); in.State != load.StateLoading {
		t.Fatalf("proxy pods = %+v, want loading", in)
	}
	if in := input(t, r, httpRoutesGVR); in.State != load.StateOK {
		t.Fatalf("httproutes = %+v, want ok", in)
	}
}

// TestLoadDeadlineErrorIsLoading lets the cancelled request record its error
// before the final snapshot: the input is still loading, not failed.
func TestLoadDeadlineErrorIsLoading(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthy}})
	sel := testenv.ProxyPodSelector(extension.New())
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "pods", LabelSelector: sel}, testenv.Action{Delay: 2 * time.Second})
	t.Cleanup(load.SetDeadlineHook(func(cache.Snapshotter) { time.Sleep(200 * time.Millisecond) }))
	r := mustLoad(t, env, 500*time.Millisecond, load.Needs{ProxyPods: true})

	if in := input(t, r, cache.ProxyPodsGVR); in.State != load.StateLoading {
		t.Fatalf("proxy pods = %+v, want loading", in)
	}
}

// TestLoadPostDeadlineFailure: a Pod watch cancelled by the deadline leaves Pods
// ok in Inputs, and the engine reads the same access state.
func TestLoadPostDeadlineFailure(t *testing.T) {
	testenv.ClassicWatch(t)
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthy}})
	sel := testenv.ProxyPodSelector(extension.New())
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "pods", LabelSelector: sel}, testenv.Action{Delay: time.Hour})
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindWatch, Resource: "pods"}, testenv.Action{Delay: time.Hour})
	t.Cleanup(load.SetDeadlineHook(func(s cache.Snapshotter) {
		// Every later Pod request runs under the ended context, so the error stays recorded.
		for end := time.Now().Add(5 * time.Second); s.Snapshot().Access[cache.PodsGVR].Err == ""; time.Sleep(10 * time.Millisecond) {
			if time.Now().After(end) {
				t.Error("the Pod watch failure was never recorded")
				return
			}
		}
	}))
	r := mustLoad(t, env, time.Second, load.Needs{ProxyPods: true})

	if in := input(t, r, cache.PodsGVR); in.State != load.StateOK {
		t.Fatalf("pods = %+v, want ok (settled before the deadline)", in)
	}
	for gvr, in := range r.Inputs {
		if a := r.Model.Snapshot.Access[gvr]; a.OK != (in.State == load.StateOK) {
			t.Errorf("%v: engine access %+v, output state %s", gvr, a, in.State)
		}
	}
	if len(r.Model.Installs) != 1 || r.Model.Installs[0].Process == nil || !r.Model.Installs[0].Process.Controller.Observed {
		t.Error("controller Pods not observed by the engine, though pods is ok")
	}
}

func TestLoadRequestedTimeout(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthy}})
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "httproutes.gateway.networking.k8s.io"}, testenv.Action{Delay: 3 * time.Second})
	_, err := runLoad(t, env, time.Second, load.Needs{Requested: []schema.GroupVersionResource{httpRoutesGVR}})

	var te *load.TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("err = %v, want *TimeoutError", err)
	}
	if te.Stage != "loading" || !slices.Equal(te.Pending, []string{"httproutes.gateway.networking.k8s.io"}) {
		t.Fatalf("TimeoutError = %+v, want loading with httproutes pending", te)
	}
	if !strings.Contains(err.Error(), "httproutes.gateway.networking.k8s.io") {
		t.Fatalf("error %q does not name the pending kind", err)
	}
}

func TestLoadUnrequestedTimeout(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthy}})
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "httproutes.gateway.networking.k8s.io"}, testenv.Action{Delay: 3 * time.Second})
	r := mustLoad(t, env, time.Second, load.Needs{})

	if in := input(t, r, httpRoutesGVR); in.State != load.StateLoading {
		t.Fatalf("httproutes = %+v, want loading", in)
	}
}

func TestLoadSlowDiscovery(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthy}})
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindDiscovery, Resource: "*"}, testenv.Action{Delay: 400 * time.Millisecond})
	start := time.Now()
	_, err := runLoad(t, env, time.Second, load.Needs{})
	assertDiscoveryTimeout(t, err, time.Since(start))
}

func TestLoadDiscoveryRetryAfter(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthy}})
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindDiscovery, Resource: "*"}, testenv.Action{Status: http.StatusTooManyRequests, RetryAfter: 10})
	start := time.Now()
	_, err := runLoad(t, env, time.Second, load.Needs{})
	assertDiscoveryTimeout(t, err, time.Since(start))
}

func assertDiscoveryTimeout(t *testing.T, err error, took time.Duration) {
	t.Helper()
	var te *load.TimeoutError
	if !errors.As(err, &te) || te.Stage != "discovery" {
		t.Fatalf("err = %v, want *TimeoutError at discovery", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if took >= 1500*time.Millisecond {
		t.Fatalf("Load returned after %v, want < 1.5s", took)
	}
}

// TestLoadRequestedNotLoaded requests a version the cache does not watch:
// Load must fail naming it, not succeed without the input.
func TestLoadRequestedNotLoaded(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthy}})
	beta := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1beta1", Resource: "httproutes"}
	r, err := runLoad(t, env, 10*time.Second, load.Needs{Requested: []schema.GroupVersionResource{httpRoutesGVR, beta}})

	if err == nil || r != nil {
		t.Fatalf("Load = %v, %v; want an error and no result", r, err)
	}
	if !strings.Contains(err.Error(), "httproutes.v1beta1.gateway.networking.k8s.io") {
		t.Fatalf("error %q does not name the missing version", err)
	}
	var te *load.TimeoutError
	if errors.As(err, &te) {
		t.Fatalf("err = %v, want a non-timeout error", err)
	}
}
