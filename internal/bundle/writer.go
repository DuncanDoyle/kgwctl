package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"

	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// Content types recorded in Item.ContentType.
const (
	ContentObject     = "object"
	ContentConfigDump = "config-dump"
	ContentJSON       = "json"
	ContentPrometheus = "prometheus"
	ContentText       = "text"
)

// Item reasons set by the writer.
const (
	ReasonInvalidPath   = "invalid path"
	ReasonDuplicate     = "duplicate path"
	ReasonSizeCap       = "size cap"
	ReasonStaging       = "staging"
	ReasonSizeBudget    = "size budget"
	ReasonForbiddenKind = "forbidden kind"
	ReasonInvalidJSON   = "invalid JSON"
	ReasonNotPrometheus = "not Prometheus text"
	ReasonSealed        = "sealed"
)

const (
	// markerReserve keeps room under a text cap for the truncation line.
	markerReserve = 64
	invalidPart   = "[invalid]"
)

// truncation is the writer's marker line for a cut text item or footer.
func truncation(reason string) string { return "[kgwctl: truncated: " + reason + "]" }

// errStop ends a text stream the writer no longer stages.
var errStop = errors.New("bundle: stop staging")

// Writer stages redacted collector output and writes the archive. Every Sink
// method redacts before staging; Seal freezes staged state; Finalize commits
// it in a fixed order within the size budget.
type Writer struct {
	dest   Dest
	sp     *Spool
	root   string
	lim    Limits
	ro     redact.Options
	server string
	now    func() time.Time

	mu        sync.Mutex
	sealed    bool
	sealedAt  time.Time
	finalized bool
	order     []string
	seq       int
	records   []*record
	keys      map[string]map[string]bool // collector -> lower-cased staged paths
	calls     map[*call]struct{}
	late      map[string]int
	excl      map[Exclusion]int // Count is zero in keys
	frozen    []Exclusion
	runLog    []string
	runLogN   int64
	runLogCut bool

	// errCache holds error text already redacted, as collectors repeat errors.
	errMu    sync.Mutex
	errCache map[string]string
}

// record is one item outcome; e holds the staged body of an ok item.
type record struct {
	collector string
	seq       int
	item      Item
	findings  []redact.Finding
	e         *entry
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
	findings  []redact.Finding // of the staged lines
	fidx      map[redact.Finding]int
	late      bool    // in flight at Seal; the result is discarded
	kept      *record // a text item kept at Seal
	dropErr   error   // staging failed mid-stream
}

// NewWriter returns a writer that stages into sp and writes a tar.gz with
// members under root to d. ro tunes redaction; server is the API server URL
// that error text hides.
func NewWriter(d Dest, sp *Spool, root string, l Limits, ro redact.Options, server string, now func() time.Time) *Writer {
	return &Writer{
		dest: d, sp: sp, root: root, lim: l, ro: ro, server: server, now: now,
		keys: map[string]map[string]bool{}, calls: map[*call]struct{}{},
		late: map[string]int{}, excl: map[Exclusion]int{},
	}
}

// Sink returns the Sink for one collector.
func (w *Writer) Sink(collector string) pb.Sink { return &sink{w: w, name: collector} }

// Log adds a line to run.log, through the error text redactor. Lines past the
// run.log cap are dropped and the footer ends with a truncation line.
func (w *Writer) Log(line string) {
	s := redact.ErrorText(errors.New(line), w.server)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.finalized || w.runLogCut {
		return
	}
	if w.runLogN+int64(len(s))+1 > w.lim.RunLog {
		w.runLogCut = true
		return
	}
	w.runLog = append(w.runLog, s)
	w.runLogN += int64(len(s)) + 1
}

// maxErrCache bounds the error text cache.
const maxErrCache = 4096

// errorText renders err for the manifest and errors.txt: a remote error as
// its status only, anything else through the error text redactor.
func (w *Writer) errorText(err error) string {
	if redact.IsRemote(err) {
		return redact.RemoteError(err)
	}
	msg := err.Error()
	w.errMu.Lock()
	s, ok := w.errCache[msg]
	w.errMu.Unlock()
	if ok {
		return s
	}
	s = redact.ErrorText(err, w.server)
	w.errMu.Lock()
	if w.errCache == nil {
		w.errCache = map[string]string{}
	}
	if len(w.errCache) < maxErrCache {
		w.errCache[msg] = s
	}
	w.errMu.Unlock()
	return s
}

func itemStatus(err error) pb.ItemStatus {
	switch {
	case apierrors.IsForbidden(err):
		return pb.ItemDenied
	case errors.Is(err, context.DeadlineExceeded), apierrors.IsTimeout(err), apierrors.IsServerTimeout(err):
		return pb.ItemTimeout
	}
	return pb.ItemFailed
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

// addRecord appends an item outcome; the caller holds w.mu.
func (w *Writer) addRecord(collector string, it Item, fs []redact.Finding, e *entry) *record {
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

// safeSource replaces every Source name that fails validation, so no
// unvalidated value reaches the manifest.
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
	return s
}

// begin registers a Sink write after checking the seal, the path and
// duplicates within the collector.
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
	c.key = strings.ToLower(p.String())
	if w.keys[collector][c.key] {
		return nil, w.duplicate(c)
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
func (w *Writer) stage(c *call, fs []redact.Finding, reason string) (*record, error) {
	if w.keys[c.collector][c.key] {
		return nil, w.duplicate(c)
	}
	if w.keys[c.collector] == nil {
		w.keys[c.collector] = map[string]bool{}
	}
	w.keys[c.collector][c.key] = true
	it := c.item(w)
	it.Status, it.Reason = pb.ItemOK, reason
	e := c.e
	return w.addRecord(c.collector, it, fs, &e), nil
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
	_, err := w.stage(c, o.findings, "")
	return err
}

// structured reads a body up to limit, redacts it with fn and stages it.
func (w *Writer) structured(c *call, r io.Reader, limit int64, fn func([]byte, redact.Options) ([]byte, []redact.Finding, error)) error {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return w.finish(c, outcome{status: itemStatus(err), reason: w.errorText(err), err: err})
	}
	if int64(len(body)) > limit {
		return w.finish(c, outcome{status: pb.ItemDropped, reason: ReasonSizeCap, err: fmt.Errorf("%w: %s", pb.ErrBudget, ReasonSizeCap)})
	}
	out, fs, err := fn(body, w.ro)
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
	c.addFindings(fs)
	return nil
}

// addFindings sums fs into c.findings per rule and location.
func (c *call) addFindings(fs []redact.Finding) {
	for _, f := range fs {
		k := redact.Finding{Rule: f.Rule, Location: f.Location}
		if i, ok := c.fidx[k]; ok {
			c.findings[i].Count += f.Count
			continue
		}
		if c.fidx == nil {
			c.fidx = map[redact.Finding]int{}
		}
		c.fidx[k] = len(c.findings)
		c.findings = append(c.findings, f)
	}
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
	_, err := w.stage(c, res.Findings, reason)
	return err
}

// Seal freezes staged state at the collection deadline. A text item with
// staged lines is kept with a truncation line; every other call in flight is
// counted as late for its collector and its result is not written. Exclude
// counts are frozen. Later Sink calls return ErrSealed.
func (w *Writer) Seal() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sealed {
		return
	}
	w.sealed = true
	w.sealedAt = w.now()
	for c := range w.calls {
		if c.text && c.lines > 0 && c.dropErr == nil && w.sp.add(&c.e, []byte(truncation(ReasonSealed)+"\n"), true) == nil {
			c.kept, _ = w.stage(c, c.findings, "truncated: "+ReasonSealed)
			c.late = c.kept == nil // a duplicate, already recorded by stage
			continue
		}
		c.late = true
		w.late[c.collector]++
	}
	w.frozen = w.exclusions()
}

// exclusions returns the counts sorted by kind then reason; the caller holds w.mu.
func (w *Writer) exclusions() []Exclusion {
	out := make([]Exclusion, 0, len(w.excl))
	for k, n := range w.excl {
		k.Count = n
		out = append(out, k)
	}
	slices.SortFunc(out, func(a, b Exclusion) int {
		if c := strings.Compare(a.Kind, b.Kind); c != 0 {
			return c
		}
		return strings.Compare(string(a.Reason), string(b.Reason))
	})
	return out
}

// Excluded returns the exclusion counts, frozen at Seal, sorted by kind then
// reason.
func (w *Writer) Excluded() []Exclusion {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sealed {
		return slices.Clone(w.frozen)
	}
	return w.exclusions()
}

// Late returns, per collector, the calls that were in flight at Seal.
func (w *Writer) Late() map[string]int {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]int, len(w.late))
	for k, v := range w.late {
		out[k] = v
	}
	return out
}

// Items returns every item outcome in commit order: collector order, then
// path. Before Finalize, collectors sort by name.
func (w *Writer) Items() []Item {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sortRecords()
	out := make([]Item, len(w.records))
	for i, r := range w.records {
		out[i] = r.item
	}
	return out
}

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

// Finalize seals if needed, then commits staged items in order then path
// within the size budget, writes the footers within their caps and closes
// the destination. It always removes the spool; on error it aborts the
// destination. It has no timer: it writes at most MaxSize of tar content.
func (w *Writer) Finalize(order []string, m Manifest) (err error) {
	w.Seal()
	w.mu.Lock()
	defer w.mu.Unlock()
	defer func() { err = errors.Join(err, w.sp.Remove()) }()
	if w.finalized {
		return errors.New("bundle: writer already finalized")
	}
	w.finalized = true
	w.order = slices.Clone(order)
	if err := w.commit(m); err != nil {
		return errors.Join(err, w.dest.Abort())
	}
	return nil
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

// countTo counts bytes written through to w.
type countTo struct {
	w io.Writer
	n int64
}

func (c *countTo) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// commit writes the archive; the caller holds w.mu.
func (w *Writer) commit(m Manifest) error {
	w.sortRecords()
	end := w.now()

	// The footer reserve and the two end-of-archive blocks come off MaxSize first.
	budget := w.lim.MaxSize - 1024
	for _, f := range w.footers() {
		budget -= headerCost(w.header(f.name, f.cap, end)) + pad(f.cap)
	}
	seen := map[string]bool{}
	var used int64
	for _, r := range w.records {
		if r.e == nil {
			continue
		}
		key := strings.ToLower(r.item.Path)
		reason := ""
		if seen[key] {
			reason = ReasonDuplicate
		} else if cost := headerCost(w.header(r.item.Path, r.e.size, r.item.CapturedAt)) + pad(r.e.size); used+cost > budget {
			reason = ReasonSizeBudget
		} else {
			used += cost
		}
		seen[key] = true
		if reason != "" {
			r.item.Status, r.item.Reason = pb.ItemDropped, reason
			r.e, r.findings = nil, nil
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

	files := map[string][]byte{
		pb.ErrorsFile:     w.errorsFooter(&m),
		pb.RedactionsFile: w.redactionsFooter(),
		pb.RunLogFile:     textFooter(w.runLog, w.runLogCut, w.lim.RunLog),
	}
	files[pb.ManifestFile] = w.manifestFooter(m, end)
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
	return w.dest.Close()
}

// manifestFooter fills the writer's fields of m and encodes it within its cap.
func (w *Writer) manifestFooter(m Manifest, end time.Time) []byte {
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
	// Items past the cap by their compact size alone can never fit, so they
	// are cut before Bound's search.
	var n int64
	m.Items = make([]Item, 0, len(w.records))
	for _, r := range w.records {
		b, _ := json.Marshal(r.item)
		if n += int64(len(b)) + 1; n > w.lim.Manifest {
			m.ItemsOmitted += len(w.records) - len(m.Items)
			break
		}
		m.Items = append(m.Items, r.item)
	}
	Bound(&m, w.lim.Manifest)
	var b bytes.Buffer
	_ = WriteManifest(&b, m)
	return b.Bytes()
}

// errorsFooter is one line per failed collector, then per item that is not
// ok or skipped. It also redacts the collector errors in m.
func (w *Writer) errorsFooter(m *Manifest) []byte {
	var lines []string
	m.Collectors = slices.Clone(m.Collectors)
	for i := range m.Collectors {
		c := &m.Collectors[i]
		if c.Error != "" {
			c.Error = redact.ErrorText(errors.New(c.Error), w.server)
		}
		if c.Status == pb.CollectorFailed || c.Status == pb.CollectorTimeout {
			lines = append(lines, fmt.Sprintf("%s: %s: %s", c.Name, c.Status, c.Error))
		}
	}
	for _, r := range w.records {
		if s := r.item.Status; s != pb.ItemOK && s != pb.ItemSkipped {
			lines = append(lines, fmt.Sprintf("%s: %s: %s: %s", r.collector, r.item.Path, s, r.item.Reason))
		}
	}
	return textFooter(lines, false, w.lim.Errors)
}

// textFooter joins lines within limit, ending with a truncation line when
// lines were cut.
func textFooter(lines []string, cut bool, limit int64) []byte {
	var b bytes.Buffer
	room := limit - markerReserve
	for _, l := range lines {
		if int64(b.Len()+len(l)+1) > room {
			cut = true
			break
		}
		b.WriteString(l)
		b.WriteByte('\n')
	}
	if cut {
		b.WriteString(truncation(ReasonSizeCap) + "\n")
	}
	return b.Bytes()
}

// redactedFile is one file's entry in redactions.json.
type redactedFile struct {
	Path            string           `json:"path"`
	Findings        []redact.Finding `json:"findings"`
	FindingsOmitted int              `json:"findingsOmitted,omitempty"`
}

// redactionsFooter lists per committed file its findings, within the cap;
// files and findings past it are counted as omitted.
func (w *Writer) redactionsFooter() []byte {
	var files []redactedFile
	for _, r := range w.records {
		if r.e != nil && len(r.findings) > 0 {
			files = append(files, redactedFile{Path: r.item.Path, Findings: r.findings})
		}
	}
	var b bytes.Buffer
	b.WriteString("{\"files\": [")
	room := w.lim.Redactions - 128 // closing text and the omitted count
	fits := func(enc []byte) bool { return int64(b.Len()+len(enc)) <= room }
	omitted := 0
	for i, f := range files {
		if enc := encodeFile(f, i > 0); fits(enc) {
			b.Write(enc)
			continue
		}
		// Keep the longest prefix of this file's findings that fits, then stop.
		part := func(k int) []byte {
			return encodeFile(redactedFile{Path: f.Path, Findings: f.Findings[:k], FindingsOmitted: len(f.Findings) - k}, i > 0)
		}
		k := sort.Search(len(f.Findings)+1, func(k int) bool { return !fits(part(k)) }) - 1
		if k > 0 {
			b.Write(part(k))
			i++
		}
		omitted = len(files) - i
		break
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
	body, err := yaml.Marshal(obj.Object)
	if err != nil {
		return s.w.finish(c, outcome{status: pb.ItemFailed, reason: s.w.errorText(err), err: err})
	}
	if int64(len(body)) > s.w.lim.Other {
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
	s.w.addRecord(s.name, Item{Path: path, CapturedAt: s.w.now(), Source: safeSource(src), Status: itemStatus(err), Reason: text}, nil, nil)
}

func (s *sink) Exclude(gk schema.GroupKind, reason pb.ExcludeReason) error {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	if s.w.sealed {
		return pb.ErrSealed
	}
	group := pb.Core
	if gk.Group != "" {
		if _, err := pb.Name(pb.Group, gk.Group); err != nil {
			return err
		}
		group = gk.Group
	}
	if _, err := pb.Name(pb.Kind, gk.Kind); err != nil {
		return err
	}
	s.w.excl[Exclusion{Kind: group + "/" + gk.Kind, Reason: reason}]++
	return nil
}
