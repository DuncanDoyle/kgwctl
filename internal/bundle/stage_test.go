package bundle

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

func TestDefaultLimitsMinimum(t *testing.T) {
	if _, err := DefaultLimits(31 << 20); err == nil {
		t.Error("31Mi: no error")
	}
	l, err := DefaultLimits(32 << 20)
	if err != nil {
		t.Fatalf("32Mi: %v", err)
	}
	if sum := l.Manifest + l.Redactions + l.Errors + l.RunLog; sum > 8<<20 {
		t.Errorf("32Mi: footer caps sum %d > 8 MiB", sum)
	}
	d := mustLimits(t, 512<<20)
	want := Limits{MaxSize: 512 << 20, RAM: 64 << 20, Log: 10 << 20, ConfigDump: 64 << 20, Metrics: 8 << 20, Other: 16 << 20,
		Manifest: 4 << 20, Redactions: 4 << 20, Errors: 1 << 20, RunLog: 1 << 20}
	if d != want {
		t.Errorf("512Mi: %+v, want %+v", d, want)
	}
}

// spoolDir returns the one spool directory under parent, or "" when none.
func spoolDir(t *testing.T, parent string) string {
	t.Helper()
	es, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) > 1 {
		t.Fatalf("%d entries under %s", len(es), parent)
	}
	if len(es) == 0 {
		return ""
	}
	return filepath.Join(parent, es[0].Name())
}

// secretLog is n lines that each carry a Bearer canary the pattern pass removes.
func secretLog(n int) []byte {
	var b bytes.Buffer
	for i := range n {
		fmt.Fprintf(&b, "%06d Authorization: Bearer kgwctl-canary-spool-%06d %s\n", i, i, strings.Repeat("s", 900))
	}
	return b.Bytes()
}

func TestSpool(t *testing.T) {
	if mustLimits(t, 512<<20).RAM != 64<<20 {
		t.Fatal("the RAM budget is not 64 MiB")
	}

	t.Run("spill", func(t *testing.T) {
		l := mustLimits(t, 32<<20)
		l.RAM = 64 << 10 // the 64 MiB budget, scaled down
		h := newHarness(t, l, redact.Options{}, "")
		dir := spoolDir(t, h.dir)
		if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
			t.Fatalf("spool dir mode %v, err %v", st.Mode().Perm(), err)
		}
		for i := range 3 {
			if err := h.w.Sink("c").Text(pb.Path{"logs", fmt.Sprintf("s%d.log", i)}, bytes.NewReader(secretLog(100)), pb.Source{}); err != nil {
				t.Fatal(err)
			}
		}
		files, _ := os.ReadDir(dir)
		if len(files) != 1 {
			t.Fatalf("%d spool files, want 1", len(files))
		}
		f := filepath.Join(dir, files[0].Name())
		st, _ := os.Stat(f)
		if st.Mode().Perm() != 0o600 || st.Size() < 64<<10 {
			t.Fatalf("spool file mode %v size %d", st.Mode().Perm(), st.Size())
		}
		b, _ := os.ReadFile(f)
		if bytes.Contains(b, []byte("kgwctl-canary")) {
			t.Error("the spool file holds a canary")
		}
		h.w.Seal()
		a := h.finish(t, Manifest{}, "c")
		if d, _ := a.File("logs/s2.log"); !bytes.Contains(d, []byte("000099 Authorization: Bearer [redacted]")) {
			t.Error("spilled item not read back")
		}
		if spoolDir(t, h.dir) != "" {
			t.Error("spool dir left after success")
		}
	})

	t.Run("staging-bound", func(t *testing.T) {
		l := mustLimits(t, 32<<20)
		parent := t.TempDir()
		sp, err := NewSpool(parent, 64<<10, 512<<10)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		w := NewWriter(Stdout(&buf), sp, "root", l, redact.Options{}, "", fixedNow)
		var dropped int
		for i := range 8 {
			err := w.Sink("c").Text(pb.Path{"logs", fmt.Sprintf("s%d.log", i)}, bytes.NewReader(textBody("x", 100)), pb.Source{})
			if errors.Is(err, pb.ErrBudget) {
				dropped++
			} else if err != nil {
				t.Fatal(err)
			}
		}
		if dropped < 2 {
			t.Fatalf("%d staging drops, want at least 2", dropped)
		}
		var n int
		for _, it := range w.Items() {
			if it.Status == pb.ItemDropped && it.Reason == "staging" {
				n++
			}
		}
		if n != dropped {
			t.Errorf("%d items dropped: staging, want %d", n, dropped)
		}
		// Cancel: the runner removes the spool without finalizing.
		if err := sp.Remove(); err != nil {
			t.Fatal(err)
		}
		if spoolDir(t, parent) != "" {
			t.Error("spool dir left after cancel")
		}
		if err := sp.Remove(); err != nil {
			t.Errorf("second Remove: %v", err)
		}
	})

	t.Run("failed-finalize", func(t *testing.T) {
		l := mustLimits(t, 32<<20)
		l.RAM = 64 << 10
		parent := t.TempDir()
		sp, err := NewSpool(parent, l.RAM, l.MaxSize)
		if err != nil {
			t.Fatal(err)
		}
		w := NewWriter(Stdout(failWriter{}), sp, "root", l, redact.Options{}, "", fixedNow)
		if err := w.Sink("c").Text(pb.Path{"logs", "s.log"}, bytes.NewReader(textBody("x", 300)), pb.Source{}); err != nil {
			t.Fatal(err)
		}
		w.Seal()
		if err := w.Finalize([]string{"c"}, Manifest{}); err == nil {
			t.Fatal("Finalize on a failing destination succeeded")
		}
		if spoolDir(t, parent) != "" {
			t.Error("spool dir left after a failed Finalize")
		}
	})
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }
