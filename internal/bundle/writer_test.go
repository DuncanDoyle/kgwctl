package bundle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"

	"github.com/DuncanDoyle/kgwctl/internal/bundle/bundletest"
	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

var t0 = time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

func fixedNow() time.Time { return t0 }

// bg is the context of Sink writes whose capture context does not matter.
var bg = context.Background()

type harness struct {
	w   *Writer
	buf *bytes.Buffer
	sp  *Spool
	dir string
}

func mustLimits(t *testing.T, max int64) Limits {
	t.Helper()
	l, err := DefaultLimits(max)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func newHarness(t *testing.T, l Limits, ro redact.Options, server string) *harness {
	t.Helper()
	dir := t.TempDir()
	sp, err := NewSpool(dir, l.RAM, l.MaxSize)
	if err != nil {
		t.Fatal(err)
	}
	buf := &bytes.Buffer{}
	return &harness{w: NewWriter(Stdout(buf), sp, DefaultName(t0), l, ro, server, fixedNow), buf: buf, sp: sp, dir: dir}
}

func (h *harness) finish(t *testing.T, m Manifest, order ...string) bundletest.Archive {
	t.Helper()
	if err := h.w.Finalize(order, m); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	return bundletest.Extract(t, bytes.NewReader(h.buf.Bytes()))
}

func itemByPath(items []Item, p string) []Item {
	var out []Item
	for _, it := range items {
		if it.Path == p {
			out = append(out, it)
		}
	}
	return out
}

func objectFromYAML(t *testing.T, b []byte) *unstructured.Unstructured {
	t.Helper()
	j, err := yaml.YAMLToJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{}
	if err := u.UnmarshalJSON(j); err != nil {
		t.Fatal(err)
	}
	return u
}

type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, errors.New("stream reset") }

var corpusExt = map[string]string{
	bundletest.MethodObject: ".yaml", bundletest.MethodConfigDump: ".json", bundletest.MethodJSON: ".json",
	bundletest.MethodPrometheus: ".txt", bundletest.MethodText: ".log", bundletest.MethodError: ".json",
}

func TestWriterCorpusReplay(t *testing.T) {
	fixtures := bundletest.Corpus(t)
	if len(fixtures) < 30 {
		t.Fatalf("corpus has %d cases", len(fixtures))
	}
	wantErr := map[string]error{
		"forbidden-kind": pb.ErrForbiddenKind,
		"invalid-json":   redact.ErrInvalidJSON,
		"not-prometheus": redact.ErrNotPrometheus,
	}
	for _, fx := range fixtures {
		t.Run(fx.Dir+"/"+fx.Name, func(t *testing.T) {
			h := newHarness(t, mustLimits(t, 512<<20), redact.Options{ExtraHeaders: fx.ExtraHeaders}, fx.Server)
			s := h.w.Sink("corpus")
			p := pb.Path{"corpus", fx.Name + corpusExt[fx.Method]}
			src := pb.Source{Namespace: "team-a", Pod: "gw-0", UID: uid1, Operation: pb.OpCustom}
			var err error
			switch fx.Method {
			case bundletest.MethodObject:
				err = s.Object(bg, p, objectFromYAML(t, fx.Input), src)
			case bundletest.MethodConfigDump:
				err = s.JSON(bg, JSONConfigDump, p, bytes.NewReader(fx.Input), src)
			case bundletest.MethodJSON:
				err = s.JSON(bg, JSONVersion, p, bytes.NewReader(fx.Input), src)
			case bundletest.MethodPrometheus:
				err = s.Prometheus(bg, p, bytes.NewReader(fx.Input), src)
			case bundletest.MethodText:
				var r io.Reader = bytes.NewReader(fx.Input)
				if fx.ReadError {
					r = io.MultiReader(r, failReader{})
				}
				err = s.Text(bg, p, r, src)
			case bundletest.MethodError:
				s.Error(p, src, fx.Err)
			}
			if fx.ExpectError != "" {
				if !errors.Is(err, wantErr[fx.ExpectError]) {
					t.Fatalf("err = %v, want %v", err, wantErr[fx.ExpectError])
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			m := Manifest{}
			if fx.Name == "context-name" {
				m.Context = strings.TrimSpace(string(fx.Input))
			}
			h.w.Seal()
			a := h.finish(t, m, "corpus")

			data, ok := a.File(p.String())
			written := fx.Method != bundletest.MethodError && fx.ExpectError == ""
			if ok != written {
				t.Fatalf("member %s present = %v, want %v", p, ok, written)
			}
			if !written {
				errs, _ := a.File(pb.ErrorsFile)
				if !strings.Contains(string(errs), p.String()) {
					t.Errorf("errors.txt has no line for %s:\n%s", p, errs)
				}
			}
			for _, k := range fx.FindingsOnly {
				if !strings.Contains(string(data), k) {
					t.Errorf("map key %q missing from the item", k)
				}
				// A map key may stay in its own item only.
				for i := range a.Members {
					if a.Members[i].Name == p.String() {
						a.Members[i].Data = bytes.ReplaceAll(a.Members[i].Data, []byte(k), nil)
					}
				}
			}
			for _, f := range []string{pb.ManifestFile, pb.ErrorsFile, pb.RedactionsFile, pb.RunLogFile} {
				if _, ok := a.File(f); !ok {
					t.Errorf("footer %s missing", f)
				}
			}
			if written {
				checkReplayKept(t, a, fx, p.String(), data)
			}
			bundletest.AssertNoCanary(t, a, fx.Canaries)
		})
	}
}

func TestWriterUndeclaredType(t *testing.T) {
	h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
	s := h.w.Sink("downstream")
	// The sink offers the typed Sink methods plus the built-in JSON writer,
	// whose JSONArtifact parameter code outside the module cannot name (D-029).
	got := reflect.TypeOf(s)
	want := reflect.TypeOf((*JSONSink)(nil)).Elem()
	if got.NumMethod() != want.NumMethod() {
		var names []string
		for i := range got.NumMethod() {
			names = append(names, got.Method(i).Name)
		}
		t.Fatalf("sink methods %v, want only the %d JSONSink methods", names, want.NumMethod())
	}
	if m, _ := got.MethodByName("JSON"); m.Type.In(2) != reflect.TypeOf(JSONArtifact(0)) {
		t.Fatalf("JSON takes %v, want a JSONArtifact", m.Type)
	}
	if _, ok := s.(io.Writer); ok {
		t.Fatal("sink is an io.Writer")
	}
	payload := []byte("raw downstream payload kgwctl-canary-21-raw\n")
	if err := s.JSON(bg, JSONVersion, pb.Path{"x", "raw.json"}, bytes.NewReader(payload), pb.Source{}); !errors.Is(err, redact.ErrInvalidJSON) {
		t.Fatalf("JSON err = %v", err)
	}
	if err := s.Prometheus(bg, pb.Path{"x", "raw.txt"}, bytes.NewReader(payload), pb.Source{}); !errors.Is(err, redact.ErrNotPrometheus) {
		t.Fatalf("Prometheus err = %v", err)
	}
	a := h.finish(t, Manifest{}, "downstream")
	bundletest.AssertNoCanary(t, a, []string{"kgwctl-canary-21-raw"})
}

// textBody is n lines of about 1 KiB each, distinct per seed.
func textBody(seed string, n int) []byte {
	var b bytes.Buffer
	pad := strings.Repeat(". ", 500)
	for i := range n {
		fmt.Fprintf(&b, "%s line %06d %s\n", seed, i, pad)
	}
	return b.Bytes()
}

type stagedItem struct {
	collector string
	path      pb.Path
	body      []byte
}

// smallLimits are DefaultLimits scaled down to a 1 MiB bundle, so budget tests
// stage kilobytes instead of tens of megabytes.
func smallLimits() Limits {
	return Limits{MaxSize: 1 << 20, RAM: 64 << 20, Log: 256 << 10, ConfigDump: 256 << 10, Metrics: 256 << 10, Other: 256 << 10,
		Manifest: 64 << 10, Redactions: 64 << 10, Errors: 16 << 10, RunLog: 16 << 10}
}

// budgetItems total about 0.95 MiB: under the spool bound, over the item budget.
func budgetItems() []stagedItem {
	var out []stagedItem
	for i := range 24 {
		c := "a"
		if i%2 == 1 {
			c = "b"
		}
		out = append(out, stagedItem{c, pb.Path{"logs", fmt.Sprintf("item-%02d.log", i)}, textBody(fmt.Sprint(i), (i%5+1)*14)})
	}
	return out
}

func stageAll(t *testing.T, w *Writer, items []stagedItem) {
	t.Helper()
	for _, it := range items {
		if err := w.Sink(it.collector).Text(bg, it.path, bytes.NewReader(it.body), pb.Source{}); err != nil {
			t.Fatalf("%s: %v", it.path, err)
		}
	}
}

func TestWriterBudgetOrder(t *testing.T) {
	l := smallLimits()
	items := budgetItems()
	reversed := slices.Clone(items)
	slices.Reverse(reversed)

	var kept [2][]string
	for i, order := range [][]stagedItem{items, reversed} {
		h := newHarness(t, l, redact.Options{}, "")
		stageAll(t, h.w, order)
		h.w.Seal()
		a := h.finish(t, Manifest{}, "a", "b")
		if a.TarSize > l.MaxSize {
			t.Errorf("tar content %d > MaxSize %d", a.TarSize, l.MaxSize)
		}
		var dropped int
		for _, it := range h.w.Items() {
			switch {
			case it.Status == pb.ItemOK:
				kept[i] = append(kept[i], it.Path)
			case it.Status == pb.ItemDropped && it.Reason == "size budget":
				dropped++
				if _, ok := a.File(it.Path); ok {
					t.Errorf("dropped %s is in the archive", it.Path)
				}
			default:
				t.Errorf("unexpected item %+v", it)
			}
		}
		if dropped == 0 || len(kept[i]) == 0 {
			t.Fatalf("kept %d, dropped %d: the budget is not exercised", len(kept[i]), dropped)
		}
		slices.Sort(kept[i])
	}
	if !slices.Equal(kept[0], kept[1]) {
		t.Errorf("kept sets differ:\n%v\n%v", kept[0], kept[1])
	}
}

// TestWriterFooterCaps fills every footer past its cap at the smallest
// --max-size and checks the extracted archive.
func TestWriterFooterCaps(t *testing.T) {
	l := mustLimits(t, MinSize)
	h := newHarness(t, l, redact.Options{}, "")
	reason := errors.New("capture failed: " + strings.Repeat("r", 40))
	var order []string
	for c := range 10 {
		name := fmt.Sprintf("c%d", c)
		order = append(order, name)
		s := h.w.Sink(name)
		for i := range 4200 {
			s.Error(pb.Path{"items", fmt.Sprintf("item-%05d.json", i)}, pb.Source{Operation: pb.OpCustom}, reason)
		}
	}
	// 24 documents of 2500 findings each, at distinct schema-field paths,
	// overflow redactions.json.
	names := strings.Fields("apiVersion kind metadata name namespace uid spec status items conditions type reason " +
		"message replicas key operator values template containers image args command ports port protocol")
	var doc bytes.Buffer
	doc.WriteByte('{')
	for i, a := range names {
		if i > 0 {
			doc.WriteByte(',')
		}
		fmt.Fprintf(&doc, "%q:{", a)
		for j, b := range names {
			if j > 0 {
				doc.WriteByte(',')
			}
			fmt.Fprintf(&doc, `%q:{"password":"x","token":"x","apiKey":"x","secret":"x"}`, b)
		}
		doc.WriteByte('}')
	}
	doc.WriteByte('}')
	for i := range 24 {
		if err := h.w.Sink("c0").JSON(bg, JSONVersion, pb.Path{"docs", fmt.Sprintf("d-%02d.json", i)}, bytes.NewReader(doc.Bytes()), pb.Source{}); err != nil {
			t.Fatal(err)
		}
	}
	// run.log is filled in TestRunLogCap: its redactor is too slow under -race here.
	h.w.Log("progress")
	h.w.Seal()
	a := h.finish(t, Manifest{}, order...)

	var total int64
	for _, f := range h.w.footers() {
		b, ok := a.File(f.name)
		if !ok || int64(len(b)) > f.cap {
			t.Errorf("%s present %v, %d bytes, cap %d", f.name, ok, len(b), f.cap)
		}
		total += int64(len(b))
	}
	if total > MinSize/4 {
		t.Errorf("footers total %d > %d", total, MinSize/4)
	}
	if m := archiveManifest(t, a); m.ItemsOmitted < 10*(4200-4096) {
		t.Errorf("itemsOmitted = %d", m.ItemsOmitted)
	}
	if b, _ := a.File(pb.ErrorsFile); !strings.HasSuffix(string(b), "\n[kgwctl: truncated: size cap]\n") {
		t.Error("errors.txt does not end with a truncation line")
	}
	var red struct {
		Files []struct {
			FindingsOmitted int `json:"findingsOmitted"`
		} `json:"files"`
	}
	rb, _ := a.File(pb.RedactionsFile)
	if err := json.Unmarshal(rb, &red); err != nil || len(red.Files) == 0 || red.Files[len(red.Files)-1].FindingsOmitted == 0 {
		t.Errorf("redactions.json: %v, %+v", err, red)
	}
	if a.TarSize > l.MaxSize {
		t.Errorf("tar content %d > %d", a.TarSize, l.MaxSize)
	}
}

func TestWriterItemCaps(t *testing.T) {
	l := mustLimits(t, 512<<20)
	l.Log = 1 << 20 // the 10 MiB log cap, scaled down to keep -race fast
	h := newHarness(t, l, redact.Options{}, "")
	s := h.w.Sink("c")

	big := io.MultiReader(strings.NewReader(`{"a":"`), io.LimitReader(repeatReader('a'), 70<<20), strings.NewReader(`"}`))
	dump := pb.Path{"proxies", "ns", "gw", pb.ConfigDumpFile}
	if err := s.JSON(bg, JSONConfigDump, dump, big, pb.Source{}); !errors.Is(err, pb.ErrBudget) {
		t.Fatalf("70 MiB config dump: err = %v, want ErrBudget", err)
	}
	log := pb.Path{"controller", "ns", "pod", "logs", "c.log"}
	if err := s.Text(bg, log, bytes.NewReader(textBody("big", 1100)), pb.Source{}); err != nil {
		t.Fatal(err)
	}
	h.w.Seal()
	a := h.finish(t, Manifest{}, "c")

	if _, ok := a.File(dump.String()); ok {
		t.Error("the config dump is in the archive")
	}
	if it := itemByPath(h.w.Items(), dump.String()); len(it) != 1 || it[0].Status != pb.ItemDropped || it[0].Reason != "size cap" {
		t.Errorf("config dump item %+v", it)
	}
	errs, _ := a.File(pb.ErrorsFile)
	if !strings.Contains(string(errs), dump.String()) {
		t.Errorf("errors.txt has no line for the config dump:\n%s", errs)
	}
	data, ok := a.File(log.String())
	if !ok || int64(len(data)) > l.Log {
		t.Fatalf("log present %v, %d bytes, cap %d", ok, len(data), l.Log)
	}
	if !strings.HasSuffix(string(data), "\n[kgwctl: truncated: size cap]\n") {
		t.Errorf("log does not end with a truncation line: %q", data[len(data)-80:])
	}
	if strings.Count(string(data), "[kgwctl: truncated") != 1 {
		t.Error("more than one truncation line")
	}
}

type repeatReader byte

func (r repeatReader) Read(p []byte) (int, error) {
	chunk := bytes.Repeat([]byte{byte(r)}, 64<<10)
	n := 0
	for n < len(p) {
		n += copy(p[n:], chunk)
	}
	return n, nil
}

func TestWriterSealTextKeepsLines(t *testing.T) {
	h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
	pr, pw := io.Pipe()
	p := pb.Path{"controller", "ns", "pod", "logs", "c.log"}
	done := make(chan error, 1)
	go func() { done <- h.w.Sink("logs").Text(bg, p, pr, pb.Source{}) }()

	if _, err := pw.Write([]byte("line one\nAuthorization: Bearer abc.def-ghi\n")); err != nil {
		t.Fatal(err)
	}
	// Text handles both lines before it reads again, so this write returning
	// means both are staged.
	if _, err := pw.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	// The runner seals and finalizes while the stream is still open.
	h.w.Seal()
	a := h.finish(t, Manifest{}, "logs")
	pw.CloseWithError(errors.New("canceled"))
	if err := <-done; !errors.Is(err, pb.ErrSealed) {
		t.Errorf("Text err = %v, want ErrSealed", err)
	}
	if n := h.w.Late()["logs"]; n != 1 {
		t.Errorf("late = %d, want 1: the call was in flight", n)
	}
	data, _ := a.File(p.String())
	if want := "line one\nAuthorization: Bearer [redacted]\n[kgwctl: truncated: sealed]\n"; string(data) != want {
		t.Errorf("log = %q, want %q", data, want)
	}
	if it := itemByPath(h.w.Items(), p.String()); len(it) != 1 || it[0].Redactions != 1 {
		t.Errorf("item %+v, want 1 redaction", it)
	}
	rb, _ := a.File(pb.RedactionsFile)
	if !strings.Contains(string(rb), `"path":"`+p.String()+`"`) || !strings.Contains(string(rb), `"location":"line:2"`) {
		t.Errorf("redactions.json lacks the sealed item's finding:\n%s", rb)
	}
}

// signalReader closes started on its first Read, then reads r.
type signalReader struct {
	once    sync.Once
	started chan struct{}
	r       io.Reader
}

func (s *signalReader) Read(p []byte) (int, error) {
	s.once.Do(func() { close(s.started) })
	return s.r.Read(p)
}

func TestWriterLateSampledAtSeal(t *testing.T) {
	h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
	s := h.w.Sink("c")
	var pws []*io.PipeWriter
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		pr, pw := io.Pipe()
		pws = append(pws, pw)
		sr := &signalReader{started: make(chan struct{}), r: pr}
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.JSON(bg, JSONVersion, pb.Path{"late", fmt.Sprintf("f%d.json", i)}, sr, pb.Source{})
		}()
		<-sr.started
	}
	h.w.Seal()
	if n := h.w.Late()["c"]; n != 2 {
		t.Fatalf("Late()[c] = %d, want 2", n)
	}
	for _, pw := range pws {
		_, _ = pw.Write([]byte(`{"a":"b"}`))
		pw.Close()
	}
	wg.Wait()
	for i, err := range errs {
		if !errors.Is(err, pb.ErrSealed) {
			t.Errorf("call %d: err = %v, want ErrSealed", i, err)
		}
	}
	if err := s.JSON(bg, JSONVersion, pb.Path{"late", "after.json"}, strings.NewReader(`{}`), pb.Source{}); !errors.Is(err, pb.ErrSealed) {
		t.Errorf("after seal: err = %v", err)
	}
	if n := h.w.Late()["c"]; n != 2 {
		t.Errorf("Late()[c] = %d after a post-seal call", n)
	}
	a := h.finish(t, Manifest{}, "c")
	for _, m := range a.Members {
		if strings.HasPrefix(m.Name, "late/") {
			t.Errorf("late member %s in the archive", m.Name)
		}
	}
}

func TestWriterDuplicates(t *testing.T) {
	for _, first := range []string{"a", "b"} {
		t.Run("first-"+first, func(t *testing.T) {
			h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
			writes := map[string]pb.Path{"a": {"x", "f.txt"}, "b": {"X", "F.txt"}}
			order := []string{first, map[string]string{"a": "b", "b": "a"}[first]}
			for _, c := range order {
				if err := h.w.Sink(c).Text(bg, writes[c], strings.NewReader("from "+c+"\n"), pb.Source{}); err != nil {
					t.Fatal(err)
				}
			}
			if err := h.w.Sink("a").Text(bg, pb.Path{"x", "f.txt"}, strings.NewReader("again\n"), pb.Source{}); !errors.Is(err, pb.ErrDuplicate) {
				t.Errorf("same collector, same path: err = %v", err)
			}
			if err := h.w.Sink("a").Text(bg, pb.Path{pb.ErrorsFile}, strings.NewReader("x\n"), pb.Source{}); !errors.Is(err, pb.ErrInvalidPath) {
				t.Errorf("errors.txt: err = %v", err)
			}
			if err := h.w.Sink("a").JSON(bg, JSONVersion, pb.Path{"bad", "a:b.json"}, strings.NewReader("{}"), pb.Source{}); !errors.Is(err, pb.ErrInvalidPath) || strings.Contains(err.Error(), "a:b") {
				t.Errorf("bad name: err = %v, want ErrInvalidPath without the value", err)
			}
			h.w.Seal()
			a := h.finish(t, Manifest{}, "a", "b")

			if d, _ := a.File("x/f.txt"); string(d) != "from a\n" {
				t.Errorf("x/f.txt = %q", d)
			}
			if _, ok := a.File("X/F.txt"); ok {
				t.Error("b's X/F.txt is in the archive")
			}
			items := h.w.Items()
			if it := itemByPath(items, "X/F.txt"); len(it) != 1 || it[0].Status != pb.ItemDropped || it[0].Reason != "duplicate path" {
				t.Errorf("X/F.txt item %+v", it)
			}
			var invalid int
			for _, it := range items {
				if it.Status == pb.ItemFailed && it.Reason == "invalid path" {
					invalid++
					if strings.Contains(it.Path, "a:b") {
						t.Errorf("invalid path recorded with its value: %q", it.Path)
					}
				}
			}
			if invalid != 2 {
				t.Errorf("%d invalid path items, want 2: %+v", invalid, items)
			}
			errs, _ := a.File(pb.ErrorsFile)
			if !strings.Contains(string(errs), "X/F.txt") || !strings.Contains(string(errs), "invalid path") {
				t.Errorf("errors.txt:\n%s", errs)
			}
		})
	}
}

func TestWriterRegularEntries(t *testing.T) {
	h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
	s := h.w.Sink("c")
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
		"metadata": map[string]any{"name": "r", "namespace": "ns"},
	}}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Object(bg, pb.Path{pb.ResourcesDir, "gateway.networking.k8s.io", "HTTPRoute", "ns", "r.yaml"}, u, pb.Source{}))
	must(s.JSON(bg, JSONVersion, pb.Path{pb.ClusterDir, pb.VersionFile}, strings.NewReader(`{"v":"1"}`), pb.Source{}))
	must(s.Prometheus(bg, pb.Path{"controller", "ns", "p", pb.MetricsFile}, strings.NewReader("# TYPE up gauge\nup 1\n"), pb.Source{}))
	must(s.JSON(bg, JSONConfigDump, pb.Path{"proxies", "ns", "p", pb.ConfigDumpFile}, strings.NewReader(`{"configs":[]}`), pb.Source{}))
	must(s.Text(bg, pb.Path{"proxies", "ns", "p", pb.StatsFile}, strings.NewReader("a: 1\n"), pb.Source{}))
	h.w.Seal()
	a := h.finish(t, Manifest{}, "c")
	if a.Root != DefaultName(t0) {
		t.Errorf("root %q", a.Root)
	}
	if len(a.Members) != 9 {
		t.Errorf("%d members, want 5 items and 4 footers", len(a.Members))
	}
	for _, m := range a.Members {
		if m.Header.Typeflag != '0' || m.Header.Mode != 0o644 {
			t.Errorf("%s: type %q mode %o", m.Name, m.Header.Typeflag, m.Header.Mode)
		}
	}
	y, _ := a.File("resources/gateway.networking.k8s.io/HTTPRoute/ns/r.yaml")
	if !strings.Contains(string(y), "kind: HTTPRoute") {
		t.Errorf("object YAML %q", y)
	}
}

func TestWriterExclude(t *testing.T) {
	h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
	s := h.w.Sink("resources")
	must := func(gk schema.GroupKind, r pb.ExcludeReason) {
		t.Helper()
		if err := s.Exclude(gk, r); err != nil {
			t.Fatal(err)
		}
	}
	must(schema.GroupKind{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute"}, pb.ExcludeScope)
	must(schema.GroupKind{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute"}, pb.ExcludeScope)
	must(schema.GroupKind{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute"}, pb.ExcludeOwnership)
	must(schema.GroupKind{Kind: "Service"}, pb.ExcludeScope)
	for _, gk := range []schema.GroupKind{{Group: "Bad_Group", Kind: "X"}, {Kind: "bad-kind"}, {Kind: ""}} {
		if err := s.Exclude(gk, pb.ExcludeScope); !errors.Is(err, pb.ErrInvalidPath) {
			t.Errorf("%v: err = %v", gk, err)
		}
	}
	h.w.Seal()
	if err := s.Exclude(schema.GroupKind{Kind: "Service"}, pb.ExcludeScope); !errors.Is(err, pb.ErrSealed) {
		t.Errorf("after seal: err = %v", err)
	}
	want := []Exclusion{
		{Kind: "core/Service", Reason: pb.ExcludeScope, Count: 1},
		{Kind: "gateway.networking.k8s.io/HTTPRoute", Reason: pb.ExcludeOwnership, Count: 1},
		{Kind: "gateway.networking.k8s.io/HTTPRoute", Reason: pb.ExcludeScope, Count: 2},
	}
	if got := h.w.Excluded(); !reflect.DeepEqual(got, want) {
		t.Errorf("Excluded() = %+v, want %+v", got, want)
	}
}

func TestFinalizeBoundedBySize(t *testing.T) {
	l := smallLimits()
	h := newHarness(t, l, redact.Options{}, "")
	for i := range 12 {
		p := pb.Path{"logs", fmt.Sprintf("big-%02d.log", i)}
		if err := h.w.Sink("c").Text(bg, p, bytes.NewReader(textBody(fmt.Sprint(i), 100)), pb.Source{}); err != nil && !errors.Is(err, pb.ErrBudget) {
			t.Fatal(err)
		}
	}
	for i := range 2000 {
		h.w.Sink("c").Error(pb.Path{"e", fmt.Sprintf("e-%05d.json", i)}, pb.Source{}, errors.New(strings.Repeat("e", 200)))
	}
	h.w.Seal()
	a := h.finish(t, Manifest{}, "c")
	if a.TarSize > l.MaxSize {
		t.Errorf("tar content %d > MaxSize %d", a.TarSize, l.MaxSize)
	}
	var kept, budget int
	for _, it := range h.w.Items() {
		switch {
		case it.Status == pb.ItemOK:
			kept++
		case it.Status == pb.ItemDropped && it.Reason == ReasonSizeBudget:
			budget++
		}
	}
	if kept == 0 || budget == 0 {
		t.Errorf("kept %d, dropped by budget %d: want both", kept, budget)
	}
}

func TestWriterRemoteErrorStatusOnly(t *testing.T) {
	h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
	// A Status with no code and no reason still never shows its message.
	err := &apierrors.StatusError{ErrStatus: metav1.Status{Message: `{"clientSecret":"x"} kgwctl-canary-i1-body`}}
	h.w.Sink("c").Error(pb.Path{"x", "f.json"}, pb.Source{}, fmt.Errorf("list: %w", err))
	h.w.Seal()
	a := h.finish(t, Manifest{}, "c")
	bundletest.AssertNoCanary(t, a, nil)
	if it := itemByPath(h.w.Items(), "x/f.json"); len(it) != 1 || it[0].Reason != "request failed" {
		t.Errorf("item %+v", it)
	}
}

func TestWriterSourceValidated(t *testing.T) {
	h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
	bad := pb.Source{Namespace: "ns kgwctl-canary-p2-ns", Pod: "Bearer:kgwctl-canary-p2-pod", Container: "C/kgwctl-canary-p2-c", UID: "kgwctl-canary-p2-uid zz"}
	good := pb.Source{Namespace: "team-a", Pod: "gw-0.x", Container: "envoy", UID: uid1}
	kube := kubefake.NewClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "gw-0.x", UID: uid1}})
	var gets []string
	kube.PrependReactor("get", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		gets = append(gets, a.GetNamespace()+"/"+a.(k8stesting.GetAction).GetName())
		return false, nil, nil
	})
	h.w.usePods(kube, time.Minute)
	s := h.w.Sink("c")
	if err := s.JSON(bg, JSONVersion, pb.Path{"x", "bad.json"}, strings.NewReader(`{}`), bad); err != nil {
		t.Fatal(err)
	}
	s.Error(pb.Path{"x", "err.json"}, bad, errors.New("boom"))
	if err := s.JSON(bg, JSONVersion, pb.Path{"x", "good.json"}, strings.NewReader(`{}`), good); err != nil {
		t.Fatal(err)
	}
	h.w.Seal()
	a := h.finish(t, Manifest{}, "c")
	bundletest.AssertNoCanary(t, a, nil)
	good.Identity = pb.IdentityVerified
	if it := itemByPath(h.w.Items(), "x/good.json"); len(it) != 1 || it[0].Source != good {
		t.Errorf("valid source changed: %+v", it)
	}
	// An invalid Pod source is never sent in a re-read; its item gets reread-failed.
	if len(gets) != 1 || gets[0] != "team-a/gw-0.x" {
		t.Errorf("Pod GETs %q, want only the valid source's", gets)
	}
	if it := itemByPath(h.w.Items(), "x/bad.json"); len(it) != 1 || it[0].Source.Identity != pb.IdentityRereadFailed {
		t.Errorf("bad item %+v, want reread-failed", it)
	}
}

// checkReplayKept asserts a written corpus item keeps its useful content and
// that redactions.json reports its findings with schema-field locations only.
func checkReplayKept(t *testing.T, a bundletest.Archive, fx bundletest.Fixture, path string, data []byte) {
	t.Helper()
	text := string(data)
	if fx.Method == bundletest.MethodObject {
		// As the corpus test does, also match the decoded string values.
		var v any
		if err := yaml.Unmarshal(data, &v); err != nil {
			t.Fatal(err)
		}
		var walk func(any)
		walk = func(v any) {
			switch v := v.(type) {
			case map[string]any:
				for _, e := range v {
					walk(e)
				}
			case []any:
				for _, e := range v {
					walk(e)
				}
			case string:
				text += "\n" + v
			}
		}
		walk(v)
	}
	for _, k := range fx.Keep {
		if !strings.Contains(text, k) {
			t.Errorf("kept value %q missing from the member", k)
		}
	}
	var red struct {
		Files []struct {
			Path     string           `json:"path"`
			Findings []redact.Finding `json:"findings"`
		} `json:"files"`
	}
	rb, _ := a.File(pb.RedactionsFile)
	if err := json.Unmarshal(rb, &red); err != nil {
		t.Fatalf("redactions.json: %v", err)
	}
	var got []redact.Finding
	for _, f := range red.Files {
		if f.Path == path {
			got = f.Findings
		}
	}
	for _, e := range fx.Expect {
		if !slices.ContainsFunc(got, func(f redact.Finding) bool { return string(f.Rule) == e.Rule && f.Location == e.Location }) {
			t.Errorf("redactions.json lacks {%s %s}; got %+v", e.Rule, e.Location, got)
		}
	}
	for _, f := range got {
		loc := f.Location
		if i := strings.LastIndex(loc, "keys[]"); f.Rule == redact.RuleJWKS && i >= 0 {
			loc = loc[:i] // a fixed JWK member name follows
		}
		if seg := mapKeySegment(loc); seg != "" {
			t.Errorf("location %q shows key %q", f.Location, seg)
		}
		for _, k := range fx.FindingsOnly {
			if strings.Contains(f.Location, k) {
				t.Errorf("location %q holds map key %q", f.Location, k)
			}
		}
	}
}

// mapKeySegment returns the first literal segment of loc that is not a
// schema field name, or "" when there is none.
func mapKeySegment(loc string) string {
	if strings.HasPrefix(loc, "line:") {
		return ""
	}
	loc = strings.NewReplacer("[key]", ".", "[]", ".").Replace(loc)
	for _, s := range strings.Split(loc, ".") {
		if s != "" && !redact.SchemaField(s) {
			return s
		}
	}
	return ""
}
