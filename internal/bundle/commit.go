package bundle

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// sortRecords puts records in commit order; the caller holds w.mu.
func (w *Writer) sortRecords() {
	rank := func(c string) int {
		if i := slices.Index(w.order, c); i >= 0 {
			return i
		}
		return len(w.order)
	}
	slices.SortStableFunc(w.records, func(a, b *record) int {
		if c := rank(a.collector) - rank(b.collector); c != 0 {
			return c
		}
		if c := strings.Compare(a.collector, b.collector); c != 0 {
			return c
		}
		if c := strings.Compare(a.item.Path, b.item.Path); c != 0 {
			return c
		}
		return a.seq - b.seq
	})
}

// footer names in archive order, with their caps.
func (w *Writer) footers() []struct {
	name string
	cap  int64
} {
	return []struct {
		name string
		cap  int64
	}{
		{pb.ManifestFile, w.lim.Manifest}, {pb.ErrorsFile, w.lim.Errors},
		{pb.RedactionsFile, w.lim.Redactions}, {pb.RunLogFile, w.lim.RunLog},
	}
}

func (w *Writer) header(name string, size int64, mod time.Time) *tar.Header {
	return &tar.Header{Typeflag: tar.TypeReg, Name: w.root + "/" + name, Size: size, Mode: 0o644, ModTime: mod.UTC().Truncate(time.Second)}
}

// headerCost is the bytes tar writes for h, PAX records included.
func headerCost(h *tar.Header) int64 {
	var c countWriter
	if err := tar.NewWriter(&c).WriteHeader(h); err != nil {
		return 1 << 62
	}
	return c.n
}

func pad(n int64) int64 { return (n + 511) &^ 511 }

// place decides, in commit order, which staged items commit. An item whose
// path equals a committed one ignoring case, or is a file or directory prefix
// of one, or that passes the budget, is dropped and claims no path.
func (w *Writer) place(end time.Time) {
	// The footer reserve and the two end-of-archive blocks come off MaxSize first.
	budget := w.lim.MaxSize - 1024
	for _, f := range w.footers() {
		budget -= headerCost(w.header(f.name, f.cap, end)) + pad(f.cap)
	}
	files, dirs := map[string]bool{}, map[string]bool{}
	var used int64
	for _, r := range w.records {
		if r.e == nil {
			continue
		}
		key := strings.ToLower(r.item.Path)
		reason := ""
		switch {
		case files[key]:
			reason = ReasonDuplicate
		case dirs[key] || hasFileParent(key, files):
			reason = ReasonPathConflict
		default:
			cost := headerCost(w.header(r.item.Path, r.e.size, r.item.CapturedAt)) + pad(r.e.size)
			if used+cost > budget {
				reason = ReasonSizeBudget
				break
			}
			used += cost
			files[key] = true
			for i := range len(key) {
				if key[i] == '/' {
					dirs[key[:i]] = true
				}
			}
		}
		if reason != "" {
			r.item.Status, r.item.Reason = pb.ItemDropped, reason
			r.e, r.findings = nil, nil
		}
	}
}

func hasFileParent(key string, files map[string]bool) bool {
	for i := range len(key) {
		if key[i] == '/' && files[key[:i]] {
			return true
		}
	}
	return false
}

// commit writes the archive; the caller holds w.mu.
func (w *Writer) commit(m Manifest) error {
	w.sortRecords()
	end := w.now()
	w.place(end)

	m = w.prepare(m, end)
	// errors.txt reads the full collector list, which manifest bounding may cut.
	cols := m.Collectors
	m, mb, err := w.manifestFooter(m)
	if err != nil {
		return err
	}
	files := map[string][]byte{
		pb.ManifestFile:   mb,
		pb.ErrorsFile:     w.errorsFooter(cols),
		pb.RedactionsFile: w.redactionsFooter(),
		pb.RunLogFile:     textFooter(w.runLog, w.runLogCut, w.lim.RunLog),
	}
	for _, f := range w.footers() {
		if int64(len(files[f.name])) > f.cap {
			return fmt.Errorf("bundle: %s over its cap", f.name)
		}
	}

	gz := gzip.NewWriter(w.dest)
	tw := tar.NewWriter(gz)
	for _, r := range w.records {
		if r.e == nil {
			continue
		}
		if err := tw.WriteHeader(w.header(r.item.Path, r.e.size, r.item.CapturedAt)); err != nil {
			return err
		}
		if _, err := io.Copy(tw, w.sp.reader(r.e)); err != nil {
			return err
		}
	}
	for _, f := range w.footers() {
		b := files[f.name]
		if err := tw.WriteHeader(w.header(f.name, int64(len(b)), end)); err != nil {
			return err
		}
		if _, err := tw.Write(b); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := w.dest.Close(); err != nil {
		return err
	}
	w.committed = m
	return nil
}

// prepare fills the writer's fields of m, the exclusions included, redacts its free text and marks a
// collector partial when one of its items did not commit.
func (w *Writer) prepare(m Manifest, end time.Time) Manifest {
	if m.Context != "" {
		m.Context = redact.ErrorText(errors.New(m.Context), w.server)
	}
	if m.Server != "" {
		m.Server = redact.ErrorText(errors.New(m.Server), w.server)
	}
	if m.Sealed.IsZero() {
		m.Sealed = w.sealedAt
	}
	if m.End.IsZero() {
		m.End = end
	}
	if m.Finalization == "" {
		m.Finalization = "complete"
	}
	m.Excluded = slices.Clone(w.frozen)
	deg := w.degraded()
	m.Collectors = slices.Clone(m.Collectors)
	for i := range m.Collectors {
		c := &m.Collectors[i]
		if c.Error != "" {
			c.Error = redact.ErrorText(errors.New(c.Error), w.server)
		}
		if c.Status == pb.CollectorOK && deg[c.Name] {
			c.Status = pb.CollectorPartial
		}
	}
	return m
}
