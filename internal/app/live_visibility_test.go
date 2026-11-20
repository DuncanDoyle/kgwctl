package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/engine"
	corev1 "k8s.io/api/core/v1"

	"github.com/DuncanDoyle/kgwctl/internal/observe"
)

// Modes of switchScraper.
const (
	scrapeClean int32 = iota
	scrapeReject
	scrapeFail
)

// switchScraper serves clean metrics, active xDS rejects, or a failing scrape, by mode.
type switchScraper struct{ mode *atomic.Int32 }

func (s switchScraper) Scrape(ctx context.Context, p *corev1.Pod, port int32) (io.ReadCloser, error) {
	switch s.mode.Load() {
	case scrapeReject:
		return rejecting.Scrape(ctx, p, port)
	case scrapeFail:
		return nil, errors.New("the server does not allow access to the requested resource")
	}
	return metricsScraper{}.Scrape(ctx, p, port)
}

// observedEntry is the part of an ObservedFinding these tests read.
type observedEntry struct {
	Code              string
	FirstSeen         time.Time
	ClearedAt, LostAt *time.Time
	Evidence          json.RawMessage
	NextSteps         []struct{ Command string }
}

// switchRun runs check --live with a switchScraper and returns a function that
// sets the mode and steps the clock until a published model satisfies want.
func switchRun(t *testing.T, timeout string) (*liveRig, *atomic.Int32, func(int32, func(*engine.Published) bool)) {
	t.Helper()
	env := checkEnv(t, checkInstall, checkController)
	mode := &atomic.Int32{}
	published := make(chan *engine.Published, 100000)
	h := fastHooks(switchScraper{mode})
	h.Published = func(p observe.Publication) { published <- p.Published }
	r := startLive(t, env, liveOpts{}, h, "--timeout", timeout, "-o", "json")
	to := func(m int32, want func(*engine.Published) bool) {
		t.Helper()
		mode.Store(m)
		for range 40 {
			r.fc.Step(5 * time.Second)
			at := r.fc.Now()
			deadline := time.After(10 * time.Second)
			for paced := false; !paced; {
				select {
				case p := <-published:
					if want(p) && !p.Model.ComputedAt.Before(at) {
						return
					}
					paced = !p.Model.ComputedAt.Before(at)
				case <-deadline:
					t.Fatalf("no model at %v within 10s", at)
				}
			}
		}
		t.Fatal("no model as wanted within 40 steps")
	}
	return r, mode, to
}

// xdsDown reports whether the xDS rejects check of p could not run.
func xdsDown(p *engine.Published) bool {
	for _, im := range p.Model.Installs {
		for _, c := range im.Health.Checks {
			if c.Name == "xDS rejects" && (c.Unavailable != "" || !c.OK && c.Error != "") {
				return true
			}
		}
	}
	return false
}

func observed001(t *testing.T, stdout string) observedEntry {
	t.Helper()
	var doc struct {
		Item struct {
			Observation struct{ Observed []observedEntry }
		}
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout)
	}
	for _, f := range doc.Item.Observation.Observed {
		if f.Code == "KGWUI-001" {
			return f
		}
	}
	t.Fatalf("observed lacks 001:\n%s", stdout)
	return observedEntry{}
}

// TestLiveRejectionThenScrapeFails: the rejection is no longer observed, never cleared.
func TestLiveRejectionThenScrapeFails(t *testing.T) {
	r, _, to := switchRun(t, "5m")
	to(scrapeReject, has001)
	to(scrapeFail, func(p *engine.Published) bool { return !has001(p) && xdsDown(p) })
	res := r.finish()
	f := observed001(t, res.stdout)
	if f.ClearedAt != nil || f.LostAt == nil || f.LostAt.Before(f.FirstSeen) {
		t.Errorf("001 cleared %v lost %v, want lost after first seen and not cleared:\n%s", f.ClearedAt, f.LostAt, res.stdout)
	}
	if len(f.Evidence) == 0 {
		t.Errorf("001 lost its evidence:\n%s", res.stdout)
	}
	p := r.progress.String()
	if strings.Contains(p, "CLEARED  KGWUI-001") || !strings.Contains(p, "UNKNOWN  KGWUI-001") {
		t.Errorf("progress must show 001 as UNKNOWN, never CLEARED:\n%s", p)
	}
}

// TestLiveRejectionThenCleanScrape: the check ran without the finding, so it cleared.
func TestLiveRejectionThenCleanScrape(t *testing.T) {
	r, _, to := switchRun(t, "5m")
	to(scrapeReject, has001)
	to(scrapeClean, func(p *engine.Published) bool { return !has001(p) })
	res := r.finish()
	f := observed001(t, res.stdout)
	if f.ClearedAt == nil || f.LostAt != nil {
		t.Errorf("001 cleared %v lost %v, want cleared:\n%s", f.ClearedAt, f.LostAt, res.stdout)
	}
	if len(f.Evidence) == 0 {
		t.Errorf("cleared 001 lost its evidence:\n%s", res.stdout)
	}
	if !strings.Contains(r.progress.String(), "CLEARED  KGWUI-001") {
		t.Errorf("progress has no CLEARED line for 001:\n%s", r.progress.String())
	}
}

// TestLiveScrapeFailsAndRecovers: the collector still publishes, yet the checks reading it stop and resume.
func TestLiveScrapeFailsAndRecovers(t *testing.T) {
	r, _, to := switchRun(t, "5m")
	to(scrapeFail, xdsDown)
	to(scrapeClean, func(p *engine.Published) bool { return !xdsDown(p) })
	r.finish()
	p := r.progress.String()
	down, up := strings.Index(p, "CHECK    xDS rejects: not checked: pod "), strings.Index(p, "CHECK    xDS rejects: checked again")
	if down < 0 || up < down {
		t.Errorf("progress lacks a check stopping, then running again (at %d, %d):\n%s", down, up, p)
	}
}
