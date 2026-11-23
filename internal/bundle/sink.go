package bundle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	yamlv2 "go.yaml.in/yaml/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// sink is a collector's view of the writer. It has only the Sink methods, so
// no content type without a redactor can be written.
type sink struct {
	w    *Writer
	name string
}

func (s *sink) Object(ctx context.Context, p pb.Path, u *unstructured.Unstructured, src pb.Source) error {
	c, err := s.w.begin(ctx, s.name, p, ContentObject, src, false)
	if err != nil {
		return err
	}
	obj := u.DeepCopy()
	fs, err := redact.Object(obj, s.w.ro)
	if errors.Is(err, redact.ErrForbiddenKind) {
		return s.w.finish(c, outcome{status: pb.ItemFailed, reason: ReasonForbiddenKind, err: fmt.Errorf("%w: %s", pb.ErrForbiddenKind, obj.GetKind())})
	}
	if err != nil {
		return s.w.finish(c, outcome{status: pb.ItemFailed, reason: s.w.errorText(err), err: err})
	}
	body, over, err := encodeYAML(obj.Object, s.w.lim.Other)
	if err != nil {
		return s.w.finish(c, outcome{status: pb.ItemFailed, reason: s.w.errorText(err), err: err})
	}
	if over {
		return s.w.finish(c, outcome{status: pb.ItemDropped, reason: ReasonSizeCap, err: fmt.Errorf("%w: %s", pb.ErrBudget, ReasonSizeCap)})
	}
	return s.w.finish(c, outcome{body: body, findings: fs})
}

func (s *sink) ConfigDump(ctx context.Context, p pb.Path, r io.Reader, src pb.Source) error {
	c, err := s.w.begin(ctx, s.name, p, ContentConfigDump, src, false)
	if err != nil {
		return err
	}
	return s.w.structured(c, r, s.w.lim.ConfigDump, redact.ConfigDump)
}

func (s *sink) JSON(ctx context.Context, p pb.Path, r io.Reader, src pb.Source) error {
	c, err := s.w.begin(ctx, s.name, p, ContentJSON, src, false)
	if err != nil {
		return err
	}
	return s.w.structured(c, r, s.w.lim.Other, redact.JSON)
}

func (s *sink) Prometheus(ctx context.Context, p pb.Path, r io.Reader, src pb.Source) error {
	c, err := s.w.begin(ctx, s.name, p, ContentPrometheus, src, false)
	if err != nil {
		return err
	}
	return s.w.structured(c, r, s.w.lim.Metrics, redact.Prometheus)
}

// Text stages redacted lines as they are read; a log (.log) is capped at the
// log cap, other text at the other cap.
func (s *sink) Text(ctx context.Context, p pb.Path, r io.Reader, src pb.Source) error {
	c, err := s.w.begin(ctx, s.name, p, ContentText, src, true)
	if err != nil {
		return err
	}
	limit := s.w.lim.Other
	if strings.HasSuffix(p[len(p)-1], ".log") {
		limit = s.w.lim.Log
	}
	// The reader's error, if any, is recorded as a truncation; errStop is ours.
	res, err := redact.Text(r, lineSink{s.w, c}, limit-markerReserve, s.w.ro)
	if res.Reason == redact.ReasonReadError && errors.Is(err, context.DeadlineExceeded) {
		res.Reason = ReasonTimeout // the item's request timeout cut the stream
	}
	return s.w.finishText(c, res)
}

// omitStatus is the item status of each omission reason (D-027).
var omitStatus = map[pb.OmitReason]pb.ItemStatus{
	pb.OmitNotRunning: pb.ItemSkipped, pb.OmitNoInstall: pb.ItemSkipped, pb.OmitNoMetricsPort: pb.ItemDisabled,
}

func (s *sink) Omit(p pb.Path, src pb.Source, reason pb.OmitReason) error {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	if s.w.sealed {
		return pb.ErrSealed
	}
	st, ok := omitStatus[reason]
	if !ok || pb.ValidatePath(p) != nil || src.Identity != "" {
		// Nothing is recorded, and the rejected values stay out of the error.
		return fmt.Errorf("%w: omission", pb.ErrInvalidPath)
	}
	s.w.addOmission(s.name, Item{Path: p.String(), CapturedAt: s.w.now(), Source: safeSource(src), Status: st, Reason: string(reason)})
	return nil
}

func (s *sink) Error(p pb.Path, src pb.Source, err error) {
	// Identity is output only (D-027); an error item is never re-read.
	src.Identity = ""
	text := s.w.errorText(err)
	path := p.String()
	if pb.ValidatePath(p) != nil {
		path = safePath(p)
	}
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	if s.w.sealed {
		return
	}
	s.w.addRecord(s.name, Item{Path: path, CapturedAt: s.w.now(), Source: safeSource(src), Status: ItemStatus(err), Reason: text}, nil, nil)
}

func (s *sink) Exclude(gk schema.GroupKind, reason pb.ExcludeReason) error {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	if s.w.sealed {
		return pb.ErrSealed
	}
	group := pb.Core
	if gk.Group != "" {
		group = gk.Group
	}
	_, gerr := pb.Name(pb.Group, group)
	_, kerr := pb.Name(pb.Kind, gk.Kind)
	if gerr != nil || kerr != nil || !excludeReasons[reason] {
		// The rejected values are left out of the record and the error.
		s.w.addRecord(s.name, Item{Path: invalidPart, CapturedAt: s.w.now(), Status: pb.ItemFailed, Reason: ReasonInvalidExcl}, nil, nil)
		return fmt.Errorf("%w: exclusion", pb.ErrInvalidPath)
	}
	s.w.excl[Exclusion{Kind: group + "/" + gk.Kind, Reason: reason}]++
	return nil
}

// capWriter keeps up to max bytes; a write past that fails and sets over.
type capWriter struct {
	b    bytes.Buffer
	max  int64
	over bool
}

func (c *capWriter) Write(p []byte) (int, error) {
	if int64(c.b.Len()+len(p)) > c.max {
		c.over = true
		return 0, errStop
	}
	return c.b.Write(p)
}

// encodeYAML streams v as YAML, in sigs.k8s.io/yaml's format, and stops once
// the output passes max; over reports that.
func encodeYAML(v any, max int64) (body []byte, over bool, err error) {
	cw := &capWriter{max: max}
	enc := yamlv2.NewEncoder(cw)
	if err = enc.Encode(v); err == nil {
		err = enc.Close()
	}
	if cw.over {
		return nil, true, nil
	}
	return cw.b.Bytes(), false, err
}
