package bundle

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

func TestDefaultName(t *testing.T) {
	at := time.Date(2026, 11, 22, 9, 5, 7, 0, time.FixedZone("CET", 3600))
	if got := DefaultName(at); got != "kgwctl-bundle-20261122T080507Z" {
		t.Errorf("DefaultName = %q", got)
	}
}

func TestCreateFileExclusive(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "old.tar.gz")
	if err := os.WriteFile(existing, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateFile(existing); err == nil {
		t.Error("CreateFile on an existing path succeeded")
	}
	if b, _ := os.ReadFile(existing); string(b) != "keep" {
		t.Errorf("existing file changed: %q", b)
	}

	p := filepath.Join(dir, "new.tar.gz")
	d, err := CreateFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("new file mode %v, err %v", st.Mode().Perm(), err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

// failingDest fails every write after its first n bytes.
type failingDest struct {
	Dest
	n int
}

func (f *failingDest) Write(p []byte) (int, error) {
	if len(p) > f.n {
		return 0, errors.New("disk full")
	}
	f.n -= len(p)
	return f.Dest.Write(p)
}

func stagedWriter(t *testing.T, d Dest) *Writer {
	t.Helper()
	l := mustLimits(t, 32<<20)
	sp, err := NewSpool(t.TempDir(), l.RAM, l.MaxSize)
	if err != nil {
		t.Fatal(err)
	}
	w := NewWriter(d, sp, "root", l, redact.Options{}, "", fixedNow)
	if err := w.Sink("c").Text(pb.Path{"logs", "a.log"}, bytes.NewReader(textBody("a", 200)), pb.Source{}); err != nil {
		t.Fatal(err)
	}
	w.Seal()
	return w
}

func TestFinalizeFailureRemovesFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "out.tar.gz")
	f, err := CreateFile(p)
	if err != nil {
		t.Fatal(err)
	}
	w := stagedWriter(t, &failingDest{Dest: f, n: 100})
	if err := w.Finalize([]string{"c"}, Manifest{}); err == nil {
		t.Fatal("Finalize succeeded on a failing file")
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("output file left behind: %v", err)
	}

	so := Stdout(failWriter{})
	if err := stagedWriter(t, so).Finalize([]string{"c"}, Manifest{}); err == nil {
		t.Fatal("Finalize succeeded on a failing stdout")
	}
	if err := so.Abort(); err != nil {
		t.Errorf("stdout Abort: %v", err)
	}
}
