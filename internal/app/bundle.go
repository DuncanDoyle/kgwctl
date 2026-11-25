package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	ib "github.com/DuncanDoyle/kgwctl/internal/bundle"
	"github.com/DuncanDoyle/kgwctl/internal/bundle/collect"
	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/proxydump"
	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// apiserverPlaceholder is the manifest's server value; the real host is never recorded.
const apiserverPlaceholder = "<apiserver>"

// bundleTestHooks replace bundle's clock in tests; the zero value is production.
type bundleTestHooks struct {
	// Now is the run clock; it names the archive and starts the log window.
	Now func() time.Time
	// Check replaces check's seams for the check collector.
	Check checkTestHooks
	// Dialer replaces the port-forwarder to proxy admin ports.
	Dialer proxydump.Dialer
	// Logs replaces the Pod log source.
	Logs collect.LogSource
	// Scraper replaces the pods/proxy metrics scraper.
	Scraper collect.Scraper
}

// collectorDeps are what built-in collectors read beyond Env; zero values
// are enough for their names.
type collectorDeps struct {
	f      *Flags
	rc     *rest.Config // guarded
	res    *load.Result
	dialer proxydump.Dialer
	logs   collect.LogSource
	scrape collect.Scraper
	// noEndpoints is --no-endpoints: no EDS config dump and no clusters capture.
	noEndpoints bool
}

// builtinCollectors are the collectors every build has, in registration order.
func builtinCollectors(reg *extension.Registry, build BuildInfo, d collectorDeps, hooks bundleTestHooks) []pb.Collector {
	return []pb.Collector{collect.Resources(reg), collect.Workloads(), collect.Install(),
		collect.Version(build, d.res, d.dialer), newCheckCollector(d.f, d.rc, reg, build, hooks.Check), collect.Logs(d.logs),
		collect.ControllerMetrics(d.scrape), collect.ProxyMetrics(d.scrape), collect.ProxyAdmin(d.dialer, d.noEndpoints)}
}

// newBundleCmd builds the bundle command (phase 4 design §3). extra are the
// downstream collectors, registered after the built-in ones.
func newBundleCmd(reg *extension.Registry, build BuildInfo, extra []pb.Collector, hooks bundleTestHooks) *cobra.Command {
	var (
		output                 string
		since, timeout         time.Duration
		tail                   int64
		skip, redactHeaders    []string
		maxSize                string
		noEndpoints, listNames bool
	)
	cmd := &cobra.Command{
		Use:   "bundle",
		Short: "Write a support bundle of the cluster's kgateway state to a .tar.gz",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return &ExitError{Code: 2, Err: fmt.Errorf("bundle takes no arguments, got %d\nUsage: %s", len(args), cmd.UseLine())}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			cs := append(builtinCollectors(reg, build, collectorDeps{}, hooks), extra...)
			names, err := collectorNames(cs)
			if err != nil {
				return usageErr(err)
			}
			if listNames {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), strings.Join(names, "\n"))
				return err
			}
			skipSet := map[string]bool{}
			for _, s := range skip {
				if !slices.Contains(names, s) {
					return usageErr(fmt.Errorf("unknown collector %q in --skip; see --list-collectors", s))
				}
				skipSet[s] = true
			}
			limits, err := parseLimits(maxSize)
			if err != nil {
				return usageErr(err)
			}
			if since <= 0 || timeout <= 0 || tail <= 0 {
				return usageErr(errors.New("--since, --timeout and --tail must be positive"))
			}
			f, err := FlagsFrom(cmd)
			if err != nil {
				return usageErr(err)
			}
			now := hooks.Now
			if now == nil {
				now = time.Now
			}
			t0 := now()
			root := ib.DefaultName(t0)
			// After the first Ctrl-C a slow finalization must not trap the user.
			defer defaultOnCancel(cmd.Context())()

			// The output is created before any client exists, so a bad -o makes no request.
			var dest ib.Dest
			if output == "-" {
				dest = ib.Stdout(cmd.OutOrStdout())
			} else {
				if output == "" {
					output = root + ".tar.gz"
				}
				if dest, err = ib.CreateFile(output); err != nil {
					return usageErr(fmt.Errorf("create output: %w", err))
				}
			}
			ok := false
			defer func() {
				if !ok {
					_ = dest.Abort()
				}
			}()

			rc, _, err := f.RESTConfig()
			if err != nil {
				return usageErr(err)
			}
			kctx, err := currentContext(f)
			if err != nil {
				return usageErr(err)
			}
			// Every bundle-owned client, install detection included, goes through the guard (§6.1).
			// Collectors list per kind and namespace, so they get install detection's throttle.
			grc := load.ThrottleDefaults(ib.Guard(rc))
			// Server Warning headers are dropped: their text never reaches stderr (§7.1).
			grc.WarningHandler, grc.WarningHandlerWithContext = rest.NoWarnings{}, rest.NoWarnings{}
			kube, err := kubernetes.NewForConfig(grc)
			if err != nil {
				return usageErr(err)
			}
			dyn, err := dynamic.NewForConfig(grc)
			if err != nil {
				return usageErr(err)
			}
			disc, err := discovery.NewDiscoveryClientForConfig(grc)
			if err != nil {
				return usageErr(err)
			}
			// One --timeout clock from the first cluster request: detection, inventory and Run share it.
			deadline := time.Now().Add(timeout)
			rctx, rcancel := context.WithDeadline(cmd.Context(), deadline)
			defer rcancel()
			dctx, dcancel := f.Context(rctx)
			var reached atomic.Bool
			res, err := loadBundle(dctx, reg, grc, &reached)
			dcancel()
			// A deadline during detection after the cluster answered still seals a bundle (§3.1).
			var te *load.TimeoutError
			if err != nil && (cmd.Context().Err() != nil || !errors.As(err, &te) || !reached.Load()) {
				return &ExitError{Code: 2, Err: loadError(cmd.Context(), err, rc.Host)}
			}
			detectLate := err != nil || stillLoading(res)
			explicitNs := cmd.Flags().Changed(flagNamespace)
			var (
				scope    = baseScope(f, explicitNs)
				gws      ib.GatewaySet
				keep     func(*unstructured.Unstructured) pb.ExcludeReason
				warns    []string
				ctrls    []ib.ControllerSelector
				invErrs  []error
				replicas []pb.Replica
			)
			if res != nil {
				scope, gws, keep, warns, err = resolveScope(f, explicitNs, res)
				// The pinned install may just not have loaded yet.
				if errors.Is(err, errNoSuchInstall) && detectLate {
					res, scope, err = nil, baseScope(f, explicitNs), nil
				}
				if err != nil {
					return err
				}
			}
			if res != nil {
				ctrls, invErrs = controllerSelectors(res, scope)
				for _, e := range invErrs {
					warns = append(warns, e.Error())
				}
			}
			if detectLate {
				// What detection did not load is unknown, not absent.
				warns = slices.DeleteFunc(warns, func(w string) bool { return strings.HasPrefix(w, "no kgateway install detected") })
				warns = append([]string{"install detection timed out; installs and Gateways may be missing"}, warns...)
			}
			for _, w := range warns {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", w)
			}
			if res != nil {
				ictx, cancel := f.Context(rctx)
				var errs []error
				replicas, errs = ib.Inventory(ictx, kube, scope, gws, ctrls)
				cancel()
				invErrs = append(invErrs, errs...)
				scope = withReplicaNamespaces(scope, replicas)
			}

			sp, err := ib.NewSpool("", limits.RAM, limits.MaxSize)
			if err != nil {
				return usageErr(fmt.Errorf("create spool: %w", err))
			}
			// The writer hides the real host in error text; the manifest only gets the placeholder.
			w := ib.NewWriter(dest, sp, root, limits, redact.Options{ExtraHeaders: redactHeaders, MaxSize: limits.MaxSize}, rc.Host, now)
			inv := w.Sink(inventoryName)
			if detectLate {
				// Detection ended at a deadline: installs and Gateways may be missing.
				inv.Error(pb.Path{inventoryName, "detection"}, pb.Source{}, fmt.Errorf("install detection: %w", context.DeadlineExceeded))
			}
			for _, e := range invErrs {
				var re *ib.ReplicaError
				if errors.As(e, &re) {
					inv.Error(re.Path, re.Source, re.Err)
				} else {
					inv.Error(pb.Path{inventoryName}, pb.Source{}, e)
				}
			}
			env := pb.Env{
				Kube: kube, Dynamic: dyn, Discovery: disc,
				Scope:          scope,
				Replicas:       replicas,
				Keep:           keep,
				Window:         pb.Window{Start: t0, Since: t0.Add(-since), Tail: tail},
				Now:            now,
				RequestTimeout: f.RequestTimeout,
			}
			m := ib.Manifest{
				Build:   ib.BuildInfo{Version: build.Version, Commit: build.Commit},
				Context: kctx, Server: apiserverPlaceholder,
				Flags: ib.RecordFlags(cmd.Flags()), Window: env.Window, Start: t0,
				Scope: scope, Replicas: replicas,
			}
			dialer := hooks.Dialer
			if dialer == nil {
				dialer = proxydump.NewPortForwarder(grc)
			}
			logs := hooks.Logs
			if logs == nil {
				logs = collect.KubeLogSource(kube)
			}
			scrape := hooks.Scraper
			if scrape == nil {
				if scrape, err = collect.StatusScraper(grc); err != nil {
					return usageErr(err)
				}
			}
			cs = append(builtinCollectors(reg, build, collectorDeps{f: f, rc: grc, res: res, dialer: dialer, logs: logs, scrape: scrape, noEndpoints: noEndpoints}, hooks), extra...)
			if res == nil {
				deadline = time.Now() // no model: collection ends before any collector starts
			}
			m, err = ib.Run(cmd.Context(), cs, env, w, m, ib.RunOptions{Skip: skipSet, Deadline: deadline})
			if err != nil {
				return &ExitError{Code: 2, Err: fmt.Errorf("write bundle: %w", err)}
			}
			ok = true
			summarize(cmd.ErrOrStderr(), m, output)
			return nil
		},
	}
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return &ExitError{Code: 2, Err: fmt.Errorf("%w\nUsage: %s", err, c.UseLine())}
	})
	fs := cmd.Flags()
	fs.StringVarP(&output, "output", "o", "", "Output file; default kgwctl-bundle-<UTC time>.tar.gz, - for stdout")
	fs.DurationVar(&since, "since", 24*time.Hour, "Include logs and Events newer than this")
	fs.Int64Var(&tail, "tail", 5000, "Log lines per container")
	fs.DurationVar(&timeout, "timeout", 5*time.Minute, "Deadline for collection")
	fs.StringSliceVar(&skip, "skip", nil, "Collectors to leave out, comma-separated")
	fs.StringVar(&maxSize, "max-size", "512Mi", "Cap on the uncompressed bundle size, at least 32Mi")
	fs.StringSliceVar(&redactHeaders, "redact-header", nil, "Header names to add to the sensitive-header list")
	fs.BoolVar(&noEndpoints, "no-endpoints", false, "Leave out Envoy endpoint data")
	fs.BoolVar(&listNames, "list-collectors", false, "Print the collector names and exit")
	return cmd
}

func usageErr(err error) error { return &ExitError{Code: 2, Err: err} }

// inventoryName attributes replica inventory errors in errors.txt.
const inventoryName = "inventory"

// loadBundle detects installs and loads the model over the guarded config,
// bounded by ctx; an unreachable API server fails it. reached is set once
// the API server sends any response.
func loadBundle(ctx context.Context, reg *extension.Registry, grc *rest.Config, reached *atomic.Bool) (*load.Result, error) {
	cfg := rest.CopyConfig(grc)
	cfg.Wrap(func(next http.RoundTripper) http.RoundTripper { return &reachedTransport{next: next, reached: reached} })
	c, err := load.NewClients(ctx, cfg)
	if err != nil {
		return nil, err
	}
	// Proxy Pods give the version collector each Gateway's proxy image tags. The engine
	// logger is discarded, as in check: a rejected install's error can quote cluster data (§7.1).
	return load.Load(ctx, c, reg, load.Needs{ProxyPods: true, Logger: engineLogger(io.Discard)})
}

// reachedTransport sets reached when a response arrives.
type reachedTransport struct {
	next    http.RoundTripper
	reached *atomic.Bool
}

// RoundTrip implements http.RoundTripper.
func (t *reachedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(r)
	if resp != nil {
		t.reached.Store(true)
	}
	return resp, err
}

// WrappedRoundTripper lets client-go reach the transport underneath.
func (t *reachedTransport) WrappedRoundTripper() http.RoundTripper { return t.next }

// stillLoading reports whether detection ended with inputs still loading,
// which happens only when its context ended first.
func stillLoading(res *load.Result) bool {
	for _, in := range res.Inputs {
		if in.State == load.StateLoading {
			return true
		}
	}
	return false
}

// loadError classifies a failed install detection: interrupted, no reachable
// cluster, or another load failure. The text is redacted and cut (§7.1).
func loadError(ctx context.Context, err error, host string) error {
	var (
		ue *url.Error
		te *load.TimeoutError
	)
	what := "load cluster"
	switch {
	case ctx.Err() != nil:
		what = "interrupted during install detection"
	case errors.As(err, &ue), errors.As(err, &te) && te.Stage == load.StageDiscovery:
		what = "no reachable cluster"
	}
	return fmt.Errorf("%s: %s", what, redact.ErrorText(err, host))
}

// collectorNames validates and returns the names of cs.
func collectorNames(cs []pb.Collector) ([]string, error) {
	var names []string
	for _, c := range cs {
		n := c.Name()
		if err := pb.ValidName(n); err != nil {
			return nil, err
		}
		if slices.Contains(names, n) {
			return nil, fmt.Errorf("duplicate collector name %q", n)
		}
		names = append(names, n)
	}
	return names, nil
}

// parseLimits turns --max-size into the writer's limits.
func parseLimits(s string) (ib.Limits, error) {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return ib.Limits{}, fmt.Errorf("invalid --max-size %q: %w", s, err)
	}
	return ib.DefaultLimits(q.Value())
}

// summarize prints one line per collector, then the counts and the output path.
func summarize(w io.Writer, m ib.Manifest, output string) {
	for _, c := range m.Collectors {
		line := fmt.Sprintf("collector %s: %s", c.Name, c.Status)
		if c.Error != "" {
			line += " (" + c.Error + ")"
		}
		fmt.Fprintln(w, line)
	}
	var files, errs, reds int
	for _, it := range m.Items {
		switch it.Status {
		case pb.ItemOK:
			files++
		case pb.ItemFailed, pb.ItemDenied, pb.ItemTimeout, pb.ItemDropped:
			errs++ // skipped and disabled items are not errors
		}
		reds += it.Redactions
	}
	fmt.Fprintf(w, "%d files, %d errors, %d redactions\n", files, errs, reds)
	if output != "-" {
		fmt.Fprintln(w, "wrote "+output)
	}
}
