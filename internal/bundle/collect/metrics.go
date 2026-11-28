package collect

import (
	"context"
	"errors"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ib "github.com/DuncanDoyle/kgwctl/internal/bundle"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// errNoMetricsPort fails a controller replica whose install has no metrics port.
var errNoMetricsPort = fmt.Errorf("%s: no metrics port", pb.OpMetrics)

type metricsCollector struct {
	name string
	role pb.Role
	s    Scraper
}

// ControllerMetrics returns the collector of each controller replica's
// /metrics on its install's port, at controller/<ns>/<pod>/metrics.txt.
func ControllerMetrics(s Scraper) pb.Collector {
	return metricsCollector{name: "controller-metrics", role: pb.RoleController, s: s}
}

// ProxyMetrics returns the collector of each proxy replica's /metrics on
// 9091, declared or not, at proxies/<ns>/<pod>/metrics.txt.
func ProxyMetrics(s Scraper) pb.Collector {
	return metricsCollector{name: "proxy-metrics", role: pb.RoleProxy, s: s}
}

// Name implements pb.Collector.
func (c metricsCollector) Name() string { return c.name }

// Collect implements pb.Collector. Unverified replicas get no item; a Pod
// that is not running is skipped (design §8.2).
func (c metricsCollector) Collect(ctx context.Context, env pb.Env, s pb.Sink) error {
	var todo []pb.Replica
	for _, r := range env.Replicas {
		if r.Role == c.role && r.Identity != pb.IdentityUnverified {
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
				if ctx.Err() != nil {
					continue
				}
				if err := c.capture(ctx, env, s, r); err != nil {
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

// capture scrapes one replica into the Sink, which re-reads the Pod after
// it. The scrape is bounded by RequestTimeout. It returns only ErrSealed.
func (c metricsCollector) capture(ctx context.Context, env pb.Env, s pb.Sink, r pb.Replica) error {
	dir, port := pb.ControllerDir, r.MetricsPort
	if r.Role == pb.RoleProxy {
		dir, port = pb.ProxiesDir, ib.ProxyMetricsPort
	}
	p := pb.Path{dir, r.Namespace, r.Pod, pb.MetricsFile}
	src := pb.Source{Namespace: r.Namespace, Pod: r.Pod, UID: r.UID, Operation: pb.OpMetrics}
	if r.Phase != corev1.PodRunning {
		return sealed(s.Omit(p, src, pb.OmitNotRunning))
	}
	if port == 0 {
		s.Error(p, src, errNoMetricsPort)
		return nil
	}
	rctx, cancel := requestContext(ctx, env.RequestTimeout)
	defer cancel()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: r.Namespace, Name: r.Pod, UID: r.UID}}
	body, err := c.s.Scrape(rctx, pod, port)
	if err != nil {
		// A refused connection to an undeclared port means metrics are off (design §6).
		if ib.Refused(err) && !r.MetricsPortDeclared {
			return sealed(s.Omit(p, src, pb.OmitNoMetricsPort))
		}
		if ctx.Err() != nil && !errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf("%s: %w", pb.OpMetrics, context.DeadlineExceeded)
		}
		s.Error(p, src, err)
		return nil
	}
	defer body.Close()
	// ctx, not rctx: the Sink bounds its re-read itself.
	return sealed(s.Prometheus(ctx, p, body, src))
}
