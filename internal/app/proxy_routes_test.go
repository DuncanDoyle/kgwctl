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

	"github.com/DuncanDoyle/xdsview/pkg/adapter/kgateway"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/model"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/provenance"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/DuncanDoyle/kgwctl/internal/app"
	"github.com/DuncanDoyle/kgwctl/internal/print"
	"github.com/DuncanDoyle/kgwctl/internal/proxyview"
	"github.com/DuncanDoyle/kgwctl/internal/proxyview/proxyviewtest"
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
	return runRoutesModules(t, kubeconfig, h, nil, args...)
}

// runRoutesModules is runRoutes with provenance modules; nil is the default.
func runRoutesModules(t *testing.T, kubeconfig string, h app.ProxyTestHooks, mods []provenance.Module, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	cfg := app.Config{Build: testBuild, Out: &out, Err: &errb, Now: func() time.Time { return captureTime }, Modules: mods}
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
		// Policy records at every level: the trailer says where each was recorded (D-024).
		{name: "levels", args: []string{"--file", "../proxyview/testdata/levels.json"}},
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

	txt := runRoutes(t, "unused-kubeconfig", app.ProxyTestHooks{}, args...)
	if txt.code != 0 || txt.stderr != "" {
		t.Fatalf("text: exit %d, stderr %q; want 0 and empty", txt.code, txt.stderr)
	}
	r := runRoutes(t, "unused-kubeconfig", app.ProxyTestHooks{}, append(args, "-o", "json")...)
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
	if len(doc.Item.Warnings) != 1 || !strings.HasPrefix(doc.Item.Warnings[0].Message, warning) || len(doc.Item.Listeners) != 1 {
		t.Fatalf("JSON warnings %+v, listeners %d; want one naming the listener and filter chain", doc.Item.Warnings, len(doc.Item.Listeners))
	}
	// The message names the listener and filter chain, so the line adds nothing.
	if lastLine(txt.stdout) != "warning: "+doc.Item.Warnings[0].Message || strings.Count(txt.stdout, "warning:") != 1 {
		t.Errorf("text lacks the warning line, once and as the message:\n%s", txt.stdout)
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

// serveBody is a fake admin server that serves body on every request.
func serveBody(t *testing.T, body []byte) fakeDialer {
	t.Helper()
	return newFakeDialer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
}

func readDump(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(dumpsDir + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// asStrict returns known JSON with its strategy named strict, to compare with strict JSON.
func asStrict(s string) string {
	return strings.Replace(s, `"strategy": "known"`, `"strategy": "strict"`, 1)
}

const (
	listWarning  = "Warning: could not list HTTPRoutes: "
	knownWarning = "Warning: --select known: no route can be chosen from known HTTPRoutes"
	// chosenRow is the route row of a route --select known chose.
	chosenRow = "HTTPRoute kgwctl-proxy/orders · rule 0 · match 0 (chosen from known HTTPRoutes)"
)

// stderrLines splits stderr into lines without the final newline.
func stderrLines(s string) []string {
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// TestProxyRoutesSelectFlag: --select at the command boundary (R19, R21 to R24a).
func TestProxyRoutesSelectFlag(t *testing.T) {
	// Context 1 is denied the HTTPRoute list; context 0 is allowed it.
	env := newEnv(t, testenv.Options{Fixtures: []string{routesFixture}, Contexts: 2})
	env.Proxies[1].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "httproutes.gateway.networking.k8s.io"}, testenv.Action{Status: http.StatusForbidden})
	allowed, denied := testenv.ContextName(0), testenv.ContextName(1)
	live := []string{"proxy", "routes", "proxy", "-n", "kgwctl-proxy"}
	route := []string{"--route", "kgwctl-proxy/orders"}
	policies, collide := readDump(t, "policies.json"), readDump(t, "collide-meta.json")
	// runLive runs live proxy routes in context ctx, serving body, with --select s and -o o.
	runLive := func(t *testing.T, ctx string, body []byte, s, o string, extra ...string) result {
		t.Helper()
		args := append(append(append([]string{"--context", ctx}, live...), extra...), "--select", s, "-o", o)
		return runRoutes(t, env.Kubeconfig, app.ProxyTestHooks{Dialer: serveBody(t, body)}, args...)
	}

	t.Run("usage", func(t *testing.T) {
		before := len(env.Proxies[0].Requests())
		missing := filepath.Join(t.TempDir(), "nope.json")
		const withFile = "--select known needs the cluster's HTTPRoutes; it does not work with --file"
		for _, tc := range []struct {
			args []string
			want string
		}{
			{args: []string{"--file", missing, "--select", "known"}, want: withFile},
			{args: []string{"--file", missing, "--select", "known", "--route", "kgwctl-proxy/orders"}, want: withFile},
			{args: []string{"--file", missing, "--select", "bogus"}, want: `invalid --select "bogus"`},
			{args: []string{"proxy", "-n", "kgwctl-proxy", "--select", "bogus"}, want: `invalid --select "bogus"`},
		} {
			r := runRoutes(t, env.Kubeconfig, app.ProxyTestHooks{}, append([]string{"proxy", "routes"}, tc.args...)...)
			if r.code != 2 || r.stdout != "" || !strings.Contains(r.stderr, tc.want) || strings.Contains(r.stderr, "nope.json") {
				t.Errorf("%v: exit %d, stdout %q, stderr %q; want 2, empty and %q without reading the file", tc.args, r.code, r.stdout, r.stderr, tc.want)
			}
		}
		if reqs := env.Proxies[0].Requests()[before:]; len(reqs) != 0 {
			t.Errorf("usage errors sent %d requests: %v", len(reqs), reqs)
		}
	})

	t.Run("R21-select-match", func(t *testing.T) {
		out := map[string]string{}
		for _, s := range []string{"strict", "known"} {
			for _, o := range []string{"text", "json"} {
				r := runLive(t, allowed, collide, s, o, route...)
				if r.code != 0 || r.stderr != "" {
					t.Fatalf("%s %s: exit %d, stderr %q; want 0 and empty", s, o, r.code, r.stderr)
				}
				out[s+o] = r.stdout
			}
		}
		if out["stricttext"] != out["knowntext"] {
			t.Errorf("text differs:\nstrict:\n%s\nknown:\n%s", out["stricttext"], out["knowntext"])
		}
		if asStrict(out["knownjson"]) != out["strictjson"] || !strings.Contains(out["knownjson"], `"strategy": "known"`) {
			t.Errorf("JSON differs other than in selection.strategy:\nstrict:\n%s\nknown:\n%s", out["strictjson"], out["knownjson"])
		}
		if !strings.Contains(out["knownjson"], `"chosenBy": "select"`) || strings.Contains(out["knownjson"], `"chosenBy": "known"`) {
			t.Errorf("known JSON: want the route chosen by select:\n%s", out["knownjson"])
		}
	})

	t.Run("R27-R28-three-strategies", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			body      []byte
			knownSame bool // known prints what strict prints
		}{{"collide-meta", collide, true}, {"policies", policies, false}} {
			out := map[string]string{}
			for _, s := range []string{"exact", "strict", "known"} {
				for _, o := range []string{"text", "json"} {
					r := runLive(t, allowed, tc.body, s, o, route...)
					if r.code != 0 || r.stderr != "" {
						t.Fatalf("%s %s %s: exit %d, stderr %q; want 0 and empty", tc.name, s, o, r.code, r.stderr)
					}
					out[s+o] = r.stdout
				}
			}
			exactJSON := strings.Replace(out["exactjson"], `"strategy": "exact"`, `"strategy": "strict"`, 1)
			if out["exacttext"] != out["stricttext"] || exactJSON != out["strictjson"] {
				t.Errorf("%s: exact differs from strict other than in selection.strategy:\n%s", tc.name, out["exacttext"])
			}
			if got := out["knowntext"] == out["stricttext"]; got != tc.knownSame {
				t.Errorf("%s: known text equal to strict %v, want %v", tc.name, got, tc.knownSame)
			}
			if !strings.Contains(out["exactjson"], `"inferred": []`) {
				t.Errorf("%s: exact JSON lacks an empty inferred list", tc.name)
			}
		}
	})

	t.Run("R19-known-chooses", func(t *testing.T) {
		for _, o := range []string{"text", "json"} {
			r := runLive(t, allowed, policies, "known", o, route...)
			if r.code != 0 || r.stderr != "" {
				t.Fatalf("%s: exit %d, stderr %q; want 0 and empty", o, r.code, r.stderr)
			}
			name := "live-policies-orders-known"
			if o == "json" {
				name = "json-" + name
				if n := strings.Count(r.stdout, `"chosenBy": "known"`); n != 4 {
					t.Errorf("JSON has %d chosenBy known, want 4 (selection and origin of both routes)", n)
				}
			} else if n := strings.Count(r.stdout, chosenRow+"\n"); n != 2 || strings.Contains(r.stdout, "ambiguous") {
				t.Errorf("text has %d marked rows, want 2 and nothing ambiguous:\n%s", n, r.stdout)
			}
			golden(t, "proxy-routes", name, r.stdout)
		}
	})

	t.Run("R23-no-route", func(t *testing.T) {
		for _, tc := range []struct {
			name, ctx string
		}{{"allowed", allowed}, {"denied", denied}} {
			for _, o := range []string{"text", "json"} {
				s, k, e := runLive(t, tc.ctx, policies, "strict", o), runLive(t, tc.ctx, policies, "known", o), runLive(t, tc.ctx, policies, "exact", o)
				for _, x := range []result{k, e} {
					if s.code != 0 || x.code != 0 || s.stdout != x.stdout || s.stderr != x.stderr {
						t.Errorf("%s %s: exit %d and %d; stdout or stderr differ between strategies; stderr %q and %q", tc.name, o, s.code, x.code, s.stderr, x.stderr)
					}
				}
				lines := stderrLines(k.stderr)
				switch {
				case tc.name == "allowed" && k.stderr != "":
					t.Errorf("allowed %s: stderr %q, want empty", o, k.stderr)
				case tc.name == "denied" && (len(lines) != 1 || !strings.HasPrefix(lines[0], listWarning)):
					t.Errorf("denied %s: stderr %q, want the list warning only", o, k.stderr)
				}
			}
		}
	})

	t.Run("R24-route-list-denied", func(t *testing.T) {
		out := map[string]result{}
		for _, s := range []string{"strict", "known"} {
			for _, o := range []string{"text", "json"} {
				r := runLive(t, denied, policies, s, o, route...)
				if r.code != 0 {
					t.Fatalf("%s %s: exit %d, stderr %q; want 0", s, o, r.code, r.stderr)
				}
				want := 1
				if s == "known" {
					want = 2
				}
				lines := stderrLines(r.stderr)
				if len(lines) != want || !strings.HasPrefix(lines[0], listWarning) || (want == 2 && lines[1] != knownWarning) {
					t.Errorf("%s %s: stderr %q, want the list warning and under known %q", s, o, r.stderr, knownWarning)
				}
				out[s+o] = r
			}
		}
		if out["stricttext"].stdout != out["knowntext"].stdout {
			t.Errorf("text differs:\nstrict:\n%s\nknown:\n%s", out["stricttext"].stdout, out["knowntext"].stdout)
		}
		if asStrict(out["knownjson"].stdout) != out["strictjson"].stdout {
			t.Errorf("JSON differs other than in selection.strategy")
		}
		golden(t, "proxy-routes", "json-live-policies-orders-known-denied", out["knownjson"].stdout)
	})

	// R24: selection.knownHTTPRoutes says whether the cluster's HTTPRoutes were listed.
	t.Run("R24-known-input-status", func(t *testing.T) {
		kc := unreachableKubeconfig(t, "default")
		for _, tc := range []struct {
			name, want string
			run        func() result
		}{
			{"allowed-known", "listed", func() result { return runLive(t, allowed, policies, "known", "json", route...) }},
			{"allowed-strict", "listed", func() result { return runLive(t, allowed, policies, "strict", "json", route...) }},
			{"allowed-exact", "listed", func() result { return runLive(t, allowed, policies, "exact", "json", route...) }},
			{"denied-known", "unavailable", func() result { return runLive(t, denied, policies, "known", "json", route...) }},
			{"denied-strict", "unavailable", func() result { return runLive(t, denied, policies, "strict", "json", route...) }},
			{"file-strict", "notRead", func() result {
				return runRoutes(t, kc, app.ProxyTestHooks{}, "proxy", "routes", "--file", dumpsDir+"policies.json", "--route", "kgwctl-proxy/orders", "-o", "json")
			}},
			{"file-exact", "notRead", func() result {
				return runRoutes(t, kc, app.ProxyTestHooks{}, "proxy", "routes", "--file", dumpsDir+"policies.json", "--route", "kgwctl-proxy/orders", "-o", "json", "--select", "exact")
			}},
		} {
			r := tc.run()
			var doc struct {
				Item struct {
					Selection struct {
						KnownHTTPRoutes string `json:"knownHTTPRoutes"`
					}
				}
			}
			if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil || r.code != 0 {
				t.Fatalf("%s: exit %d, err %v, stdout:\n%s", tc.name, r.code, err, r.stdout)
			}
			if got := doc.Item.Selection.KnownHTTPRoutes; got != tc.want {
				t.Errorf("%s: selection.knownHTTPRoutes %q, want %q", tc.name, got, tc.want)
			}
		}
	})

	t.Run("R24a-later-error", func(t *testing.T) {
		r := runLive(t, denied, []byte("{"), "known", "text", route...)
		lines := stderrLines(r.stderr)
		if r.code != 2 || r.stdout != "" || len(lines) != 2 || !strings.HasPrefix(lines[0], listWarning) || !strings.Contains(lines[1], "parse config dump") {
			t.Errorf("exit %d, stdout %q, stderr %q; want 2, empty, the list warning and the parse error", r.code, r.stdout, r.stderr)
		}
	})

	t.Run("write-error", func(t *testing.T) {
		for _, o := range []string{"text", "json"} {
			var errb bytes.Buffer
			cfg := app.Config{Build: testBuild, Out: failWriter{}, Err: &errb, Now: func() time.Time { return captureTime }}
			root := app.NewRootWithProxyHooks(cfg, app.ProxyTestHooks{Dialer: serveBody(t, policies)})
			args := append(append([]string{"--kubeconfig", env.Kubeconfig, "--context", denied}, live...), "--route", "kgwctl-proxy/orders", "--select", "known", "-o", o)
			code := run(root, args...)
			lines := stderrLines(errb.String())
			if code != 2 || len(lines) != 2 || !strings.HasPrefix(lines[0], listWarning) || !strings.Contains(lines[1], "write routes") {
				t.Errorf("%s: exit %d, stderr %q; want 2, the list warning and the write error", o, code, errb.String())
			}
		}
	})
}

// TestProxyRoutesExactFile: --select exact works with --file (R27 to R29).
func TestProxyRoutesExactFile(t *testing.T) {
	kc := unreachableKubeconfig(t, "default")
	for _, tc := range []struct {
		name string
		args []string
	}{
		// Route metadata: the exact route matches.
		{name: "exact-collide-meta-orders", args: []string{"--file", dumpsDir + "collide-meta.json", "--gateway", "kgwctl-proxy/proxy", "--route", "kgwctl-proxy/orders"}},
		// No metadata: nothing matches.
		{name: "exact-policies-orders", args: []string{"--file", dumpsDir + "policies.json", "--route", "kgwctl-proxy/orders"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, o := range []string{"text", "json"} {
				args := append(append([]string{"proxy", "routes", "-o", o}, tc.args...), "--select", "exact")
				r := runRoutes(t, kc, app.ProxyTestHooks{}, args...)
				if r.code != 0 || r.stderr != "" {
					t.Fatalf("%s: exit %d, stderr %q; want 0 and empty", o, r.code, r.stderr)
				}
				name := tc.name
				if o == "json" {
					name = "json-" + name
				}
				golden(t, "proxy-routes", name, r.stdout)
			}
		})
	}

	// R29: a route strict matches on a name reading is inferred under exact.
	args := []string{"proxy", "routes", "--file", "../proxyview/testdata/levels.json", "--route", "shop/orders"}
	strict := runRoutes(t, kc, app.ProxyTestHooks{}, args...)
	exact := runRoutes(t, kc, app.ProxyTestHooks{}, append(args, "--select", "exact")...)
	if strict.code != 0 || strings.Contains(strict.stdout, "not matched") || !strings.Contains(strict.stdout, "HTTPRoute shop/orders · rule 0") {
		t.Errorf("strict: exit %d, stdout:\n%s\nwant the route matched", strict.code, strict.stdout)
	}
	if exact.code != 0 || !strings.Contains(exact.stdout, "no route matches HTTPRoute shop/orders\ninferred routes, not matched:\n") {
		t.Errorf("exact: exit %d, stdout:\n%s\nwant the route listed as inferred", exact.code, exact.stdout)
	}
	j := runRoutes(t, kc, app.ProxyTestHooks{}, append(args, "--select", "exact", "-o", "json")...)
	var doc struct {
		Item struct {
			Selection struct {
				Strategy string
				Routes   []any
				Inferred []struct{ Name string }
			}
		}
	}
	if err := json.Unmarshal([]byte(j.stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if s := doc.Item.Selection; s.Strategy != "exact" || len(s.Routes) != 0 || len(s.Inferred) != 1 {
		t.Errorf("exact JSON selection %+v, want strategy exact, no route and one inferred", s)
	}
}

// inferredIndexCopies is a wrapper's extra: for each exact RouteSource claim,
// an agreeing copy whose indices are inferred.
func inferredIndexCopies(_ *model.Snapshot, inner []provenance.Claim) []provenance.Claim {
	var out []provenance.Claim
	for _, c := range inner {
		r, ok := c.Assertion.(provenance.RouteSource)
		if !ok || c.Certainty != model.Exact {
			continue
		}
		r.IndexCertainty = model.Inferred
		c.Assertion, c.Ref = r, ""
		out = append(out, c)
	}
	return out
}

// TestProxyRoutesExactAgreeingClaim: under --select exact --rule, an agreeing
// claim with inferred indices does not hide the metadata's exact indices (DD-4).
func TestProxyRoutesExactAgreeingClaim(t *testing.T) {
	kc := unreachableKubeconfig(t, "default")
	args := []string{"proxy", "routes", "--file", dumpsDir + "collide-meta.json", "--route", "kgwctl-proxy/orders", "--rule", "0", "--select", "exact"}
	plain := runRoutes(t, kc, app.ProxyTestHooks{}, args...)
	if plain.code != 0 || plain.stderr != "" || strings.Contains(plain.stdout, "not matched") || !strings.Contains(plain.stdout, "HTTPRoute kgwctl-proxy/orders · rule 0") {
		t.Fatalf("default module: exit %d, stderr %q, stdout:\n%s\nwant the route matched", plain.code, plain.stderr, plain.stdout)
	}
	// The row shows the exact indices the selection used, with no suffix.
	mods := []provenance.Module{proxyviewtest.Wrap("example", kgateway.New(), inferredIndexCopies)}
	text := runRoutesModules(t, kc, app.ProxyTestHooks{}, mods, args...)
	if text.code != 0 || text.stderr != "" || strings.Contains(text.stdout, "not matched") || strings.Contains(text.stdout, "partly from name") ||
		!strings.Contains(text.stdout, " · HTTPRoute kgwctl-proxy/orders · rule 0 · match 0\n") {
		t.Errorf("text with the agreeing claim: exit %d, stderr %q, stdout:\n%s\nwant the route matched with exact indices", text.code, text.stderr, text.stdout)
	}
	j := runRoutesModules(t, kc, app.ProxyTestHooks{}, mods, append(args, "-o", "json")...)
	var doc struct {
		Item struct {
			Listeners []struct {
				FilterChains []struct {
					HCM struct {
						VirtualHosts []struct {
							Routes []struct {
								Origin struct{ Readings []map[string]any }
							}
						}
					}
				}
			}
			Selection struct {
				Routes, Inferred []any
			}
		}
	}
	if err := json.Unmarshal([]byte(j.stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if s := doc.Item.Selection; len(s.Routes) != 1 || len(s.Inferred) != 0 {
		t.Errorf("JSON with the agreeing claim: selection %+v, want one route and none inferred", s)
	}
	// indexCertainty is absent when it equals the reading's certainty: exact here.
	var rds []map[string]any
	for _, l := range doc.Item.Listeners {
		for _, fc := range l.FilterChains {
			for _, vh := range fc.HCM.VirtualHosts {
				for _, r := range vh.Routes {
					rds = append(rds, r.Origin.Readings...)
				}
			}
		}
	}
	if len(rds) != 1 || rds[0]["certainty"] != "exact" || rds[0]["indexCertainty"] != nil {
		t.Errorf("JSON readings %v, want one exact reading without indexCertainty", rds)
	}
}

// TestProxyRoutesExactUnknownMatch: metadata with a rule index and no match,
// on a route name with no index. strict matches; exact --rule lists it as inferred (DD-4).
func TestProxyRoutesExactUnknownMatch(t *testing.T) {
	kc := unreachableKubeconfig(t, "default")
	args := []string{"proxy", "routes", "--file", "../proxyview/testdata/rule-no-match.json", "--route", "shop/orders", "--rule", "0"}
	strict := runRoutes(t, kc, app.ProxyTestHooks{}, args...)
	if strict.code != 0 || strict.stderr != "" || strings.Contains(strict.stdout, "not matched") || !strings.Contains(strict.stdout, " · HTTPRoute shop/orders · rule 0\n") {
		t.Errorf("strict: exit %d, stderr %q, stdout:\n%s\nwant the route matched", strict.code, strict.stderr, strict.stdout)
	}
	exact := runRoutes(t, kc, app.ProxyTestHooks{}, append(args, "--select", "exact")...)
	want := "no route matches HTTPRoute shop/orders rule 0\ninferred routes, not matched:\n  custom-orders-route · HTTPRoute shop/orders · rule 0\n"
	if exact.code != 0 || exact.stderr != "" || !strings.HasSuffix(exact.stdout, want) {
		t.Errorf("exact: exit %d, stderr %q, stdout:\n%s\nwant it to end with:\n%s", exact.code, exact.stderr, exact.stdout, want)
	}
	j := runRoutes(t, kc, app.ProxyTestHooks{}, append(args, "--select", "exact", "-o", "json")...)
	var doc struct {
		Item struct {
			Selection struct {
				Routes   []any
				Inferred []struct {
					Readings []struct{ RuleIndex, MatchIndex int }
				}
			}
		}
	}
	if err := json.Unmarshal([]byte(j.stdout), &doc); err != nil {
		t.Fatal(err)
	}
	s := doc.Item.Selection
	if len(s.Routes) != 0 || len(s.Inferred) != 1 || len(s.Inferred[0].Readings) != 1 || s.Inferred[0].Readings[0].MatchIndex != -1 {
		t.Errorf("exact JSON selection %+v, want no route and one inferred with match index -1", s)
	}
}

// otherRuleOnSecondUse is a wrapper's extra: on the second use of the shared
// route config, a proxy/orders-kgwctl reading with rule index 1.
func otherRuleOnSecondUse(snap *model.Snapshot, inner []provenance.Claim) []provenance.Claim {
	route := snap.Listeners[1].FilterChains[0].HCM.RouteConfig.VirtualHosts[0].Routes[0].ID
	var out []provenance.Claim
	for _, c := range inner {
		r, ok := c.Assertion.(provenance.RouteSource)
		if !ok || c.Subject != route || r.Route != proxyview.HTTPRouteRef(types.NamespacedName{Namespace: "proxy", Name: "orders-kgwctl"}) {
			continue
		}
		r.RuleIndex = 1
		c.Assertion, c.Ref = r, ""
		out = append(out, c)
	}
	return out
}

// TestProxyRoutesSharedSourceSameOutcome: two ambiguous uses of one route
// config whose readings differ only by index print two lines that show the indices (DD-5).
func TestProxyRoutesSharedSourceSameOutcome(t *testing.T) {
	kc := unreachableKubeconfig(t, "default")
	args := []string{"proxy", "routes", "--file", "../proxyview/testdata/shared-source.json", "--route", "kgwctl-proxy/orders", "--rule", "0"}
	mods := []provenance.Module{proxyviewtest.Wrap("example", kgateway.New(), otherRuleOnSecondUse)}
	r := runRoutesModules(t, kc, app.ProxyTestHooks{}, mods, args...)
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("exit %d, stderr %q; want 0 and empty", r.code, r.stderr)
	}
	const name = "orders_example-route-0-httproute-orders-kgwctl-proxy-0-0-matcher-0"
	if n := strings.Count(r.stdout, name); n != 2 || !strings.Contains(r.stdout, "HTTPRoute proxy/orders-kgwctl · rule 1") {
		t.Errorf("text lists the route %d times:\n%s\nwant two lines that show the rule indices", n, r.stdout)
	}
	golden(t, "proxy-routes", "shared-source-same-outcome", r.stdout)
}

// reversedOnSecondUse wraps a module and reverses the order of its
// RouteSource claims on the second use of the shared route config.
type reversedOnSecondUse struct{ provenance.Module }

func (reversedOnSecondUse) Name() string { return "example" }

func (m reversedOnSecondUse) Claims(snap *model.Snapshot) []provenance.Claim {
	claims := m.Module.Claims(snap)
	route := snap.Listeners[1].FilterChains[0].HCM.RouteConfig.VirtualHosts[0].Routes[0].ID
	var at []int
	for i, c := range claims {
		if _, ok := c.Assertion.(provenance.RouteSource); ok && c.Subject == route {
			at = append(at, i)
		}
	}
	for i, j := 0, len(at)-1; i < j; i, j = i+1, j-1 {
		claims[at[i]], claims[at[j]] = claims[at[j]], claims[at[i]]
	}
	return claims
}

// TestProxyRoutesSharedSourceClaimOrder: two uses with the same readings in
// a different claim order print once (DD-5).
func TestProxyRoutesSharedSourceClaimOrder(t *testing.T) {
	kc := unreachableKubeconfig(t, "default")
	args := []string{"proxy", "routes", "--file", "../proxyview/testdata/shared-source.json", "--route", "kgwctl-proxy/orders", "--rule", "0"}
	r := runRoutesModules(t, kc, app.ProxyTestHooks{}, []provenance.Module{reversedOnSecondUse{kgateway.New()}}, args...)
	const name = "orders_example-route-0-httproute-orders-kgwctl-proxy-0-0-matcher-0"
	if n := strings.Count(r.stdout, name); r.code != 0 || n != 1 {
		t.Errorf("exit %d, text lists the route %d times:\n%s\nwant once", r.code, n, r.stdout)
	}
}

// exactOnSecondUse is a wrapper's extra: on the second use of the shared route
// config, an exact kgwctl-proxy/orders reading with inferred indices.
func exactOnSecondUse(snap *model.Snapshot, inner []provenance.Claim) []provenance.Claim {
	route := snap.Listeners[1].FilterChains[0].HCM.RouteConfig.VirtualHosts[0].Routes[0].ID
	var out []provenance.Claim
	for _, c := range inner {
		r, ok := c.Assertion.(provenance.RouteSource)
		if !ok || c.Subject != route || r.Route != proxyview.HTTPRouteRef(types.NamespacedName{Namespace: "kgwctl-proxy", Name: "orders"}) {
			continue
		}
		r.IndexCertainty, r.Exhaustive = model.Inferred, true
		c.Assertion, c.Ref, c.Certainty = r, "", model.Exact
		out = append(out, c)
	}
	return out
}

// TestProxyRoutesSharedSourceOutcomes: two uses of one route config with
// different outcomes are both listed; equal outcomes print once (DD-5).
func TestProxyRoutesSharedSourceOutcomes(t *testing.T) {
	kc := unreachableKubeconfig(t, "default")
	args := []string{"proxy", "routes", "--file", "../proxyview/testdata/shared-source.json", "--route", "kgwctl-proxy/orders", "--rule", "0", "--select", "exact"}
	const name = "orders_example-route-0-httproute-orders-kgwctl-proxy-0-0-matcher-0"
	same := runRoutes(t, kc, app.ProxyTestHooks{}, args...)
	if same.code != 0 || strings.Count(same.stdout, name) != 1 {
		t.Errorf("equal outcomes: exit %d, stdout:\n%s\nwant the route listed once", same.code, same.stdout)
	}
	mods := []provenance.Module{proxyviewtest.Wrap("example", kgateway.New(), exactOnSecondUse)}
	r := runRoutesModules(t, kc, app.ProxyTestHooks{}, mods, args...)
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("exit %d, stderr %q; want 0 and empty", r.code, r.stderr)
	}
	for _, want := range []string{"inferred routes, not matched:\n", "ambiguous routes, not matched:\n"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("text lacks %q", want)
		}
	}
	if n := strings.Count(r.stdout, name); n != 2 {
		t.Errorf("text lists the route %d times, want once per outcome", n)
	}
	golden(t, "proxy-routes", "exact-shared-source-two-outcomes", r.stdout)
}
