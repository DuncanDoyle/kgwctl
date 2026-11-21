package proxydump

import (
	"bufio"
	"bytes"
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
	srv     *httptest.Server
	body    []byte
	fail    string
	gone    chan struct{} // closed when the upgraded connection closes
	done    chan struct{} // closed at test end, so the handler returns
	mu      sync.Mutex
	streams []http.Header
	reqs    []*http.Request
}

func newSPDYPod(t *testing.T, body []byte, fail string) *spdyPod {
	t.Helper()
	p := &spdyPod{body: body, fail: fail, gone: make(chan struct{}), done: make(chan struct{})}
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

// TestPortForwardOpensNoListener: Dial yields the forwarded stream itself, not
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
