package bundle

import (
	"bytes"
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

func (s *sink) Object(p pb.Path, u *unstructured.Unstructured, src pb.Source) error {
	c, err := s.w.begin(s.name, p, ContentObject, src, false)
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

func (s *sink) ConfigDump(p pb.Path, r io.Reader, src pb.Source) error {
	c, err := s.w.begin(s.name, p, ContentConfigDump, src, false)
	if err != nil {
		return err
	}
	return s.w.structured(c, r, s.w.lim.ConfigDump, redact.ConfigDump)
}

func (s *sink) JSON(p pb.Path, r io.Reader, src pb.Source) error {
	c, err := s.w.begin(s.name, p, ContentJSON, src, false)
	if err != nil {
		return err
	}
	return s.w.structured(c, r, s.w.lim.Other, redact.JSON)
}

func (s *sink) Prometheus(p pb.Path, r io.Reader, src pb.Source) error {
	c, err := s.w.begin(s.name, p, ContentPrometheus, src, false)
	if err != nil {
		return err
	}
	return s.w.structured(c, r, s.w.lim.Metrics, redact.Prometheus)
}

// Text stages redacted lines as they are read; a log (.log) is capped at the
// log cap, other text at the other cap.
func (s *sink) Text(p pb.Path, r io.Reader, src pb.Source) error {
	c, err := s.w.begin(s.name, p, ContentText, src, true)
	if err != nil {
		return err
	}
	limit := s.w.lim.Other
	if strings.HasSuffix(p[len(p)-1], ".log") {
		limit = s.w.lim.Log
	}
	// The reader's error, if any, is recorded as a truncation; errStop is ours.
	res, _ := redact.Text(r, lineSink{s.w, c}, limit-markerReserve, s.w.ro)
	return s.w.finishText(c, res)
}

func (s *sink) Error(p pb.Path, src pb.Source, err error) {
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
