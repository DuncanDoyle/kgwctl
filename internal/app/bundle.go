package app

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	ib "github.com/DuncanDoyle/kgwctl/internal/bundle"
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
func builtinCollectors(*extension.Registry, bundleTestHooks) []pb.Collector { return nil }

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
			kube, err := kubernetes.NewForConfig(rc)
			if err != nil {
				return usageErr(err)
			}
			dyn, err := dynamic.NewForConfig(rc)
			if err != nil {
				return usageErr(err)
			}
			disc, err := discovery.NewDiscoveryClientForConfig(rc)
			if err != nil {
				return usageErr(err)
			}
			// 4.2.2 wraps rc in the request guard before these clients are built.
			_ = noEndpoints // 4.3.x collectors read it through Env.

			sp, err := ib.NewSpool("", limits.RAM, limits.MaxSize)
			if err != nil {
				return usageErr(fmt.Errorf("create spool: %w", err))
			}
			// The writer hides the real host in error text; the manifest only gets the placeholder.
			w := ib.NewWriter(dest, sp, root, limits, redact.Options{ExtraHeaders: redactHeaders, MaxSize: limits.MaxSize}, rc.Host, now)
			env := pb.Env{
				Kube: kube, Dynamic: dyn, Discovery: disc,
				Window:         pb.Window{Start: t0, Since: t0.Add(-since), Tail: tail},
				Now:            now,
				RequestTimeout: f.RequestTimeout,
				// 4.2.2 and later tasks fill Scope, Replicas and Keep here.
			}
			m := ib.Manifest{
				Build:   ib.BuildInfo{Version: build.Version, Commit: build.Commit},
				Context: kctx, Server: apiserverPlaceholder,
				Flags: ib.RecordFlags(cmd.Flags()), Window: env.Window, Start: t0,
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
		if it.Status == pb.ItemOK {
			files++
		} else {
			errs++
		}
		reds += it.Redactions
	}
	fmt.Fprintf(w, "%d files, %d errors, %d redactions\n", files, errs, reds)
	if output != "-" {
		fmt.Fprintln(w, "wrote "+output)
	}
}
