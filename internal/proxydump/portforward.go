package proxydump

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"golang.org/x/net/proxy"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/httpstream"
	spdystream "k8s.io/apimachinery/pkg/util/httpstream/spdy"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// stopGrace bounds how long a stop waits for the forwarder goroutine to end.
const stopGrace = time.Second

// maxStatusBody caps how much of a rejected upgrade's body is read.
const maxStatusBody = 64 << 10

// NewPortForwarder returns a Dialer that port-forwards through the API server
// in cfg to an OS-assigned port on 127.0.0.1. The tunnel stops when ctx ends or
// stop is called; no socket it opened outlives either, also through a proxy.
func NewPortForwarder(cfg *rest.Config) Dialer { return &portForwarder{cfg: cfg} }

type portForwarder struct{ cfg *rest.Config }

// Dial implements Dialer. A rejected upgrade returns the API server's status
// as a typed error, so a denied create pods/portforward is a Forbidden.
func (p *portForwarder) Dial(ctx context.Context, pod types.NamespacedName, port int) (string, func(), error) {
	kc, err := kubernetes.NewForConfig(p.cfg)
	if err != nil {
		return "", nil, fmt.Errorf("build kube client: %w", err)
	}
	u := kc.CoreV1().RESTClient().Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("portforward").URL()
	conns := &connSet{}
	d, err := newUpgradeDialer(ctx, p.cfg, u, conns)
	if err != nil {
		conns.closeAll()
		return "", nil, err
	}
	stopCh, readyCh, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	fw, err := portforward.NewOnAddresses(d, []string{"127.0.0.1"}, []string{"0:" + strconv.Itoa(port)}, stopCh, readyCh, io.Discard, io.Discard)
	if err != nil {
		conns.closeAll()
		return "", nil, err
	}
	var fwErr error
	go func() { fwErr = fw.ForwardPorts(); close(done) }()

	var once sync.Once
	stop := func() {
		once.Do(func() {
			close(stopCh)
			// Closing the sockets ends ForwardPorts even mid-upgrade; wait for it, bounded.
			conns.closeAll()
			select {
			case <-done:
			case <-time.After(stopGrace):
			}
		})
	}
	select {
	case <-readyCh:
	case <-done:
		stop()
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		if d.err != nil {
			return "", nil, d.err // typed, unlike ForwardPorts' wrapping
		}
		return "", nil, fwErr
	case <-ctx.Done():
		stop()
		return "", nil, ctx.Err()
	}
	ports, err := fw.GetPorts()
	if err != nil || len(ports) != 1 {
		stop()
		return "", nil, fmt.Errorf("port-forward has no local port: %v", err)
	}
	stopAfter := context.AfterFunc(ctx, stop)
	return "http://127.0.0.1:" + strconv.Itoa(int(ports[0].Local)), func() { stopAfter(); stop() }, nil
}

// upgradeDialer is spdy.NewDialer with the upgrade bound to ctx. It keeps the
// typed error of a rejected upgrade, since ForwardPorts flattens it to text.
type upgradeDialer struct {
	ctx      context.Context
	upgrader spdy.Upgrader
	client   *http.Client
	url      *url.URL
	err      error
}

// newUpgradeDialer sends the upgrade over sockets it dials itself, directly or
// through the proxy client-go would use, and closes them when ctx ends.
func newUpgradeDialer(ctx context.Context, cfg *rest.Config, u *url.URL, conns *connSet) (*upgradeDialer, error) {
	tlsConfig, err := rest.TLSConfigFor(cfg)
	if err != nil {
		return nil, err
	}
	// A nil config makes the upgrade skip verification; an empty one verifies against system roots.
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	}
	proxyFor := http.ProxyFromEnvironment
	if cfg.Proxy != nil {
		proxyFor = cfg.Proxy
	}
	proxyURL, err := proxyFor(&http.Request{URL: u})
	if err != nil {
		return nil, fmt.Errorf("resolve proxy for %s: %w", u.Host, err)
	}
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialVia(ctx, proxyURL, tlsConfig, network, addr, conns)
	}
	upgrader, err := spdystream.NewRoundTripperWithConfig(spdystream.RoundTripperConfig{
		PingPeriod:       5 * time.Second,
		UpgradeTransport: &http.Transport{TLSClientConfig: tlsConfig, DialContext: dial},
	})
	if err != nil {
		return nil, err
	}
	wrapped, err := rest.HTTPWrappersForConfig(cfg, upgrader)
	if err != nil {
		return nil, err
	}
	// Auth wrappers re-add credentials on every hop, so a redirect is never followed.
	client := &http.Client{
		Transport:     wrapped,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &upgradeDialer{ctx: ctx, upgrader: upgrader, client: client, url: u}, nil
}

// Dial implements httpstream.Dialer.
func (d *upgradeDialer) Dial(protocols ...string) (httpstream.Connection, string, error) {
	req, err := http.NewRequestWithContext(d.ctx, http.MethodPost, d.url.String(), nil)
	if err != nil {
		return nil, "", err
	}
	for _, p := range protocols {
		req.Header.Add(httpstream.HeaderProtocolVersion, p)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		if d.ctx.Err() != nil {
			err = d.ctx.Err()
		}
		d.err = err
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		d.err = fmt.Errorf("port-forward upgrade at %s was redirected (%s); redirects are not followed", d.url.Host, resp.Status)
		return nil, "", d.err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		d.err = statusError(resp)
		return nil, "", d.err
	}
	conn, err := d.upgrader.NewConnection(resp)
	if err != nil {
		d.err = err
		return nil, "", err
	}
	return conn, resp.Header.Get(httpstream.HeaderProtocolVersion), nil
}

// statusError turns a rejected upgrade into the API server's typed error.
func statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxStatusBody))
	var st metav1.Status
	if json.Unmarshal(body, &st) == nil && st.Kind == "Status" && st.Code != 0 {
		return &apierrors.StatusError{ErrStatus: st}
	}
	return apierrors.NewGenericServerResponse(resp.StatusCode, http.MethodPost,
		schema.GroupResource{Resource: "pods/portforward"}, "", string(body), 0, false)
}

// connSet holds the raw sockets of one tunnel, so they can all be closed.
type connSet struct {
	mu     sync.Mutex
	socks  []sock
	closed bool
}

// sock is one raw socket and the unregister func of its ctx callback.
type sock struct {
	conn      net.Conn
	stopAfter func() bool
}

// add keeps c and closes it when ctx ends, or closes it at once when the set
// is already closed.
func (s *connSet) add(ctx context.Context, c net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		_ = c.Close()
		return
	}
	s.socks = append(s.socks, sock{conn: c, stopAfter: context.AfterFunc(ctx, func() { _ = c.Close() })})
}

func (s *connSet) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for _, k := range s.socks {
		k.stopAfter()
		_ = k.conn.Close()
	}
	s.socks = nil
}

// dialVia opens a TCP socket to addr, through proxyURL when set (HTTP CONNECT
// or SOCKS5), and closes it when ctx ends. An https proxy is verified with
// proxyTLS. Every socket goes into conns.
func dialVia(ctx context.Context, proxyURL *url.URL, proxyTLS *tls.Config, network, addr string, conns *connSet) (net.Conn, error) {
	raw := func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		c, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		conns.add(ctx, c)
		return c, nil
	}
	if proxyURL == nil {
		return raw(ctx, network, addr)
	}
	switch proxyURL.Scheme {
	case "socks5", "socks5h":
		var auth *proxy.Auth
		if proxyURL.User != nil {
			pass, _ := proxyURL.User.Password()
			auth = &proxy.Auth{User: proxyURL.User.Username(), Password: pass}
		}
		pd, err := proxy.SOCKS5("tcp", hostPort(proxyURL), auth, contextDialer(raw))
		if err != nil {
			return nil, err
		}
		return pd.(proxy.ContextDialer).DialContext(ctx, network, addr)
	case "http", "https", "":
		return dialConnect(ctx, proxyURL, proxyTLS, addr, raw)
	}
	return nil, fmt.Errorf("proxy URL scheme %q not supported", proxyURL.Scheme)
}

// contextDialer adapts a dial func to proxy.ContextDialer.
type contextDialer func(ctx context.Context, network, addr string) (net.Conn, error)

func (f contextDialer) Dial(network, addr string) (net.Conn, error) {
	return f(context.Background(), network, addr)
}

func (f contextDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return f(ctx, network, addr)
}

// dialConnect opens a tunnel to addr with HTTP CONNECT through proxyURL. Like
// net/http, it verifies an https proxy with the API server's TLS config.
func dialConnect(ctx context.Context, proxyURL *url.URL, proxyTLS *tls.Config, addr string, raw contextDialer) (net.Conn, error) {
	c, err := raw(ctx, "tcp", hostPort(proxyURL))
	if err != nil {
		return nil, err
	}
	if proxyURL.Scheme == "https" {
		cfg := &tls.Config{}
		if proxyTLS != nil {
			cfg = proxyTLS.Clone()
		}
		if cfg.ServerName == "" {
			cfg.ServerName = proxyURL.Hostname()
		}
		cfg.NextProtos = []string{"http/1.1"} // CONNECT is sent as HTTP/1.1
		tc := tls.Client(c, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = c.Close()
			return nil, err
		}
		c = tc
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: addr}, Host: addr, Header: http.Header{}}
	if u := proxyURL.User; u != nil {
		pass, _ := u.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(u.Username()+":"+pass)))
	}
	if err := req.Write(c); err != nil {
		_ = c.Close()
		return nil, err
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_ = c.Close()
		return nil, fmt.Errorf("CONNECT through proxy %s: %s", proxyURL.Redacted(), resp.Status)
	}
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: c, r: br}, nil
	}
	return c, nil
}

// bufferedConn reads bytes the CONNECT response read ahead before the socket.
type bufferedConn struct {
	net.Conn
	r io.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// hostPort is u's host with the scheme's default port when none is given.
func hostPort(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}
	switch u.Scheme {
	case "https":
		return net.JoinHostPort(u.Hostname(), "443")
	case "socks5", "socks5h":
		return net.JoinHostPort(u.Hostname(), "1080")
	}
	return net.JoinHostPort(u.Hostname(), "80")
}
