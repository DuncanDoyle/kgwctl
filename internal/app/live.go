package app

import (
	"io"
	"os"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/spf13/cobra"
	"k8s.io/utils/clock"

	"github.com/DuncanDoyle/kgwctl/internal/observe"
	"github.com/DuncanDoyle/kgwctl/internal/print"
	"github.com/DuncanDoyle/kgwctl/internal/view"
)

// liveConfig is what runLive needs from the check command.
type liveConfig struct {
	reg     *extension.Registry
	sel     view.CheckSelect
	f       *Flags
	build   BuildInfo
	output  string
	timeout time.Duration
	hooks   checkTestHooks
}

// liveRun tracks the models of one --live run from the acquired one on (design §4.2).
type liveRun struct {
	cfg      liveConfig
	progress io.Writer // nil off a terminal
	color    bool
	tr       observe.Tracker
	// base and cut bound the tracked models: from the acquired one to the last before the stop; cut 0 is open.
	base, cut uint64
	last      observe.Publication
	lastView  view.Check
	// started is the baseline's time, when tracking starts.
	started time.Time
	// install is set once a tracked model had a selected install.
	install bool
}

// view builds the report of p with the inputs as they were when p was published.
func (l *liveRun) view(p observe.Publication) view.Check {
	c := view.BuildCheck(p.Model, l.cfg.reg, l.cfg.sel)
	c.Incomplete = incomplete(p.Inputs, len(p.Model.Installs) > 0)
	return c
}

// feed tracks p, skipping models before the acquired one, and writes its events when render is set.
// The first model tracked is the baseline: its whole state prints, as raised.
func (l *liveRun) feed(p observe.Publication, render bool) {
	if p.Model.Seq < l.base || l.cut != 0 && p.Model.Seq > l.cut {
		return
	}
	c := l.view(p)
	at := p.Model.ComputedAt.UTC()
	events := l.tr.Add(c, at)
	if l.last.Published == nil {
		events = observe.Diff(view.Check{}, c, at)
		l.started = at
	}
	l.last, l.lastView = p, c
	if len(c.Installs) > 0 {
		l.install = true
	}
	if render && l.progress != nil {
		for _, e := range events {
			// Progress is best effort; the report on stdout is what counts.
			_ = print.Progress(l.progress, e, l.color)
		}
	}
	if l.cfg.hooks.Tracked != nil {
		l.cfg.hooks.Tracked(p.Published)
	}
}

// runLive keeps a's session until --timeout on kgwctl's clock or the first
// Ctrl-C, then prints one report with the Observation (design §3.2). A Ctrl-C
// any time before the exit code is chosen gives exit 2.
func runLive(cmd *cobra.Command, a *acquired, cfg liveConfig) error {
	defer a.stop()
	ctx := cmd.Context()
	clk := clock.WithTicker(clock.RealClock{})
	if cfg.hooks.Clock != nil {
		clk = cfg.hooks.Clock
	}
	l := &liveRun{cfg: cfg, base: a.p.Model.Seq}
	if cfg.hooks.Progress != nil {
		l.progress = cfg.hooks.Progress
	} else if f, ok := cmd.ErrOrStderr().(*os.File); ok && print.IsTerminal(f) {
		l.progress, l.color = f, print.ColorEnabled(f)
	}
	acquiredPub := observe.Publication{Published: a.p, Inputs: a.inputs}
	timer := clk.NewTimer(cfg.timeout)
	defer timer.Stop()
	beat := clk.NewTicker(heartbeatEvery)
	defer beat.Stop()
	begin := clk.Now()
	if l.progress != nil {
		var ids []string
		for _, ic := range l.view(acquiredPub).Installs {
			ids = append(ids, ic.Install.Namespace+"/"+ic.Install.Name)
		}
		_ = print.ProgressStart(l.progress, begin, cfg.sel.Context, ids, cfg.timeout)
	}

	models := a.s.Models()
	reason := ""
	for reason == "" {
		select {
		case p, ok := <-models:
			if !ok {
				models = nil // the session ended with ctx; Done follows
				continue
			}
			l.feed(p, true)
		case <-timer.C():
			reason = stopTimeout
		case <-beat.C():
			if l.progress != nil {
				_ = print.Heartbeat(l.progress, clk.Now(), clk.Since(begin), cfg.timeout, l.lastView)
			}
		case <-ctx.Done():
			reason = stopInterrupted
		}
	}
	ended := clk.Now()
	// The observation window ends here; later models are outside it and may reflect the shutdown.
	l.cut = a.s.Current().Model.Seq
	if cfg.hooks.Stopping != nil {
		cfg.hooks.Stopping()
	}
	a.stop()
	// Tracking may not skip a model of the run, even one no render reached.
	for _, p := range a.s.Drain() {
		l.feed(p, false)
	}
	if l.last.Published == nil {
		l.feed(acquiredPub, false)
	}
	// A Ctrl-C during shutdown still wins over the timeout.
	if ctx.Err() != nil {
		reason = stopInterrupted
	}
	if reason == stopInterrupted && !l.install {
		return interrupted()
	}
	c := l.view(l.last)
	c.Observation = &view.Observation{StartedAt: l.started, EndedAt: ended.UTC(), StopReason: reason, Observed: l.tr.Observed()}
	if err := writeCheck(cmd, cfg.output, c, cfg.build, ended); err != nil {
		return err
	}
	if reason == stopInterrupted || ctx.Err() != nil {
		return interrupted()
	}
	return reportExit(l.last.Published, c, "during the run", cfg.build.Name)
}
