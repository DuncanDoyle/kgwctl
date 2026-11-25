package app_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/DuncanDoyle/kgwctl/internal/app"
	"github.com/DuncanDoyle/kgwctl/internal/bundle/bundletest"
	"github.com/DuncanDoyle/kgwctl/internal/proxydump"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
)

// runBundleWith runs `bundle args` with reg (nil for the default) and hooks; hooks.Now defaults to bundleNow.
func runBundleWith(t *testing.T, kubeconfig string, reg *extension.Registry, hooks app.BundleTestHooks, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	if hooks.Now == nil {
		hooks.Now = func() time.Time { return bundleNow }
	}
	cfg := app.Config{Registry: reg, Build: testBuild, Out: &out, Err: &errb, Now: testNow}
	code := run(app.NewRootWithBundleHooks(cfg, hooks), append([]string{"--kubeconfig", kubeconfig, "bundle"}, args...)...)
	return result{code: code, stdout: out.String(), stderr: errb.String()}
}

// archiveWith is runBundleWith to stdout, extracted; a non-zero exit fails the test.
func archiveWith(t *testing.T, kubeconfig string, reg *extension.Registry, hooks app.BundleTestHooks, args ...string) bundletest.Archive {
	t.Helper()
	r := runBundleWith(t, kubeconfig, reg, hooks, append([]string{"-o", "-"}, args...)...)
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	return bundletest.Extract(t, strings.NewReader(r.stdout))
}

// proxyPods writes Running proxy Pods of Gateway kgateway-system/http, one per name.
func proxyPods(t *testing.T, names ...string) string {
	t.Helper()
	var docs []string
	for _, n := range names {
		docs = append(docs, proxyPodYAML(n))
	}
	path := filepath.Join(t.TempDir(), "proxies.yaml")
	if err := os.WriteFile(path, []byte(strings.Join(docs, "---\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func proxyPodYAML(name string) string {
	return "apiVersion: v1\nkind: Pod\nmetadata:\n  name: " + name + "\n  namespace: kgateway-system\n" +
		"  labels: {kgateway: kube-gateway, app.kubernetes.io/component: proxy, app.kubernetes.io/name: http,\n" +
		"    app.kubernetes.io/instance: http, gateway.networking.k8s.io/gateway-name: http}\n" +
		"  annotations: {gateway.kgateway.dev/gateway-full-name: http}\n" +
		"spec: {containers: [{name: kgateway-proxy, image: cr.kgateway.dev/kgateway-dev/envoy-wrapper:v2.5.0}]}\n" +
		"status: {phase: Running}\n"
}

// fakeAdmin is a proxydump.Dialer that answers each Pod's admin request with
// serve's body, or fails as serve says.
type fakeAdmin struct {
	serve func(pod types.NamespacedName) (body string, err error)
}

func (f fakeAdmin) Dial(_ context.Context, pod types.NamespacedName, _ int) (io.ReadWriteCloser, func(), error) {
	body, err := f.serve(pod)
	var remote error
	if err != nil {
		var st apierrors.APIStatus
		if errors.As(err, &st) {
			return nil, nil, err // a rejected upgrade
		}
		remote = err // the kubelet's error stream fails the read
	}
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		if _, err := http.ReadRequest(bufio.NewReader(server)); err != nil || remote != nil {
			return
		}
		fmt.Fprintf(server, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	}()
	if remote != nil {
		return failingConn{client, remote}, func() { client.Close() }, nil
	}
	return client, func() { client.Close() }, nil
}

// failingConn takes the request, then fails every read with err.
type failingConn struct {
	net.Conn
	err error
}

func (c failingConn) Read([]byte) (int, error) { return 0, c.err }

func serverInfo(version string) string {
	return `{"version":"` + version + `","state":"LIVE","command_line_options":{"concurrency":2}}`
}

// versionDoc is cluster/version.json.
type versionDoc struct {
	Client struct{ Version string } `json:"client"`
	Server *struct {
		GitVersion string `json:"gitVersion"`
	} `json:"server"`
	Installs []struct {
		ID, Controller string
		Proxies        []struct{ Gateway, Version string }
	} `json:"installs"`
	Observed []struct {
		Namespace, Pod, Version, Status string
	} `json:"observed"`
}

// itemsDoc is the manifest's item list.
type itemsDoc struct {
	Item struct {
		Items []struct {
			Path, Status string
			Source       struct{ Pod, Identity, Operation string }
		} `json:"items"`
	} `json:"item"`
}

func readVersion(t *testing.T, a bundletest.Archive) versionDoc {
	t.Helper()
	b, ok := a.File("cluster/version.json")
	if !ok {
		t.Fatal("archive lacks cluster/version.json")
	}
	var v versionDoc
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("version.json: %v\n%s", err, b)
	}
	return v
}

func readItems(t *testing.T, a bundletest.Archive) itemsDoc {
	t.Helper()
	b, _ := a.File("manifest.json")
	var d itemsDoc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestVersionFile: version.json holds the discovery server version, the
// install's controller and proxy image tags and one observed entry per proxy
// replica; a denied replica has no version, a replaced Pod is recorded as
// changed, a terminating one is captured, a label-only one is never dialled,
// and kubelet error text never reaches the archive. The production
// port-forwarder's denial is denied too.
func TestVersionFile(t *testing.T) {
	const canary = "kgwctl-canary-kubelet-stream"
	// A label-only Pod is unverified; a Pod held by a finalizer is deleted below, so it is terminating.
	// The proxy Deployment gives the install its proxy image tag.
	extra := filepath.Join(t.TempDir(), "extra.yaml")
	unverified := strings.Replace(proxyPodYAML("http-unverified"), "  annotations: {gateway.kgateway.dev/gateway-full-name: http}\n", "", 1)
	terminating := strings.Replace(proxyPodYAML("http-terminating"), "  labels:", "  finalizers: [example.com/hold]\n  labels:", 1)
	const labels = "{app.kubernetes.io/name: http, app.kubernetes.io/instance: http, gateway.networking.k8s.io/gateway-name: http}"
	deployment := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: http\n  namespace: kgateway-system\n" +
		"  labels: {app.kubernetes.io/managed-by: kgateway, app.kubernetes.io/component: proxy, app.kubernetes.io/name: http,\n" +
		"    app.kubernetes.io/instance: http, gateway.networking.k8s.io/gateway-name: http}\n" +
		"spec:\n  replicas: 1\n  selector: {matchLabels: " + labels + "}\n" +
		"  template:\n    metadata: {labels: " + labels + "}\n" +
		"    spec: {containers: [{name: kgateway-proxy, image: cr.kgateway.dev/kgateway-dev/envoy-wrapper:v2.5.0}]}\n"
	if err := os.WriteFile(extra, []byte(unverified+"---\n"+terminating+"---\n"+deployment), 0o600); err != nil {
		t.Fatal(err)
	}
	env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture, proxyPods(t, "http-a", "http-b", "http-denied", "http-err", "http-replaced"), extra}})
	kube, err := kubernetes.NewForConfig(env.Admin)
	if err != nil {
		t.Fatal(err)
	}
	pods := kube.CoreV1().Pods("kgateway-system")
	if err := pods.Delete(t.Context(), "http-terminating", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		patch := []byte(`{"metadata":{"finalizers":null}}`)
		_, _ = pods.Patch(context.Background(), "http-terminating", types.MergePatchType, patch, metav1.PatchOptions{})
	})
	var (
		mu     sync.Mutex
		dialed = map[string]bool{}
	)
	admin := fakeAdmin{serve: func(pod types.NamespacedName) (string, error) {
		mu.Lock()
		dialed[pod.Name] = true
		mu.Unlock()
		switch pod.Name {
		case "http-terminating":
			return serverInfo("ddd/1.36.1/Clean/RELEASE/BoringSSL"), nil
		case "http-a":
			return serverInfo("aaa/1.36.1/Clean/RELEASE/BoringSSL"), nil
		case "http-b":
			return serverInfo("bbb/1.37.0/Clean/RELEASE/BoringSSL"), nil
		case "http-denied":
			return "", apierrors.NewForbidden(schema.GroupResource{Resource: "pods/portforward"}, pod.Name, errors.New(canary))
		case "http-err":
			return "", errors.New("port-forward to port 19000 failed: " + canary)
		}
		// The Pod is replaced while it is captured.
		ctx := context.Background()
		if err := kube.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: new(int64)}); err != nil {
			return "", err
		}
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "kgateway-proxy", Image: "example.com/envoy:v1"}}}}
		if _, err := kube.CoreV1().Pods(pod.Namespace).Create(ctx, p, metav1.CreateOptions{}); err != nil {
			return "", err
		}
		return serverInfo("ccc/1.36.1/Clean/RELEASE/BoringSSL"), nil
	}}

	// proxy-admin is skipped: its dials would also replace http-replaced.
	a := archiveWith(t, env.Kubeconfig, nil, app.BundleTestHooks{Dialer: admin}, "--skip", "check,proxy-admin")
	v := readVersion(t, a)
	if v.Server == nil || v.Server.GitVersion == "" {
		t.Errorf("server version missing: %+v", v.Server)
	}
	if v.Client.Version != testBuild.Version {
		t.Errorf("client %q, want %q", v.Client.Version, testBuild.Version)
	}
	if len(v.Installs) != 1 || v.Installs[0].Controller != "2.5.0" {
		t.Fatalf("installs %+v, want the controller image tag 2.5.0", v.Installs)
	}
	if !slices.ContainsFunc(v.Installs[0].Proxies, func(p struct{ Gateway, Version string }) bool {
		return p.Gateway == "kgateway-system/http" && p.Version == "2.5.0"
	}) {
		t.Errorf("proxies %+v, want kgateway-system/http at image tag 2.5.0", v.Installs[0].Proxies)
	}
	if dialed["http-unverified"] {
		t.Error("the unverified Pod was dialled")
	}
	got := map[string]string{}
	for _, o := range v.Observed {
		got[o.Pod] = o.Status + " " + o.Version
	}
	want := map[string]string{
		"http-a":           "ok aaa/1.36.1/Clean/RELEASE/BoringSSL",
		"http-b":           "ok bbb/1.37.0/Clean/RELEASE/BoringSSL",
		"http-denied":      "denied ",
		"http-err":         "failed ",
		"http-replaced":    "ok ccc/1.36.1/Clean/RELEASE/BoringSSL",
		"http-terminating": "ok ddd/1.36.1/Clean/RELEASE/BoringSSL",
		"http-unverified":  "skipped ",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("observed %v, want %v", got, want)
	}
	assertMembers(t, a, []string{"proxies/kgateway-system/http-a/server_info.json", "proxies/kgateway-system/http-replaced/server_info.json"},
		[]string{"proxies/kgateway-system/http-denied/server_info.json", "proxies/kgateway-system/http-err/server_info.json"})
	ids := map[string]string{}
	for _, it := range readItems(t, a).Item.Items {
		if strings.HasSuffix(it.Path, "/server_info.json") {
			ids[it.Source.Pod] = it.Status + " " + it.Source.Identity
			if it.Source.Operation != "server-info" {
				t.Errorf("%s operation %q", it.Path, it.Source.Operation)
			}
		}
	}
	if ids["http-a"] != "ok verified" || ids["http-replaced"] != "ok changed" || ids["http-denied"] != "denied " {
		t.Errorf("server_info items %v", ids)
	}
	fileHas(t, a, "errors.txt", "proxies/kgateway-system/http-denied/server_info.json", "403 Forbidden")
	bundletest.AssertNoCanary(t, a, []string{canary})

	// The production port-forwarder through the guard: a 403 upgrade is denied.
	hits := env.Proxies[0].Rule(testenv.Match{Resource: "pods", Subresource: "portforward"}, testenv.Action{Status: http.StatusForbidden, Body: []byte(canary)})
	a = archiveWith(t, env.Kubeconfig, nil, app.BundleTestHooks{}, "--skip", "check")
	if hits.N() == 0 {
		t.Fatal("no port-forward request reached the API server")
	}
	for _, o := range readVersion(t, a).Observed {
		if o.Pod != "http-unverified" && (o.Status != "denied" || o.Version != "") {
			t.Errorf("production dialer: %+v, want denied without a version", o)
		}
	}
	bundletest.AssertNoCanary(t, a, []string{canary})
}

// timeRe matches the RFC 3339 times check output carries.
var timeRe = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?(Z|[+-]\d\d:\d\d)$`)

// normalizeJSON parses b and replaces every RFC 3339 time with "T".
func normalizeJSON(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				x[k] = walk(e)
			}
		case []any:
			for i, e := range x {
				x[i] = walk(e)
			}
		case string:
			if timeRe.MatchString(x) {
				return "T"
			}
		}
		return v
	}
	return walk(v)
}

// TestCheckCollectorMatchesCheck: check/check.json is `check -o json` with the
// same -n, apart from times.
func TestCheckCollectorMatchesCheck(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController)
	reg := statsRegistry(fakeStats{})
	r := runCheck(t, env, reg, fastHooks(metricsScraper{}), "check", "-o", "json", "-n", "kgateway-system")
	if r.stdout == "" {
		t.Fatalf("check printed nothing; exit %d, stderr %q", r.code, r.stderr)
	}
	a := archiveWith(t, env.Kubeconfig, reg, app.BundleTestHooks{Check: fastHooks(metricsScraper{})}, "-n", "kgateway-system", "--skip", "version")
	got, ok := a.File("check/check.json")
	if !ok {
		t.Fatal("archive lacks check/check.json")
	}
	if g, w := normalizeJSON(t, got), normalizeJSON(t, []byte(r.stdout)); !reflect.DeepEqual(g, w) {
		t.Errorf("check.json differs from check -o json:\n got %s\nwant %s", got, r.stdout)
	}
}

// TestCheckCollectorDenialBody: a list denied with a pattern-free body is
// recorded as 403 Forbidden; the body never reaches the archive.
func TestCheckCollectorDenialBody(t *testing.T) {
	const canary = "kgwctl-canary-denial-body"
	env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture}})
	body := []byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","code":403,"reason":"Forbidden","message":"` + canary + `"}`)
	hits := env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: httpRoutes}, testenv.Action{Status: http.StatusForbidden, Body: body})
	a := archiveWith(t, env.Kubeconfig, nil, app.BundleTestHooks{Check: fastHooks(metricsScraper{})}, "--skip", "version")
	if hits.N() == 0 {
		t.Fatal("the rule never applied")
	}
	b, _ := a.File("check/check.json")
	var doc struct {
		Item struct {
			Incomplete []struct{ Input, State, Reason string } `json:"incomplete"`
		} `json:"item"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("check.json: %v\n%s", err, b)
	}
	if !slices.ContainsFunc(doc.Item.Incomplete, func(in struct{ Input, State, Reason string }) bool {
		return strings.HasPrefix(in.Input, "httproutes") && in.State == "denied" && strings.Contains(in.Reason, "403 Forbidden")
	}) {
		t.Errorf("check.json incomplete %+v, want httproutes denied with 403 Forbidden", doc.Item.Incomplete)
	}
	fileHas(t, a, "errors.txt", "resources/gateway.networking.k8s.io/HTTPRoute", "check/gateway.networking.k8s.io/httproutes", "403 Forbidden")
	if _, ok := a.File("run.log"); !ok {
		t.Error("archive lacks run.log")
	}
	bundletest.AssertNoCanary(t, a, []string{canary})
}

// TestCheckCollectorOnlyDenial: an input only check reads is denied
// (--skip resources). check.json is kept, the manifest records the denial
// and errors.txt gets the cluster-wide reader grant line.
func TestCheckCollectorOnlyDenial(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture}})
	hits := env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: httpRoutes}, testenv.Action{Status: http.StatusForbidden})
	a := archiveWith(t, env.Kubeconfig, nil, app.BundleTestHooks{Check: fastHooks(metricsScraper{})}, "--skip", "version,resources")
	if hits.N() == 0 {
		t.Fatal("the rule never applied")
	}
	if _, ok := a.File("check/check.json"); !ok {
		t.Error("archive lacks check/check.json")
	}
	const path = "check/gateway.networking.k8s.io/httproutes"
	found := false
	for _, it := range readItems(t, a).Item.Items {
		found = found || it.Path == path && it.Status == "denied" && it.Source.Operation == "check"
	}
	if !found {
		t.Errorf("manifest items %+v, want %s denied by check", readItems(t, a).Item.Items, path)
	}
	fileHas(t, a, "errors.txt", path, "403 Forbidden")
	_, grants := grantLines(t, a)
	if want := []string{"grant: get, list and watch cluster-wide: see the kgwctl-reader ClusterRole in docs/rbac.yaml"}; !slices.Equal(grants, want) {
		t.Errorf("grant lines %q, want %q", grants, want)
	}
}

// TestCheckCollectorRedirectBody: a terminal 3xx whose Status body carries a
// pattern-free value is recorded by code only; the body reaches no member.
func TestCheckCollectorRedirectBody(t *testing.T) {
	const canary = "kgwctl-canary-redirect-body"
	env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture}})
	body := []byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","code":300,"message":"` + canary + `"}`)
	hits := env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: httpRoutes}, testenv.Action{Status: http.StatusMultipleChoices, Body: body})
	a := archiveWith(t, env.Kubeconfig, nil, app.BundleTestHooks{Check: fastHooks(metricsScraper{})}, "--skip", "version")
	if hits.N() == 0 {
		t.Fatal("the rule never applied")
	}
	b, _ := a.File("check/check.json")
	var doc struct {
		Item struct {
			Incomplete []struct{ Input, State, Reason string } `json:"incomplete"`
		} `json:"item"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("check.json: %v\n%s", err, b)
	}
	if !slices.ContainsFunc(doc.Item.Incomplete, func(in struct{ Input, State, Reason string }) bool {
		return strings.HasPrefix(in.Input, "httproutes") && strings.Contains(in.Reason, "300 Multiple Choices")
	}) {
		t.Errorf("check.json incomplete %+v, want httproutes with 300 Multiple Choices", doc.Item.Incomplete)
	}
	bundletest.AssertNoCanary(t, a, []string{canary})
}

// TestBundleRedirectLocation: a 3xx is never followed, and its Location value
// reaches no member; net/http would quote it in its error text.
func TestBundleRedirectLocation(t *testing.T) {
	const canary = "kgwctl-canary-location"
	for name, loc := range map[string]string{
		"unparseable":  "%zz " + canary,
		"opaque":       "x:" + canary,
		"foreign host": "//" + canary + ".invalid/p",
		// Same path, new query: the rule matches again, so it loops.
		"same-origin loop": "?" + canary,
	} {
		t.Run(name, func(t *testing.T) {
			env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture}})
			hits := env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: httpRoutes}, testenv.Action{
				Status: http.StatusFound,
				Body:   []byte(`{}`),
				Header: http.Header{"Location": {loc}},
			})
			a := archiveWith(t, env.Kubeconfig, nil, app.BundleTestHooks{Check: fastHooks(metricsScraper{})}, "--skip", "version")
			if hits.N() == 0 {
				t.Fatal("the rule never applied")
			}
			fileHas(t, a, "errors.txt", "resources/gateway.networking.k8s.io/HTTPRoute")
			bundletest.AssertNoCanary(t, a, []string{canary})
		})
	}
}

// TestCheckCollectorUnwatchedRoute: check names team-b's route, which the
// install's discovery selector leaves out, with KGWUI-004.
func TestCheckCollectorUnwatchedRoute(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{ownershipFixture(t)}})
	a := archiveWith(t, env.Kubeconfig, nil, app.BundleTestHooks{Check: fastHooks(metricsScraper{})}, "--skip", "version")
	b, _ := a.File("check/check.json")
	var doc struct {
		Item struct {
			Installs []struct {
				Findings []json.RawMessage `json:"findings"`
			} `json:"installs"`
		} `json:"item"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("check.json: %v\n%s", err, b)
	}
	for _, ic := range doc.Item.Installs {
		for _, f := range ic.Findings {
			s := string(f)
			if strings.Contains(s, `"KGWUI-004"`) && strings.Contains(s, "team-b") && strings.Contains(s, "route-b") {
				return
			}
		}
	}
	t.Errorf("check.json has no KGWUI-004 for team-b/route-b:\n%s", b)
}

// TestBundleMetadataCanaries: canaries in the context name, a --redact-header
// value, the -o path and a map key reach no member or member name; the map key
// stays only in its own object.
func TestBundleMetadataCanaries(t *testing.T) {
	const (
		ctxName = "arn:aws:eks:eu-west-1:000000000000:cluster/prod?token=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.kgwctl-canary-ctx"
		header  = "x-kgwctl-canary-hdr"
		mapKey  = "kgwctl-canary-mapkey"
	)
	route := "apiVersion: gateway.networking.k8s.io/v1\nkind: HTTPRoute\nmetadata:\n  name: canary\n  namespace: default\n" +
		"  annotations: {" + mapKey + ": \"Bearer kgwctl-canary-mapval\"}\nspec: {parentRefs: [{name: http, namespace: kgateway-system}]}\n"
	fixture := filepath.Join(t.TempDir(), "route.yaml")
	if err := os.WriteFile(fixture, []byte(route), 0o600); err != nil {
		t.Fatal(err)
	}
	env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture, fixture}})

	cfg, err := clientcmd.LoadFromFile(env.Kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Contexts[ctxName] = cfg.Contexts[cfg.CurrentContext]
	delete(cfg.Contexts, cfg.CurrentContext)
	cfg.CurrentContext = ctxName
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*cfg, kubeconfig); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "kgwctl-canary-out.tar.gz")
	r := runBundleWith(t, kubeconfig, nil, app.BundleTestHooks{Check: fastHooks(metricsScraper{})}, "-o", out, "--redact-header", header)
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	a := bundletest.Extract(t, f)
	own := "resources/gateway.networking.k8s.io/HTTPRoute/default/canary.yaml"
	fileHas(t, a, own, mapKey)
	for i := range a.Members {
		if strings.TrimPrefix(a.Members[i].Name, a.Root+"/") == own {
			a.Members[i].Data = bytes.ReplaceAll(a.Members[i].Data, []byte(mapKey), nil)
		}
	}
	if _, ok := a.File("check/check.json"); !ok {
		t.Error("archive lacks check/check.json")
	}
	bundletest.AssertNoCanary(t, a, []string{"kgwctl-canary-ctx", header, "kgwctl-canary-out", mapKey, "kgwctl-canary-mapval"})
}

// TestBundleRegistryAlias: a registry kind resolving to core secrets is never
// requested, even with an admin kubeconfig; it is an error line and the run exits 0.
func TestBundleRegistryAlias(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture}})
	reg := extension.New(extension.WithKinds(kinds.Descriptor{Kind: "Secret", Resource: "secrets", Versions: []string{"v1"}, Namespaced: true}))
	a := archiveWith(t, env.Kubeconfig, reg, app.BundleTestHooks{Check: fastHooks(metricsScraper{})}, "--skip", "version")
	if slices.ContainsFunc(env.Proxies[0].Requests(), func(r string) bool { return strings.Contains(r, "/secrets") }) {
		t.Error("a secrets request reached the API server")
	}
	fileHas(t, a, "errors.txt", "resources/core/Secret")
	if _, ok := a.File("check/check.json"); !ok {
		t.Error("archive lacks check/check.json")
	}
}

var _ proxydump.Dialer = fakeAdmin{}
