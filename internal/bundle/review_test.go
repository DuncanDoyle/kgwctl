package bundle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/DuncanDoyle/kgwctl/internal/bundle/bundletest"
	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// archiveManifest decodes manifest.json from a.
func archiveManifest(t *testing.T, a bundletest.Archive) Manifest {
	t.Helper()
	var env struct {
		Item Manifest `json:"item"`
	}
	mb, _ := a.File(pb.ManifestFile)
	if err := json.Unmarshal(mb, &env); err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	return env.Item
}

func TestSinkRejectsUnknownEnums(t *testing.T) {
	const id, reason, remote = "Bearer kgwctl-canary-b1-identity", "kgwctl-canary-b1-reason", "KgwctlCanaryRemoteReason"
	h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
	s := h.w.Sink("c")
	if err := s.JSON(pb.Path{"x", "a.json"}, strings.NewReader(`{}`), pb.Source{Identity: id}); !errors.Is(err, pb.ErrInvalidPath) {
		t.Errorf("JSON with unknown identity: err = %v", err)
	}
	if err := s.Exclude(schema.GroupKind{Kind: "Service"}, reason); !errors.Is(err, pb.ErrInvalidPath) {
		t.Errorf("Exclude with unknown reason: err = %v", err)
	}
	if err := s.Exclude(schema.GroupKind{Kind: "Service"}, pb.ExcludeNone); !errors.Is(err, pb.ErrInvalidPath) {
		t.Errorf("Exclude with no reason: err = %v", err)
	}
	s.Error(pb.Path{"x", "b.json"}, pb.Source{Identity: id}, errors.New("boom"))
	s.Error(pb.Path{"x", "c.json"}, pb.Source{}, &apierrors.StatusError{ErrStatus: metav1.Status{Code: 403, Reason: remote}})
	h.w.Seal()
	a := h.finish(t, Manifest{}, "c")
	bundletest.AssertNoCanary(t, a, []string{remote})
	if it := itemByPath(h.w.Items(), "x/a.json"); len(it) != 1 || it[0].Status != pb.ItemFailed || it[0].Reason != "invalid source" {
		t.Errorf("x/a.json item %+v", it)
	}
	if it := itemByPath(h.w.Items(), "x/c.json"); len(it) != 1 || it[0].Reason != "request failed (403)" {
		t.Errorf("x/c.json item %+v", it)
	}
	if ex := h.w.Excluded(); len(ex) != 0 {
		t.Errorf("Excluded() = %+v", ex)
	}
}

func TestCapExcludedManyReasons(t *testing.T) {
	var m Manifest
	for i := range 1100 {
		m.Excluded = append(m.Excluded, Exclusion{Kind: "core/Service", Reason: pb.ExcludeReason(fmt.Sprintf("r%d", i)), Count: 1})
	}
	Bound(&m, 0)
	total := 0
	for _, e := range m.Excluded {
		total += e.Count
	}
	if total != 1100 {
		t.Errorf("total %d, want 1100", total)
	}
}

func TestManifestFooterNeverOverCap(t *testing.T) {
	l := smallLimits()
	l.Manifest = 2048
	h := newHarness(t, l, redact.Options{}, "")
	// Exclusions are never cut, so 40 long groups keep even the minimal manifest over 2 KiB.
	for i := range 40 {
		group := fmt.Sprintf("g%d.", i) + strings.Repeat("a", 200) + ".io"
		if err := h.w.Sink("c").Exclude(schema.GroupKind{Group: group, Kind: "Route"}, pb.ExcludeScope); err != nil {
			t.Fatal(err)
		}
	}
	m := Manifest{Finalization: "complete"}
	h.w.Seal()
	a := h.finish(t, m)
	mb, _ := a.File(pb.ManifestFile)
	if int64(len(mb)) > l.Manifest {
		t.Fatalf("manifest.json %d bytes, cap %d", len(mb), l.Manifest)
	}
	if got := archiveManifest(t, a); !got.Truncated {
		t.Error("placeholder manifest not marked truncated")
	}
	if eb, _ := a.File(pb.ErrorsFile); !strings.Contains(string(eb), pb.ManifestFile) {
		t.Errorf("errors.txt does not record the placeholder:\n%s", eb)
	}
}

func TestErrorRecordsBounded(t *testing.T) {
	h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
	s := h.w.Sink("c")
	for i := range 5000 {
		s.Error(pb.Path{"e", fmt.Sprintf("e-%05d.json", i)}, pb.Source{}, errors.New("boom"))
	}
	h.w.Seal()
	a := h.finish(t, Manifest{}, "c")
	if n := len(h.w.Items()); n != 4096 {
		t.Errorf("%d item records, want 4096", n)
	}
	if got := archiveManifest(t, a); got.ItemsOmitted != 904 || len(got.Items) != 4096 {
		t.Errorf("manifest items %d, omitted %d", len(got.Items), got.ItemsOmitted)
	}
	if eb, _ := a.File(pb.ErrorsFile); !strings.Contains(string(eb), "c: 904 more items not listed") {
		t.Errorf("errors.txt has no omitted line:\n%s", eb[len(eb)-200:])
	}
}

// allocated returns the bytes fn allocates.
func allocated(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestStructuredIndentBounded(t *testing.T) {
	h := newHarness(t, smallLimits(), redact.Options{}, "")
	s := h.w.Sink("c")
	// 3000 nested arrays are 6 KB compact and about 18 MB indented.
	deep := strings.Repeat("[", 3000) + strings.Repeat("]", 3000)
	p := pb.Path{"x", "deep.json"}
	var err error
	if n := allocated(func() { err = s.ConfigDump(p, strings.NewReader(deep), pb.Source{}) }); n > 32<<20 {
		t.Errorf("ConfigDump allocated %d bytes", n)
	}
	if err != nil {
		t.Fatalf("ConfigDump: %v", err)
	}
	// 2000 nested maps indent to about 4 MB of YAML, past the 256 KiB cap.
	obj := map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "s"}}
	cur := obj
	for range 2000 {
		next := map[string]any{}
		cur["a"] = next
		cur = next
	}
	op := pb.Path{"x", "deep.yaml"}
	if n := allocated(func() { err = s.Object(op, &unstructured.Unstructured{Object: obj}, pb.Source{}) }); n > 24<<20 { // the redactor's locations cost O(depth²); YAML would add 40 MB
		t.Errorf("Object allocated %d bytes", n)
	}
	if !errors.Is(err, pb.ErrBudget) {
		t.Errorf("Object: err = %v, want ErrBudget", err)
	}
	h.w.Seal()
	a := h.finish(t, Manifest{}, "c")
	if d, ok := a.File(p.String()); !ok || string(d) != deep+"\n" {
		t.Errorf("config dump present %v, %d bytes", ok, len(d))
	}
}

func TestTextMarkerReserved(t *testing.T) {
	l := smallLimits()
	sp, err := NewSpool(t.TempDir(), l.RAM, indexCost+1000)
	if err != nil {
		t.Fatal(err)
	}
	w := NewWriter(Stdout(io.Discard), sp, "root", l, redact.Options{}, "", fixedNow)
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- w.Sink("c").Text(pb.Path{"x", "a.log"}, pr, pb.Source{}) }()
	// Ten lines fill the spool exactly without a reserve; with one, Text stops
	// at the tenth. Either way the spool stays within its bound.
	wrote := make(chan struct{})
	go func() {
		defer close(wrote)
		for range 10 {
			_, _ = pw.Write([]byte(strings.Repeat("l", 99) + "\n"))
		}
		_, _ = pw.Write([]byte("partial"))
	}()
	finished := false
	select {
	case <-wrote:
	case <-done:
		finished = true
	}
	w.Seal()
	if used := sp.used(); used > sp.max {
		t.Errorf("spool used %d > bound %d after the seal marker", used, sp.max)
	}
	pw.CloseWithError(errors.New("canceled"))
	if !finished {
		<-done
	}
	if err := w.Finalize([]string{"c"}, Manifest{}); err != nil {
		t.Fatal(err)
	}
}

func TestRunReturnsCommittedManifest(t *testing.T) {
	const canary = "kgwctl-canary-b6-context"
	h := newHarness(t, smallLimits(), redact.Options{}, "")
	c := fake("c", func(_ context.Context, s pb.Sink) error {
		// Nine items fit the spool but not the item budget.
		for i := range 9 {
			if err := s.Text(pb.Path{"logs", fmt.Sprintf("big-%02d.log", i)}, bytes.NewReader(textBody(fmt.Sprint(i), 100)), pb.Source{}); err != nil {
				return err
			}
		}
		return nil
	})
	m, err := Run(context.Background(), []pb.Collector{c}, runEnv(), h.w, Manifest{Context: "ctx Bearer " + canary}, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	a := bundletest.Extract(t, bytes.NewReader(h.buf.Bytes()))
	var got bytes.Buffer
	if err := WriteManifest(&got, m); err != nil {
		t.Fatal(err)
	}
	if mb, _ := a.File(pb.ManifestFile); !bytes.Equal(got.Bytes(), mb) {
		t.Errorf("returned manifest differs from manifest.json")
	}
	if strings.Contains(m.Context, canary) {
		t.Errorf("returned context %q", m.Context)
	}
	if st := statusOf(t, m, "c").Status; st != pb.CollectorPartial {
		t.Errorf("collector status %s, want partial", st)
	}
}

func TestCommitPathConflicts(t *testing.T) {
	t.Run("file-dir", func(t *testing.T) {
		for _, order := range [][]string{{"a", "b"}, {"b", "a"}} {
			h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
			writes := map[string]pb.Path{"a": {"d", "f"}, "b": {"d", "f", "g.txt"}}
			for c, p := range writes {
				if err := h.w.Sink(c).Text(p, strings.NewReader(c+"\n"), pb.Source{}); err != nil {
					t.Fatal(err)
				}
			}
			h.w.Seal()
			h.finish(t, Manifest{}, order...)
			later := writes[order[1]].String()
			if it := itemByPath(h.w.Items(), later); len(it) != 1 || it[0].Status != pb.ItemDropped || it[0].Reason != "path conflict" {
				t.Errorf("order %v: %s item %+v", order, later, it)
			}
		}
	})
	t.Run("case-within-collector", func(t *testing.T) {
		for _, first := range []pb.Path{{"x", "f.txt"}, {"X", "F.txt"}} {
			h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
			second := pb.Path{"X", "F.txt"}
			if first[0] == "X" {
				second = pb.Path{"x", "f.txt"}
			}
			for _, p := range []pb.Path{first, second} {
				_ = h.w.Sink("c").Text(p, strings.NewReader(p.String()+"\n"), pb.Source{})
			}
			h.w.Seal()
			a := h.finish(t, Manifest{}, "c")
			// Commit order is by path, so X/F.txt wins whichever came first.
			if d, ok := a.File("X/F.txt"); !ok || string(d) != "X/F.txt\n" {
				t.Errorf("first %v: X/F.txt = %q, %v", first, d, ok)
			}
		}
	})
	t.Run("dropped-does-not-claim", func(t *testing.T) {
		h := newHarness(t, smallLimits(), redact.Options{}, "")
		// a's three fillers commit first; its d/f.log is then over the budget.
		for i := range 3 {
			if err := h.w.Sink("a").Text(pb.Path{"a", fmt.Sprintf("big-%d.log", i)}, bytes.NewReader(textBody("f", 240)), pb.Source{}); err != nil {
				t.Fatal(err)
			}
		}
		p := pb.Path{"d", "f.log"}
		if err := h.w.Sink("a").Text(p, bytes.NewReader(textBody("big", 240)), pb.Source{}); err != nil {
			t.Fatal(err)
		}
		if err := h.w.Sink("b").Text(p, strings.NewReader("small\n"), pb.Source{}); err != nil {
			t.Fatal(err)
		}
		h.w.Seal()
		a := h.finish(t, Manifest{}, "a", "b")
		if d, _ := a.File(p.String()); string(d) != "small\n" {
			t.Errorf("d/f.log = %d bytes, want b's small item", len(d))
		}
	})
}

// panicDest panics on its first write.
type panicDest struct {
	Dest
	aborted bool
}

func (*panicDest) Write([]byte) (int, error) { panic("disk gone") }
func (d *panicDest) Abort() error            { d.aborted = true; return nil }

func TestFinalizePanicAborts(t *testing.T) {
	d := &panicDest{Dest: Stdout(io.Discard)}
	w := stagedWriter(t, d)
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Finalize panicked: %v", r)
			}
		}()
		err = w.Finalize([]string{"c"}, Manifest{})
	}()
	if err == nil || !d.aborted {
		t.Errorf("err = %v, aborted %v", err, d.aborted)
	}
}

func TestFinalizeEncodeErrorAborts(t *testing.T) {
	p := t.TempDir() + "/out.tar.gz"
	f, err := CreateFile(p)
	if err != nil {
		t.Fatal(err)
	}
	w := stagedWriter(t, f)
	// encoding/json refuses a year past 9999.
	if err := w.Finalize([]string{"c"}, Manifest{Start: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}); err == nil {
		t.Fatal("Finalize succeeded with an unencodable manifest")
	}
	if _, err := CreateFile(p); err != nil {
		t.Errorf("output file left behind: %v", err)
	}
}

func TestRunNoCollectAfterCancel(t *testing.T) {
	for i := range 20 {
		h := newHarness(t, mustLimits(t, MinSize), redact.Options{}, "")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var cs []pb.Collector
		var fs []*fakeCollector
		for j := range 4 {
			f := fake(fmt.Sprintf("c%d", j), func(context.Context, pb.Sink) error { return nil })
			cs, fs = append(cs, f), append(fs, f)
		}
		m, err := Run(ctx, cs, runEnv(), h.w, Manifest{}, RunOptions{Workers: 4})
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fs {
			if f.called.Load() {
				t.Fatalf("run %d: %s called after cancel", i, f.name)
			}
			if st := statusOf(t, m, f.name).Status; st != pb.CollectorTimeout {
				t.Fatalf("run %d: %s = %s", i, f.name, st)
			}
		}
	}
}

func TestRunSealAcceptance(t *testing.T) {
	h := newHarness(t, mustLimits(t, MinSize), redact.Options{}, "")
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	route := schema.GroupKind{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute"}
	done := make(chan struct{})
	logs := fake("logs", func(_ context.Context, s pb.Sink) error {
		defer close(done)
		_ = s.Exclude(route, pb.ExcludeScope)
		_ = s.Exclude(route, pb.ExcludeScope)
		go func() {
			_, _ = pw.Write([]byte("line one\n"))
			// Text handles the first line before it reads again.
			_, _ = pw.Write([]byte("partial"))
			cancel()
		}()
		err := s.Text(pb.Path{"logs", "a.log"}, pr, pb.Source{})
		_ = s.Exclude(route, pb.ExcludeScope) // after the seal: not counted
		return err
	})
	queued := fake("queued", func(context.Context, pb.Sink) error { return nil })
	m, err := Run(ctx, []pb.Collector{logs, queued}, runEnv(), h.w, Manifest{}, RunOptions{Workers: 1})
	pw.CloseWithError(errors.New("canceled"))
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if queued.called.Load() {
		t.Error("queued collector was called")
	}
	got := archiveManifest(t, bundletest.Extract(t, bytes.NewReader(h.buf.Bytes())))
	if e := statusOf(t, got, "logs"); e.Late != 1 || e.Status != pb.CollectorTimeout {
		t.Errorf("logs entry %+v, want late 1 and timeout", e)
	}
	if e := statusOf(t, got, "queued"); e.Status != pb.CollectorTimeout {
		t.Errorf("queued entry %+v", e)
	}
	want := []Exclusion{{Kind: "gateway.networking.k8s.io/HTTPRoute", Reason: pb.ExcludeScope, Count: 2}}
	if len(got.Excluded) != 1 || got.Excluded[0] != want[0] {
		t.Errorf("excluded %+v, want %+v", got.Excluded, want)
	}
	if it := itemByPath(got.Items, "logs/a.log"); len(it) != 1 || it[0].Status != pb.ItemOK {
		t.Errorf("kept text item %+v", it)
	}
	_ = m
}

func TestRunLogCap(t *testing.T) {
	l := smallLimits()
	h := newHarness(t, l, redact.Options{}, "")
	for i := range 60 {
		h.w.Log(fmt.Sprintf("progress line %d %s", i, strings.Repeat("p", 400)))
	}
	h.w.Seal()
	a := h.finish(t, Manifest{})
	rl, _ := a.File(pb.RunLogFile)
	if int64(len(rl)) > l.RunLog || !strings.HasSuffix(string(rl), "\n[kgwctl: truncated: size cap]\n") {
		t.Errorf("run.log %d bytes, cap %d, no marker", len(rl), l.RunLog)
	}
}

func TestErrorCacheBounded(t *testing.T) {
	h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
	s := h.w.Sink("c")
	for i := range 4 {
		s.Error(pb.Path{"e", fmt.Sprintf("e-%d.json", i)}, pb.Source{}, errors.New(fmt.Sprint(i)+strings.Repeat("e ", 128<<10)))
	}
	var kept int
	for k := range h.w.errCache {
		kept += len(k)
	}
	if kept > 4096 {
		t.Errorf("error cache keeps %d key bytes", kept)
	}
}

func TestTextFindingsCapped(t *testing.T) {
	h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
	var in bytes.Buffer
	for range 5000 {
		in.WriteString("Bearer x\n")
	}
	p := pb.Path{"logs", "a.log"}
	if err := h.w.Sink("c").Text(p, &in, pb.Source{}); err != nil {
		t.Fatal(err)
	}
	h.w.Seal()
	a := h.finish(t, Manifest{}, "c")
	var red struct {
		Files []struct {
			Findings        []redact.Finding `json:"findings"`
			FindingsOmitted int              `json:"findingsOmitted"`
		} `json:"files"`
	}
	rb, _ := a.File(pb.RedactionsFile)
	if err := json.Unmarshal(rb, &red); err != nil || len(red.Files) != 1 {
		t.Fatalf("redactions.json: %v", err)
	}
	if f := red.Files[0]; len(f.Findings) != 4096 || f.FindingsOmitted != 904 {
		t.Errorf("findings %d, omitted %d", len(f.Findings), f.FindingsOmitted)
	}
	if it := itemByPath(h.w.Items(), p.String()); len(it) != 1 || it[0].Redactions != 5000 {
		t.Errorf("item %+v", it)
	}
}

func TestObjectSequenceIndentBounded(t *testing.T) {
	h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
	var v any = "v"
	for range 6000 {
		v = []any{v, nil}
	}
	obj := map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "s"}, "f": v}
	var err error
	n := allocated(func() {
		err = h.w.Sink("c").Object(pb.Path{"x", "s.yaml"}, &unstructured.Unstructured{Object: obj}, pb.Source{})
	})
	if !errors.Is(err, pb.ErrBudget) || n > yamlAllocBound {
		t.Errorf("err = %v, allocated %d bytes", err, n)
	}
}

func TestFinalizeSealPanicAborts(t *testing.T) {
	p := t.TempDir() + "/out.tar.gz"
	f, err := CreateFile(p)
	if err != nil {
		t.Fatal(err)
	}
	l := mustLimits(t, 32<<20)
	dir := t.TempDir()
	sp, err := NewSpool(dir, l.RAM, l.MaxSize)
	if err != nil {
		t.Fatal(err)
	}
	var broken atomic.Bool
	clock := func() time.Time {
		if broken.Load() {
			panic("clock gone")
		}
		return t0
	}
	w := NewWriter(f, sp, "root", l, redact.Options{}, "", clock)
	if err := w.Sink("c").Text(pb.Path{"logs", "a.log"}, strings.NewReader("x\n"), pb.Source{}); err != nil {
		t.Fatal(err)
	}
	broken.Store(true)
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Finalize panicked: %v", r)
			}
		}()
		if err := w.Finalize([]string{"c"}, Manifest{}); err == nil {
			t.Error("Finalize succeeded")
		}
	}()
	if _, err := CreateFile(p); err != nil {
		t.Errorf("output file left behind: %v", err)
	}
	if d := spoolDir(t, dir); d != "" {
		t.Errorf("spool left behind: %s", d)
	}
}

// nameCancels cancels the run on its second Name call, the one runOne logs.
type nameCancels struct {
	fakeCollector
	names  atomic.Int32
	cancel func()
}

func (n *nameCancels) Name() string {
	if n.names.Add(1) == 2 {
		n.cancel()
	}
	return n.name
}

func TestRunNoCollectWhenNameCancels(t *testing.T) {
	h := newHarness(t, mustLimits(t, MinSize), redact.Options{}, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &nameCancels{fakeCollector: fakeCollector{name: "c", collect: func(context.Context, pb.Sink) error { return nil }}, cancel: cancel}
	_ = c.Name() // Run reads the name first
	reported := false
	runOne(ctx, c, runEnv(), h.w, fixedNow, func(pb.CollectorStatus, error, time.Duration) { reported = true })
	if c.called.Load() || reported {
		t.Errorf("Collect called %v, reported %v after cancel", c.called.Load(), reported)
	}
}

func TestErrorsFooterKeepsCollectorsWithPlaceholder(t *testing.T) {
	l := smallLimits()
	l.Manifest = 2048
	h := newHarness(t, l, redact.Options{}, "")
	for i := range 40 {
		group := fmt.Sprintf("g%d.", i) + strings.Repeat("a", 200) + ".io"
		if err := h.w.Sink("c").Exclude(schema.GroupKind{Group: group, Kind: "Route"}, pb.ExcludeScope); err != nil {
			t.Fatal(err)
		}
	}
	h.w.Seal()
	m := Manifest{Collectors: []CollectorEntry{{Name: "c", Status: pb.CollectorFailed, Error: "boom"}}}
	a := h.finish(t, m, "c")
	if eb, _ := a.File(pb.ErrorsFile); !strings.Contains(string(eb), "c: failed: boom") {
		t.Errorf("errors.txt lost the collector error:\n%s", eb)
	}
}

func TestErrorTextRedactedBeforeCut(t *testing.T) {
	h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
	err := errors.New(`password="dummy-first dummy-second ` + strings.Repeat("x", 16<<10) + `"`)
	h.w.Sink("c").Error(pb.Path{"x", "a.json"}, pb.Source{}, err)
	h.w.Seal()
	a := h.finish(t, Manifest{}, "c")
	bundletest.AssertNoCanary(t, a, []string{"dummy-first", "dummy-second"})
}

func TestObjectMultilineIndentBounded(t *testing.T) {
	h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
	var v any = strings.Repeat("x\n", 20_000)
	for range 1500 {
		v = map[string]any{"f": v}
	}
	obj := map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "s"}, "spec": v}
	var err error
	n := allocated(func() {
		err = h.w.Sink("c").Object(pb.Path{"x", "s.yaml"}, &unstructured.Unstructured{Object: obj}, pb.Source{})
	})
	if !errors.Is(err, pb.ErrBudget) || n > yamlAllocBound {
		t.Errorf("err = %v, allocated %d bytes", err, n)
	}
}

func TestRunSealPanicAborts(t *testing.T) {
	p := t.TempDir() + "/out.tar.gz"
	f, err := CreateFile(p)
	if err != nil {
		t.Fatal(err)
	}
	l := mustLimits(t, MinSize)
	dir := t.TempDir()
	sp, err := NewSpool(dir, l.RAM, l.MaxSize)
	if err != nil {
		t.Fatal(err)
	}
	var broken atomic.Bool
	clock := func() time.Time {
		if broken.Load() {
			panic("clock gone")
		}
		return t0
	}
	w := NewWriter(f, sp, "root", l, redact.Options{}, "", clock)
	c := fake("c", func(_ context.Context, s pb.Sink) error {
		err := s.Text(pb.Path{"logs", "a.log"}, strings.NewReader("x\n"), pb.Source{})
		broken.Store(true) // the seal is the next clock read
		return err
	})
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Run panicked: %v", r)
			}
		}()
		if _, err := Run(context.Background(), []pb.Collector{c}, runEnv(), w, Manifest{}, RunOptions{}); err == nil {
			t.Error("Run succeeded")
		}
	}()
	if _, err := CreateFile(p); err != nil {
		t.Errorf("output file left behind: %v", err)
	}
	if d := spoolDir(t, dir); d != "" {
		t.Errorf("spool left behind: %s", d)
	}
}

func TestObjectYAMLStreamBounded(t *testing.T) {
	wrap := func(leaf string) map[string]any {
		var v any = leaf
		for range 200 {
			v = map[string]any{"f": v}
		}
		return map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "s"}, "spec": v}
	}
	h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
	s := h.w.Sink("c")
	// U+2028 separators: go-yaml indents each one, about 24 MB in all.
	big := wrap("x\n" + strings.Repeat("x\u2028", 60_000) + "x")
	var err error
	n := allocated(func() {
		err = s.Object(pb.Path{"x", "big.yaml"}, &unstructured.Unstructured{Object: big}, pb.Source{})
	})
	if !errors.Is(err, pb.ErrBudget) || n > yamlAllocBound {
		t.Errorf("big: err = %v, allocated %d bytes", err, n)
	}
	// A trailing blank makes go-yaml quote the scalar: it fits.
	small := wrap(strings.Repeat("x\n", 50_000) + "x ") // a block scalar would be 20 MB
	if err := s.Object(pb.Path{"x", "small.yaml"}, &unstructured.Unstructured{Object: small}, pb.Source{}); err != nil {
		t.Errorf("small: %v", err)
	}
}

// yamlAllocBound is what a dropped object may allocate: the YAML buffer grows
// to the 16 MiB cap (about 32 MB in all), plus the redactor's copies.
const yamlAllocBound = 100 << 20

func TestObjectYAMLFormatUnchanged(t *testing.T) {
	for _, fx := range bundletest.Corpus(t) {
		if fx.Method != bundletest.MethodObject || fx.ExpectError != "" {
			continue
		}
		u := objectFromYAML(t, fx.Input)
		want, err := yaml.Marshal(u.Object)
		if err != nil {
			t.Fatal(err)
		}
		got, over, err := encodeYAML(u.Object, 16<<20)
		if err != nil || over || !bytes.Equal(got, want) {
			t.Errorf("%s/%s: streamed YAML differs from sigs.k8s.io/yaml (err %v, over %v)", fx.Dir, fx.Name, err, over)
		}
	}
}
