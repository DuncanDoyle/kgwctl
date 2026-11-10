package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/ctrlmetrics"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/clock"

	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/observe"
	"github.com/DuncanDoyle/kgwctl/internal/print"
	"github.com/DuncanDoyle/kgwctl/internal/view"
)

// clusterAccessCheck is the one check name check reads: it not passing means a
// kind the checks need could not be read, which is exit 2 (design §3.4).
const clusterAccessCheck = "cluster access"

// Flags of check --live and the heartbeat interval (design §3.2).
const (
	flagLive        = "live"
	flagTimeout     = "timeout"
	defaultTimeout  = 6 * time.Minute
	heartbeatEvery  = 30 * time.Second
	stopTimeout     = "timeout"
	stopInterrupted = "interrupted"
)

// checkTestHooks replace check's scraper, clock and timers in tests; the zero value is production.
type checkTestHooks struct {
	Scraper                                  ctrlmetrics.Scraper
	Clock                                    clock.WithTicker
	Quiet, FirstPublishWait, MetricsInterval time.Duration
	// Waiting is called with the current model each time check wakes while waiting for completion.
	Waiting func(*engine.Published)
	// Tracked is called with each model a --live run tracks, after its progress lines.
	Tracked func(*engine.Published)
	// Progress, if set, receives --live progress as a terminal would, without colour.
	Progress io.Writer
	// Published is observe.Options.Published.
	Published func(observe.Publication)
	// Stopping is called once a --live run decides to stop, before its session stops.
	Stopping func()
}

// newCheckCmd builds the check command (design §3.1, §4.5).
func newCheckCmd(reg *extension.Registry, build BuildInfo, now func() time.Time, hooks checkTestHooks) *cobra.Command {
	var (
		output  string
		live    bool
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Run every check over the selected installs and report what is wrong",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			start := time.Now()
			if output != print.FormatText && output != print.FormatJSON && output != print.FormatYAML {
				return fmt.Errorf("invalid --output %q: want text, json or yaml", output)
			}
			if cmd.Flags().Changed(flagTimeout) && !live {
				return fmt.Errorf("--%s is only valid with --%s", flagTimeout, flagLive)
			}
			if live && timeout <= 0 {
				return fmt.Errorf("invalid --%s %v: must be positive", flagTimeout, timeout)
			}
			if live {
				// After the first Ctrl-C a slow report must not trap the user.
				defer defaultOnCancel(cmd.Context())()
			}
			f, err := FlagsFrom(cmd)
			if err != nil {
				return err
			}
			// Only an explicit -n filters; no -n shows every namespace (design §3.1).
			ns := f.Namespace
			if f.AllNamespaces {
				ns = ""
			}
			sel := view.CheckSelect{Namespace: ns, Install: f.Install, AllControllers: f.AllControllers}
			if err := sel.Validate(); err != nil {
				return err
			}
			rc, _, err := f.RESTConfig()
			if err != nil {
				return err
			}
			if sel.Context, err = currentContext(f); err != nil {
				return err
			}
			o := observe.Options{Scraper: hooks.Scraper, Clock: hooks.Clock, Quiet: hooks.Quiet, FirstPublishWait: hooks.FirstPublishWait,
				MetricsInterval: hooks.MetricsInterval, Logger: engineLogger(cmd.ErrOrStderr()), Published: hooks.Published}
			// Since times from this moment on only say when kgwctl first looked (design §3.3).
			sel.ObservedFrom = time.Now()
			if hooks.Clock != nil {
				sel.ObservedFrom = hooks.Clock.Now()
			}
			a, err := acquire(cmd.Context(), f, start, rc, reg, o, hooks.Waiting)
			if err != nil {
				return err
			}
			if live {
				return runLive(cmd, a, liveConfig{reg: reg, sel: sel, f: f, build: build, output: output, timeout: timeout, hooks: hooks})
			}
			a.stop()
			p := a.p
			c := view.BuildCheck(p.Model, reg, sel)
			c.Incomplete = incomplete(a.inputs, len(p.Model.Installs) > 0)
			if err := writeCheck(cmd, output, c, build, now()); err != nil {
				return err
			}
			return checkExit(cmd, p, c, f, build.Name)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", print.FormatText, "Output format: text, json or yaml")
	cmd.Flags().BoolVar(&live, flagLive, false, "Keep observing until --timeout or Ctrl-C, then report")
	cmd.Flags().DurationVar(&timeout, flagTimeout, defaultTimeout, "How long --live observes")
	return cmd
}

// acquired is a running session and the model acquisition ended with.
type acquired struct {
	s      *observe.Session
	p      *engine.Published
	inputs map[schema.GroupVersionResource]load.Input
	// stop stops the session; it is idempotent.
	stop func()
}

// acquire starts a session bound to ctx and returns it with its model once complete,
// or at the deadline of --request-timeout from start (design §4.5), with the inputs at that moment.
// It fails with exit 2 when ctx ends first or discovery misses the deadline. The caller stops the session.
func acquire(ctx context.Context, f *Flags, start time.Time, rc *rest.Config, reg *extension.Registry, o observe.Options,
	waiting func(*engine.Published),
) (*acquired, error) {
	// ctx ending stops acquisition, not the session: the caller decides what the report covers.
	sctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	ok := false
	defer func() {
		if !ok {
			cancel()
		}
	}()
	var deadline <-chan time.Time
	if f.RequestTimeout > 0 {
		t := time.NewTimer(f.RequestTimeout - time.Since(start))
		defer t.Stop()
		deadline = t.C
	}
	c, err := load.NewClients(sctx, rc)
	if err != nil {
		return nil, err
	}
	type started struct {
		s   *observe.Session
		err error
	}
	ch := make(chan started, 1)
	go func() {
		s, err := observe.Start(sctx, c, reg, o)
		ch <- started{s, err}
	}()
	var s *observe.Session
	select {
	case r := <-ch:
		if r.err != nil {
			if ctx.Err() != nil {
				return nil, interrupted()
			}
			return nil, r.err
		}
		s = r.s
	case <-deadline:
		cancel()
		if r := <-ch; r.s != nil {
			r.s.Stop()
		}
		return nil, &ExitError{Code: 2, Err: fmt.Errorf("discovery did not finish within --%s %v", flagRequestTimeout, f.RequestTimeout)}
	case <-ctx.Done():
		cancel()
		if r := <-ch; r.s != nil {
			r.s.Stop()
		}
		return nil, interrupted()
	}
	defer func() {
		if !ok {
			s.Stop()
		}
	}()

	stopped := false
	for !stopped && !s.Complete() {
		if waiting != nil {
			waiting(s.Current())
		}
		select {
		case <-s.Changed():
		case <-deadline:
			stopped = true
		case <-ctx.Done():
			stopped = true
		}
	}
	// Inputs before Stop: a collector cut off by Stop reads as returned.
	p, inputs := s.Current(), s.Inputs()
	if ctx.Err() != nil && p == nil {
		return nil, interrupted()
	}
	if p == nil {
		return nil, &ExitError{Code: 2, Err: fmt.Errorf("no model computed within --%s %v", flagRequestTimeout, f.RequestTimeout)}
	}
	ok = true
	return &acquired{s: s, p: p, inputs: inputs, stop: func() { s.Stop(); cancel() }}, nil
}

func interrupted() error { return &ExitError{Code: 2, Err: errors.New("interrupted")} }

// checkExit returns nil, a findings exit 1 or an exit 2 once the report is out (design §3.4).
func checkExit(cmd *cobra.Command, p *engine.Published, c view.Check, f *Flags, name string) error {
	if cmd.Context().Err() != nil {
		return interrupted()
	}
	return reportExit(p, c, fmt.Sprintf("within --%s %v", flagRequestTimeout, f.RequestTimeout), name)
}

// reportExit is checkExit's rules on the final model p and its report c; within ends the not-synced error.
func reportExit(p *engine.Published, c view.Check, within, name string) error {
	switch {
	case p.Model.Snapshot == nil || !p.Model.Snapshot.Synced:
		return &ExitError{Code: 2, Err: fmt.Errorf("the cluster was not read %s", within)}
	case len(c.Installs) == 0:
		return &ExitError{Code: 2, Err: fmt.Errorf("no kgateway install found; run %q to see what the cluster runs", name+" version")}
	}
	for _, ic := range c.Installs {
		for _, cs := range ic.Checks {
			if cs.Name == clusterAccessCheck && !cs.OK {
				return &ExitError{Code: 2, Err: fmt.Errorf("install %s: some kinds the checks need could not be read", ic.Install.ID)}
			}
		}
	}
	if c.ExitCode() != 0 {
		return &ExitError{Code: c.ExitCode()}
	}
	return nil
}

// writeCheck prints c as text with its hint line, or as one envelope.
func writeCheck(cmd *cobra.Command, output string, c view.Check, build BuildInfo, now time.Time) error {
	w := cmd.OutOrStdout()
	if output != print.FormatText {
		return print.Envelope(w, output, "Check", c)
	}
	f, isFile := w.(*os.File)
	if err := print.CheckTextFor(w, c, now, isFile && print.ColorEnabled(f), build); err != nil {
		return err
	}
	if hint := print.CheckHintFor(c, now, build.Name); hint != "" {
		_, err := fmt.Fprintln(w, hint)
		return err
	}
	return nil
}

// incomplete are the inputs that are not ok, by name. Without an install the
// collectors have nothing to collect, so they are left out.
func incomplete(inputs map[schema.GroupVersionResource]load.Input, installs bool) []load.Input {
	var out []load.Input
	for gvr, in := range inputs {
		if in.State != load.StateOK && (installs || gvr != observe.CollectorGVR(in.Resource)) {
			out = append(out, in)
		}
	}
	slices.SortFunc(out, func(a, b load.Input) int { return cmp.Compare(a.Resource, b.Resource) })
	return out
}

// currentContext is the kube context the flags select: --context, else the kubeconfig's current one.
func currentContext(f *Flags) (string, error) {
	if f.ContextName != "" {
		return f.ContextName, nil
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = f.Kubeconfig
	raw, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).RawConfig()
	if err != nil {
		return "", fmt.Errorf("load kubeconfig: %w", err)
	}
	return raw.CurrentContext, nil
}
