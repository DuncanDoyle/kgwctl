package bundle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// manifestFooter adds the items to m and encodes it within its cap. When even
// the bounded manifest passes the cap, it writes a fixed placeholder.
func (w *Writer) manifestFooter(m Manifest) (Manifest, []byte, error) {
	// Items past the cap by their compact size alone can never fit, so they
	// are cut before Bound's search.
	var n int64
	m.Items = make([]Item, 0, min(len(w.records), 4096))
	for i, r := range w.records {
		b, err := json.Marshal(r.item)
		if err != nil {
			return m, nil, err
		}
		if n += int64(len(b)) + 1; n > w.lim.Manifest {
			m.ItemsOmitted += len(w.records) - i
			break
		}
		m.Items = append(m.Items, r.item)
	}
	for _, k := range w.failOmit {
		m.ItemsOmitted += k
	}
	for _, k := range w.omitOver {
		m.ItemsOmitted += k
	}
	Bound(&m, w.lim.Manifest)
	var b bytes.Buffer
	if err := WriteManifest(&b, m); err != nil {
		return m, nil, err
	}
	if int64(b.Len()) <= w.lim.Manifest {
		return m, b.Bytes(), nil
	}
	w.placeholder = true
	m = Manifest{Start: m.Start, Sealed: m.Sealed, End: m.End, Finalization: cut(m.Finalization, 64), Truncated: true}
	b.Reset()
	err := WriteManifest(&b, m)
	return m, b.Bytes(), err
}

// lineBuf joins lines within a limit; once a line does not fit, it takes no
// more and ends with a truncation line.
type lineBuf struct {
	b    bytes.Buffer
	room int64
	cut  bool
}

func newLineBuf(limit int64) *lineBuf { return &lineBuf{room: limit - markerReserve} }

func (l *lineBuf) add(s string) bool {
	if l.cut || int64(l.b.Len()+len(s)+1) > l.room {
		l.cut = true
		return false
	}
	l.b.WriteString(s)
	l.b.WriteByte('\n')
	return true
}

func (l *lineBuf) bytes() []byte {
	if l.cut {
		l.b.WriteString(truncation(ReasonSizeCap) + "\n")
	}
	return l.b.Bytes()
}

// errorsFooter is one line for a placeholder manifest, per failed collector,
// per collector with uncounted items, then per item that is not ok or
// skipped and not an omission, built within the cap.
func (w *Writer) errorsFooter(cols []CollectorEntry) []byte {
	lb := newLineBuf(w.lim.Errors)
	if w.placeholder {
		lb.add("bundle: " + pb.ManifestFile + ": placeholder: " + ReasonSizeCap)
	}
	for _, c := range cols {
		if c.Status == pb.CollectorFailed || c.Status == pb.CollectorTimeout {
			lb.add(fmt.Sprintf("%s: %s: %s", c.Name, c.Status, c.Error))
		}
	}
	names := make([]string, 0, len(w.failOmit))
	for c := range w.failOmit {
		names = append(names, c)
	}
	slices.Sort(names)
	for _, c := range names {
		lb.add(fmt.Sprintf("%s: %d more items not listed", c, w.failOmit[c]))
	}
	for _, r := range w.records {
		if s := r.item.Status; !r.omit && s != pb.ItemOK && s != pb.ItemSkipped {
			if !lb.add(fmt.Sprintf("%s: %s: %s: %s", r.collector, r.item.Path, s, r.item.Reason)) {
				break
			}
		}
	}
	return lb.bytes()
}

// textFooter joins lines within limit, ending with a truncation line when
// lines were cut.
func textFooter(lines []string, cut bool, limit int64) []byte {
	lb := newLineBuf(limit)
	for _, l := range lines {
		if !lb.add(l) {
			break
		}
	}
	lb.cut = lb.cut || cut
	return lb.bytes()
}

// redactedFile is one file's entry in redactions.json.
type redactedFile struct {
	Path            string           `json:"path"`
	Findings        []redact.Finding `json:"findings"`
	FindingsOmitted int              `json:"findingsOmitted,omitempty"`
}

// redactionsFooter lists per committed file its findings, built within the
// cap; files and findings past it are counted as omitted.
func (w *Writer) redactionsFooter() []byte {
	var b bytes.Buffer
	b.WriteString("{\"files\": [")
	room := w.lim.Redactions - 128 // closing text and the omitted count
	fits := func(enc []byte) bool { return int64(b.Len()+len(enc)) <= room }
	omitted, first, full := 0, true, false
	for _, r := range w.records {
		if r.e == nil || len(r.findings) == 0 {
			continue
		}
		if full {
			omitted++
			continue
		}
		if enc := encodeFile(redactedFile{Path: r.item.Path, Findings: r.findings, FindingsOmitted: r.fomit}, !first); fits(enc) {
			b.Write(enc)
			first = false
			continue
		}
		// Keep the longest prefix of this file's findings that fits, then stop.
		full = true
		part := func(k int) []byte {
			return encodeFile(redactedFile{Path: r.item.Path, Findings: r.findings[:k], FindingsOmitted: len(r.findings) - k + r.fomit}, !first)
		}
		if k := sort.Search(len(r.findings)+1, func(k int) bool { return !fits(part(k)) }) - 1; k > 0 {
			b.Write(part(k))
		} else {
			omitted++
		}
	}
	fmt.Fprintf(&b, "\n], \"filesOmitted\": %d}\n", omitted)
	return b.Bytes()
}

func encodeFile(f redactedFile, comma bool) []byte {
	b, _ := json.Marshal(f)
	if comma {
		return append([]byte(",\n"), b...)
	}
	return append([]byte("\n"), b...)
}
