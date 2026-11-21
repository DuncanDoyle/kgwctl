package proxydump

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

// AdminPort is the port of Envoy's admin interface inside the proxy Pod.
const AdminPort = 19000

// ErrTooLarge means a response body exceeded its size limit.
var ErrTooLarge = errors.New("response exceeds size limit")

// Dialer opens a tunnel to a port of a Pod.
type Dialer interface {
	// Dial returns the base URL that reaches the port and a stop func that
	// releases the tunnel.
	Dial(ctx context.Context, pod types.NamespacedName, port int) (baseURL string, stop func(), err error)
}

// Capture is one config dump response, streamed from the proxy.
type Capture struct {
	Pod        types.NamespacedName
	CapturedAt time.Time
	// Body streams the dump. Closing it also releases the tunnel; further
	// calls to Close do nothing.
	Body io.ReadCloser
}

// tunnelBody releases the tunnel exactly once, after the body is closed.
type tunnelBody struct {
	io.ReadCloser
	once sync.Once
	err  error
	stop func()
}

func (b *tunnelBody) Close() error {
	b.once.Do(func() {
		b.err = b.ReadCloser.Close()
		b.stop()
	})
	return b.err
}

// Fetch sends one GET /config_dump to the admin port of pod through d.
// Redirects are refused. On success the caller must close Capture.Body; on
// error the tunnel is already released.
func Fetch(ctx context.Context, d Dialer, pod types.NamespacedName, now func() time.Time) (*Capture, error) {
	base, stop, err := d.Dial(ctx, pod, AdminPort)
	if err != nil {
		if stop != nil {
			stop()
		}
		return nil, fmt.Errorf("open tunnel to %s: %w", pod, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base, "/")+"/config_dump", nil)
	if err != nil {
		stop()
		return nil, err
	}
	// Keep-alives off so closing the body closes the connection.
	client := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("refusing to follow redirect")
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		stop()
		return nil, fmt.Errorf("fetch config dump from %s: %w", pod, err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		stop()
		return nil, fmt.Errorf("fetch config dump from %s: admin endpoint returned %s", pod, resp.Status)
	}
	return &Capture{Pod: pod, CapturedAt: now(), Body: &tunnelBody{ReadCloser: resp.Body, stop: stop}}, nil
}

// ReadCapped reads all of r and returns ErrTooLarge when it holds more than
// max bytes.
func ReadCapped(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%w of %d bytes", ErrTooLarge, max)
	}
	return b, nil
}
