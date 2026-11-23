package collect

import (
	"context"
	"fmt"
	"io"
	"sync"
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
// turn on one worker, so each Envoy serves one admin request at a time.
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

// replica runs r's captures until the run ends. It returns only ErrSealed.
func (c proxyAdminCollector) replica(ctx context.Context, env pb.Env, s pb.Sink, r pb.Replica) error {
	for _, ac := range c.caps {
		if ctx.Err() != nil {
			return nil
		}
		p := pb.Path{pb.ProxiesDir, r.Namespace, r.Pod, ac.file}
		src := pb.Source{Namespace: r.Namespace, Pod: r.Pod, UID: r.UID, Operation: ac.op.Code()}
		var err error
		if r.Phase != corev1.PodRunning {
			err = s.Omit(p, src, pb.OmitNotRunning)
		} else {
			err = c.capture(ctx, env, s, r, ac, p, src)
		}
		if err := sealed(err); err != nil {
			return err
		}
	}
	return nil
}

// capture sends one operation and writes its body; the Sink re-reads the Pod
// after it (D-027). The request and body read are bounded by RequestTimeout.
func (c proxyAdminCollector) capture(ctx context.Context, env pb.Env, s pb.Sink, r pb.Replica, ac adminCapture, p pb.Path, src pb.Source) error {
	op := src.Operation
	rctx, cancel := requestContext(ctx, env.RequestTimeout)
	defer cancel()
	now := env.Now
	if now == nil {
		now = time.Now
	}
	cp, err := proxydump.FetchOp(rctx, c.d, types.NamespacedName{Namespace: r.Namespace, Name: r.Pod}, ac.op, now)
	if err != nil {
		err = adminError(op, err)
		// Cut short by the run's end, the item is timeout (design §3).
		if ctx.Err() != nil {
			err = fmt.Errorf("%s: %w", op, context.DeadlineExceeded)
		}
		s.Error(p, src, err)
		return nil
	}
	defer cp.Body.Close()
	// ctx, not rctx: the Sink bounds its re-read itself.
	return ac.write(s)(ctx, p, adminBody{cp.Body, op}, src)
}

// adminBody replaces a read error, such as the kubelet's error stream, with
// adminError's fixed text (§7.1).
type adminBody struct {
	r  io.Reader
	op pb.Op
}

func (b adminBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err != nil && err != io.EOF {
		err = adminError(b.op, err)
	}
	return n, err
}
