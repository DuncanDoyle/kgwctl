package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/engine"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/version"

	ib "github.com/DuncanDoyle/kgwctl/internal/bundle"
	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/proxydump"
	"github.com/DuncanDoyle/kgwctl/internal/view"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// VersionFile is cluster/version.json (design §4).
type VersionFile struct {
	Client view.BuildInfo `json:"client"`
	// Server is the discovery server version; nil when it could not be read.
	Server *version.Info `json:"server,omitempty"`
	// Installs are the selected installs with their controller and proxy image tags.
	Installs []view.InstallVersion `json:"installs,omitempty"`
	// Observed is one entry per proxy replica, by namespace and Pod.
	Observed []ObservedEnvoy `json:"observed"`
}

// ObservedEnvoy is the Envoy version one proxy replica reported in /server_info.
type ObservedEnvoy struct {
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	// Version is server_info's version field; empty unless Status is ok.
	Version string        `json:"version,omitempty"`
	Status  pb.ItemStatus `json:"status"`
}

// serverInfoCap bounds a /server_info body, the Sink's cap for other items.
const serverInfoCap = 16 << 20

// maxVersion bounds the recorded version string.
const maxVersion = 256

type versionCollector struct {
	build view.BuildInfo
	res   *load.Result
	d     proxydump.Dialer
}

// Version returns the collector of cluster/version.json: build, discovery
// server version, the selected installs' image tags from res, and each proxy
// replica's Envoy version, captured at proxies/<ns>/<pod>/server_info.json
// over d. Unverified and not-running replicas are not captured.
func Version(build view.BuildInfo, res *load.Result, d proxydump.Dialer) pb.Collector {
	return versionCollector{build: build, res: res, d: d}
}

// Name implements pb.Collector.
func (versionCollector) Name() string { return "version" }

// Collect implements pb.Collector.
// version.json is written with whatever finished: a hung replica costs its own
// entry, never the file (design §3).
func (c versionCollector) Collect(ctx context.Context, env pb.Env, s pb.Sink) error {
	vf := VersionFile{Client: c.build, Installs: c.installs(env.Scope, s), Observed: []ObservedEnvoy{}}
	if sv, err := serverVersion(ctx, env); err != nil {
		s.Error(pb.Path{pb.ClusterDir}, pb.Source{}, err)
	} else {
		vf.Server = sv
	}
	obs, err := c.observe(ctx, env, s)
	if err != nil {
		return err
	}
	vf.Observed = obs
	b, err := json.Marshal(vf)
	if err != nil {
		return err
	}
	return sealed(s.JSON(pb.Path{pb.ClusterDir, pb.VersionFile}, bytes.NewReader(b), pb.Source{}))
}

// serverVersion reads /version, bounded by RequestTimeout.
func serverVersion(ctx context.Context, env pb.Env) (*version.Info, error) {
	rctx, cancel := requestContext(ctx, env.RequestTimeout)
	defer cancel()
	rc := env.Discovery.RESTClient()
	if rc == nil {
		// A client without REST access (a fake) is bounded by waiting only.
		return proxydump.Await(rctx, env.Discovery.ServerVersion, nil)
	}
	b, err := rc.Get().AbsPath("/version").Do(rctx).Raw()
	if err != nil {
		return nil, err
	}
	var v version.Info
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("decode server version: %w", err)
	}
	return &v, nil
}

// captureWorkers bounds concurrent server-info captures, as the runner bounds collectors.
const captureWorkers = 8

// observe captures the proxy replicas with bounded concurrency. Captures stop
// early enough to leave time for version.json; a replica not started by
// then is timeout. It returns only ErrSealed.
func (c versionCollector) observe(ctx context.Context, env pb.Env, s pb.Sink) ([]ObservedEnvoy, error) {
	var proxies []pb.Replica
	for _, r := range env.Replicas {
		if r.Role == pb.RoleProxy {
			proxies = append(proxies, r)
		}
	}
	out := make([]ObservedEnvoy, len(proxies))
	started := make([]bool, len(proxies))
	var todo []int
	for i, r := range proxies {
		out[i] = ObservedEnvoy{Namespace: r.Namespace, Pod: r.Pod, Status: pb.ItemSkipped}
		// Terminating and unready replicas are still captured (design §8.2).
		if r.Identity != pb.IdentityUnverified && r.Phase == corev1.PodRunning {
			out[i].Status = pb.ItemTimeout
			todo = append(todo, i)
		}
	}
	cctx, cancel := captureContext(ctx)
	defer cancel()
	var (
		mu       sync.Mutex
		sealErr  error
		wg       sync.WaitGroup
		jobs     = make(chan int)
		workers  = min(captureWorkers, len(todo))
		fitsNext = func() bool {
			d, ok := cctx.Deadline()
			return cctx.Err() == nil && (!ok || env.RequestTimeout <= 0 || time.Until(d) >= env.RequestTimeout)
		}
	)
	for range workers {
		wg.Go(func() {
			for i := range jobs {
				if !fitsNext() {
					continue
				}
				mu.Lock()
				started[i] = true
				mu.Unlock()
				o, err := c.capture(cctx, env, s, proxies[i])
				mu.Lock()
				out[i] = o
				if errors.Is(err, pb.ErrSealed) {
					sealErr = err
				}
				mu.Unlock()
			}
		})
	}
feed:
	for _, i := range todo {
		select {
		case jobs <- i:
		case <-cctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if sealErr != nil {
		return nil, sealErr
	}
	for _, i := range todo {
		if !started[i] {
			r := proxies[i]
			s.Error(pb.Path{pb.ProxiesDir, r.Namespace, r.Pod, pb.ServerInfoFile},
				pb.Source{Namespace: r.Namespace, Pod: r.Pod, UID: r.UID, Operation: pb.OpServerInfo},
				fmt.Errorf("%s: not started before the deadline: %w", pb.OpServerInfo, context.DeadlineExceeded))
		}
	}
	return out, nil
}

// captureContext ends captures before ctx's deadline, keeping a reserve of a
// tenth of the time left, at most a second, to write version.json.
func captureContext(ctx context.Context) (context.Context, context.CancelFunc) {
	d, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, d.Add(-min(time.Second, time.Until(d)/10)))
}

// installs returns the version view's entries for the scope's installs; a
// failure is an error at cluster/.
func (c versionCollector) installs(sc pb.Scope, s pb.Sink) []view.InstallVersion {
	if c.res == nil {
		return nil
	}
	// Every detected install, as `version` sees them; the scope's are kept below.
	f, warns := view.NewFilter(c.res, view.Scope{AllControllers: true})
	for _, w := range warns {
		s.Error(pb.Path{pb.ClusterDir}, pb.Source{}, errors.New(w))
	}
	v, err := view.Version(c.build, c.res, f)
	if err != nil {
		s.Error(pb.Path{pb.ClusterDir}, pb.Source{}, err)
		return nil
	}
	ids := map[string]bool{}
	for _, im := range c.res.Model.Installs {
		if selected(im, sc) {
			ids[im.Resolved.ID()] = true
		}
	}
	var out []view.InstallVersion
	for _, iv := range v.Installs {
		if ids[iv.ID] {
			out = append(out, iv)
		}
	}
	return out
}

func selected(im engine.InstallModel, sc pb.Scope) bool {
	for _, in := range sc.Installs {
		if in.Namespace == im.Resolved.Namespace && in.Deployment == im.Resolved.Name {
			return true
		}
	}
	return false
}

// capture fetches r's /server_info, re-reads the Pod and writes the item. It
// returns only ErrSealed or the context's error.
func (c versionCollector) capture(ctx context.Context, env pb.Env, s pb.Sink, r pb.Replica) (ObservedEnvoy, error) {
	o := ObservedEnvoy{Namespace: r.Namespace, Pod: r.Pod}
	src := pb.Source{Namespace: r.Namespace, Pod: r.Pod, UID: r.UID, Operation: pb.OpServerInfo}
	p := pb.Path{pb.ProxiesDir, r.Namespace, r.Pod, pb.ServerInfoFile}
	rctx, cancel := requestContext(ctx, env.RequestTimeout)
	body, err := fetchServerInfo(rctx, c.d, types.NamespacedName{Namespace: r.Namespace, Name: r.Pod}, env.Now)
	cancel()
	if err != nil {
		err = adminError(pb.OpServerInfo, err)
		s.Error(p, src, err)
		o.Status = ib.ItemStatus(err)
		return o, nil
	}
	// The re-read gets its own request bound (design §8.2).
	rctx, cancel = requestContext(ctx, env.RequestTimeout)
	src.Identity = ib.Reidentify(rctx, env.Kube, r)
	cancel()
	if err := s.JSON(p, bytes.NewReader(body), src); err != nil {
		if errors.Is(err, pb.ErrSealed) {
			return o, err
		}
		o.Status = pb.ItemFailed
		if errors.Is(err, pb.ErrBudget) {
			o.Status = pb.ItemDropped
		}
		return o, nil
	}
	o.Status = pb.ItemOK
	var si struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(body, &si) == nil && len(si.Version) <= maxVersion {
		o.Version = si.Version
	}
	return o, nil
}

// fetchServerInfo reads one /server_info response, capped.
func fetchServerInfo(ctx context.Context, d proxydump.Dialer, pod types.NamespacedName, now func() time.Time) ([]byte, error) {
	if now == nil {
		now = time.Now
	}
	c, err := proxydump.FetchOp(ctx, d, pod, proxydump.OpServerInfo, now)
	if err != nil {
		return nil, err
	}
	defer c.Body.Close()
	return proxydump.ReadCapped(c.Body, serverInfoCap)
}

// adminStatusRe is proxydump's own text for a non-200 admin response.
var adminStatusRe = regexp.MustCompile(`admin endpoint returned ([1-5][0-9]{2})\b`)

// adminError keeps a Kubernetes status or context error and replaces any
// other text, such as the kubelet's error stream, with fixed wording (§7.1).
func adminError(op pb.Op, err error) error {
	var st apierrors.APIStatus
	switch {
	case errors.As(err, &st):
		return err
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s: %w", op, context.DeadlineExceeded)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("%s: %w", op, context.Canceled)
	case errors.Is(err, proxydump.ErrTooLarge):
		return fmt.Errorf("%s: %w", op, proxydump.ErrTooLarge)
	}
	if m := adminStatusRe.FindStringSubmatch(err.Error()); m != nil {
		code, _ := strconv.Atoi(m[1])
		return fmt.Errorf("%s: admin endpoint returned %d %s", op, code, http.StatusText(code))
	}
	return fmt.Errorf("%s: request failed", op)
}
