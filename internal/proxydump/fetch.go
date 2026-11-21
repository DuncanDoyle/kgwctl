package proxydump

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/DuncanDoyle/kgwctl/internal/load"
)

// AdminPort is the port of Envoy's admin interface inside the proxy Pod.
const AdminPort = 19000

// ErrTooLarge means a response body exceeded its size limit.
var ErrTooLarge = errors.New("response exceeds size limit")

// Dialer opens a tunnel to a port of a Pod.
type Dialer interface {
	// Dial returns a stream connected to the port and a stop func that
	// releases the tunnel. It opens no listening socket.
	Dial(ctx context.Context, pod types.NamespacedName, port int) (conn io.ReadWriteCloser, stop func(), err error)
}

// Capture is one config dump response, streamed from the proxy.
type Capture struct {
	Pod        types.NamespacedName
	CapturedAt time.Time
	// Body streams the dump. Closing it also releases the tunnel; further
	// calls to Close do nothing.
	Body io.ReadCloser
}

// tunnelBody releases the tunnel exactly once, on Close or when ctx ends.
type tunnelBody struct {
	r         io.Reader
	ctx       context.Context
	release   func() error
	stopAfter func() bool
}

// Read reports the context's error when ctx ending cut the read short.
func (b *tunnelBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err != nil && b.ctx.Err() != nil {
		err = b.ctx.Err()
	}
	return n, err
}

func (b *tunnelBody) Close() error {
	b.stopAfter()
	return b.release()
}

// Fetch sends one GET /config_dump to the admin port of pod over the stream d
// opens. Redirects are refused. On success the caller must close Capture.Body;
// the tunnel is also released when ctx ends. On error it is already released.
func Fetch(ctx context.Context, d Dialer, pod types.NamespacedName, now func() time.Time) (*Capture, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open tunnel to %s: %w", pod, err)
	}
	conn, stop, err := d.Dial(ctx, pod, AdminPort)
	if err != nil {
		if stop != nil {
			stop()
		}
		return nil, fmt.Errorf("open tunnel to %s: %w", pod, err)
	}
	release := sync.OnceValue(func() error {
		err := conn.Close()
		stop()
		return err
	})
	stopAfter := context.AfterFunc(ctx, func() { _ = release() })
	fail := func(err error) (*Capture, error) {
		stopAfter()
		_ = release()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, fmt.Errorf("fetch config dump from %s: %w", pod, err)
	}
	if ctx.Err() != nil {
		return fail(ctx.Err())
	}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://localhost:%d/config_dump", AdminPort), nil)
	if err != nil {
		return fail(err)
	}
	req.Close = true // one request per stream
	if err := req.Write(conn); err != nil {
		return fail(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	switch {
	case err != nil:
		return fail(err)
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return fail(fmt.Errorf("admin endpoint returned %s; refusing to follow redirect", resp.Status))
	case resp.StatusCode != http.StatusOK:
		return fail(fmt.Errorf("admin endpoint returned %s", resp.Status))
	}
	// Closing the stream ends the body; resp.Body.Close would drain it first.
	body := &tunnelBody{r: resp.Body, ctx: ctx, release: release, stopAfter: stopAfter}
	return &Capture{Pod: pod, CapturedAt: now(), Body: body}, nil
}

// FetchError is a failed Fetch from the Pod that FetchGateway selected.
type FetchError struct {
	Pod types.NamespacedName
	Err error
}

func (e *FetchError) Error() string { return e.Err.Error() }
func (e *FetchError) Unwrap() error { return e.Err }

// FetchGateway selects the replica of gw to dump with SelectPod and fetches
// its config dump. It returns ctx's error by ctx's deadline even when the
// selection or the dial ignores ctx, as client-go's exec credential plugins
// do; a capture that completes later is released. A failed fetch is a
// *FetchError naming the Pod.
func FetchGateway(ctx context.Context, c load.Clients, d Dialer, gw types.NamespacedName, pod string, now func() time.Time) (*Capture, error) {
	type result struct {
		capture *Capture
		err     error
	}
	done := make(chan result, 1)
	go func() {
		target, err := SelectPod(ctx, c, gw, pod)
		if err != nil {
			done <- result{err: err}
			return
		}
		capture, err := Fetch(ctx, d, target, now)
		if err != nil {
			err = &FetchError{Pod: target, Err: err}
		}
		done <- result{capture, err}
	}()
	select {
	case r := <-done:
		return r.capture, r.err
	case <-ctx.Done():
		go func() {
			if r := <-done; r.capture != nil {
				_ = r.capture.Body.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

// ReadCapped reads all of r and returns ErrTooLarge when it holds more than
// max bytes.
func ReadCapped(r io.Reader, max int64) ([]byte, error) {
	limit := max
	if max < math.MaxInt64 {
		limit = max + 1 // one byte over tells "at the cap" from "over it"
	}
	b, err := io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%w of %d bytes", ErrTooLarge, max)
	}
	return b, nil
}
