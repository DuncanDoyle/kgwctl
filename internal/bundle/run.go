package bundle

import (
	"cmp"
	"context"
	"fmt"
	"sync"
	"time"

	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// defaultWorkers is the collector pool size when RunOptions.Workers is 0.
const defaultWorkers = 8

// RunOptions tune Run.
type RunOptions struct {
	// Workers is the number of collectors that run at once; 0 means 8.
	Workers int
	// Skip names the collectors to leave out.
	Skip map[string]bool
	// NoInstall names the collectors left out because no install was
	// detected; they are skipped with reason "no install". Skip comes first.
	NoInstall map[string]bool
	// Deadline ends collection; the zero time means only ctx ends it.
	Deadline time.Time
	// NotStarted, if set, replaces "collection deadline reached" as the error
	// of every collector unfinished at the seal. Set it only when collection
	// ends at once for another reason, so that no collector starts.
	NotStarted string
}

// result is the outcome of one collector, set once.
type result struct {
	entry CollectorEntry
	done  bool
}

// Run runs cs on a worker pool and writes the archive. Collection ends when
// every collector is done, o.Deadline passes or ctx ends. The writer is then
// sealed, collectors still running are recorded as timeout without being
// waited for, and the archive is finalized with the collectors in
// registration order. A collector is never started once collection has
// ended. The writer re-reads Pods with env.Kube, bounded by
// env.RequestTimeout. Run returns the manifest as written to manifest.json,
// or the finalization error and the zero Manifest.
func Run(ctx context.Context, cs []pb.Collector, env pb.Env, w *Writer, m Manifest, o RunOptions) (Manifest, error) {
	dctx, cancel := context.WithCancel(ctx)
	if !o.Deadline.IsZero() {
		cancel() // release the first context before replacing it
		dctx, cancel = context.WithDeadline(ctx, o.Deadline)
	}
	defer cancel()

	now := env.Now
	if now == nil {
		now = time.Now
		env.Now = now
	}
	// Pod re-reads use the collectors' guarded client and request timeout.
	w.usePods(env.Kube, env.RequestTimeout)
	order := make([]string, len(cs))
	res := make([]result, len(cs))
	var mu sync.Mutex
	var pending []int
	for i, c := range cs {
		order[i] = c.Name()
		res[i].entry = CollectorEntry{Name: order[i]}
		reason := ""
		switch {
		case o.Skip[order[i]]:
			reason = "flag"
		case o.NoInstall[order[i]]:
			reason = string(pb.OmitNoInstall)
		}
		if reason != "" {
			res[i].entry.Status, res[i].entry.Reason, res[i].done = pb.CollectorSkipped, reason, true
			w.Log("collector " + order[i] + " skipped: " + reason)
			continue
		}
		pending = append(pending, i)
	}

	// finish records the outcome of collector i unless collection has ended.
	finish := func(i int, status pb.CollectorStatus, err error, d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		if res[i].done {
			return
		}
		e := &res[i].entry
		e.Status, e.DurationMs, res[i].done = status, d.Milliseconds(), true
		if err != nil {
			e.Error = w.errorText(err)
		} else if status == pb.CollectorTimeout {
			e.Error = "collection deadline reached"
		}
		line := fmt.Sprintf("collector %s %s in %dms", e.Name, status, e.DurationMs)
		if e.Error != "" {
			line += ": " + e.Error
		}
		w.Log(line)
	}

	jobs := make(chan int)
	var wg sync.WaitGroup
	workers := o.Workers
	if workers <= 0 {
		workers = defaultWorkers
	}
	workers = min(workers, len(pending))
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if dctx.Err() != nil {
					continue // left as timeout, never started
				}
				runOne(dctx, cs[i], env, w, now, func(st pb.CollectorStatus, err error, d time.Duration) { finish(i, st, err, d) })
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, i := range pending {
			if dctx.Err() != nil {
				return
			}
			select {
			case jobs <- i:
			case <-dctx.Done():
				return
			}
		}
	}()
	allDone := make(chan struct{})
	go func() { wg.Wait(); close(allDone) }()

	start := now()
	select {
	case <-allDone:
	case <-dctx.Done():
	}
	cancel()
	if err := w.sealOrAbort(); err != nil {
		return Manifest{}, err
	}

	late := w.Late()
	mu.Lock()
	for i := range res {
		e := &res[i].entry
		if !res[i].done {
			e.Status, e.DurationMs, res[i].done = pb.CollectorTimeout, now().Sub(start).Milliseconds(), true
			e.Error = cmp.Or(o.NotStarted, "collection deadline reached")
		}
		e.Late = late[e.Name]
		m.Collectors = append(m.Collectors, *e)
	}
	mu.Unlock()
	if err := w.Finalize(order, m); err != nil {
		return Manifest{}, err
	}
	return w.Committed(), nil
}

// runOne runs c, turning a panic into an error, and reports the outcome. It
// never calls Collect once ctx has ended.
func runOne(ctx context.Context, c pb.Collector, env pb.Env, w *Writer, now func() time.Time, report func(pb.CollectorStatus, error, time.Duration)) {
	name := c.Name()
	w.Log("collector " + name + " start")
	s := w.Sink(name)
	// Checked after all setup, right before Collect: nothing starts once
	// collection has ended, and the collector stays timeout.
	if ctx.Err() != nil {
		return
	}
	t0 := now()
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v", r)
			}
		}()
		return c.Collect(ctx, env, s)
	}()
	d := now().Sub(t0)
	switch {
	// A collector that returns once collection has ended, with or without an
	// error, may have lost writes to the seal.
	case ctx.Err() != nil:
		report(pb.CollectorTimeout, err, d)
	case err == nil:
		report(pb.CollectorOK, nil, d)
	default:
		report(pb.CollectorFailed, err, d)
	}
}
