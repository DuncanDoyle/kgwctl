package app_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/DuncanDoyle/kgwctl/internal/app"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
)

const (
	idleGateway   = "testdata/proxy/idle-gateway.yaml"
	readerBinding = "testdata/proxy/reader-binding.yaml"
	rbacYAML      = "../../docs/rbac.yaml"
	// waitBound is a failsafe for channel waits, never a pacing delay.
	waitBound = 10 * time.Second
)

// dumpArgs dumps the check fixture's Gateway, which has one Ready replica.
var dumpArgs = []string{"proxy", "dump", "http", "-n", "kgateway-system"}

// fakeDialer serves h in place of a port-forward to the admin port.
type fakeDialer struct{ srv *httptest.Server }

func newFakeDialer(t *testing.T, h http.HandlerFunc) fakeDialer {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return fakeDialer{srv: srv}
}

func (d fakeDialer) Dial(context.Context, types.NamespacedName, int) (string, func(), error) {
	return d.srv.URL, func() {}, nil
}

// runProxy runs the root command with h's dialer against kubeconfig.
func runProxy(t *testing.T, kubeconfig string, h app.ProxyTestHooks, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	root := app.NewRootWithProxyHooks(app.Config{Build: testBuild, Out: &out, Err: &errb, Now: testNow}, h)
	start := time.Now()
	code := run(root, append([]string{"--kubeconfig", kubeconfig}, args...)...)
	return result{code: code, stdout: out.String(), stderr: errb.String(), took: time.Since(start)}
}

func TestProxyDumpStreams(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{checkInstall}})
	// 3 MiB of arbitrary bytes: streamed unchanged, with no size cap.
	body := make([]byte, 3<<20)
	for i := range body {
		body[i] = byte(rand.IntN(256))
	}
	paths := make(chan string, 4)
	d := newFakeDialer(t, func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.RequestURI()
		_, _ = w.Write(body)
	})
	r := runProxy(t, env.Kubeconfig, app.ProxyTestHooks{Dialer: d}, dumpArgs...)
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("exit %d, stderr %q; want 0 and empty", r.code, r.stderr)
	}
	if !bytes.Equal([]byte(r.stdout), body) {
		t.Fatalf("stdout differs from the dump (%d bytes, want %d)", len(r.stdout), len(body))
	}
	if len(paths) != 1 {
		t.Fatalf("got %d requests, want 1", len(paths))
	}
	if p := <-paths; p != "/config_dump" {
		t.Fatalf("request = %q, want /config_dump", p)
	}
}

func TestProxyDumpNoGatewayArg(t *testing.T) {
	env := newEnv(t, testenv.Options{})
	r := runProxy(t, env.Kubeconfig, app.ProxyTestHooks{}, "proxy", "dump")
	if r.code != 2 || r.stdout != "" {
		t.Fatalf("exit %d, stdout %q; want 2 and empty", r.code, r.stdout)
	}
	if !strings.Contains(r.stderr, "Gateway") || !strings.Contains(r.stderr, "Usage:") {
		t.Fatalf("stderr = %q, want a usage error naming the Gateway", r.stderr)
	}
}

func TestProxyDumpNoReplica(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{idleGateway}})
	r := runProxy(t, env.Kubeconfig, app.ProxyTestHooks{}, "proxy", "dump", "idle", "-n", "kgateway-system")
	if r.code != 2 || r.stdout != "" {
		t.Fatalf("exit %d, stdout %q; want 2 and empty", r.code, r.stdout)
	}
	if !strings.Contains(r.stderr, "kgateway-system/idle") || !strings.Contains(r.stderr, "--pod") {
		t.Fatalf("stderr = %q, want the Gateway and --pod", r.stderr)
	}
}

func TestProxyDumpPortForwardForbidden(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{checkInstall}})
	env.Proxies[0].Rule(testenv.Match{Resource: "pods", Subresource: "portforward"}, testenv.Action{Status: http.StatusForbidden})
	r := runProxy(t, env.Kubeconfig, app.ProxyTestHooks{}, dumpArgs...)
	if r.code != 2 || r.stdout != "" {
		t.Fatalf("exit %d, stdout %q; want 2 and empty; stderr %q", r.code, r.stdout, r.stderr)
	}
	for _, want := range []string{"create pods/portforward", "docs/rbac.yaml"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("stderr = %q, want %q", r.stderr, want)
		}
	}
}

func TestProxyDumpDeadline(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{checkInstall}})
	d := newFakeDialer(t, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	r := runProxy(t, env.Kubeconfig, app.ProxyTestHooks{Dialer: d}, append(dumpArgs, "--request-timeout", "300ms")...)
	if r.code != 2 || r.stdout != "" {
		t.Fatalf("exit %d, stdout %q; want 2 and empty; stderr %q", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr, "--request-timeout") {
		t.Errorf("stderr = %q, want the deadline named", r.stderr)
	}
	if r.took > 3*time.Second {
		t.Fatalf("returned after %v, want within 3s", r.took)
	}
}

// holdPortForward makes env's proxy hold every pods/portforward request until the test ends.
func holdPortForward(t *testing.T, env *testenv.Env) *testenv.RuleHits {
	t.Helper()
	held := make(chan struct{})
	t.Cleanup(func() { close(held) })
	return env.Proxies[0].Rule(testenv.Match{Resource: "pods", Subresource: "portforward"}, testenv.Action{Until: held})
}

// requireUpgradeAbandoned checks that the port-forward request arrived and its client went away.
func requireUpgradeAbandoned(t *testing.T, hits *testenv.RuleHits) {
	t.Helper()
	if hits.N() != 1 {
		t.Fatalf("port-forward requests = %d, want 1", hits.N())
	}
	select {
	case <-hits.ClientGone():
	case <-time.After(waitBound):
		t.Fatal("the stalled port-forward connection was never closed")
	}
}

func TestProxyDumpUpgradeHangs(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{checkInstall}})
	hits := holdPortForward(t, env)
	r := runProxy(t, env.Kubeconfig, app.ProxyTestHooks{}, append(dumpArgs, "--request-timeout", "2s")...)
	if r.code != 2 || r.stdout != "" {
		t.Fatalf("exit %d, stdout %q; want 2 and empty; stderr %q", r.code, r.stdout, r.stderr)
	}
	if r.took > 5*time.Second {
		t.Fatalf("returned after %v, want within 5s", r.took)
	}
	requireUpgradeAbandoned(t, hits)
}

func TestProxyDumpUpgradeHangsViaProxy(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start func(t *testing.T) *tunnelProxy
	}{
		{name: "http-connect", start: startConnectProxy},
		{name: "socks5", start: startSOCKS5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newEnv(t, testenv.Options{Fixtures: []string{checkInstall}})
			hits := holdPortForward(t, env)
			tp := tc.start(t)
			kc := withProxyURL(t, env.Kubeconfig, tp.url)
			const timeout = 2 * time.Second
			r := runProxy(t, kc, app.ProxyTestHooks{}, append(dumpArgs, "--request-timeout", timeout.String())...)
			if r.code != 2 || r.stdout != "" {
				t.Fatalf("exit %d, stdout %q; want 2 and empty; stderr %q", r.code, r.stdout, r.stderr)
			}
			if r.took > timeout+3*time.Second {
				t.Fatalf("returned after %v, want within %v", r.took, timeout+3*time.Second)
			}
			requireUpgradeAbandoned(t, hits)
			select {
			case <-tp.upgradeClosed:
			case <-time.After(waitBound):
				t.Fatal("the proxy never saw kgwctl close the port-forward tunnel")
			}
			if tp.tunnels() == 0 {
				t.Fatal("no request went through the proxy")
			}
		})
	}
}

// TestCheckAsReader: the kgwctl-reader ClusterRole from docs/rbac.yaml alone serves check.
func TestCheckAsReader(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController, checkRouteBroken, rbacYAML, readerBinding)
	admin := runCheck(t, env, statsRegistry(fakeStats{}), fastHooks(metricsScraper{}), "check")
	reader := *env
	reader.Kubeconfig = env.AsUser(t, "kgwctl-test-reader")
	r := runCheck(t, &reader, statsRegistry(fakeStats{}), fastHooks(metricsScraper{}), "check")
	if admin.code != 1 || !strings.Contains(admin.stdout, "KGWUI-013") {
		t.Fatalf("admin run: exit %d, want 1 with KGWUI-013\n%s", admin.code, admin.stdout)
	}
	if r.code != admin.code || r.stderr != "" {
		t.Fatalf("reader run: exit %d, stderr %q; want %d and empty", r.code, r.stderr, admin.code)
	}
	if got, want := normalizeTimes(r.stdout), normalizeTimes(admin.stdout); got != want {
		t.Fatalf("reader report differs from admin's\n--- reader ---\n%s\n--- admin ---\n%s", got, want)
	}
}

// withProxyURL copies kubeconfig with every cluster's proxy-url set to proxyURL.
func withProxyURL(t *testing.T, kubeconfig, proxyURL string) string {
	t.Helper()
	kc, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range kc.Clusters {
		c.ProxyURL = proxyURL
	}
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*kc, path); err != nil {
		t.Fatal(err)
	}
	return path
}

// tunnelProxy is a test proxy between kgwctl and the API server. It closes
// upgradeClosed once the client closes a tunnel that carried a port-forward.
type tunnelProxy struct {
	url           string
	upgradeClosed chan struct{}
	once          sync.Once
	mu            sync.Mutex
	conns         []net.Conn
	n             int
}

func newTunnelProxy(t *testing.T) (*tunnelProxy, net.Listener) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tp := &tunnelProxy{upgradeClosed: make(chan struct{})}
	t.Cleanup(func() {
		_ = ln.Close()
		tp.mu.Lock()
		defer tp.mu.Unlock()
		for _, c := range tp.conns {
			_ = c.Close()
		}
	})
	return tp, ln
}

func (tp *tunnelProxy) track(c net.Conn) {
	tp.mu.Lock()
	tp.conns = append(tp.conns, c)
	tp.mu.Unlock()
}

func (tp *tunnelProxy) tunnels() int {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.n
}

// pipe relays client (with any bytes already buffered in br) to target. It never
// closes client itself, so a finished client→target copy means the client closed.
func (tp *tunnelProxy) pipe(client net.Conn, br *bufio.Reader, target string) {
	up, err := net.Dial("tcp", target)
	if err != nil {
		_ = client.Close()
		return
	}
	tp.track(up)
	tp.mu.Lock()
	tp.n++
	tp.mu.Unlock()
	go func() { _, _ = io.Copy(client, up) }()
	// Plain HTTP inside the tunnel: the request line shows a port-forward.
	first := make([]byte, 4096)
	n, err := br.Read(first)
	if n > 0 {
		_, _ = up.Write(first[:n])
	}
	upgrade := bytes.Contains(first[:n], []byte("/portforward"))
	if err == nil {
		_, _ = io.Copy(up, br)
	}
	_ = up.Close()
	if upgrade {
		tp.once.Do(func() { close(tp.upgradeClosed) })
	}
}

// startConnectProxy serves HTTP CONNECT tunnels and plain forward-proxy requests.
func startConnectProxy(t *testing.T) *tunnelProxy {
	tp, ln := newTunnelProxy(t)
	tp.url = "http://" + ln.Addr().String()
	forward := &httputil.ReverseProxy{Rewrite: func(*httputil.ProxyRequest) {}, FlushInterval: -1}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			forward.ServeHTTP(w, r)
			return
		}
		c, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		tp.track(c)
		_, _ = c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		tp.pipe(c, brw.Reader, r.Host)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return tp
}

// startSOCKS5 serves SOCKS5 CONNECT without authentication (RFC 1928).
func startSOCKS5(t *testing.T) *tunnelProxy {
	tp, ln := newTunnelProxy(t)
	tp.url = "socks5://" + ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			tp.track(c)
			go func() {
				br := bufio.NewReader(c)
				target, err := socksHandshake(br, c)
				if err != nil {
					_ = c.Close()
					return
				}
				tp.pipe(c, br, target)
			}()
		}
	}()
	return tp
}

func socksHandshake(br *bufio.Reader, w io.Writer) (string, error) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(br, hdr); err != nil || hdr[0] != 5 {
		return "", errors.New("bad greeting")
	}
	if _, err := io.ReadFull(br, make([]byte, hdr[1])); err != nil {
		return "", err
	}
	if _, err := w.Write([]byte{5, 0}); err != nil {
		return "", err
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil || req[1] != 1 {
		return "", errors.New("bad request")
	}
	var host string
	switch req[3] {
	case 1:
		ip := make([]byte, 4)
		if _, err := io.ReadFull(br, ip); err != nil {
			return "", err
		}
		host = net.IP(ip).String()
	case 3:
		l, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		name := make([]byte, l)
		if _, err := io.ReadFull(br, name); err != nil {
			return "", err
		}
		host = string(name)
	default:
		return "", errors.New("unsupported address type")
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(br, port); err != nil {
		return "", err
	}
	if _, err := w.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port)))), nil
}
