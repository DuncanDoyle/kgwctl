package bundle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/DuncanDoyle/kgwctl/internal/bundle/bundletest"
	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// TestOmitRecorded: each reason gives its status and reason in the manifest,
// with no archive member, redactions entry or errors.txt line, and the
// collector stays ok.
func TestOmitRecorded(t *testing.T) {
	for _, tc := range []struct {
		reason pb.OmitReason
		want   pb.ItemStatus
	}{
		{pb.OmitNotRunning, pb.ItemSkipped},
		{pb.OmitNoInstall, pb.ItemSkipped},
		{pb.OmitNoMetricsPort, pb.ItemDisabled},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
			p := pb.Path{"proxies", "gw", "p-0", pb.MetricsFile}
			src := pb.Source{Namespace: "gw", Pod: "p-0", UID: uid1, Operation: pb.OpMetrics}
			c := fake("metrics", func(ctx context.Context, s pb.Sink) error {
				if err := s.Omit(p, src, tc.reason); err != nil {
					return err
				}
				return s.Text(ctx, pb.Path{"x", "ok.txt"}, strings.NewReader("Bearer abc.def-ghi\n"), pb.Source{})
			})
			m, err := Run(t.Context(), []pb.Collector{c}, runEnv(), h.w, Manifest{}, RunOptions{})
			if err != nil {
				t.Fatal(err)
			}
			a := bundletest.Extract(t, bytes.NewReader(h.buf.Bytes()))
			it := itemByPath(archiveManifest(t, a).Items, p.String())
			if len(it) != 1 || it[0].Status != tc.want || it[0].Reason != string(tc.reason) || it[0].Size != 0 || it[0].Redactions != 0 {
				t.Fatalf("omitted item %+v, want %s: %s", it, tc.want, tc.reason)
			}
			if it[0].Source != src {
				t.Errorf("source %+v, want %+v", it[0].Source, src)
			}
			if _, ok := a.File(p.String()); ok {
				t.Error("omission has an archive member")
			}
			if rb, _ := a.File(pb.RedactionsFile); strings.Contains(string(rb), p.String()) {
				t.Errorf("redactions.json names the omission:\n%s", rb)
			}
			if eb, _ := a.File(pb.ErrorsFile); len(eb) != 0 {
				t.Errorf("errors.txt:\n%s", eb)
			}
			if e := statusOf(t, m, "metrics"); e.Status != pb.CollectorOK {
				t.Errorf("collector %+v, want ok", e)
			}
		})
	}
}

// TestOmitRejected: an invalid path, reason or a caller-set identity is an
// error and records nothing; after the seal Omit returns ErrSealed.
func TestOmitRejected(t *testing.T) {
	h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
	s := h.w.Sink("c")
	good := pb.Path{"proxies", "gw", "p-0", pb.MetricsFile}
	for name, call := range map[string]func() error{
		"path":     func() error { return s.Omit(pb.Path{"bad", "a:b.txt"}, pb.Source{}, pb.OmitNotRunning) },
		"footer":   func() error { return s.Omit(pb.Path{pb.ErrorsFile}, pb.Source{}, pb.OmitNotRunning) },
		"reason":   func() error { return s.Omit(good, pb.Source{}, "kgwctl-canary-omit-reason") },
		"empty":    func() error { return s.Omit(good, pb.Source{}, "") },
		"identity": func() error { return s.Omit(good, pb.Source{Identity: pb.IdentityVerified}, pb.OmitNotRunning) },
	} {
		if err := call(); !errors.Is(err, pb.ErrInvalidPath) || strings.Contains(err.Error(), "canary") {
			t.Errorf("%s: err = %v, want ErrInvalidPath without the value", name, err)
		}
	}
	if items := h.w.Items(); len(items) != 0 {
		t.Errorf("rejected omissions recorded: %+v", items)
	}
	h.w.Seal()
	if err := s.Omit(good, pb.Source{}, pb.OmitNotRunning); !errors.Is(err, pb.ErrSealed) {
		t.Errorf("after seal: err = %v, want ErrSealed", err)
	}
	if items := h.w.Items(); len(items) != 0 {
		t.Errorf("post-seal omission recorded: %+v", items)
	}
}

// TestOmitClaimsNoPath: an omission at the path of a later real item, in the
// same or an earlier collector, does not stop that item from committing.
func TestOmitClaimsNoPath(t *testing.T) {
	h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
	p := pb.Path{"proxies", "gw", "p-0", "stats.txt"}
	if err := h.w.Sink("a").Omit(p, pb.Source{}, pb.OmitNotRunning); err != nil {
		t.Fatal(err)
	}
	if err := h.w.Sink("b").Omit(p, pb.Source{}, pb.OmitNotRunning); err != nil {
		t.Fatal(err)
	}
	if err := h.w.Sink("b").Text(bg, p, strings.NewReader("real\n"), pb.Source{}); err != nil {
		t.Fatal(err)
	}
	h.w.Seal()
	a := h.finish(t, Manifest{}, "a", "b")
	if data, _ := a.File(p.String()); string(data) != "real\n" {
		t.Errorf("member %q, want the real item", data)
	}
	var ok, skipped int
	for _, it := range itemByPath(archiveManifest(t, a).Items, p.String()) {
		switch it.Status {
		case pb.ItemOK:
			ok++
		case pb.ItemSkipped:
			skipped++
		}
	}
	if ok != 1 || skipped != 2 {
		t.Errorf("%d ok and %d skipped items, want 1 and 2", ok, skipped)
	}
}

// TestOmitBounded: omissions past the per-collector list bound count in the
// manifest's omitted count, separately from failures, with no errors.txt line.
func TestOmitBounded(t *testing.T) {
	h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
	s := h.w.Sink("c")
	for i := range maxFailRecords + 3 {
		if err := s.Omit(pb.Path{"o", fmt.Sprintf("o-%05d.txt", i)}, pb.Source{}, pb.OmitNotRunning); err != nil {
			t.Fatal(err)
		}
	}
	s.Error(pb.Path{"e", "e.json"}, pb.Source{}, errors.New("boom"))
	h.w.Seal()
	a := h.finish(t, Manifest{Collectors: []CollectorEntry{{Name: "c", Status: pb.CollectorOK}}}, "c")
	got := archiveManifest(t, a)
	if got.ItemsOmitted != 3 || len(got.Items) != maxFailRecords+1 {
		t.Errorf("manifest items %d, omitted %d; want %d and 3", len(got.Items), got.ItemsOmitted, maxFailRecords+1)
	}
	if it := itemByPath(got.Items, "e/e.json"); len(it) != 1 {
		t.Error("the failure past the omissions is not listed")
	}
	eb, _ := a.File(pb.ErrorsFile)
	if strings.Contains(string(eb), "more items not listed") || !strings.Contains(string(eb), "e/e.json") {
		t.Errorf("errors.txt:\n%s", eb)
	}
	if e := statusOf(t, got, "c"); e.Status != pb.CollectorPartial {
		t.Errorf("collector %+v, want partial from the one failure", e)
	}
}
