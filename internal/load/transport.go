package load

import (
	"context"
	"io"
	"net/http"
	"sync"
)

// BindContext returns a transport wrapper that ties every request to ctx: when
// ctx ends, in-flight requests are cancelled and new ones fail with ctx.Err().
// It is the WrapTransport that bounds all of a command's I/O (design §4.4).
func BindContext(ctx context.Context) func(http.RoundTripper) http.RoundTripper {
	return func(next http.RoundTripper) http.RoundTripper { return &boundTransport{ctx: ctx, next: next} }
}

type boundTransport struct {
	ctx  context.Context
	next http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (b *boundTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := b.ctx.Err(); err != nil {
		return nil, err
	}
	rctx, cancel := context.WithCancelCause(req.Context())
	stop := context.AfterFunc(b.ctx, func() { cancel(context.Cause(b.ctx)) })
	release := func() { stop(); cancel(nil) }
	resp, err := b.next.RoundTrip(req.WithContext(rctx))
	if err != nil {
		release()
		if b.ctx.Err() != nil {
			return nil, b.ctx.Err()
		}
		return nil, err
	}
	// Streams (watches) outlive RoundTrip, so the merge ends when the body closes.
	resp.Body = &boundBody{ReadCloser: resp.Body, release: release}
	return resp, nil
}

type boundBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

// Close implements io.Closer.
func (b *boundBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}
