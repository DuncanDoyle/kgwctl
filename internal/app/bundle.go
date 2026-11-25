package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	// noInstall is set when complete detection found no install (design §3.2).
	noInstall bool
}

// builtinCollectors are the collectors every build has, in registration order.
// resources lists with the kinds install detection loaded; check keeps reg, as `check` does.
func builtinCollectors(reg *extension.Registry, build BuildInfo, d collectorDeps, hooks bundleTestHooks) []pb.Collector {
	var install, logs pb.Collector = collect.Install(), collect.Logs(d.logs)
	if d.noInstall {
		install, logs = omitNoInstall{install, pb.InstallDir}, omitNoInstall{logs, pb.ControllerDir}
	}
	return []pb.Collector{collect.Resources(collect.Registry(reg)), collect.Workloads(), install,
		collect.Version(build, d.res, d.dialer), newCheckCollector(d.f, d.rc, reg, build, hooks.Check), logs,
		collect.ControllerMetrics(d.scrape), collect.ProxyMetrics(d.scrape), collect.ProxyAdmin(d.dialer, d.noEndpoints)}
}

// noInstallCollectors are skipped whole with reason "no install".
var noInstallCollectors = map[string]bool{"controller-metrics": true}

// omitNoInstall records the controller part of a collector, under dir, as
// omitted with reason "no install", then runs the collector.
type omitNoInstall struct {
	pb.Collector
	dir string
}

// Collect implements pb.Collector.
func (c omitNoInstall) Collect(ctx context.Context, env pb.Env, s pb.Sink) error {
	if err := s.Omit(pb.Path{c.dir}, pb.Source{}, pb.OmitNoInstall); errors.Is(err, pb.ErrSealed) {
		return err
	}
	return c.Collector.Collect(ctx, env, s)
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

			// Configuration errors come before the output exists, so they leave no archive (D-030).
			if f.Install != "" {
				if _, err := install.ParsePinned(f.Install); err != nil {
					return usageErr(err)
				}
			}
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
			scrape := hooks.Scraper
			if scrape == nil {
				if scrape, err = collect.StatusScraper(grc); err != nil {
					return usageErr(err)
				}
			}
			// Remove is idempotent; the writer also removes the spool when it commits.
			sp, err := ib.NewSpool("", limits.RAM, limits.MaxSize)
			if err != nil {
				return usageErr(fmt.Errorf("create spool: %w", err))
			}
			defer func() { _ = sp.Remove() }()

			// Setup that can fail is done; the output is created before any cluster request, so a bad -o makes none.
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

			// One --timeout clock from the first cluster request: detection, inventory and Run share it.
			deadline := time.Now().Add(timeout)
			rctx, rcancel := context.WithDeadline(cmd.Context(), deadline)
			defer rcancel()
			dctx, dcancel := f.Context(rctx)
			res, err := loadBundle(dctx, reg, grc)
			dcancel()
			// An interrupt aborts; any other detection failure is sealed without cluster data (D-030).
			if err != nil && cmd.Context().Err() != nil {
				return &ExitError{Code: 2, Err: loadError(cmd.Context(), err, rc.Host)}
			}
			var (
				noData     error  // why the archive holds no cluster data
				detectItem error  // the inventory/detection item, with a fixed reason
				notStarted string // the error of the collectors that never ran
			)
			if err != nil {
				detectItem, notStarted = detectionOutcome(err)
				noData = detectionError(err, notStarted, rc.Host)
			}
			detectLate := res != nil && stillLoading(res)
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
				scope, gws, keep, warns, err = resolveScope(f, explicitNs, detectLate, res)
				if errors.Is(err, errNoSuchInstall) {
					// With detection cut, the pinned install may just not have loaded yet.
					detectItem, notStarted, noData = errNoSuchInstall, errNoSuchInstall.Error(), err
					if detectLate {
						detectItem, notStarted = errDetectionTimeout, msgDetectionTimeout
						noData = errors.New(msgDetectionTimeout + " before the --install install was found")
					}
					res, scope, warns, err = nil, baseScope(f, explicitNs), nil, nil
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
			if detectLate && res != nil {
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

			// The writer hides the real host in error text; the manifest only gets the placeholder.
			w := ib.NewWriter(dest, sp, root, limits, redact.Options{ExtraHeaders: redactHeaders, MaxSize: limits.MaxSize}, rc.Host, now)
			inv := w.Sink(inventoryName)
			if detectItem == nil && detectLate {
				// Detection ended at a deadline: installs and Gateways may be missing.
				detectItem = errDetectionTimeout
			}
			if detectItem != nil {
				inv.Error(pb.Path{inventoryName, "detection"}, pb.Source{}, detectItem)
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
			// A cut detection or an unread detection input leaves an absent install unknown.
			noInstall := res != nil && installsAbsent(res, detectLate)
			cs = append(builtinCollectors(reg, build, collectorDeps{f: f, rc: grc, res: res, dialer: dialer, logs: logs, scrape: scrape,
				noEndpoints: noEndpoints, noInstall: noInstall}, hooks), extra...)
			ro := ib.RunOptions{Skip: skipSet, Deadline: deadline}
			if noInstall {
				ro.NoInstall = noInstallCollectors
			}
			if res == nil {
				// No model: no collector starts. Say why unless --timeout itself has passed.
				ro.Deadline = time.Now()
				if rctx.Err() == nil {
					ro.NotStarted = notStarted
				}
			}
			m, err = ib.Run(cmd.Context(), cs, env, w, m, ro)
			if err != nil {
				return &ExitError{Code: 2, Err: fmt.Errorf("write bundle: %w", err)}
			}
			ok = true
			summarize(cmd.ErrOrStderr(), m, output)
			// The exit code follows what the archive holds, not how detection ended (D-030).
			if !holdsClusterData(m) {
				if noData == nil {
					noData = errors.New("no collector wrote cluster data")
					if detectLate {
						noData = errors.New(msgDetectionTimeout + "; no collector wrote cluster data")
					}
				}
				return &ExitError{Code: 2, Err: fmt.Errorf("bundle holds no cluster data: %s", noData.Error())}
			}
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
// bounded by ctx; an unreachable API server fails it. It loads every kind the
// resources collector lists, so the scope rules see them (design §3.2).
func loadBundle(ctx context.Context, reg *extension.Registry, grc *rest.Config) (*load.Result, error) {
	reg = collect.Registry(reg)
	c, err := load.NewClients(ctx, grc)
	if err != nil {
		return nil, err
	}
	// Proxy Pods give the version collector each Gateway's proxy image tags. The engine
	// logger is discarded, as in check: a rejected install's error can quote cluster data (§7.1).
	return load.Load(ctx, c, reg, load.Needs{ProxyPods: true, Logger: engineLogger(io.Discard)})
}

// holdsClusterData reports whether the archive holds what the cluster said:
// an ok item other than version.json, or a collector other than version that
// finished ok or partial, so an empty cluster still counts. version is left
// out because it writes version.json even without a cluster answer. Items cut
// from the manifest count, since only a large run loses any.
func holdsClusterData(m ib.Manifest) bool {
	if m.ItemsOmitted > 0 {
		return true
	}
	versionFile := pb.Path{pb.ClusterDir, pb.VersionFile}.String()
	if slices.ContainsFunc(m.Items, func(it ib.Item) bool { return it.Status == pb.ItemOK && it.Path != versionFile }) {
		return true
	}
	return slices.ContainsFunc(m.Collectors, func(c ib.CollectorEntry) bool {
		return c.Name != "version" && (c.Status == pb.CollectorOK || c.Status == pb.CollectorPartial)
	})
}

// Fixed inventory/detection items (D-030); their status comes from ib.ItemStatus.
var (
	errDetectionTimeout = fmt.Errorf("install detection: %w", context.DeadlineExceeded)
	errDetectionDenied  = apierrors.NewForbidden(schema.GroupResource{}, "", errors.New("install detection denied"))
	errDetectionFailed  = errors.New("install detection failed")
)

// msgDetectionTimeout is the error of collectors that never ran after a detection timeout.
const msgDetectionTimeout = "install detection timed out"

// detectionOutcome maps a failed detection to its fixed item and the error of
// the collectors that never ran.
func detectionOutcome(err error) (error, string) {
	switch ib.ItemStatus(err) {
	case pb.ItemTimeout:
		return errDetectionTimeout, msgDetectionTimeout
	case pb.ItemDenied:
		return errDetectionDenied, "install detection denied"
	}
	return errDetectionFailed, errDetectionFailed.Error()
}

// detectionError is the stderr cause of a failed detection: the fixed outcome
// for a timeout, else loadError's text, both redacted (§7.1).
func detectionError(err error, outcome, host string) error {
	if outcome == msgDetectionTimeout {
		return fmt.Errorf("%s: %s", outcome, redact.ErrorText(err, host))
	}
	return loadError(context.Background(), err, host)
}

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
