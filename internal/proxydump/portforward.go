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
	"sync/atomic"
	"time"

	"golang.org/x/net/proxy"
	corev1 "k8s.io/api/core/v1"
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

// maxStatusBody caps how much of a rejected upgrade's body is read.
const maxStatusBody = 64 << 10

// NewPortForwarder returns a Dialer that port-forwards through the API server
// in cfg and speaks to the port over the forward's data stream, in process. The
// tunnel stops when ctx ends or stop is called; no socket it opened outlives
// either, also through a proxy.
func NewPortForwarder(cfg *rest.Config) Dialer { return &portForwarder{cfg: cfg} }

type portForwarder struct{ cfg *rest.Config }

// Dial implements Dialer. A rejected upgrade returns the API server's status
// as a typed error, so a denied create pods/portforward is a Forbidden.
func (p *portForwarder) Dial(ctx context.Context, pod types.NamespacedName, port int) (io.ReadWriteCloser, func(), error) {
	kc, err := kubernetes.NewForConfig(p.cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("build kube client: %w", err)
	}
	u := kc.CoreV1().RESTClient().Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("portforward").URL()
	conns := &connSet{}
	d, err := newUpgradeDialer(ctx, p.cfg, u, conns)
	if err != nil {
		conns.closeAll()
		return nil, nil, err
	}
	conn, protocol, err := d.Dial(portforward.PortForwardProtocolV1Name)
	if err != nil {
		conns.closeAll()
		return nil, nil, err
	}
	stop := sync.OnceFunc(func() {
		_ = conn.Close()
		conns.closeAll()
	})
	stopAfter := context.AfterFunc(ctx, stop)
	release := func() { stopAfter(); stop() }
	if protocol != portforward.PortForwardProtocolV1Name {
		release()
		return nil, nil, fmt.Errorf("port-forward to %s: server speaks %q, not %q", pod, protocol, portforward.PortForwardProtocolV1Name)
	}
	s, err := openPortStream(conn, port)
	if err != nil {
		release()
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, err
	}
	return s, release, nil
}

// portStream is one forwarded connection: the data stream, plus the error
// stream on which the remote side reports a forward that failed.
type portStream struct {
	data    httpstream.Stream
	errs    httpstream.Stream
	port    int
	closed  atomic.Bool
	done    chan struct{} // closed once the error stream is read to its end
	remote  error         // the remote side's error; set before done closes
	waiting func()        // tests: runs as a read starts waiting for the error stream
}

// openPortStream creates the error and data streams for port on conn, with
// the headers client-go's port forwarder sends.
func openPortStream(conn httpstream.Connection, port int) (*portStream, error) {
	h := http.Header{}
	h.Set(corev1.StreamType, corev1.StreamTypeError)
	h.Set(corev1.PortHeader, strconv.Itoa(port))
	h.Set(corev1.PortForwardRequestIDHeader, "0")
	errs, err := conn.CreateStream(h)
	if err != nil {
		return nil, fmt.Errorf("create error stream for port %d: %w", port, err)
	}
	_ = errs.Close() // kgwctl writes nothing on it
	h.Set(corev1.StreamType, corev1.StreamTypeData)
	data, err := conn.CreateStream(h)
	if err != nil {
		_ = errs.Reset()
		return nil, fmt.Errorf("create data stream for port %d: %w", port, err)
	}
	s := &portStream{data: data, errs: errs, port: port, done: make(chan struct{})}
	go s.watch()
	return s, nil
}

// watch reads the error stream; a message fails the forward and ends reads.
func (s *portStream) watch() {
	msg, _ := io.ReadAll(s.errs)
	if len(msg) > 0 {
		s.remote = fmt.Errorf("port-forward to port %d failed: %s", s.port, msg)
	}
	close(s.done)
	if s.remote != nil {
		_ = s.data.Reset()
	}
}

// Read returns the remote side's error, if it reported one, in place of the
// data stream's end. After Close it returns net.ErrClosed, never a clean EOF.
func (s *portStream) Read(p []byte) (int, error) {
	n, err := s.data.Read(p)
	if err == nil {
		return n, nil
	}
	if s.closed.Load() {
		return n, net.ErrClosed
	}
	// The error stream can trail the data stream's end. Close, and the tunnel's
	// stop on ctx end, also end the error stream, so this wait always ends.
	if s.waiting != nil {
		s.waiting()
	}
	<-s.done
	switch {
	case s.closed.Load():
		return n, net.ErrClosed
	case s.remote != nil:
		return n, s.remote
	}
	return n, err
}

func (s *portStream) Write(p []byte) (int, error) { return s.data.Write(p) }

// Close resets both streams.
func (s *portStream) Close() error {
	s.closed.Store(true)
	_ = s.errs.Reset()
	return s.data.Reset()
}

// upgradeDialer is spdy.NewDialer with the upgrade bound to ctx. It returns
// the typed error of a rejected upgrade.
type upgradeDialer struct {
	ctx      context.Context
	upgrader spdy.Upgrader
	client   *http.Client
	url      *url.URL
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
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, "", fmt.Errorf("port-forward upgrade at %s was redirected (%s); redirects are not followed", d.url.Host, resp.Status)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, "", statusError(resp)
	}
	conn, err := d.upgrader.NewConnection(resp)
	if err != nil {
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
