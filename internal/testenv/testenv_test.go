package testenv_test

import (
	"context"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/cache"
	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
)

const healthy = "testdata/healthy.yaml"

var (
	httpRoutesGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	crdsGVR       = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
)

func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }

// clients builds the production clients from the Env's kubeconfig.
func clients(t *testing.T, env *testenv.Env) load.Clients {
	t.Helper()
	cfg, err := clientcmd.BuildConfigFromFlags("", env.Kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	c, err := load.NewClients(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// startCache starts the library cache the way kgateway-ui's app wires it.
func startCache(t *testing.T, env *testenv.Env) *cache.Cache {
	t.Helper()
	c := clients(t, env)
	reg := extension.New()
	served, err := kinds.Resolve(load.Discovery(t.Context(), c.Discovery), reg.Kinds)
	if err != nil {
		t.Fatal(err)
	}
	var podSel, depSel, proxySel []labels.Selector
	for _, d := range reg.Detectors {
		podSel = append(podSel, d.ControllerPodSelector())
		depSel = append(depSel, d.ControllerSelector(), d.ProxySelector())
		if pd, ok := d.(install.ProxyPodDetector); ok {
			proxySel = append(proxySel, pd.ProxyPodSelector())
		}
	}
	ca := cache.New(cache.Config{Dynamic: c.Dynamic, Kube: c.Kube, Served: served, PodSelectors: podSel,
		DeploymentSelectors: depSel, ProxyPodSelectors: proxySel})
	ca.Start(t.Context())
	return ca
}

func TestEnvServesFixture(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthy}})
	c := clients(t, env)

	l, err := load.Discovery(t.Context(), c.Discovery).ServerResourcesForGroupVersion("gateway.networking.k8s.io/v1")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(l.APIResources, func(r metav1.APIResource) bool { return r.Name == "httproutes" }) {
		t.Fatalf("discovery of gateway.networking.k8s.io/v1 lacks httproutes: %v", l.APIResources)
	}
	routes, err := c.Dynamic.Resource(httpRoutesGVR).List(t.Context(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(routes.Items); n != 1 {
		t.Fatalf("got %d HTTPRoutes, want 1", n)
	}
	crd, err := c.Metadata.Resource(crdsGVR).Get(t.Context(), "httproutes.gateway.networking.k8s.io", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if v := crd.Annotations["gateway.networking.k8s.io/bundle-version"]; v != "v1.7.0" {
		t.Fatalf("bundle-version = %q, want v1.7.0", v)
	}
}

func TestProxyDelaysOnlyMatchingSelector(t *testing.T) {
	env := testenv.New(t, testenv.Options{})
	sel := testenv.ProxyPodSelector(extension.New())
	if sel == "" {
		t.Fatal("ProxyPodSelector returned no selector for the upstream registry")
	}
	// Proxy Pods are held until controller Pods settle, so only the order is checked.
	release := make(chan struct{})
	unhold := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unhold)
	hits := env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "pods", LabelSelector: sel}, testenv.Action{Until: release})
	ca := startCache(t, env)

	eventually(t, "controller Pods to settle", func() bool { return ca.Snapshot().Access[cache.PodsGVR].OK })
	if ca.Snapshot().Access[cache.ProxyPodsGVR].OK {
		t.Fatal("proxy Pods settled while their request was held")
	}
	unhold()
	eventually(t, "proxy Pods to settle", func() bool { return ca.Snapshot().Access[cache.ProxyPodsGVR].OK })
	if hits.N() == 0 {
		t.Fatal("hold rule never matched")
	}
}

// TestProxySelectorMatch checks that a rule with a label selector holds only
// requests carrying that selector.
func TestProxySelectorMatch(t *testing.T) {
	env := testenv.New(t, testenv.Options{})
	sel := "app.kubernetes.io/component=proxy,app.kubernetes.io/managed-by=kgateway"
	release := make(chan struct{})
	unhold := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unhold)
	hits := env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "pods", LabelSelector: sel}, testenv.Action{Until: release})
	c := clients(t, env)
	list := func(ctx context.Context, selector string) error {
		_, err := c.Kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: selector})
		return err
	}

	// The bound only stops a wrongly held request from hanging the test.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := list(ctx, "kgateway=kgateway"); err != nil || hits.N() != 0 {
		t.Fatalf("unmatched selector: err %v, hits %d; want it answered, not held", err, hits.N())
	}
	// Same selector in another order: the proxy compares parsed selectors.
	done := make(chan error, 1)
	go func() {
		done <- list(t.Context(), "app.kubernetes.io/managed-by=kgateway,app.kubernetes.io/component=proxy")
	}()
	eventually(t, "the matched request to be held", func() bool { return hits.N() == 1 })
	select {
	case err := <-done:
		t.Fatalf("matched selector returned while held: %v", err)
	default:
	}
	unhold()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// eventually fails the test unless cond holds within 10s; the bound is a hang guard, not a timing check.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestProxyMatchesWatchList(t *testing.T) {
	for _, tc := range []struct {
		name      string
		classic   bool
		watchList bool // whether the acquisition must be a watch-list request
	}{
		{name: "default gates", watchList: true},
		{name: "classic watch", classic: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.classic {
				testenv.ClassicWatch(t)
			}
			env := testenv.New(t, testenv.Options{})
			p := env.Proxies[0]
			hits := p.Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "httproutes.gateway.networking.k8s.io"}, testenv.Action{Status: http.StatusForbidden})
			ca := startCache(t, env)

			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			var acc cache.Access
			for acc.Err == "" && ctx.Err() == nil {
				time.Sleep(20 * time.Millisecond)
				acc = ca.Snapshot().Access[httpRoutesGVR]
			}
			if acc.OK || !acc.Denied {
				t.Fatalf("httproutes access = %+v, want denied", acc)
			}
			if hits.N() == 0 {
				t.Fatal("403 rule never matched")
			}
			sawWatchList := slices.ContainsFunc(p.Requests(), func(r string) bool {
				return strings.Contains(r, "/httproutes?") && strings.Contains(r, "sendInitialEvents=true")
			})
			if sawWatchList != tc.watchList {
				t.Fatalf("watch-list request seen = %v, want %v; requests: %v", sawWatchList, tc.watchList, p.Requests())
			}
		})
	}
}

func TestCleanupRemovesObjects(t *testing.T) {
	var admin *testenv.Env
	t.Run("apply", func(t *testing.T) {
		admin = testenv.New(t, testenv.Options{Fixtures: []string{healthy}})
	})
	dyn := dynamic.NewForConfigOrDie(admin.Admin)
	for _, gvr := range []schema.GroupVersionResource{
		{Version: "v1", Resource: "pods"},
		{Group: "apps", Version: "v1", Resource: "deployments"},
		{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gatewayclasses"},
		{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"},
		httpRoutesGVR,
	} {
		l, err := dyn.Resource(gvr).List(context.Background(), metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if n := len(l.Items); n != 0 {
			t.Errorf("%s: %d objects left after cleanup", gvr.Resource, n)
		}
	}
}

// TestProxyHoldsWatchPastList checks the "list allowed, watch denied after
// sync" fault: the kind is OK after its LIST, then denied once the held watch fails.
func TestProxyHoldsWatchPastList(t *testing.T) {
	testenv.ClassicWatch(t)
	env := testenv.New(t, testenv.Options{})
	p := env.Proxies[0]
	hits := p.Rule(testenv.Match{Kind: testenv.KindWatch, Resource: "httproutes.gateway.networking.k8s.io"}, testenv.Action{HoldWatchThenStatus: http.StatusForbidden})
	ca := startCache(t, env)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var sawOK, sawOpen bool
	var acc cache.Access
	for acc.Err == "" && ctx.Err() == nil {
		time.Sleep(10 * time.Millisecond)
		acc = ca.Snapshot().Access[httpRoutesGVR]
		sawOK = sawOK || acc.OK
		sawOpen = sawOpen || p.OpenWatches() > 0
	}
	if !sawOK {
		t.Fatal("httproutes never became OK before the watch failed")
	}
	if !sawOpen {
		t.Fatal("proxy never reported an open watch")
	}
	if acc.OK || !acc.Denied {
		t.Fatalf("httproutes access = %+v, want denied", acc)
	}
	if hits.N() == 0 {
		t.Fatal("hold rule never matched")
	}
}

func TestProxyCountsConnsAndWatches(t *testing.T) {
	env := testenv.New(t, testenv.Options{})
	p := env.Proxies[0]
	ctx, cancel := context.WithCancel(t.Context())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.URL()+"/api/v1/namespaces?watch=true", nil)
	tr := &http.Transport{}
	defer tr.CloseIdleConnections()
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if p.WatchesSeen() != 1 || p.OpenConns() != 1 {
		t.Errorf("watches seen %d, open conns %d; want 1 and 1", p.WatchesSeen(), p.OpenConns())
	}
	cancel()
	resp.Body.Close()
	deadline := time.Now().Add(2 * time.Second)
	for p.OpenConns() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if p.OpenConns() != 0 || p.WatchesSeen() != 1 {
		t.Errorf("after cancel: open conns %d, watches seen %d; want 0 and 1", p.OpenConns(), p.WatchesSeen())
	}
}

// TestProxySubresourceMatch checks that a Subresource rule answers pods/<name>/proxy
// requests and leaves the Pod's own GET and list requests alone.
func TestProxySubresourceMatch(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthy}})
	hits := env.Proxies[0].Rule(testenv.Match{Resource: "pods", Subresource: "proxy"}, testenv.Action{Status: http.StatusForbidden})
	c := clients(t, env)
	const ns, pod = "kgateway-system", "kgateway-7d9f8b6c5-abcde"

	var code int
	c.Kube.CoreV1().RESTClient().Get().Namespace(ns).Resource("pods").Name("http:" + pod + ":9092").
		SubResource("proxy").Suffix("metrics").Do(t.Context()).StatusCode(&code)
	if code != http.StatusForbidden {
		t.Fatalf("pods/proxy GET: status %d, want 403", code)
	}
	if _, err := c.Kube.CoreV1().Pods(ns).Get(t.Context(), pod, metav1.GetOptions{}); err != nil {
		t.Fatalf("Pod GET: %v", err)
	}
	if _, err := c.Kube.CoreV1().Pods(ns).List(t.Context(), metav1.ListOptions{}); err != nil {
		t.Fatalf("Pod list: %v", err)
	}
	if hits.N() != 1 {
		t.Fatalf("rule hits = %d, want 1", hits.N())
	}
}

// TestProxyBody: a rule with Body answers with those bytes, not a Status.
func TestProxyBody(t *testing.T) {
	env := testenv.New(t, testenv.Options{})
	const body = "kgwctl-canary-proxy-body"
	hits := env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "pods"}, testenv.Action{Status: http.StatusForbidden, Body: []byte(body)})
	var code int
	raw, _ := clients(t, env).Kube.CoreV1().RESTClient().Get().Resource("pods").Do(t.Context()).StatusCode(&code).Raw()
	if code != http.StatusForbidden || string(raw) != body || hits.N() != 1 {
		t.Fatalf("status %d, body %q, hits %d; want 403, %q, 1", code, raw, hits.N(), body)
	}
}

// TestProxySubresourceAnyVerb: a Subresource rule matches a POST, such as a
// pods/portforward upgrade, for both deny and hold.
func TestProxySubresourceAnyVerb(t *testing.T) {
	env := testenv.New(t, testenv.Options{Fixtures: []string{healthy}})
	const ns, pod = "kgateway-system", "kgateway-7d9f8b6c5-abcde"
	post := func(ctx context.Context, c load.Clients) (int, error) {
		var code int
		err := c.Kube.CoreV1().RESTClient().Post().Namespace(ns).Resource("pods").Name(pod).
			SubResource("portforward").Do(ctx).StatusCode(&code).Error()
		return code, err
	}

	deny := env.Proxies[0].Rule(testenv.Match{Resource: "pods", Subresource: "portforward"}, testenv.Action{Status: http.StatusForbidden})
	c := clients(t, env)
	if code, _ := post(t.Context(), c); code != http.StatusForbidden || deny.N() != 1 {
		t.Fatalf("deny: status %d, hits %d; want 403 and 1", code, deny.N())
	}

	held := make(chan struct{})
	defer close(held)
	env2 := testenv.New(t, testenv.Options{})
	hold := env2.Proxies[0].Rule(testenv.Match{Resource: "pods", Subresource: "portforward"}, testenv.Action{Until: held})
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if _, err := post(ctx, clients(t, env2)); err == nil {
		t.Fatal("held POST returned without error")
	}
	select {
	case <-hold.ClientGone():
	case <-time.After(10 * time.Second):
		t.Fatal("proxy never saw the held client go away")
	}
	if hold.N() != 1 {
		t.Fatalf("hold hits = %d, want 1", hold.N())
	}
}
