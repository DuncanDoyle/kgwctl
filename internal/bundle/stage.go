package bundle

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const (
	mib = 1 << 20
	// MinSize is the smallest --max-size.
	MinSize = 32 * mib
	// indexCost is what one staged item's index entry counts against the spool bound.
	indexCost = 256
)

// Limits are the size caps of one bundle (design §8.3).
type Limits struct {
	// MaxSize caps the uncompressed tar content, items and footers together.
	MaxSize int64
	// RAM is the staging budget held in memory before the spool spills to disk.
	RAM int64
	// Item caps per content: logs, config dumps, metrics and everything else.
	Log, ConfigDump, Metrics, Other int64
	// Footer caps; their sum is the footer reserve inside MaxSize.
	Manifest, Redactions, Errors, RunLog int64
}

// DefaultLimits returns the limits for maxSize. The footer caps are 4, 4, 1
// and 1 MiB, scaled down when their sum passes a quarter of maxSize. A
// maxSize below 32 MiB is an error.
func DefaultLimits(maxSize int64) (Limits, error) {
	if maxSize < MinSize {
		return Limits{}, fmt.Errorf("--max-size %d is below the minimum of 32Mi", maxSize)
	}
	l := Limits{
		MaxSize: maxSize, RAM: 64 * mib,
		Log: 10 * mib, ConfigDump: 64 * mib, Metrics: 8 * mib, Other: 16 * mib,
		Manifest: 4 * mib, Redactions: 4 * mib, Errors: mib, RunLog: mib,
	}
	if sum := l.Manifest + l.Redactions + l.Errors + l.RunLog; sum > maxSize/4 {
		scale := func(v int64) int64 { return v * (maxSize / 4) / sum }
		l.Manifest, l.Redactions, l.Errors, l.RunLog = scale(l.Manifest), scale(l.Redactions), scale(l.Errors), scale(l.RunLog)
	}
	return l, nil
}

// errSpoolFull means staging an item would pass the spool bound.
var errSpoolFull = errors.New("bundle: staging bound reached")

// Spool holds staged, already-redacted item bytes: in memory up to its RAM
// budget, then in a 0600 file in a 0700 directory. RAM and file bytes plus a
// per-item index cost stay within its bound.
type Spool struct {
	mu       sync.Mutex
	dir      string
	ram, max int64
	ramUsed  int64
	fileSize int64
	items    int64
	f        *os.File
	removed  bool
}

// NewSpool creates the spool's 0700 temporary directory under dir.
func NewSpool(dir string, ram, max int64) (*Spool, error) {
	d, err := os.MkdirTemp(dir, "kgwctl-spool-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(d, 0o700); err != nil {
		_ = os.RemoveAll(d)
		return nil, err
	}
	return &Spool{dir: d, ram: ram, max: max}, nil
}

// Remove closes the spool file and deletes the directory. It is safe to call
// more than once.
func (s *Spool) Remove() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.removed {
		return nil
	}
	s.removed = true
	var err error
	if s.f != nil {
		err = s.f.Close()
	}
	return errors.Join(err, os.RemoveAll(s.dir))
}

// entry is one staged item: a RAM prefix, then segments of the spool file.
type entry struct {
	mem  []byte
	segs []fileSeg
	size int64
	// open means the entry holds an index slot.
	open bool
}

type fileSeg struct{ off, n int64 }

func (s *Spool) used() int64 { return s.ramUsed + s.fileSize + s.items*indexCost }

// add appends b to e. It fails with errSpoolFull when that would pass the
// bound, unless force is set (for a short truncation line).
func (s *Spool) add(e *entry, b []byte, force bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.removed {
		return errSpoolFull
	}
	need := int64(len(b))
	if !e.open {
		need += indexCost
	}
	if !force && s.used()+need > s.max {
		return errSpoolFull
	}
	if !e.open {
		e.open = true
		s.items++
	}
	e.size += int64(len(b))
	if len(e.segs) == 0 && s.ramUsed+int64(len(b)) <= s.ram {
		e.mem = append(e.mem, b...)
		s.ramUsed += int64(len(b))
		return nil
	}
	if s.f == nil {
		f, err := os.OpenFile(filepath.Join(s.dir, "spool"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		s.f = f
	}
	if _, err := s.f.WriteAt(b, s.fileSize); err != nil {
		return err
	}
	if n := len(e.segs); n > 0 && e.segs[n-1].off+e.segs[n-1].n == s.fileSize {
		e.segs[n-1].n += int64(len(b))
	} else {
		e.segs = append(e.segs, fileSeg{off: s.fileSize, n: int64(len(b))})
	}
	s.fileSize += int64(len(b))
	return nil
}

// release frees e's RAM and index slot. File bytes stay counted, since the
// file only grows.
func (s *Spool) release(e *entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.open {
		s.items--
	}
	s.ramUsed -= int64(len(e.mem))
	*e = entry{}
}

// reader returns e's bytes.
func (s *Spool) reader(e *entry) io.Reader {
	rs := []io.Reader{bytes.NewReader(e.mem)}
	for _, sg := range e.segs {
		rs = append(rs, io.NewSectionReader(s.f, sg.off, sg.n))
	}
	return io.MultiReader(rs...)
}
