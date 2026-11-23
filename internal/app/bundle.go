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
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	ib "github.com/DuncanDoyle/kgwctl/internal/bundle"
	"github.com/DuncanDoyle/kgwctl/internal/bundle/collect"
	"github.com/DuncanDoyle/kgwctl/internal/load"
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
}

// builtinCollectors are the collectors every build has, in registration order.
func builtinCollectors(reg *extension.Registry, _ bundleTestHooks) []pb.Collector {
	return []pb.Collector{collect.Resources(reg), collect.Workloads(), collect.Install()}
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
			cs := append(builtinCollectors(reg, hooks), extra...)
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
			_ = noEndpoints // 4.3.x collectors read it through Env.

			res, err := loadBundle(cmd, f, reg, grc)
			if err != nil {
				return &ExitError{Code: 2, Err: loadError(cmd.Context(), err, rc.Host)}
			}
			scope, gws, keep, warns, err := resolveScope(f, cmd.Flags().Changed(flagNamespace), res)
			if err != nil {
				return err
			}
			ctrls, invErrs := controllerSelectors(res, scope)
			for _, e := range invErrs {
				warns = append(warns, e.Error())
			}
			for _, w := range warns {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", w)
			}
			ictx, cancel := f.Context(cmd.Context())
			replicas, errs := ib.Inventory(ictx, kube, scope, gws, ctrls)
			cancel()
			invErrs = append(invErrs, errs...)
			scope = withReplicaNamespaces(scope, replicas)

			sp, err := ib.NewSpool("", limits.RAM, limits.MaxSize)
			if err != nil {
				return usageErr(fmt.Errorf("create spool: %w", err))
			}
			// The writer hides the real host in error text; the manifest only gets the placeholder.
			w := ib.NewWriter(dest, sp, root, limits, redact.Options{ExtraHeaders: redactHeaders, MaxSize: limits.MaxSize}, rc.Host, now)
			inv := w.Sink(inventoryName)
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
			m, err = ib.Run(cmd.Context(), cs, env, w, m, ib.RunOptions{Skip: skipSet, Deadline: time.Now().Add(timeout)})
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
// bounded by --request-timeout; an unreachable API server fails it.
func loadBundle(cmd *cobra.Command, f *Flags, reg *extension.Registry, grc *rest.Config) (*load.Result, error) {
	ctx, cancel := f.Context(cmd.Context())
	defer cancel()
	c, err := load.NewClients(ctx, grc)
	if err != nil {
		return nil, err
	}
	return load.Load(ctx, c, reg, load.Needs{Logger: engineLogger(cmd.ErrOrStderr())})
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
