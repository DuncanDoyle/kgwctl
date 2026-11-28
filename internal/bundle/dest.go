package bundle

import (
	"errors"
	"io"
	"os"
	"time"
)

// Dest is where the archive goes. Abort undoes a failed write: it removes a
// file and does nothing on stdout.
type Dest interface {
	io.Writer
	Close() error
	Abort() error
}

// CreateFile creates path with O_EXCL and mode 0600; it never overwrites.
func CreateFile(path string) (Dest, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &fileDest{f: f, path: path}, nil
}

type fileDest struct {
	f      *os.File
	path   string
	closed bool
}

func (d *fileDest) Write(p []byte) (int, error) { return d.f.Write(p) }

func (d *fileDest) Close() error {
	d.closed = true
	return d.f.Close()
}

func (d *fileDest) Abort() error {
	if !d.closed {
		_ = d.Close() // the file is removed next, so a close error does not matter
	}
	if err := os.Remove(d.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Stdout writes the archive to w. Close and Abort do nothing.
func Stdout(w io.Writer) Dest { return stdoutDest{w} }

type stdoutDest struct{ io.Writer }

func (stdoutDest) Close() error { return nil }
func (stdoutDest) Abort() error { return nil }

// DefaultName is the archive root and default file stem for a run started at t.
func DefaultName(t time.Time) string {
	return "kgwctl-bundle-" + t.UTC().Format("20060102T150405Z")
}
