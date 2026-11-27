package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/DuncanDoyle/kgwctl/internal/app"
	"github.com/DuncanDoyle/kgwctl/internal/print"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
)

const (
	routesFixture = "testdata/proxy/routes.yaml"
	dumpsDir      = "../../testdata/dumps/"
)

// captureTime is the fixed clock of the live proxy routes tests.
var captureTime = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

// runRoutes runs the root command with h's dialer, a fixed clock and kubeconfig.
func runRoutes(t *testing.T, kubeconfig string, h app.ProxyTestHooks, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	cfg := app.Config{Build: testBuild, Out: &out, Err: &errb, Now: func() time.Time { return captureTime }}
	root := app.NewRootWithProxyHooks(cfg, h)
	start := time.Now()
	code := run(root, append([]string{"--kubeconfig", kubeconfig}, args...)...)
	return result{code: code, stdout: out.String(), stderr: errb.String(), took: time.Since(start)}
}

// unreachableKubeconfig writes a kubeconfig whose server refuses connections,
// with ns as the context namespace.
func unreachableKubeconfig(t *testing.T, ns string) string {
	t.Helper()
	kc := clientcmdapi.NewConfig()
	kc.Clusters["down"] = &clientcmdapi.Cluster{Server: "https://127.0.0.1:1"}
	kc.AuthInfos["down"] = &clientcmdapi.AuthInfo{Token: "unused"}
	kc.Contexts["down"] = &clientcmdapi.Context{Cluster: "down", AuthInfo: "down", Namespace: ns}
	kc.CurrentContext = "down"
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*kc, path); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestProxyRoutesFileGolden: the tree per golden dump, read from a file with
// no cluster reachable.
func TestProxyRoutesFileGolden(t *testing.T) {
	kc := unreachableKubeconfig(t, "default")
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "demo", args: []string{"--file", dumpsDir + "demo.json", "--gateway", "team-a/team-a"}},
		{name: "policies-response", args: []string{"--file", dumpsDir + "policies.json", "--gateway", "kgwctl-proxy/proxy", "--response"}},
		{name: "policies-orders", args: []string{"--file", dumpsDir + "policies.json", "--route", "kgwctl-proxy/orders"}},
		{name: "collide-meta", args: []string{"--file", dumpsDir + "collide-meta.json", "--gateway", "kgwctl-proxy/proxy"}},
		{name: "collide-meta-orders", args: []string{"--file", dumpsDir + "collide-meta.json", "--gateway", "kgwctl-proxy/proxy", "--route", "kgwctl-proxy/orders"}},
		{name: "collide-meta-orders-rule-1", args: []string{"--file", dumpsDir + "collide-meta.json", "--route", "kgwctl-proxy/orders", "--rule", "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runRoutes(t, kc, app.ProxyTestHooks{}, append([]string{"proxy", "routes"}, tc.args...)...)
			if r.code != 0 || r.stderr != "" {
				t.Fatalf("exit %d, stderr %q; want 0 and empty", r.code, r.stderr)
			}
			golden(t, "proxy-routes", tc.name, r.stdout)
		})
	}
}

// TestProxyRoutesFileNoKubernetes: --file never calls Kubernetes, so a
// reachable cluster gives the same output as an unreachable one, and a
// --route without a namespace takes the context's.
func TestProxyRoutesFileNoKubernetes(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{routesFixture}})
	before := len(env.Proxies[0].Requests())
	args := []string{"proxy", "routes", "--file", dumpsDir + "collide-meta.json", "--gateway", "kgwctl-proxy/proxy"}
	want := runRoutes(t, unreachableKubeconfig(t, "default"), app.ProxyTestHooks{}, append(args, "--route", "kgwctl-proxy/orders")...)
	if want.code != 0 || want.stderr != "" || !strings.Contains(want.stdout, "orders.kgwctl.example") {
		t.Fatalf("unreachable: exit %d, stderr %q, stdout:\n%s", want.code, want.stderr, want.stdout)
	}
	for _, tc := range []struct {
		name       string
		kubeconfig string
		args       []string
	}{
		{name: "reachable", kubeconfig: env.Kubeconfig, args: []string{"--route", "kgwctl-proxy/orders"}},
		{name: "reachable-n", kubeconfig: env.Kubeconfig, args: []string{"--route", "orders", "-n", "kgwctl-proxy"}},
		{name: "context-namespace", kubeconfig: unreachableKubeconfig(t, "kgwctl-proxy"), args: []string{"--route", "orders"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runRoutes(t, tc.kubeconfig, app.ProxyTestHooks{}, append(args, tc.args...)...)
			if r.code != 0 || r.stderr != "" || r.stdout != want.stdout {
				t.Fatalf("exit %d, stderr %q, stdout:\n%s\nwant 0, empty and:\n%s", r.code, r.stderr, r.stdout, want.stdout)
			}
		})
	}
	if reqs := env.Proxies[0].Requests()[before:]; len(reqs) != 0 {
		t.Fatalf("--file sent %d requests to the cluster: %v", len(reqs), reqs)
	}
}

// servePolicies is a fake admin server that serves policies.json once per request.
func servePolicies(t *testing.T, paths chan<- string) fakeDialer {
	t.Helper()
	body, err := os.ReadFile(dumpsDir + "policies.json")
	if err != nil {
		t.Fatal(err)
	}
	return newFakeDialer(t, func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.RequestURI()
		_, _ = w.Write(body)
	})
}

// TestProxyRoutesLive: the live tree names the Gateway, pod and capture time,
// and the cluster's HTTPRoutes narrow origins: kgwctl-proxy/orders exists and
// proxy/orders-kgwctl doesn't, so the colliding routes carry the one-candidate note.
func TestProxyRoutesLive(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{routesFixture}})
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "live-policies"},
		{name: "live-policies-orders", args: []string{"--route", "orders"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := make(chan string, 4)
			d := servePolicies(t, paths)
			r := runRoutes(t, env.Kubeconfig, app.ProxyTestHooks{Dialer: d}, append([]string{"proxy", "routes", "proxy", "-n", "kgwctl-proxy"}, tc.args...)...)
			if r.code != 0 || r.stderr != "" {
				t.Fatalf("exit %d, stderr %q; want 0 and empty", r.code, r.stderr)
			}
			if len(paths) != 1 || <-paths != "/config_dump" {
				t.Fatalf("admin requests: want one GET /config_dump")
			}
			golden(t, "proxy-routes", tc.name, r.stdout)
		})
	}
}

// TestProxyRoutesUsage: malformed flag combinations exit 2 before any I/O.
func TestProxyRoutesUsage(t *testing.T) {
	const kc = "unused-kubeconfig"
	file := dumpsDir + "demo.json"
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "no-source", args: nil, want: "--file"},
		{name: "gateway-and-file", args: []string{"http", "--file", file}, want: "--file"},
		{name: "two-gateways", args: []string{"a", "b"}, want: "Usage:"},
		{name: "gateway-flag-live", args: []string{"http", "--gateway", "ns/http"}, want: "--gateway"},
		{name: "gateway-flag-no-ns", args: []string{"--file", file, "--gateway", "http"}, want: "--gateway"},
		{name: "pod-with-file", args: []string{"--file", file, "--pod", "p"}, want: "--pod"},
		{name: "rule-without-route", args: []string{"--file", file, "--rule", "0"}, want: "--rule"},
		{name: "negative-rule", args: []string{"--file", file, "--route", "a/b", "--rule", "-1"}, want: "--rule"},
		{name: "bad-route", args: []string{"--file", file, "--route", "a/b/c"}, want: "--route"},
		{name: "bad-max-size", args: []string{"--file", file, "--max-size", "lots"}, want: "--max-size"},
		{name: "zero-max-size", args: []string{"--file", file, "--max-size", "0"}, want: "--max-size"},
		{name: "output", args: []string{"--file", file, "-o", "xml"}, want: "--output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runRoutes(t, kc, app.ProxyTestHooks{}, append([]string{"proxy", "routes"}, tc.args...)...)
			if r.code != 2 || r.stdout != "" || !strings.Contains(r.stderr, tc.want) {
				t.Fatalf("exit %d, stdout %q, stderr %q; want 2, empty and %q", r.code, r.stdout, r.stderr, tc.want)
			}
		})
	}
}

// TestProxyRoutesFileErrors: a missing, malformed or oversized file exits 2.
func TestProxyRoutesFileErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{name: "missing", args: []string{"--file", filepath.Join(dir, "nope.json")}, want: []string{"nope.json"}},
		{name: "parse", args: []string{"--file", bad}, want: []string{"parse config dump", "bad.json"}},
		{name: "too-large", args: []string{"--file", dumpsDir + "demo.json", "--max-size", "1Ki"}, want: []string{"raise --max-size"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runRoutes(t, "unused-kubeconfig", app.ProxyTestHooks{}, append([]string{"proxy", "routes"}, tc.args...)...)
			if r.code != 2 || r.stdout != "" {
				t.Fatalf("exit %d, stdout %q; want 2 and empty; stderr %q", r.code, r.stdout, r.stderr)
			}
			for _, w := range tc.want {
				if !strings.Contains(r.stderr, w) {
					t.Errorf("stderr = %q, want %q", r.stderr, w)
				}
			}
		})
	}
}

// loopbackTunnel stands in for a port-forward: a connection to a test server
// whose listener stop closes. clientGone closes once the client drops a
// request mid-response.
type loopbackTunnel struct {
	ln         net.Listener
	clientGone chan struct{}
	stopped    chan struct{}
}

func newLoopbackTunnel(t *testing.T, body []byte) *loopbackTunnel {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	lt := &loopbackTunnel{ln: ln, clientGone: make(chan struct{}), stopped: make(chan struct{})}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
		http.NewResponseController(w).Flush()
		// Hold the response open: only the client closing ends it.
		<-r.Context().Done()
		close(lt.clientGone)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return lt
}

func (lt *loopbackTunnel) Dial(context.Context, types.NamespacedName, int) (io.ReadWriteCloser, func(), error) {
	c, err := net.Dial("tcp", lt.ln.Addr().String())
	if err != nil {
		return nil, nil, err
	}
	return c, func() {
		_ = lt.ln.Close()
		close(lt.stopped)
	}, nil
}

func TestProxyRoutesCapClosesTunnel(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{checkInstall}})
	lt := newLoopbackTunnel(t, bytes.Repeat([]byte("x"), 2<<10))
	r := runRoutes(t, env.Kubeconfig, app.ProxyTestHooks{Dialer: lt}, "proxy", "routes", "http", "-n", "kgateway-system", "--max-size", "1Ki")
	if r.code != 2 || r.stdout != "" {
		t.Fatalf("exit %d, stdout %q; want 2 and empty; stderr %q", r.code, r.stdout, r.stderr)
	}
	for _, want := range []string{"raise --max-size", "kgwctl proxy dump", "--file <path> --max-size <n>"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("stderr = %q, want %q", r.stderr, want)
		}
	}
	for name, ch := range map[string]chan struct{}{"client close": lt.clientGone, "tunnel stop": lt.stopped} {
		select {
		case <-ch:
		case <-time.After(waitBound):
			t.Fatalf("server never saw the %s", name)
		}
	}
	if c, err := net.Dial("tcp", lt.ln.Addr().String()); err == nil {
		_ = c.Close()
		t.Fatal("tunnel listener still accepts connections")
	}
}

// TestProxyRoutesNoReplica: capture errors map as for proxy dump.
func TestProxyRoutesNoReplica(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{idleGateway}})
	r := runRoutes(t, env.Kubeconfig, app.ProxyTestHooks{}, "proxy", "routes", "idle", "-n", "kgateway-system")
	if r.code != 2 || r.stdout != "" || !strings.Contains(r.stderr, "--pod") {
		t.Fatalf("exit %d, stdout %q, stderr %q; want 2, empty and --pod", r.code, r.stdout, r.stderr)
	}
}

// jsonCases are the -o json goldens: the tree's dumps, narrowed and not.
var jsonCases = []struct {
	name string
	args []string
}{
	{name: "demo", args: []string{"--file", dumpsDir + "demo.json", "--gateway", "team-a/team-a"}},
	{name: "policies", args: []string{"--file", dumpsDir + "policies.json", "--gateway", "kgwctl-proxy/proxy"}},
	{name: "policies-orders", args: []string{"--file", dumpsDir + "policies.json", "--route", "kgwctl-proxy/orders"}},
	{name: "collide-meta-orders", args: []string{"--file", dumpsDir + "collide-meta.json", "--gateway", "kgwctl-proxy/proxy", "--route", "kgwctl-proxy/orders"}},
}

// TestProxyRoutesJSONGolden: the envelope per golden dump, from a file with no cluster reachable.
func TestProxyRoutesJSONGolden(t *testing.T) {
	kc := unreachableKubeconfig(t, "default")
	for _, tc := range jsonCases {
		t.Run(tc.name, func(t *testing.T) {
			r := runRoutes(t, kc, app.ProxyTestHooks{}, append([]string{"proxy", "routes", "-o", "json"}, tc.args...)...)
			if r.code != 0 || r.stderr != "" {
				t.Fatalf("exit %d, stderr %q; want 0 and empty", r.code, r.stderr)
			}
			golden(t, "proxy-routes", "json-"+tc.name, r.stdout)
		})
	}
}

// TestProxyRoutesJSONOneDocument: stdout holds exactly one JSON document.
func TestProxyRoutesJSONOneDocument(t *testing.T) {
	kc := unreachableKubeconfig(t, "default")
	for _, tc := range jsonCases {
		t.Run(tc.name, func(t *testing.T) {
			r := runRoutes(t, kc, app.ProxyTestHooks{}, append([]string{"proxy", "routes", "-o", "json"}, tc.args...)...)
			dec := json.NewDecoder(strings.NewReader(r.stdout))
			var doc struct {
				SchemaVersion string
				Kind          string
				Item          struct {
					Source    map[string]any
					Listeners []any
				}
			}
			if err := dec.Decode(&doc); err != nil {
				t.Fatalf("decode: %v\n%s", err, r.stdout)
			}
			if _, err := dec.Token(); err != io.EOF {
				t.Fatalf("stdout holds more than one document (next token err %v)", err)
			}
			if doc.SchemaVersion != print.SchemaVersion || doc.Kind != "ProxyRoutes" {
				t.Errorf("envelope = %q %q", doc.SchemaVersion, doc.Kind)
			}
			if doc.Item.Source == nil || doc.Item.Listeners == nil {
				t.Errorf("item lacks source or listeners: %+v", doc.Item)
			}
			if strings.Contains(r.stdout, `"listeners": null`) {
				t.Errorf("stdout has null listeners")
			}
			if r.stderr != "" || strings.HasSuffix(r.stdout, "\n\n") {
				t.Errorf("stderr %q, stdout %q: want no stderr and one trailing newline", r.stderr, r.stdout)
			}
		})
	}
}

// TestProxyRoutesYAMLGolden: the same envelope as YAML.
func TestProxyRoutesYAMLGolden(t *testing.T) {
	r := runRoutes(t, unreachableKubeconfig(t, "default"), app.ProxyTestHooks{}, "proxy", "routes", "-o", "yaml",
		"--file", dumpsDir+"policies.json", "--route", "kgwctl-proxy/orders")
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("exit %d, stderr %q; want 0 and empty", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, "listeners: []") {
		t.Errorf("stdout lacks an empty listeners list:\n%s", r.stdout)
	}
	golden(t, "proxy-routes", "yaml-policies-orders", r.stdout)
}

// TestProxyRoutesLiveJSON: a live source names the Gateway, Pod and capture
// time, and a route only the cluster's HTTPRoutes narrowed keeps an ambiguous
// origin marked narrowed, in listeners and under --route in selection.ambiguous.
func TestProxyRoutesLiveJSON(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{routesFixture}})
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{name: "live-policies", want: []string{`"certainty": "ambiguous"`, `"narrowed": true`, `"notInCluster": true`}},
		{name: "live-policies-orders", args: []string{"--route", "orders"}, want: []string{`"ambiguous": [`, `"listeners": []`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := servePolicies(t, make(chan string, 4))
			args := append([]string{"proxy", "routes", "proxy", "-n", "kgwctl-proxy", "-o", "json"}, tc.args...)
			r := runRoutes(t, env.Kubeconfig, app.ProxyTestHooks{Dialer: d}, args...)
			if r.code != 0 || r.stderr != "" {
				t.Fatalf("exit %d, stderr %q; want 0 and empty", r.code, r.stderr)
			}
			for _, w := range tc.want {
				if !strings.Contains(r.stdout, w) {
					t.Errorf("stdout lacks %s", w)
				}
			}
			golden(t, "proxy-routes", "json-"+tc.name, r.stdout)
		})
	}
}

// TestProxyRoutesRouteLoadDeadline: a deadline hit while loading the
// HTTPRoutes, after the dump was read, says so.
func TestProxyRoutesRouteLoadDeadline(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{routesFixture}})
	held := make(chan struct{})
	t.Cleanup(func() { close(held) })
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "httproutes.gateway.networking.k8s.io"}, testenv.Action{Until: held})
	d := servePolicies(t, make(chan string, 4))
	r := runRoutes(t, env.Kubeconfig, app.ProxyTestHooks{Dialer: d}, "proxy", "routes", "proxy", "-n", "kgwctl-proxy", "--request-timeout", "3s")
	if r.code != 2 || r.stdout != "" {
		t.Fatalf("exit %d, stdout %q; want 2 and empty; stderr %q", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr, "HTTPRoutes") || !strings.Contains(r.stderr, "--request-timeout") || strings.Contains(r.stderr, "could not read the config dump") {
		t.Errorf("stderr = %q, want the HTTPRoutes load and --request-timeout named", r.stderr)
	}
}

// stallingTransport blocks every HTTPRoute list for d without looking at the
// request's context, as an exec credential plugin does.
type stallingTransport struct {
	next http.RoundTripper
	d    time.Duration
}

func (s stallingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.Contains(r.URL.Path, "/httproutes") {
		time.Sleep(s.d)
	}
	return s.next.RoundTrip(r)
}

// TestProxyRoutesRouteLoadIgnoresContext: a live capture succeeds and the
// HTTPRoute list blocks ignoring ctx; the command still exits 2 by --request-timeout.
func TestProxyRoutesRouteLoadIgnoresContext(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{routesFixture}})
	d := servePolicies(t, make(chan string, 4))
	h := app.ProxyTestHooks{Dialer: d, WrapTransport: func(rt http.RoundTripper) http.RoundTripper {
		return stallingTransport{next: rt, d: 10 * time.Second}
	}}
	r := runRoutes(t, env.Kubeconfig, h, "proxy", "routes", "proxy", "-n", "kgwctl-proxy", "--request-timeout", "2s")
	if r.code != 2 || r.stdout != "" {
		t.Fatalf("exit %d, stdout %q; want 2 and empty; stderr %q", r.code, r.stdout, r.stderr)
	}
	if r.took > 6*time.Second {
		t.Errorf("command took %v, want it back by --request-timeout (2s)", r.took)
	}
	for _, w := range []string{"could not finish loading the HTTPRoutes", "--request-timeout", "was read"} {
		if !strings.Contains(r.stderr, w) {
			t.Errorf("stderr = %q, want %q", r.stderr, w)
		}
	}
}

// notADump is the error for valid JSON that is not a config dump.
const notADump = "not an Envoy config dump (no configs array)"

// wrongKinds are valid JSON documents without a configs array.
var wrongKinds = map[string]string{
	"empty-object": `{}`,
	"null":         `null`,
	"kubernetes":   `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"p","namespace":"ns"}}`,
	"configs-null": `{"configs":null}`,
}

// TestProxyRoutesFileWrongKind: valid JSON without a configs array exits 2;
// a dump with an empty configs array is an empty proxy.
func TestProxyRoutesFileWrongKind(t *testing.T) {
	dir := t.TempDir()
	for name, body := range wrongKinds {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			r := runRoutes(t, "unused-kubeconfig", app.ProxyTestHooks{}, "proxy", "routes", "--file", path)
			if r.code != 2 || r.stdout != "" || !strings.Contains(r.stderr, notADump) || !strings.Contains(r.stderr, path) {
				t.Fatalf("exit %d, stdout %q, stderr %q; want 2, empty and %q", r.code, r.stdout, r.stderr, notADump)
			}
		})
	}
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte(`{"configs":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{"text", "json"} {
		r := runRoutes(t, "unused-kubeconfig", app.ProxyTestHooks{}, "proxy", "routes", "--file", empty, "-o", output)
		if r.code != 0 || r.stderr != "" || r.stdout == "" {
			t.Errorf("-o %s: exit %d, stderr %q, stdout %q; want 0 and an empty view", output, r.code, r.stderr, r.stdout)
		}
	}
}

// TestProxyRoutesLiveWrongKind: the same check applies to a live dump.
func TestProxyRoutesLiveWrongKind(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{routesFixture}})
	d := newFakeDialer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(wrongKinds["kubernetes"])) })
	r := runRoutes(t, env.Kubeconfig, app.ProxyTestHooks{Dialer: d}, "proxy", "routes", "proxy", "-n", "kgwctl-proxy")
	if r.code != 2 || r.stdout != "" || !strings.Contains(r.stderr, notADump) {
		t.Fatalf("exit %d, stdout %q, stderr %q; want 2, empty and %q", r.code, r.stdout, r.stderr, notADump)
	}
}

// TestProxyRoutesListsOnlyHTTPRoutes: live, the HTTPRoutes come from one
// direct list; no other kind is listed or watched, so none can delay it.
func TestProxyRoutesListsOnlyHTTPRoutes(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{routesFixture}})
	before := len(env.Proxies[0].Requests())
	d := servePolicies(t, make(chan string, 4))
	r := runRoutes(t, env.Kubeconfig, app.ProxyTestHooks{Dialer: d}, "proxy", "routes", "proxy", "-n", "kgwctl-proxy")
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("exit %d, stderr %q; want 0 and empty", r.code, r.stderr)
	}
	// The cluster's HTTPRoutes were used: they narrow the colliding routes.
	if !strings.Contains(r.stdout, "one candidate exists in the cluster") {
		t.Errorf("stdout lacks the one-candidate note:\n%s", r.stdout)
	}
	var lists []string
	for _, req := range env.Proxies[0].Requests()[before:] {
		if strings.Contains(req, "watch=") || strings.Contains(req, "referencegrants") {
			t.Errorf("proxy routes requested %s", req)
		}
		if strings.HasPrefix(req, "GET /apis/") && !strings.Contains(req, "/gateways/") {
			lists = append(lists, req)
		}
	}
	if len(lists) != 1 || !strings.HasPrefix(lists[0], "GET /apis/gateway.networking.k8s.io/v1/httproutes?") {
		t.Errorf("API group requests %q, want one HTTPRoute list", lists)
	}
}

// TestProxyRoutesHTTPRouteListDenied: a denied HTTPRoute list is a warning;
// origins then come from the dump alone.
func TestProxyRoutesHTTPRouteListDenied(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{routesFixture}})
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "httproutes.gateway.networking.k8s.io"}, testenv.Action{Status: http.StatusForbidden})
	d := servePolicies(t, make(chan string, 4))
	r := runRoutes(t, env.Kubeconfig, app.ProxyTestHooks{Dialer: d}, "proxy", "routes", "proxy", "-n", "kgwctl-proxy")
	if r.code != 0 || !strings.Contains(r.stderr, "Warning: could not list HTTPRoutes") {
		t.Fatalf("exit %d, stderr %q; want 0 and the warning", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, "orders.kgwctl.example") || strings.Contains(r.stdout, "one candidate exists in the cluster") {
		t.Errorf("stdout want the routes without the one-candidate note:\n%s", r.stdout)
	}
}

// lastLine returns the last line of s, without its newline.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	return lines[len(lines)-1]
}

// TestProxyRoutesParserWarning: a section the parser skipped is a warning on
// stdout, once in the block after the tree, and in JSON next to the surviving routes.
func TestProxyRoutesParserWarning(t *testing.T) {
	raw, err := os.ReadFile(dumpsDir + "demo.json")
	if err != nil {
		t.Fatal(err)
	}
	var dump map[string]any
	if err := json.Unmarshal(raw, &dump); err != nil {
		t.Fatal(err)
	}
	// A second routes section whose only route config is malformed; the parser skips that config.
	dump["configs"] = append(dump["configs"].([]any), map[string]any{
		"@type":                 "type.googleapis.com/envoy.admin.v3.RoutesConfigDump",
		"dynamic_route_configs": []any{map[string]any{"route_config": map[string]any{"name": 81}}},
	})
	body, err := json.Marshal(dump)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"proxy", "routes", "--file", path, "--gateway", "team-a/team-a"}

	r := runRoutes(t, "unused-kubeconfig", app.ProxyTestHooks{}, args...)
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("text: exit %d, stderr %q; want 0 and empty", r.code, r.stderr)
	}
	if !strings.HasPrefix(lastLine(r.stdout), "warning: skipped route config") || strings.Count(r.stdout, "warning:") != 1 || !strings.Contains(r.stdout, "shop.team-a.example") {
		t.Errorf("text lacks the warning or the surviving routes:\n%s", r.stdout)
	}

	r = runRoutes(t, "unused-kubeconfig", app.ProxyTestHooks{}, append(args, "-o", "json")...)
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("json: exit %d, stderr %q; want 0 and empty", r.code, r.stderr)
	}
	var doc struct {
		Item struct {
			Listeners []any
			Warnings  []struct{ Subject, Code, Message string }
		}
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Item.Warnings) != 1 || !strings.Contains(doc.Item.Warnings[0].Message, "skipped route config") || len(doc.Item.Listeners) != 3 {
		t.Errorf("JSON warnings %+v, listeners %d; want one warning next to listener~80 and the two static listeners", doc.Item.Warnings, len(doc.Item.Listeners))
	}
	if !strings.Contains(r.stdout, "shop.team-a.example") {
		t.Errorf("JSON lacks the surviving routes")
	}
}

// TestProxyRoutesPortForwardForbidden: a denied port-forward names the
// permission and the Role that grants it.
func TestProxyRoutesPortForwardForbidden(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{routesFixture}})
	env.Proxies[0].Rule(testenv.Match{Resource: "pods", Subresource: "portforward"}, testenv.Action{Status: http.StatusForbidden})
	r := runRoutes(t, env.Kubeconfig, app.ProxyTestHooks{}, "proxy", "routes", "proxy", "-n", "kgwctl-proxy")
	if r.code != 2 || r.stdout != "" {
		t.Fatalf("exit %d, stdout %q; want 2 and empty; stderr %q", r.code, r.stdout, r.stderr)
	}
	for _, want := range []string{"create pods/portforward", "docs/rbac.yaml"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("stderr = %q, want %q", r.stderr, want)
		}
	}
}

// TestProxyRoutesUndecodableHCM: an HTTP connection manager whose typed_config
// does not decode is a warning, once after the tree, and in JSON.
func TestProxyRoutesUndecodableHCM(t *testing.T) {
	args := []string{"proxy", "routes", "--file", "testdata/proxy/hcm-undecodable.json"}
	const warning = `listener "listener~80" filter chain "listener~80": skipped HTTP connection manager`

	r := runRoutes(t, "unused-kubeconfig", app.ProxyTestHooks{}, args...)
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("text: exit %d, stderr %q; want 0 and empty", r.code, r.stderr)
	}
	if !strings.HasPrefix(lastLine(r.stdout), "warning: "+warning) || strings.Count(r.stdout, "warning:") != 1 || !strings.Contains(r.stdout, "listener~80") {
		t.Errorf("text lacks the warning or the listener:\n%s", r.stdout)
	}

	r = runRoutes(t, "unused-kubeconfig", app.ProxyTestHooks{}, append(args, "-o", "json")...)
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("json: exit %d, stderr %q; want 0 and empty", r.code, r.stderr)
	}
	var doc struct {
		Item struct {
			Listeners []any
			Warnings  []struct{ Subject, Code, Message string }
		}
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Item.Warnings) != 1 || !strings.Contains(doc.Item.Warnings[0].Message, warning) || len(doc.Item.Listeners) != 1 {
		t.Errorf("JSON warnings %+v, listeners %d; want one naming the listener and filter chain", doc.Item.Warnings, len(doc.Item.Listeners))
	}
}

// TestProxyRoutesUpdatingListenerKept: an active listener with a newer version
// warming stays in the output with its state when --route matches nothing there.
func TestProxyRoutesUpdatingListenerKept(t *testing.T) {
	r := runRoutes(t, "unused-kubeconfig", app.ProxyTestHooks{}, "proxy", "routes", "--file", "testdata/proxy/listener-updating.json", "--route", "team-a/nomatch")
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("exit %d, stderr %q; want 0 and empty", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, "listener~80") || !strings.Contains(r.stdout, "active · updating") {
		t.Errorf("stdout lacks the updating listener:\n%s", r.stdout)
	}
}
