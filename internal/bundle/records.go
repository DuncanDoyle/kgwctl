package bundle

import (
	"regexp"
	"strings"

	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// record is one item outcome; e holds the staged body of an ok item.
type record struct {
	collector string
	seq       int
	item      Item
	findings  []redact.Finding
	fomit     int // distinct findings past maxFileFindings
	e         *entry
}

// maxFileFindings caps the distinct findings kept per item.
const maxFileFindings = 4096

// findingSet sums findings per rule and location as they arrive, keeping at
// most maxFileFindings entries; total counts every redaction.
type findingSet struct {
	list    []redact.Finding
	idx     map[redact.Finding]int
	omitted int
	total   int
}

func (s *findingSet) add(fs []redact.Finding) {
	for _, f := range fs {
		s.total += f.Count
		k := redact.Finding{Rule: f.Rule, Location: f.Location}
		if i, ok := s.idx[k]; ok {
			s.list[i].Count += f.Count
			continue
		}
		if len(s.list) >= maxFileFindings {
			s.omitted++
			continue
		}
		if s.idx == nil {
			s.idx = map[redact.Finding]int{}
		}
		s.idx[k] = len(s.list)
		s.list = append(s.list, f)
	}
}

// call is one Sink write in flight.
type call struct {
	collector string
	path      pb.Path
	key       string
	ctype     string
	src       pb.Source
	text      bool
	e         entry
	lines     int
	found     findingSet // of the staged lines, or the structured body
	late      bool       // in flight at Seal; the result is discarded
	kept      *record    // a text item kept at Seal
	dropErr   error      // staging failed mid-stream
}

// safePath renders p for a record, with every component that fails
// validation replaced, so no unvalidated value reaches the manifest.
func safePath(p pb.Path) string {
	if len(p) == 0 {
		return invalidPart
	}
	parts := make([]string, 0, len(p))
	for i, c := range p {
		if i == 8 {
			parts = append(parts, invalidPart)
			break
		}
		if pb.ValidatePath(pb.Path{"x", c}) != nil {
			c = invalidPart
		}
		parts = append(parts, c)
	}
	return strings.Join(parts, "/")
}

// addRecord appends an item outcome; the caller holds w.mu. Past
// maxFailRecords for a collector, a record that is not ok is only counted.
func (w *Writer) addRecord(collector string, it Item, fs []redact.Finding, e *entry) *record {
	if it.Status != pb.ItemOK {
		if w.failN[collector] >= maxFailRecords {
			w.failOmit[collector]++
			return nil
		}
		w.failN[collector]++
	}
	w.seq++
	for _, f := range fs {
		it.Redactions += f.Count
	}
	if e != nil {
		it.Size = e.size
	}
	r := &record{collector: collector, seq: w.seq, item: it, findings: fs, e: e}
	w.records = append(w.records, r)
	return r
}

func (c *call) item(w *Writer) Item {
	return Item{Path: c.path.String(), ContentType: c.ctype, CapturedAt: w.now(), Source: safeSource(c.src)}
}

// uidRe is a Kubernetes UID: hex digits and dashes.
var uidRe = regexp.MustCompile(`^[0-9a-fA-F-]{1,64}$`)

// safeSource replaces every Source name that fails validation and clears an
// unknown identity, so no unvalidated value reaches the manifest.
func safeSource(s pb.Source) pb.Source {
	check := func(v *string, k pb.NameKind) {
		if *v != "" {
			if _, err := pb.Name(k, *v); err != nil {
				*v = invalidPart
			}
		}
	}
	check(&s.Namespace, pb.Namespace)
	check(&s.Pod, pb.Object)
	check(&s.Container, pb.ContainerName)
	if s.UID != "" && !uidRe.MatchString(string(s.UID)) {
		s.UID = invalidPart
	}
	if !identities[s.Identity] {
		s.Identity = ""
	}
	return s
}
