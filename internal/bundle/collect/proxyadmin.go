package collect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/DuncanDoyle/kgwctl/internal/proxydump"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// adminCapture is one admin operation, its file and its content type.
type adminCapture struct {
	op   proxydump.Op
	file string
	// write is the Sink method for the content type (design §7).
	write func(pb.Sink) func(context.Context, pb.Path, io.Reader, pb.Source) error
}

func configDumpWrite(s pb.Sink) func(context.Context, pb.Path, io.Reader, pb.Source) error {
	return s.ConfigDump
}
func jsonWrite(s pb.Sink) func(context.Context, pb.Path, io.Reader, pb.Source) error { return s.JSON }
func textWrite(s pb.Sink) func(context.Context, pb.Path, io.Reader, pb.Source) error { return s.Text }

type proxyAdminCollector struct {
	d    proxydump.Dialer
	caps []adminCapture
}

// ProxyAdmin returns the collector of each proxy replica's Envoy admin
// captures over d, at proxies/<ns>/<pod>/: config_dump.json with endpoints,
// stats.txt, certs.json and clusters.txt. With noEndpoints it sends the plain
// config dump and skips clusters (D-025). server_info.json is Version's.
func ProxyAdmin(d proxydump.Dialer, noEndpoints bool) pb.Collector {
	dump := proxydump.OpConfigDumpEDS
	if noEndpoints {
		dump = proxydump.OpConfigDump
	}
	caps := []adminCapture{
		{dump, pb.ConfigDumpFile, configDumpWrite},
		{proxydump.OpStats, pb.StatsFile, textWrite},
		{proxydump.OpCerts, pb.CertsFile, jsonWrite},
	}
	if !noEndpoints {
		caps = append(caps, adminCapture{proxydump.OpClusters, pb.ClustersFile, textWrite})
	}
	return proxyAdminCollector{d: d, caps: caps}
}

// Name implements pb.Collector.
func (proxyAdminCollector) Name() string { return "proxy-admin" }

// Collect implements pb.Collector. Unverified replicas get no request; a Pod
// that is not running is skipped (design §8.2). A replica's captures run in
// turn, so this collector sends each Envoy one admin request at a time.
func (c proxyAdminCollector) Collect(ctx context.Context, env pb.Env, s pb.Sink) error {
	var todo []pb.Replica
	for _, r := range env.Replicas {
		if r.Role == pb.RoleProxy && r.Identity != pb.IdentityUnverified {
			todo = append(todo, r)
		}
	}
	var (
		mu      sync.Mutex
		sealErr error
		wg      sync.WaitGroup
		jobs    = make(chan pb.Replica)
	)
	for range min(captureWorkers, len(todo)) {
		wg.Go(func() {
			for r := range jobs {
				if err := c.replica(ctx, env, s, r); err != nil {
					mu.Lock()
					sealErr = err
					mu.Unlock()
				}
			}
		})
	}
feed:
	for _, r := range todo {
		select {
		case jobs <- r:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	return sealErr
}

// replica runs r's captures until the run ends. After a timeout or a failed
// dial or tunnel, the rest get that error and no request, so a hung pod costs
// one request timeout (design §3). It returns only ErrSealed.
func (c proxyAdminCollector) replica(ctx context.Context, env pb.Env, s pb.Sink, r pb.Replica) error {
	var abort error
	for _, ac := range c.caps {
		if ctx.Err() != nil {
			return nil
		}
		p := pb.Path{pb.ProxiesDir, r.Namespace, r.Pod, ac.file}
		src := pb.Source{Namespace: r.Namespace, Pod: r.Pod, UID: r.UID, Operation: ac.op.Code()}
		var err error
		switch {
		case r.Phase != corev1.PodRunning:
			err = s.Omit(p, src, pb.OmitNotRunning)
		case abort != nil:
			s.Error(p, src, abort)
		default:
			abort, err = c.capture(ctx, env, s, r, ac, p, src)
		}
		if err := sealed(err); err != nil {
			return err
		}
	}
	return nil
}

// capture sends one operation and writes its body; the Sink re-reads the Pod
// after it (D-027). The request and body read are bounded by RequestTimeout.
// abort is the redacted error when the replica should get no more requests.
func (c proxyAdminCollector) capture(ctx context.Context, env pb.Env, s pb.Sink, r pb.Replica, ac adminCapture, p pb.Path, src pb.Source) (abort, err error) {
	op := src.Operation
	rctx, cancel := requestContext(ctx, env.RequestTimeout)
	defer cancel()
	now := env.Now
	if now == nil {
		now = time.Now
	}
	tw := &tunnelWatch{d: c.d}
	cp, err := proxydump.FetchOp(rctx, tw, types.NamespacedName{Namespace: r.Namespace, Name: r.Pod}, ac.op, now)
	if err != nil {
		fatal := tw.failed.Load() || errors.Is(err, context.DeadlineExceeded)
		err = adminError(op, err)
		// Cut short by the run's end, the item is timeout (design §3).
		if ctx.Err() != nil {
			err = fmt.Errorf("%s: %w", op, context.DeadlineExceeded)
		}
		s.Error(p, src, err)
		if fatal {
			return err, nil
		}
		return nil, nil
	}
	defer cp.Body.Close()
	body := &adminBody{r: cp.Body, op: op}
	// ctx, not rctx: the Sink bounds its re-read itself.
	err = ac.write(s)(ctx, p, body, src)
	if body.err != nil && (tw.failed.Load() || errors.Is(body.err, context.DeadlineExceeded)) {
		return body.err, err
	}
	return nil, err
}

// adminBody replaces a read error, such as the kubelet's error stream, with
// adminError's fixed text (§7.1), and keeps it in err.
type adminBody struct {
	r   io.Reader
	op  pb.Op
	err error
}

func (b *adminBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err != nil && err != io.EOF {
		err = adminError(b.op, err)
		b.err = err
	}
	return n, err
}

// tunnelWatch is a Dialer that notes a failed dial or a stream that fails
// before it is closed, such as on the kubelet's error stream.
type tunnelWatch struct {
	d      proxydump.Dialer
	failed atomic.Bool
}

// Dial implements proxydump.Dialer.
func (w *tunnelWatch) Dial(ctx context.Context, pod types.NamespacedName, port int) (io.ReadWriteCloser, func(), error) {
	conn, stop, err := w.d.Dial(ctx, pod, port)
	if err != nil {
		w.failed.Store(true)
		return conn, stop, err
	}
	return &watchedConn{ReadWriteCloser: conn, w: w}, stop, nil
}

type watchedConn struct {
	io.ReadWriteCloser
	w      *tunnelWatch
	closed atomic.Bool
}

func (c *watchedConn) Read(p []byte) (int, error) {
	n, err := c.ReadWriteCloser.Read(p)
	c.note(err)
	return n, err
}

func (c *watchedConn) Write(p []byte) (int, error) {
	n, err := c.ReadWriteCloser.Write(p)
	c.note(err)
	return n, err
}

func (c *watchedConn) Close() error {
	c.closed.Store(true)
	return c.ReadWriteCloser.Close()
}

// note marks the tunnel failed on an error before Close; EOF is a clean end.
func (c *watchedConn) note(err error) {
	if err != nil && err != io.EOF && !c.closed.Load() {
		c.w.failed.Store(true)
	}
}
