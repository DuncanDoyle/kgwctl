package proxydump

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/DuncanDoyle/kgwctl/internal/bundle"
)

// TestPortForwardStatusNoBody: a refused upgrade is a typed error with the
// code and reason only; the body, Status or not, reaches no error.
func TestPortForwardStatusNoBody(t *testing.T) {
	const canary = "kgwctl-body-canary"
	for name, body := range map[string]string{
		"status": `{"kind":"Status","apiVersion":"v1","status":"Failure","message":"user ` + canary +
			` cannot create pods/portforward","reason":"Forbidden","code":403}`,
		"text": "denied for " + canary,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, body)
			}))
			t.Cleanup(srv.Close)
			_, _, err := NewPortForwarder(&rest.Config{Host: srv.URL}).Dial(t.Context(), testPod, AdminPort)
			if !apierrors.IsForbidden(err) {
				t.Fatalf("err = %v, want a Forbidden", err)
			}
			var se *apierrors.StatusError
			if !errors.As(err, &se) {
				t.Fatalf("err = %T, want a *StatusError", err)
			}
			st := se.Status()
			if st.Code != http.StatusForbidden || st.Reason != metav1.StatusReasonForbidden {
				t.Fatalf("status = %d %s, want 403 Forbidden", st.Code, st.Reason)
			}
			if s := fmt.Sprintf("%v %+v", err, st); strings.Contains(s, canary) {
				t.Fatalf("error carries the body: %s", s)
			}
		})
	}
}

// TestPortForwardGuardedReason: through the bundle guard, a server's reason
// phrase reaches no port-forward error, for a redirect or a refusal.
func TestPortForwardGuardedReason(t *testing.T) {
	const canary = "kgwctl-canary-reason"
	for _, code := range []int{http.StatusFound, http.StatusForbidden} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ln.Close() })
			go func() {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				if _, err := http.ReadRequest(bufio.NewReader(c)); err != nil {
					return
				}
				// A custom reason phrase; httptest cannot write one.
				fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nLocation: /elsewhere\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", code, canary)
			}()
			cfg := bundle.Guard(&rest.Config{Host: "http://" + ln.Addr().String()})
			_, _, err = NewPortForwarder(cfg).Dial(t.Context(), testPod, AdminPort)
			if err == nil {
				t.Fatal("dial succeeded")
			}
			if s := fmt.Sprintf("%v %+v", err, err); strings.Contains(s, canary) {
				t.Fatalf("error carries the reason phrase: %s", s)
			}
		})
	}
}

// tunnelProxy is an HTTP CONNECT or SOCKS5 proxy that relays to the target.
type tunnelProxy struct {
	url     *url.URL
	tunnels atomic.Int32
	closed  chan struct{} // closed once a client closes a relayed connection
	once    sync.Once
}

func startTunnelProxy(t *testing.T, socks bool) *tunnelProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	scheme := "http"
	if socks {
		scheme = "socks5"
	}
	tp := &tunnelProxy{url: &url.URL{Scheme: scheme, Host: ln.Addr().String()}, closed: make(chan struct{})}
	var mu sync.Mutex
	var conns []net.Conn
	track := func(c net.Conn) {
		mu.Lock()
		conns = append(conns, c)
		mu.Unlock()
	}
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			track(c)
			go tp.serve(c, socks, track)
		}
	}()
	return tp
}

// serve relays one client connection; it never closes the client first.
func (tp *tunnelProxy) serve(c net.Conn, socks bool, track func(net.Conn)) {
	defer c.Close()
	br := bufio.NewReader(c)
	var target string
	if socks {
		var err error
		if target, err = socks5Handshake(br, c); err != nil {
			return
		}
	} else {
		req, err := http.ReadRequest(br)
		if err != nil || req.Method != http.MethodConnect {
			return
		}
		target = req.Host
	}
	up, err := net.Dial("tcp", target)
	if err != nil {
		return
	}
	track(up)
	defer up.Close()
	ok := []byte("HTTP/1.1 200 Connection established\r\n\r\n")
	if socks {
		ok = []byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}
	}
	if _, err := c.Write(ok); err != nil {
		return
	}
	tp.tunnels.Add(1)
	go func() { _, _ = io.Copy(c, up) }()
	_, _ = io.Copy(up, br) // ends when the client closes its side
	tp.once.Do(func() { close(tp.closed) })
}

// socks5Handshake accepts no-auth SOCKS5 CONNECT (RFC 1928) and returns the target.
func socks5Handshake(br *bufio.Reader, w io.Writer) (string, error) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(br, hdr); err != nil || hdr[0] != 5 {
		return "", errors.New("not SOCKS5")
	}
	if _, err := io.ReadFull(br, make([]byte, hdr[1])); err != nil {
		return "", err
	}
	if _, err := w.Write([]byte{5, 0}); err != nil {
		return "", err
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil || req[1] != 1 {
		return "", errors.New("not a CONNECT")
	}
	var host string
	switch req[3] {
	case 1, 4:
		ip := make([]byte, 4)
		if req[3] == 4 {
			ip = make([]byte, 16)
		}
		if _, err := io.ReadFull(br, ip); err != nil {
			return "", err
		}
		host = net.IP(ip).String()
	case 3:
		n, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		name := make([]byte, n)
		if _, err := io.ReadFull(br, name); err != nil {
			return "", err
		}
		host = string(name)
	default:
		return "", errors.New("unknown address type")
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(br, port); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port[0])<<8|int(port[1]))), nil
}

// TestFetchCancelThroughProxy: an established capture through a proxy ends
// when ctx is cancelled, closing the tunnel and the proxy connection.
func TestFetchCancelThroughProxy(t *testing.T) {
	for _, socks := range []bool{false, true} {
		name := "http-connect"
		if socks {
			name = "socks5"
		}
		t.Run(name, func(t *testing.T) {
			want := []byte(`{"configs":[`)
			p := startSPDYPod(t, &spdyPod{body: want, stall: true})
			tp := startTunnelProxy(t, socks)
			cfg := p.config()
			cfg.Proxy = http.ProxyURL(tp.url)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c, err := Fetch(ctx, NewPortForwarder(cfg), testPod, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Body.Close()
			got := make([]byte, len(want))
			if _, err := io.ReadFull(c.Body, got); err != nil || !bytes.Equal(got, want) {
				t.Fatalf("body = %q, %v", got, err)
			}
			if tp.tunnels.Load() == 0 {
				t.Fatal("no connection went through the proxy")
			}
			done := make(chan error, 1)
			go func() {
				_, err := io.ReadAll(c.Body)
				done <- err
			}()
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("read err = %v, want Canceled", err)
				}
			case <-time.After(waitBound):
				t.Fatal("read still blocked after cancel")
			}
			p.waitGone(t)
			select {
			case <-tp.closed:
			case <-time.After(waitBound):
				t.Fatal("the proxy never saw the client close its connection")
			}
		})
	}
}

// TestPortForwardRefusesRedirect: a redirected upgrade is an error and the
// redirect target never sees a request, so no credentials follow it.
func TestPortForwardRefusesRedirect(t *testing.T) {
	var elsewhere atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	t.Cleanup(second.Close)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(first.Close)

	cfg := &rest.Config{Host: first.URL, BearerToken: "secret-token"}
	_, _, err := NewPortForwarder(cfg).Dial(t.Context(), testPod, AdminPort)
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("err = %v, want a redirect error", err)
	}
	if n := elsewhere.Load(); n != 0 {
		t.Fatalf("redirect target got %d requests, want 0", n)
	}
}

// TestDialViaUnregistersOnClose: closing the socket set also drops each
// socket's ctx callback, so a long-lived ctx keeps no closed socket alive.
func TestDialViaUnregistersOnClose(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
		}
	}()

	conns := &connSet{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := dialVia(ctx, nil, nil, "tcp", ln.Addr().String(), conns); err != nil {
		t.Fatal(err)
	}
	conns.mu.Lock()
	if len(conns.socks) != 1 {
		conns.mu.Unlock()
		t.Fatalf("socket set holds %d sockets, want 1", len(conns.socks))
	}
	unregister := conns.socks[0].stopAfter
	conns.mu.Unlock()

	conns.closeAll()
	if unregister() {
		t.Fatal("the socket's ctx callback was still registered after closeAll")
	}
}
