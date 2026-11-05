package testenv_test

import (
	"context"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DuncanDoyle/kgateway-ui/pkg/cache"
	"github.com/DuncanDoyle/kgateway-ui/pkg/extension"
	"github.com/DuncanDoyle/kgateway-ui/pkg/kinds"
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
	var podSel, depSel []labels.Selector
	for _, d := range reg.Detectors {
		podSel = append(podSel, d.ControllerPodSelector())
		depSel = append(depSel, d.ControllerSelector(), d.ProxySelector())
	}
	ca := cache.New(cache.Config{Dynamic: c.Dynamic, Kube: c.Kube, Served: served, PodSelectors: podSel, DeploymentSelectors: depSel})
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

// TestProxySelectorMatch checks that a rule with a label selector delays only
// requests carrying that selector.
func TestProxySelectorMatch(t *testing.T) {
	env := testenv.New(t, testenv.Options{})
	sel := "app.kubernetes.io/component=proxy,app.kubernetes.io/managed-by=kgateway"
	hits := env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "pods", LabelSelector: sel}, testenv.Action{Delay: time.Second})
	c := clients(t, env)

	timeList := func(selector string) time.Duration {
		start := time.Now()
		if _, err := c.Kube.CoreV1().Pods("").List(t.Context(), metav1.ListOptions{LabelSelector: selector}); err != nil {
			t.Fatal(err)
		}
		return time.Since(start)
	}
	if d := timeList("kgateway=kgateway"); d >= 300*time.Millisecond {
		t.Fatalf("unmatched selector took %v, want < 300ms", d)
	}
	// Same selector in another order: the proxy compares parsed selectors.
	if d := timeList("app.kubernetes.io/managed-by=kgateway,app.kubernetes.io/component=proxy"); d < time.Second {
		t.Fatalf("matched selector took %v, want >= 1s", d)
	}
	if hits.N() != 1 {
		t.Fatalf("hits = %d, want 1", hits.N())
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
			if acc.OK || !strings.Contains(acc.Err, "forbidden") {
				t.Fatalf("httproutes access = %+v, want denied (forbidden)", acc)
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
	if !strings.Contains(acc.Err, "forbidden") {
		t.Fatalf("httproutes access = %+v, want denied (forbidden)", acc)
	}
	if hits.N() == 0 {
		t.Fatal("hold rule never matched")
	}
}
