package proxydump

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	spdystream "k8s.io/apimachinery/pkg/util/httpstream/spdy"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
)

// spdyPod is an API server that accepts a port-forward upgrade and answers the
// data stream as Envoy's admin port would, or fails the forward with fail.
type spdyPod struct {
	srv  *httptest.Server
	body []byte
	fail string
	// lateFail, when set, answers 200 with a close-delimited body, ends the
	// data stream, and fails the forward with it after lateFailAfter.
	lateFail string
	// openErrs, when set, answers 200 with a close-delimited body, ends the
	// data stream, and keeps the error stream open.
	openErrs bool
	// stall, when set, answers 200 with body and keeps the data stream open.
	stall bool
	// handler, when set, answers the data stream's request.
	handler http.Handler
	gone    chan struct{} // closed when the upgraded connection closes
	done    chan struct{} // closed at test end, so the handler returns
	mu      sync.Mutex
	streams []http.Header
	reqs    []*http.Request
}

func newSPDYPod(t *testing.T, body []byte, fail string) *spdyPod {
	t.Helper()
	return startSPDYPod(t, &spdyPod{body: body, fail: fail})
}

// newSPDYPodWith answers the forwarded request with h.
func newSPDYPodWith(t *testing.T, h http.Handler) *spdyPod {
	t.Helper()
	return startSPDYPod(t, &spdyPod{handler: h})
}

// startSPDYPod serves p; its fields are set before the server starts.
func startSPDYPod(t *testing.T, p *spdyPod) *spdyPod {
	t.Helper()
	p.gone, p.done = make(chan struct{}), make(chan struct{})
	p.srv = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.srv.Close)
	t.Cleanup(func() { close(p.done) }) // runs first: cleanups are LIFO
	return p
}

func (p *spdyPod) config() *rest.Config { return &rest.Config{Host: p.srv.URL} }

func (p *spdyPod) serve(w http.ResponseWriter, r *http.Request) {
	if _, err := httpstream.Handshake(r, w, []string{portforward.PortForwardProtocolV1Name}); err != nil {
		return
	}
	accepted := make(chan httpstream.Stream, 4)
	conn := spdystream.NewResponseUpgrader().UpgradeResponse(w, r, func(s httpstream.Stream, _ <-chan struct{}) error {
		accepted <- s
		return nil
	})
	if conn == nil {
		return
	}
	defer conn.Close()
	defer func() {
		select {
		case <-conn.CloseChan():
			close(p.gone)
		case <-p.done:
		}
	}()
	var errStream, data httpstream.Stream
	for errStream == nil || data == nil {
		select {
		case s := <-accepted:
			p.mu.Lock()
			p.streams = append(p.streams, s.Headers())
			p.mu.Unlock()
			switch s.Headers().Get(corev1.StreamType) {
			case corev1.StreamTypeError:
				errStream = s
			case corev1.StreamTypeData:
				data = s
			}
		case <-conn.CloseChan():
			return
		case <-p.done:
			return
		}
	}
	if p.fail != "" {
		_, _ = errStream.Write([]byte(p.fail))
		_ = errStream.Close()
		_ = data.Close()
		return
	}
	req, err := http.ReadRequest(bufio.NewReader(data))
	if err != nil {
		return
	}
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
	if p.handler != nil {
		rec := httptest.NewRecorder()
		p.handler.ServeHTTP(rec, req)
		_ = rec.Result().Write(data)
		_ = data.Close()
		return
	}
	if p.stall {
		_, _ = io.WriteString(data, "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\n")
		_, _ = data.Write(p.body)
		return // the deferred wait holds the connection, data stream open
	}
	if p.lateFail != "" || p.openErrs {
		_, _ = io.WriteString(data, "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\n")
		_, _ = data.Write(p.body)
		_ = data.Close()
		if p.openErrs {
			return // the deferred wait holds the connection, error stream open
		}
		select {
		case <-time.After(lateFailAfter):
		case <-p.done:
			return
		}
		_, _ = errStream.Write([]byte(p.lateFail))
		_ = errStream.Close()
		return
	}
	resp := &http.Response{StatusCode: http.StatusOK, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{},
		ContentLength: int64(len(p.body)), Body: io.NopCloser(bytes.NewReader(p.body))}
	_ = resp.Write(data)
}

func (p *spdyPod) waitGone(t *testing.T) {
	t.Helper()
	select {
	case <-p.gone:
	case <-time.After(waitBound):
		t.Fatal("the port-forward connection was never closed")
	}
}

// TestPortForwardServesGET: the GET goes over the port-forward's data stream,
// on the admin port, and closing the body closes the connection.
func TestPortForwardServesGET(t *testing.T) {
	want := []byte(`{"configs":[]}`)
	p := newSPDYPod(t, want, "")
	c, err := Fetch(t.Context(), NewPortForwarder(p.config()), testPod, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(c.Body)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("body = %q, %v", got, err)
	}
	if err := c.Body.Close(); err != nil {
		t.Fatal(err)
	}
	p.waitGone(t)

	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reqs) != 1 || p.reqs[0].Method != http.MethodGet || p.reqs[0].URL.Path != "/config_dump" {
		t.Fatalf("requests = %v, want one GET /config_dump", p.reqs)
	}
	if len(p.streams) != 2 {
		t.Fatalf("got %d streams, want an error and a data stream", len(p.streams))
	}
	for _, h := range p.streams {
		if h.Get(corev1.PortHeader) != "19000" || h.Get(corev1.PortForwardRequestIDHeader) != p.streams[0].Get(corev1.PortForwardRequestIDHeader) {
			t.Fatalf("stream headers = %v, want port 19000 and one request ID", p.streams)
		}
	}
}

// TestPortForwardErrorStream: a forward the remote side fails, such as nothing
// listening on the admin port, is an error that carries its message.
func TestPortForwardErrorStream(t *testing.T) {
	const msg = "failed to connect to localhost:19000 inside namespace: connection refused"
	p := newSPDYPod(t, nil, msg)
	c, err := Fetch(t.Context(), NewPortForwarder(p.config()), testPod, time.Now)
	if err == nil {
		_ = c.Body.Close()
		t.Fatal("Fetch succeeded, want the forward's error")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v, want the error stream's message", err)
	}
	p.waitGone(t)
}

// lateFailAfter is how long the error stream trails the data stream's end:
// longer than any fixed grace a read could wait for it.
const lateFailAfter = 1500 * time.Millisecond

// TestPortForwardLateErrorStream: a forward that fails after the data stream
// ended mid-body is a read error, not a truncated dump read as complete.
func TestPortForwardLateErrorStream(t *testing.T) {
	const msg = "error forwarding port 19000: connection reset by peer"
	p := startSPDYPod(t, &spdyPod{body: []byte(`{"configs":[`), lateFail: msg})
	ctx, cancel := context.WithTimeout(t.Context(), waitBound)
	defer cancel()
	c, err := Fetch(ctx, NewPortForwarder(p.config()), testPod, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Body.Close()
	got, err := readAllBounded(t, c.Body, lateFailAfter+2*time.Second)
	if err == nil || !strings.Contains(err.Error(), "connection reset by peer") {
		t.Fatalf("read = %q, %v; want the error stream's message", got, err)
	}
}

// readAllBounded reads all of r in a goroutine and fails the test when the
// read has not ended within bound.
func readAllBounded(t *testing.T, r io.Reader, bound time.Duration) ([]byte, error) {
	t.Helper()
	type result struct {
		b   []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		b, err := io.ReadAll(r)
		done <- result{b, err}
	}()
	select {
	case res := <-done:
		return res.b, res.err
	case <-time.After(bound):
		t.Fatalf("read still blocked after %v", bound)
		return nil, nil
	}
}

// TestPortForwardCloseEndsErrorWait: Close during a read that waits on an
// open error stream ends the read with an error, not a clean EOF.
func TestPortForwardCloseEndsErrorWait(t *testing.T) {
	p := startSPDYPod(t, &spdyPod{body: []byte(`{"configs":[`), openErrs: true})
	conn, stop, err := NewPortForwarder(p.config()).Dial(t.Context(), testPod, AdminPort)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	waiting := make(chan struct{})
	conn.(*portStream).waiting = func() { close(waiting) }
	req, err := http.NewRequest(http.MethodGet, "http://localhost/config_dump", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(resp.Body)
		done <- err
	}()
	select {
	case <-waiting:
	case <-time.After(waitBound):
		t.Fatal("the read never waited for the error stream")
	}
	_ = conn.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("read after Close ended cleanly, want an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read still blocked 2s after Close")
	}
	stop()
	p.waitGone(t)
}

// TestPortForwardCtxEndsErrorWait: ctx's end during a read that waits on an
// open error stream ends the read with ctx's error.
func TestPortForwardCtxEndsErrorWait(t *testing.T) {
	p := startSPDYPod(t, &spdyPod{body: []byte(`{"configs":[`), openErrs: true})
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	c, err := Fetch(ctx, NewPortForwarder(p.config()), testPod, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Body.Close()
	if _, err := readAllBounded(t, c.Body, 2*time.Second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read err = %v, want DeadlineExceeded", err)
	}
	p.waitGone(t)
}

// TestPortForwardOpensNoListener:Dial yields the forwarded stream itself, not
// an address, so only kgwctl's own request reaches the tunnel.
func TestPortForwardOpensNoListener(t *testing.T) {
	want := []byte(`{"configs":[]}`)
	p := newSPDYPod(t, want, "")
	conn, stop, err := NewPortForwarder(p.config()).Dial(t.Context(), testPod, AdminPort)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if _, ok := conn.(net.Conn); ok {
		t.Fatalf("Dial returned a socket (%T), want the port-forward stream", conn)
	}
	req, err := http.NewRequest(http.MethodGet, "http://localhost/config_dump", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("body = %q, %v", got, err)
	}
	stop()
	p.waitGone(t)
}

// opTargets is each Op's request target from design §6.3.
var opTargets = []struct {
	op     Op
	target string
}{
	{OpConfigDump, "/config_dump"},
	{OpConfigDumpEDS, "/config_dump?include_eds"},
	{OpStats, "/stats"},
	{OpServerInfo, "/server_info"},
	{OpCerts, "/certs"},
	{OpClusters, "/clusters"},
}

// requireOneRequest checks p got exactly GET target on the admin port.
func requireOneRequest(t *testing.T, p *spdyPod, target string) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(p.reqs))
	}
	if r := p.reqs[0]; r.Method != http.MethodGet || r.RequestURI != target || r.Proto != "HTTP/1.1" {
		t.Fatalf("request line = %s %s %s, want GET %s HTTP/1.1", r.Method, r.RequestURI, r.Proto, target)
	}
	for _, h := range p.streams {
		if h.Get(corev1.PortHeader) != "19000" {
			t.Fatalf("stream headers = %v, want port 19000", p.streams)
		}
	}
}

// TestFetchOpTargets: each Op sends exactly its request line on the admin port.
func TestFetchOpTargets(t *testing.T) {
	for _, tc := range opTargets {
		t.Run(tc.op.String(), func(t *testing.T) {
			want := []byte("body of " + tc.target)
			p := newSPDYPod(t, want, "")
			c, err := FetchOp(t.Context(), NewPortForwarder(p.config()), testPod, tc.op, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(c.Body)
			_ = c.Body.Close()
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("body = %q, %v", got, err)
			}
			if c.Op != tc.op || c.Pod != testPod {
				t.Fatalf("capture op %v pod %v, want %v %v", c.Op, c.Pod, tc.op, testPod)
			}
			if tc.op.Code().String() != tc.op.String() {
				t.Fatalf("Code() = %v, want the manifest code named %q", tc.op.Code(), tc.op.String())
			}
			p.waitGone(t)
			requireOneRequest(t, p, tc.target)
		})
	}
}

// TestFetchBareConfigDump: Fetch is the config-dump operation, with no query.
func TestFetchBareConfigDump(t *testing.T) {
	p := newSPDYPod(t, []byte(`{"configs":[]}`), "")
	c, err := Fetch(t.Context(), NewPortForwarder(p.config()), testPod, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Body.Close()
	if c.Op != OpConfigDump {
		t.Fatalf("capture op = %v, want %v", c.Op, OpConfigDump)
	}
	p.waitGone(t)
	requireOneRequest(t, p, "/config_dump")
}

// TestFetchOpRedirect: a redirect is refused for every Op, and its target
// appears in no error.
func TestFetchOpRedirect(t *testing.T) {
	const location = "http://redirect-target.invalid/elsewhere-canary"
	for _, tc := range opTargets {
		t.Run(tc.op.String(), func(t *testing.T) {
			p := newSPDYPodWith(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", location)
				w.WriteHeader(http.StatusFound)
			}))
			c, err := FetchOp(t.Context(), NewPortForwarder(p.config()), testPod, tc.op, time.Now)
			if err == nil {
				_ = c.Body.Close()
				t.Fatal("FetchOp followed or accepted a redirect")
			}
			if !strings.Contains(err.Error(), "redirect") || strings.Contains(err.Error(), "canary") ||
				strings.Contains(err.Error(), "redirect-target") {
				t.Fatalf("err = %v, want a redirect refusal without the Location", err)
			}
			p.waitGone(t)
			requireOneRequest(t, p, tc.target)
		})
	}
}
