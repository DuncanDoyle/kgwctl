package redact

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"regexp"
	"strings"
)

// LineWriter receives redacted lines, without their line terminator, with the
// findings made while redacting each one. tail is the pem-tail finding the
// text gets if it ends after this line, nil when no private-key block is open.
type LineWriter interface {
	WriteLine(line []byte, fs []Finding, tail *Finding) error
}

// TextResult reports what Text did. The writer, not Text, appends the
// truncation marker.
type TextResult struct {
	Findings []Finding
	// Truncated is set when the cap or a read error ended the text early.
	Truncated bool
	// Reason is ReasonSizeCap or ReasonReadError when Truncated.
	Reason string
	// FindingsOmitted counts distinct findings past MaxTextFindings.
	FindingsOmitted int
}

// MaxTextFindings caps the distinct findings Text returns.
const MaxTextFindings = 4096

// Truncation reasons.
const (
	ReasonSizeCap   = "size cap"
	ReasonReadError = "read error"
)

var (
	pemBeginRe = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)
	pemEndRe   = regexp.MustCompile(`-----END [A-Z0-9 ]*PRIVATE KEY-----`)
)

var errLineTooLong = errors.New("line over the cap")

// Text redacts r line by line into w (design §7): PEM private-key blocks, the
// debug header-line rule, then the pattern pass. Each complete line is
// written as soon as it is read, with its findings. A partial last line before
// a read error is dropped; a clean EOF keeps it. An open PEM block is redacted
// to the end. max caps the bytes written, one newline per line included; the
// text is cut before the first line that would pass it. max <= 0 means no cap.
// Findings cover written lines only; pem-tail is reported only when the
// block's opening line was written. The error is the reader's or the writer's.
func Text(r io.Reader, w LineWriter, max int64, o Options) (TextResult, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	all := &recorder{limit: MaxTextFindings}
	t := &textState{opts: o}
	var res TextResult
	var written int64
	var err error
	// tail is the open block as of the last written line.
	var tail *Finding
	for n := 1; ; n++ {
		line, rerr := readLine(br, max)
		if rerr != nil && rerr != io.EOF {
			res.Truncated = true
			res.Reason = ReasonReadError
			if errors.Is(rerr, errLineTooLong) {
				res.Reason = ReasonSizeCap
			} else {
				err = rerr
			}
			break
		}
		if rerr == io.EOF && len(line) == 0 {
			break
		}
		t.rec = &recorder{}
		out := t.line(string(line), n)
		if max > 0 && written+int64(len(out))+1 > max {
			res.Truncated = true
			res.Reason = ReasonSizeCap
			break
		}
		next := t.tail()
		if werr := w.WriteLine([]byte(out), t.rec.findings(), next); werr != nil {
			err = werr
			break
		}
		tail = next
		for _, f := range t.rec.findings() {
			all.add(f.Rule, f.Location, f.Count)
		}
		written += int64(len(out)) + 1
		if rerr == io.EOF {
			break
		}
	}
	if tail != nil {
		all.add(tail.Rule, tail.Location, tail.Count)
	}
	res.Findings, res.FindingsOmitted = all.findings(), all.omitted
	return res, err
}

// readLine returns the next line without its newline. A line longer than max
// gives errLineTooLong; at EOF the unterminated rest comes with io.EOF.
func readLine(br *bufio.Reader, max int64) ([]byte, error) {
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		line = append(line, chunk...)
		if max > 0 && int64(len(line)) > max+1 {
			return nil, errLineTooLong
		}
		switch {
		case err == nil:
			return bytes.TrimSuffix(line, []byte("\n")), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case err == io.EOF:
			return line, io.EOF
		default:
			return nil, err
		}
	}
}

// textState tracks an open PEM block across lines.
type textState struct {
	rec  *recorder
	opts Options
	// pemStart is the line of the open block's BEGIN marker, 0 when none.
	pemStart int
}

// tail returns the pem-tail finding of the open block, nil when none.
func (t *textState) tail() *Finding {
	if t.pemStart == 0 {
		return nil
	}
	return &Finding{Rule: RulePEMTail, Location: lineLoc(t.pemStart), Count: 1}
}

// timestampRe is the kubelet's prefix on logs read with timestamps=true.
var timestampRe = regexp.MustCompile(`^\d{4}-\d\d-\d\dT[0-9:.]+(?:Z|[+-]\d\d:\d\d)\s+`)

// line redacts one line; a kubelet timestamp prefix is kept and the rules run
// on the rest.
func (t *textState) line(s string, n int) string {
	ts := timestampRe.FindString(s)
	return ts + t.body(s[len(ts):], n)
}

func (t *textState) body(s string, n int) string {
	if t.pemStart > 0 {
		end := pemEndRe.FindStringIndex(s)
		if end == nil {
			return Marker
		}
		t.rec.add(RulePattern, lineLoc(t.pemStart), 1)
		t.pemStart = 0
		rest, hits := patternPass(s[end[0]:])
		t.rec.add(RulePattern, lineLoc(n), hits)
		if strings.TrimSpace(s[:end[0]]) != "" {
			return Marker + rest
		}
		return rest
	}
	if b := pemBeginRe.FindStringIndex(s); b != nil && !pemEndRe.MatchString(s[b[1]:]) {
		prefix, hits := patternPass(s[:b[0]])
		t.rec.add(RulePattern, lineLoc(n), hits)
		t.pemStart = n
		out := prefix + s[b[0]:b[1]]
		if strings.TrimSpace(s[b[1]:]) != "" {
			out += Marker
		}
		return out
	}
	s, hits := debugHeaders(s, t.opts)
	t.rec.add(RuleDebugHeader, lineLoc(n), hits)
	s, hits = patternPass(s)
	t.rec.add(RulePattern, lineLoc(n), hits)
	return s
}
