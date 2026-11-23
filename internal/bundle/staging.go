package bundle

import (
	"errors"
	"fmt"
	"io"

	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// begin registers a Sink write after checking the seal, the path, the source
// identity and exact duplicates within the collector. Case variants and
// path conflicts are resolved at commit.
func (w *Writer) begin(collector string, p pb.Path, ctype string, src pb.Source, text bool) (*call, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sealed {
		return nil, pb.ErrSealed
	}
	c := &call{collector: collector, path: p, ctype: ctype, src: src, text: text}
	if pb.ValidatePath(p) != nil {
		it := c.item(w)
		it.Path, it.Status, it.Reason = safePath(p), pb.ItemFailed, ReasonInvalidPath
		w.addRecord(collector, it, nil, nil)
		// ValidatePath's error quotes the bad component, so it is not returned.
		return nil, fmt.Errorf("%w: %s", pb.ErrInvalidPath, safePath(p))
	}
	if !identities[src.Identity] {
		it := c.item(w)
		it.Status, it.Reason = pb.ItemFailed, ReasonInvalidSource
		w.addRecord(collector, it, nil, nil)
		return nil, fmt.Errorf("%w: source identity", pb.ErrInvalidPath)
	}
	c.key = p.String()
	if w.keys[collector][c.key] {
		return nil, w.duplicate(c)
	}
	if text {
		c.e.reserve = markerReserve
	}
	w.calls[c] = struct{}{}
	return c, nil
}

// duplicate records c as a rejected duplicate; the caller holds w.mu.
func (w *Writer) duplicate(c *call) error {
	w.sp.release(&c.e)
	it := c.item(w)
	it.Status, it.Reason = pb.ItemDropped, ReasonDuplicate
	w.addRecord(c.collector, it, nil, nil)
	return fmt.Errorf("%w: %s", pb.ErrDuplicate, c.path)
}

// stage records c as an ok item with its staged body; the caller holds w.mu.
func (w *Writer) stage(c *call, reason string) (*record, error) {
	if w.keys[c.collector][c.key] {
		return nil, w.duplicate(c)
	}
	if w.keys[c.collector] == nil {
		w.keys[c.collector] = map[string]bool{}
	}
	w.keys[c.collector][c.key] = true
	it := c.item(w)
	it.Status, it.Reason = pb.ItemOK, reason
	it.Redactions = c.found.total
	e := c.e
	r := w.addRecord(c.collector, it, nil, &e)
	r.findings, r.fomit = c.found.list, c.found.omitted
	return r, nil
}

// fail records a write that produced no item; the caller holds w.mu.
func (w *Writer) fail(c *call, status pb.ItemStatus, reason string, err error) error {
	w.sp.release(&c.e)
	it := c.item(w)
	it.Status, it.Reason = status, reason
	w.addRecord(c.collector, it, nil, nil)
	return err
}

// outcome is the result of a structured write.
type outcome struct {
	body     []byte
	findings []redact.Finding
	status   pb.ItemStatus
	reason   string
	err      error
}

// finish stages a structured write's redacted body, or records its failure.
func (w *Writer) finish(c *call, o outcome) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.calls, c)
	if c.late {
		return pb.ErrSealed
	}
	if o.status != "" {
		return w.fail(c, o.status, o.reason, o.err)
	}
	if err := w.sp.add(&c.e, o.body, false); err != nil {
		if errors.Is(err, errSpoolFull) {
			return w.fail(c, pb.ItemDropped, ReasonStaging, fmt.Errorf("%w: %s", pb.ErrBudget, ReasonStaging))
		}
		return w.fail(c, pb.ItemFailed, ReasonStaging, err)
	}
	c.found.add(o.findings)
	_, err := w.stage(c, "")
	return err
}

// structured reads a body up to limit, redacts it with fn and stages it.
func (w *Writer) structured(c *call, r io.Reader, limit int64, fn func([]byte, redact.Options) ([]byte, []redact.Finding, error)) error {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return w.finish(c, outcome{status: ItemStatus(err), reason: w.errorText(err), err: err})
	}
	if int64(len(body)) > limit {
		return w.finish(c, outcome{status: pb.ItemDropped, reason: ReasonSizeCap, err: fmt.Errorf("%w: %s", pb.ErrBudget, ReasonSizeCap)})
	}
	ro := w.ro
	ro.MaxSize = limit
	out, fs, err := fn(body, ro)
	switch {
	case errors.Is(err, redact.ErrInvalidJSON):
		return w.finish(c, outcome{status: pb.ItemFailed, reason: ReasonInvalidJSON, err: err})
	case errors.Is(err, redact.ErrNotPrometheus):
		return w.finish(c, outcome{status: pb.ItemFailed, reason: ReasonNotPrometheus, err: err})
	case err != nil:
		return w.finish(c, outcome{status: pb.ItemFailed, reason: w.errorText(err), err: err})
	case int64(len(out)) > limit:
		return w.finish(c, outcome{status: pb.ItemDropped, reason: ReasonSizeCap, err: fmt.Errorf("%w: %s", pb.ErrBudget, ReasonSizeCap)})
	}
	return w.finish(c, outcome{body: out, findings: fs})
}

// lineSink stages a text item's redacted lines as they arrive.
type lineSink struct {
	w *Writer
	c *call
}

func (l lineSink) WriteLine(b []byte, fs []redact.Finding) error {
	w, c := l.w, l.c
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sealed || c.dropErr != nil {
		return errStop
	}
	line := make([]byte, 0, len(b)+1)
	line = append(append(line, b...), '\n')
	if err := w.sp.add(&c.e, line, false); err != nil {
		c.dropErr = err
		return errStop
	}
	c.lines++
	c.found.add(fs)
	return nil
}

// finishText closes a text write: it appends a truncation line when the
// text was cut and stages it, unless Seal already decided its fate.
func (w *Writer) finishText(c *call, res redact.TextResult) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.calls, c)
	switch {
	case c.late:
		w.sp.release(&c.e)
		return pb.ErrSealed
	case c.kept != nil:
		return pb.ErrSealed
	case errors.Is(c.dropErr, errSpoolFull):
		return w.fail(c, pb.ItemDropped, ReasonStaging, fmt.Errorf("%w: %s", pb.ErrBudget, ReasonStaging))
	case c.dropErr != nil:
		return w.fail(c, pb.ItemFailed, ReasonStaging, c.dropErr)
	}
	reason := ""
	if res.Truncated {
		reason = "truncated: " + res.Reason
		if err := w.sp.add(&c.e, []byte(truncation(res.Reason)+"\n"), true); err != nil {
			return w.fail(c, pb.ItemFailed, ReasonStaging, err)
		}
	}
	// Line findings arrived with the lines; only the open-PEM finding is new.
	for _, f := range res.Findings {
		if f.Rule == redact.RulePEMTail {
			c.found.add([]redact.Finding{f})
		}
	}
	_, err := w.stage(c, reason)
	return err
}
