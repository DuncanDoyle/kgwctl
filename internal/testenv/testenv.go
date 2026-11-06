// Package testenv runs command tests against a real API server: one envtest
// (kube-apiserver and etcd) per test package with the Gateway API and
// kgateway CRDs, behind fault-injecting proxies (design §10).
//
// A package using it calls Run from TestMain and New from each test. Tests
// using it never call t.Parallel.
package testenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/DuncanDoyle/kgwctl/internal/quiet"
	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	clientfeatures "k8s.io/client-go/features"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// Options configures one test's Env.
type Options struct {
	// Fixtures are YAML files applied as admin, relative to the test's working
	// directory. A status in a fixture is written through the status subresource.
	Fixtures []string
	// Contexts is the number of proxies, one kubeconfig context each; 0 means 1.
	Contexts int
	// CRDDirs are CRD files or directories installed in addition to the
	// Gateway API and kgateway CRDs.
	CRDDirs []string
}

// Env is one test's view of the package's envtest.
type Env struct {
	// Kubeconfig is the path of a kubeconfig with one context per proxy:
	// ContextName(i) points at Proxies[i], and ContextName(0) is current.
	Kubeconfig string
	Proxies    []*Proxy
	// Admin talks to envtest directly, bypassing the proxies.
	Admin *rest.Config
}

// ContextName returns the kubeconfig context name of Env.Proxies[i].
func ContextName(i int) string { return fmt.Sprintf("ctx-%d", i) }

// shared is the package's envtest, started by the first New and stopped by Run.
var shared struct {
	mu        sync.Mutex
	env       *envtest.Environment
	admin     *rest.Config
	installed map[string]bool // CRD paths installed so far
}

// Run runs the package's tests and then stops its envtest. Call it from
// TestMain: os.Exit(testenv.Run(m)). It silences klog first, as Execute does,
// so no test's watch goroutine can race a later Execute.
func Run(m *testing.M) int {
	quiet.Klog()
	code := m.Run()
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if shared.env != nil {
		if err := shared.env.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "testenv: stop envtest: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	return code
}

// defaultCRDDirs are the CRDs every Env has, read from this package's testdata.
func defaultCRDDirs() []string {
	_, file, _, _ := runtime.Caller(0)
	crds := filepath.Join(filepath.Dir(file), "testdata", "crds")
	return []string{
		filepath.Join(crds, "gateway-api", "standard"),
		filepath.Join(crds, "gateway-api", "experimental"),
		filepath.Join(crds, "kgateway"),
	}
}

// admin starts the shared envtest if needed, installs any CRD paths not yet
// installed, and returns the admin config.
func admin(t testing.TB, crdDirs []string) *rest.Config {
	t.Helper()
	shared.mu.Lock()
	defer shared.mu.Unlock()
	var abs []string
	for _, d := range append(defaultCRDDirs(), crdDirs...) {
		a, err := filepath.Abs(d)
		if err != nil {
			t.Fatal(err)
		}
		abs = append(abs, a)
	}
	if shared.env == nil {
		if os.Getenv("KUBEBUILDER_ASSETS") == "" {
			t.Fatal("testenv: KUBEBUILDER_ASSETS is not set; run the tests with `make test`")
		}
		ctrllog.SetLogger(logr.Discard())
		env := &envtest.Environment{CRDInstallOptions: envtest.CRDInstallOptions{Paths: abs, ErrorIfPathMissing: true}}
		cfg, err := env.Start()
		if err != nil {
			t.Fatalf("testenv: start envtest: %v", err)
		}
		shared.env, shared.admin, shared.installed = env, cfg, map[string]bool{}
		for _, a := range abs {
			shared.installed[a] = true
		}
	}
	var missing []string
	for _, a := range abs {
		if !shared.installed[a] {
			missing = append(missing, a)
		}
	}
	if len(missing) > 0 {
		if _, err := envtest.InstallCRDs(shared.admin, envtest.CRDInstallOptions{Paths: missing, ErrorIfPathMissing: true}); err != nil {
			t.Fatalf("testenv: install CRDs %v: %v", missing, err)
		}
		for _, a := range missing {
			shared.installed[a] = true
		}
	}
	return rest.CopyConfig(shared.admin)
}

// New returns an Env with opts.Contexts proxies in front of the package's
// envtest and opts.Fixtures applied. At cleanup it fails the test for every
// rule that never matched, closes the proxies, and deletes every object it
// created, waiting until each is gone. Namespaces are created if absent and
// never deleted: envtest has no namespace controller to finish a deletion.
func New(t testing.TB, opts Options) *Env {
	t.Helper()
	cfg := admin(t, opts.CRDDirs)
	n := opts.Contexts
	if n <= 0 {
		n = 1
	}
	target, err := url.Parse(cfg.Host)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := rest.TransportFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	env := &Env{Admin: cfg}
	for range n {
		env.Proxies = append(env.Proxies, newProxy(target, transport))
	}
	t.Cleanup(func() {
		for i, p := range env.Proxies {
			for _, m := range p.unmatched() {
				t.Errorf("testenv: proxy %d: rule %+v never matched", i, m)
			}
			p.close()
		}
	})
	env.Kubeconfig = writeKubeconfig(t, env.Proxies)

	created := applyFixtures(t, cfg, opts.Fixtures)
	t.Cleanup(func() { deleteAll(t, cfg, created) })
	return env
}

func writeKubeconfig(t testing.TB, proxies []*Proxy) string {
	t.Helper()
	kc := clientcmdapi.NewConfig()
	for i, p := range proxies {
		name := ContextName(i)
		kc.Clusters[name] = &clientcmdapi.Cluster{Server: p.URL()}
		kc.AuthInfos[name] = &clientcmdapi.AuthInfo{}
		kc.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name}
	}
	kc.CurrentContext = ContextName(0)
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*kc, path); err != nil {
		t.Fatal(err)
	}
	return path
}

// object is a created object and the resource it lives under.
type object struct {
	gvr       schema.GroupVersionResource
	namespace string
	name      string
}

func (o object) String() string { return fmt.Sprintf("%s %s/%s", o.gvr.Resource, o.namespace, o.name) }

// applyFixtures creates every document of files in order and returns what it created.
func applyFixtures(t testing.TB, cfg *rest.Config, files []string) []object {
	t.Helper()
	if len(files) == 0 {
		return nil
	}
	dyn := dynamic.NewForConfigOrDie(cfg)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discovery.NewDiscoveryClientForConfigOrDie(cfg)))
	var created []object
	for _, f := range files {
		docs, err := readObjects(f)
		if err != nil {
			deleteAll(t, cfg, created)
			t.Fatalf("testenv: fixture %s: %v", f, err)
		}
		for _, u := range docs {
			o, err := apply(t.Context(), dyn, mapper, u)
			if o != nil {
				created = append(created, *o)
			}
			if err != nil {
				// Objects created before the failure are still deleted.
				deleteAll(t, cfg, created)
				t.Fatalf("testenv: fixture %s: %s %s/%s: %v", f, u.GetKind(), u.GetNamespace(), u.GetName(), err)
			}
		}
	}
	return created
}

func readObjects(path string) ([]*unstructured.Unstructured, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(b), 4096)
	var out []*unstructured.Unstructured
	for {
		u := &unstructured.Unstructured{}
		if err := dec.Decode(&u.Object); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, err
		}
		if len(u.Object) > 0 {
			out = append(out, u)
		}
	}
}

// apply creates u's namespace if absent, then u, then writes u's status
// through the status subresource. Namespaces themselves are not returned,
// so they are never deleted.
func apply(ctx context.Context, dyn dynamic.Interface, mapper meta.RESTMapper, u *unstructured.Unstructured) (*object, error) {
	gvk := u.GroupVersionKind()
	m, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, err
	}
	nsGVR := schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	if m.Resource == nsGVR {
		return nil, ensureNamespace(ctx, dyn, u.GetName())
	}
	ri := dynamic.ResourceInterface(dyn.Resource(m.Resource))
	if m.Scope.Name() == meta.RESTScopeNameNamespace {
		if u.GetNamespace() == "" {
			u.SetNamespace("default")
		}
		if err := ensureNamespace(ctx, dyn, u.GetNamespace()); err != nil {
			return nil, err
		}
		ri = dyn.Resource(m.Resource).Namespace(u.GetNamespace())
	}
	status, hasStatus := u.Object["status"]
	out, err := ri.Create(ctx, u, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	o := &object{gvr: m.Resource, namespace: out.GetNamespace(), name: out.GetName()}
	if hasStatus {
		out.Object["status"] = status
		if _, err := ri.UpdateStatus(ctx, out, metav1.UpdateOptions{}); err != nil {
			return o, fmt.Errorf("write status: %w", err)
		}
	}
	return o, nil
}

func ensureNamespace(ctx context.Context, dyn dynamic.Interface, name string) error {
	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(name)
	_, err := dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Create(ctx, ns, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// deleteAll deletes objs in reverse order and waits until each is gone.
func deleteAll(t testing.TB, cfg *rest.Config, objs []object) {
	t.Helper()
	if len(objs) == 0 {
		return
	}
	dyn := dynamic.NewForConfigOrDie(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ri := func(o object) dynamic.ResourceInterface {
		if o.namespace == "" {
			return dyn.Resource(o.gvr)
		}
		return dyn.Resource(o.gvr).Namespace(o.namespace)
	}
	// Background propagation and no grace period: envtest runs no GC or kubelet.
	policy, grace := metav1.DeletePropagationBackground, int64(0)
	for _, o := range slices.Backward(objs) {
		err := ri(o).Delete(ctx, o.name, metav1.DeleteOptions{PropagationPolicy: &policy, GracePeriodSeconds: &grace})
		if err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("testenv: delete %s: %v", o, err)
		}
	}
	for _, o := range objs {
		err := wait.PollUntilContextCancel(ctx, 20*time.Millisecond, true, func(ctx context.Context) (bool, error) {
			_, err := ri(o).Get(ctx, o.name, metav1.GetOptions{})
			return apierrors.IsNotFound(err), nil
		})
		if err != nil {
			t.Errorf("testenv: %s still present after cleanup", o)
		}
	}
}

// ClassicWatch turns the WatchListClient feature gate off, so informers use
// a classic LIST then WATCH, and restores the previous gates at cleanup.
// Call it before starting any informer.
func ClassicWatch(t testing.TB) {
	prev := clientfeatures.FeatureGates()
	clientfeatures.ReplaceFeatureGates(gatesWithout{Gates: prev, off: clientfeatures.WatchListClient})
	t.Cleanup(func() { clientfeatures.ReplaceFeatureGates(prev) })
}

type gatesWithout struct {
	clientfeatures.Gates
	off clientfeatures.Feature
}

// Enabled implements clientfeatures.Gates.
func (g gatesWithout) Enabled(f clientfeatures.Feature) bool {
	return f != g.off && g.Gates.Enabled(f)
}
