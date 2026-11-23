package proxydump

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

// waitBound is a failsafe for channel waits, never a pacing delay.
const waitBound = 10 * time.Second

var testPod = types.NamespacedName{Namespace: "gw-ns", Name: "gw-abc"}

// fakeTunnel stands in for a port-forward: a loopback listener in front of a
// handler. Closure is observed server-side.
type fakeTunnel struct {
	ln      net.Listener
	srv     *http.Server
	stops   atomic.Int32
	mu      sync.Mutex
	reqs    []*http.Request
	closed  chan struct{} // receives one value per connection reaching StateClosed
	stopped chan struct{} // closed by the first stop
}

func newFakeTunnel(t *testing.T, h http.HandlerFunc) *fakeTunnel {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ft := &fakeTunnel{ln: ln, closed: make(chan struct{}, 16), stopped: make(chan struct{})}
	ft.srv = &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ft.mu.Lock()
			ft.reqs = append(ft.reqs, r)
			ft.mu.Unlock()
			h(w, r)
		}),
		ConnState: func(_ net.Conn, s http.ConnState) {
			if s == http.StateClosed {
				ft.closed <- struct{}{}
			}
		},
	}
	go func() { _ = ft.srv.Serve(ln) }()
	t.Cleanup(func() { _ = ft.srv.Close() })
	return ft
}

// Dial implements Dialer with a connection to the handler.
func (ft *fakeTunnel) Dial(_ context.Context, _ types.NamespacedName, port int) (io.ReadWriteCloser, func(), error) {
	if port != AdminPort {
		return nil, nil, errors.New("unexpected port")
	}
	c, err := net.Dial("tcp", ft.ln.Addr().String())
	if err != nil {
		return nil, nil, err
	}
	return c, func() {
		_ = ft.ln.Close()
		if ft.stops.Add(1) == 1 {
			close(ft.stopped)
		}
	}, nil
}

func (ft *fakeTunnel) requests() []*http.Request {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return append([]*http.Request(nil), ft.reqs...)
}

func (ft *fakeTunnel) requireListenerClosed(t *testing.T) {
	t.Helper()
	if n := ft.stops.Load(); n != 1 {
		t.Fatalf("stop called %d times, want 1", n)
	}
	c, err := net.Dial("tcp", ft.ln.Addr().String())
	if err == nil {
		_ = c.Close()
		t.Fatal("tunnel listener still accepts connections")
	}
}

// waitListenerClosed waits for the tunnel to be released without the test
// closing anything.
func (ft *fakeTunnel) waitListenerClosed(t *testing.T) {
	t.Helper()
	select {
	case <-ft.stopped:
	case <-time.After(waitBound):
		t.Fatal("tunnel never released")
	}
	ft.requireListenerClosed(t)
}

func (ft *fakeTunnel) requireConnClosed(t *testing.T) {
	t.Helper()
	select {
	case <-ft.closed:
	case <-time.After(waitBound):
		t.Fatal("server never saw the connection close")
	}
}

func TestFetchOneGet(t *testing.T) {
	want := []byte(`{"configs":[]}`)
	ft := newFakeTunnel(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(want) })
	at := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

	c, err := Fetch(context.Background(), ft, testPod, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	if c.Pod != testPod || !c.CapturedAt.Equal(at) {
		t.Fatalf("capture = %+v", c)
	}
	got, err := io.ReadAll(c.Body)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("body = %q, %v", got, err)
	}
	if err := c.Body.Close(); err != nil {
		t.Fatal(err)
	}
	ft.requireConnClosed(t)
	ft.requireListenerClosed(t)

	reqs := ft.requests()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	if r := reqs[0]; r.Method != http.MethodGet || r.URL.Path != "/config_dump" || r.URL.RawQuery != "" {
		t.Fatalf("request = %s %s", r.Method, r.URL.String())
	}
}

func TestFetchRefusesRedirect(t *testing.T) {
	ft := newFakeTunnel(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})
	_, err := Fetch(context.Background(), ft, testPod, time.Now)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("redirect")) {
		t.Fatalf("err = %v, want redirect refusal", err)
	}
	for _, r := range ft.requests() {
		if r.URL.Path == "/elsewhere" {
			t.Fatal("redirect target was requested")
		}
	}
	ft.requireConnClosed(t)
	ft.requireListenerClosed(t)
}

func TestFetchNon200(t *testing.T) {
	ft := newFakeTunnel(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	_, err := Fetch(context.Background(), ft, testPod, time.Now)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("503")) {
		t.Fatalf("err = %v, want status 503", err)
	}
	ft.requireConnClosed(t)
	ft.requireListenerClosed(t)
}

// blockMidBody writes a partial body, then waits for the client to go away.
func blockMidBody(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("partial"))
	w.(http.Flusher).Flush()
	<-r.Context().Done()
}

func TestFetchDeadline(t *testing.T) {
	ft := newFakeTunnel(t, blockMidBody)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	c, err := Fetch(ctx, ft, testPod, time.Now)
	if err != nil {
		t.Fatalf("Fetch: %v (headers were flushed, want nil)", err)
	}
	// The stalled body read must hit the deadline; Body is never closed here.
	if _, err := io.ReadAll(c.Body); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read err = %v, want DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("returned after %s, want under 2s", d)
	}
	ft.requireConnClosed(t)
	ft.waitListenerClosed(t)
}

// TestFetchDialError: a dial that fails after opening its tunnel is still released.
func TestFetchDialError(t *testing.T) {
	ft := newFakeTunnel(t, func(http.ResponseWriter, *http.Request) {})
	dialErr := errors.New("dial failed")
	d := dialerFunc(func(ctx context.Context, pod types.NamespacedName, port int) (io.ReadWriteCloser, func(), error) {
		c, stop, err := ft.Dial(ctx, pod, port)
		if err != nil {
			return nil, nil, err
		}
		_ = c.Close()
		return nil, stop, dialErr
	})
	_, err := Fetch(context.Background(), d, testPod, time.Now)
	if !errors.Is(err, dialErr) {
		t.Fatalf("err = %v, want %v", err, dialErr)
	}
	ft.requireListenerClosed(t)
}

// holdBound releases a held fake on its own, so code that waits for it fails
// the elapsed-time check instead of hanging the suite.
const holdBound = 3 * time.Second

// hold blocks until held closes or holdBound passes, ignoring any ctx.
func hold(held <-chan struct{}) {
	select {
	case <-held:
	case <-time.After(holdBound):
	}
}

// heldDialer dials ft once held closes, ignoring ctx as an exec credential
// plugin does.
func heldDialer(ft *fakeTunnel, held <-chan struct{}) Dialer {
	return dialerFunc(func(ctx context.Context, pod types.NamespacedName, port int) (io.ReadWriteCloser, func(), error) {
		hold(held)
		return ft.Dial(ctx, pod, port)
	})
}

// TestFetchDialIgnoresCtx: Fetch returns ctx's error by the deadline when the
// dial ignores ctx; the tunnel the dial opens late is released unused.
func TestFetchDialIgnoresCtx(t *testing.T) {
	ft := newFakeTunnel(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{}")) })
	held := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	c, err := Fetch(ctx, heldDialer(ft, held), testPod, time.Now)
	if !errors.Is(err, context.DeadlineExceeded) || c != nil {
		t.Fatalf("Fetch = %v, %v; want nil and DeadlineExceeded", c, err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("returned after %v, want by the 200ms deadline", took)
	}
	close(held)
	ft.requireConnClosed(t)
	ft.waitListenerClosed(t)
	if n := len(ft.requests()); n != 0 {
		t.Fatalf("late tunnel served %d requests, want 0", n)
	}
}

// TestAwaitDoneWins: a result that is ready together with ctx's end never
// wins, and a successful one is released. select picks a ready case at
// random, so the runs cover both cases.
func TestAwaitDoneWins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 1; i <= 100; i++ {
		done := make(chan outcome[int], 1)
		done <- outcome[int]{v: i}
		released := make(chan int, 1)
		v, err := settle(ctx, done, func(v int) { released <- v })
		if !errors.Is(err, context.Canceled) || v != 0 {
			t.Fatalf("run %d: settle = %d, %v; want 0 and Canceled", i, v, err)
		}
		select {
		case r := <-released:
			if r != i {
				t.Fatalf("run %d: released %d", i, r)
			}
		case <-time.After(waitBound):
			t.Fatalf("run %d: the late result was never released", i)
		}
	}
}

// lateStream answers the GET with a full 200 once held closes. Its Close does
// not interrupt that read, like a stream that ignores ctx.
type lateStream struct {
	held   <-chan struct{}
	r      io.Reader
	closes atomic.Int32
	stops  atomic.Int32
	built  chan struct{} // closed when Fetch stamps the late Capture
}

func newLateStream(held <-chan struct{}) *lateStream {
	return &lateStream{held: held, built: make(chan struct{}),
		r: strings.NewReader("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}")}
}

func (s *lateStream) Read(p []byte) (int, error) {
	hold(s.held)
	return s.r.Read(p)
}

func (s *lateStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *lateStream) Close() error                { s.closes.Add(1); return nil }

func (s *lateStream) dialer() Dialer {
	return dialerFunc(func(context.Context, types.NamespacedName, int) (io.ReadWriteCloser, func(), error) {
		return s, func() { s.stops.Add(1) }, nil
	})
}

func (s *lateStream) now() time.Time {
	close(s.built)
	return time.Now()
}

// requireReleased waits for the late Capture and checks its stream is closed
// and its tunnel stopped once.
func (s *lateStream) requireReleased(t *testing.T) {
	t.Helper()
	select {
	case <-s.built:
	case <-time.After(waitBound):
		t.Fatal("the late Capture was never built")
	}
	if c, st := s.closes.Load(), s.stops.Load(); c < 1 || st != 1 {
		t.Fatalf("stream closed %d times, tunnel stopped %d times; want closed, stopped once", c, st)
	}
}

// TestFetchLateCapture: a 200 whose headers arrive after ctx ended is never
// returned; Fetch returns ctx's error by the deadline and releases the stream.
func TestFetchLateCapture(t *testing.T) {
	held := make(chan struct{})
	s := newLateStream(held)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	c, err := Fetch(ctx, s.dialer(), testPod, s.now)
	if !errors.Is(err, context.DeadlineExceeded) || c != nil {
		t.Fatalf("Fetch = %v, %v; want nil and DeadlineExceeded", c, err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("returned after %v, want by the 200ms deadline", took)
	}
	close(held)
	s.requireReleased(t)
}

// TestAwaitReleaseOffCaller: a blocking release never delays the return,
// whichever ready case select picks.
func TestAwaitReleaseOffCaller(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	block := make(chan struct{})
	defer close(block)
	for i := 1; i <= 20; i++ {
		done := make(chan outcome[int], 1)
		done <- outcome[int]{v: i}
		start := time.Now()
		if _, err := settle(ctx, done, func(int) { hold(block) }); !errors.Is(err, context.Canceled) {
			t.Fatalf("run %d: err = %v, want Canceled", i, err)
		}
		if took := time.Since(start); took > 2*time.Second {
			t.Fatalf("run %d: returned after %v; release ran on the caller's goroutine", i, took)
		}
	}
}

type dialerFunc func(context.Context, types.NamespacedName, int) (io.ReadWriteCloser, func(), error)

func (f dialerFunc) Dial(ctx context.Context, pod types.NamespacedName, port int) (io.ReadWriteCloser, func(), error) {
	return f(ctx, pod, port)
}

// TestFetchOpUnknown: an operation outside the allowlist fails before a dial.
func TestFetchOpUnknown(t *testing.T) {
	for _, op := range []Op{Op(99), Op(-1)} {
		var dials atomic.Int32
		d := dialerFunc(func(context.Context, types.NamespacedName, int) (io.ReadWriteCloser, func(), error) {
			dials.Add(1)
			return nil, nil, errors.New("dialed")
		})
		c, err := FetchOp(context.Background(), d, testPod, op, time.Now)
		if err == nil {
			_ = c.Body.Close()
			t.Fatalf("FetchOp(%d) succeeded, want an unknown-operation error", int(op))
		}
		if n := dials.Load(); n != 0 {
			t.Fatalf("FetchOp(%d) dialed %d times, want 0", int(op), n)
		}
	}
}

func TestFetchCloseStopsTunnel(t *testing.T) {
	ft := newFakeTunnel(t, blockMidBody)
	c, err := Fetch(context.Background(), ft, testPod, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 3)
	if _, err := io.ReadFull(c.Body, buf); err != nil {
		t.Fatal(err)
	}
	if err := c.Body.Close(); err != nil {
		t.Fatal(err)
	}
	ft.requireConnClosed(t)
	ft.requireListenerClosed(t)
	_ = c.Body.Close() // second close must not release the tunnel again
	if n := ft.stops.Load(); n != 1 {
		t.Fatalf("stop called %d times after second Close, want 1", n)
	}
}

func TestReadCappedOver(t *testing.T) {
	_, err := ReadCapped(bytes.NewReader(make([]byte, 65)), 64)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("64")) {
		t.Fatalf("err %q does not name the limit", err)
	}
}

func TestReadCappedAt(t *testing.T) {
	got, err := ReadCapped(bytes.NewReader(make([]byte, 64)), 64)
	if err != nil || len(got) != 64 {
		t.Fatalf("len=%d err=%v", len(got), err)
	}
}

func TestReadCappedMaxInt(t *testing.T) {
	got, err := ReadCapped(bytes.NewReader(make([]byte, 10)), math.MaxInt64)
	if err != nil || len(got) != 10 {
		t.Fatalf("len=%d err=%v", len(got), err)
	}
}
