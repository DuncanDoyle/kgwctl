package collect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// LogSource opens one container log stream.
type LogSource interface {
	// Stream opens the log of pod with opts; the caller closes the stream.
	Stream(ctx context.Context, pod types.NamespacedName, opts *corev1.PodLogOptions) (io.ReadCloser, error)
}

type kubeLogs struct{ kube kubernetes.Interface }

// KubeLogSource streams logs through the Pod log subresource with kube. Its
// errors carry the HTTP status and reason only, never a response body (§7.1).
func KubeLogSource(kube kubernetes.Interface) LogSource { return kubeLogs{kube} }

// Stream implements LogSource.
func (k kubeLogs) Stream(ctx context.Context, pod types.NamespacedName, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	rc, err := k.kube.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, opts).Stream(ctx)
	if err != nil {
		return nil, logError(opOf(opts), err)
	}
	return rc, nil
}

// logError keeps a status typed, so a 403 stays denied, with its message
// replaced by redact.RemoteError; any other error gets fixed text.
func logError(op pb.Op, err error) error {
	var st apierrors.APIStatus
	switch {
	case errors.As(err, &st):
		s := st.Status()
		return &apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Code: s.Code, Reason: s.Reason,
			Message: fmt.Sprintf("%s: %s", op, redact.RemoteError(err))}}
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s: %w", op, context.DeadlineExceeded)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("%s: %w", op, context.Canceled)
	}
	return fmt.Errorf("%s: %s", op, redact.RemoteError(err))
}

func opOf(opts *corev1.PodLogOptions) pb.Op {
	if opts.Previous {
		return pb.OpPreviousLog
	}
	return pb.OpLog
}

// logCap is the Sink's log cap; LimitBytes asks for logLimitSlack more, as
// the server's limit is approximate (design §3).
const (
	logCap        = 10 << 20
	logLimitSlack = 64 << 10
)

type logsCollector struct{ src LogSource }

// Logs returns the collector of controller and proxy container logs: every
// inventory container, init included, at logs/<container>.log under its
// replica, plus <container>.previous.log after a restart. Replicas that are
// not running still get logs; unverified replicas get none (design §8.2).
func Logs(src LogSource) pb.Collector { return logsCollector{src} }

// Name implements pb.Collector.
func (logsCollector) Name() string { return "logs" }

// logJob is one container log stream.
type logJob struct {
	r        pb.Replica
	c        pb.Container
	previous bool
}

// Collect implements pb.Collector.
func (c logsCollector) Collect(ctx context.Context, env pb.Env, s pb.Sink) error {
	var todo []logJob
	for _, r := range env.Replicas {
		if r.Identity == pb.IdentityUnverified {
			continue
		}
		for _, ct := range r.Containers {
			todo = append(todo, logJob{r: r, c: ct})
			if ct.Restarts > 0 {
				todo = append(todo, logJob{r: r, c: ct, previous: true})
			}
		}
	}
	var (
		mu      sync.Mutex
		sealErr error
		wg      sync.WaitGroup
		jobs    = make(chan logJob)
	)
	for range min(captureWorkers, len(todo)) {
		wg.Go(func() {
			for j := range jobs {
				if ctx.Err() != nil {
					continue
				}
				if err := c.capture(ctx, env, s, j); err != nil {
					mu.Lock()
					sealErr = err
					mu.Unlock()
				}
			}
		})
	}
feed:
	for _, j := range todo {
		select {
		case jobs <- j:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	return sealErr
}

// capture streams one log into the Sink, which re-reads the Pod after it.
// The stream is bounded by RequestTimeout. It returns only ErrSealed.
func (c logsCollector) capture(ctx context.Context, env pb.Env, s pb.Sink, j logJob) error {
	r := j.r
	dir := pb.ProxiesDir
	if r.Role == pb.RoleController {
		dir = pb.ControllerDir
	}
	ext, op := pb.Log, pb.OpLog
	if j.previous {
		ext, op = pb.PreviousLog, pb.OpPreviousLog
	}
	src := pb.Source{Namespace: r.Namespace, Pod: r.Pod, UID: r.UID, Container: j.c.Name, Operation: op}
	f, err := pb.File(j.c.Name, ext)
	if err != nil {
		s.Error(pb.Path{dir, r.Namespace, r.Pod, pb.LogsDir}, src, err)
		return nil
	}
	p := pb.Path{dir, r.Namespace, r.Pod, pb.LogsDir, f}

	limit := int64(logCap + logLimitSlack)
	opts := &corev1.PodLogOptions{Container: j.c.Name, Previous: j.previous, Timestamps: true, LimitBytes: &limit}
	if !env.Window.Since.IsZero() {
		opts.SinceTime = &metav1.Time{Time: env.Window.Since}
	}
	if env.Window.Tail > 0 {
		tail := env.Window.Tail
		opts.TailLines = &tail
	}
	rctx, cancel := requestContext(ctx, env.RequestTimeout)
	defer cancel()
	rc, err := c.src.Stream(rctx, types.NamespacedName{Namespace: r.Namespace, Name: r.Pod}, opts)
	if err != nil {
		// Cut short by the run's end, the item is timeout (design §3).
		if ctx.Err() != nil {
			err = fmt.Errorf("%s: %w", op, context.DeadlineExceeded)
		}
		s.Error(p, src, err)
		return nil
	}
	defer rc.Close()
	// ctx, not rctx: the Sink bounds its re-read itself, after a stream cut by rctx.
	return sealed(s.Text(ctx, p, rc, src))
}
