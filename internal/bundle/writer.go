package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
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

	yamlv2 "go.yaml.in/yaml/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

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
	ReasonInvalidSource = "invalid source"
	ReasonInvalidExcl   = "invalid exclusion"
	ReasonPathConflict  = "path conflict"
)

const (
	// markerReserve keeps room under a text cap for the truncation line.
	markerReserve = 64
	invalidPart   = "[invalid]"
	// maxFailRecords caps the records that are not ok, per collector.
	maxFailRecords = 4096
)

// identities and excludeReasons are the values the Sink accepts.
var (
	identities = map[pb.CaptureIdentity]bool{
		"": true, pb.IdentityVerified: true, pb.IdentityChanged: true, pb.IdentityGone: true, pb.IdentityRereadFailed: true,
	}
	excludeReasons = map[pb.ExcludeReason]bool{pb.ExcludeOwnership: true, pb.ExcludeScope: true}
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

	mu          sync.Mutex
	sealed      bool
	sealedAt    time.Time
	finalized   bool
	order       []string
	seq         int
	records     []*record
	keys        map[string]map[string]bool // collector -> staged paths
	failN       map[string]int             // records that are not ok, per collector
	failOmit    map[string]int             // such records past maxFailRecords
	committed   Manifest
	placeholder bool // manifest.json is the placeholder
	calls       map[*call]struct{}
	late        map[string]int
	excl        map[Exclusion]int // Count is zero in keys
	frozen      []Exclusion
	runLog      []string
	runLogN     int64
	runLogCut   bool

	// errCache holds error text already redacted, as collectors repeat errors.
	errMu    sync.Mutex
	errCache map[[sha256.Size]byte]string // keyed by a hash, so no raw text stays
}

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

// NewWriter returns a writer that stages into sp and writes a tar.gz with
// members under root to d. ro tunes redaction; server is the API server URL
// that error text hides.
func NewWriter(d Dest, sp *Spool, root string, l Limits, ro redact.Options, server string, now func() time.Time) *Writer {
	return &Writer{
		dest: d, sp: sp, root: root, lim: l, ro: ro, server: server, now: now,
		keys: map[string]map[string]bool{}, calls: map[*call]struct{}{},
		late: map[string]int{}, excl: map[Exclusion]int{}, failN: map[string]int{}, failOmit: map[string]int{},
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
// its status only, anything else through the error text redactor, which
// redacts the whole text before it cuts.
func (w *Writer) errorText(err error) string {
	if redact.IsRemote(err) {
		return redact.RemoteError(err)
	}
	msg := err.Error()
	key := sha256.Sum256([]byte(msg))
	w.errMu.Lock()
	s, ok := w.errCache[key]
	w.errMu.Unlock()
	if ok {
		return s
	}
	s = redact.ErrorText(errors.New(msg), w.server)
	w.errMu.Lock()
	if w.errCache == nil {
		w.errCache = map[[sha256.Size]byte]string{}
	}
	if len(w.errCache) < maxErrCache {
		w.errCache[key] = s
	}
	w.errMu.Unlock()
	return s
}

// ItemStatus is the item status the Sink records for err: denied on a 403,
// timeout on a deadline or timeout status, else failed.
func ItemStatus(err error) pb.ItemStatus {
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

// Seal freezes staged state at the collection deadline. Every call in flight
// is counted as late for its collector. A text item with staged lines keeps
// them with a truncation line; any other late result is not written. Exclude
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
		w.late[c.collector]++
		if c.text && c.lines > 0 && c.dropErr == nil && w.sp.add(&c.e, []byte(truncation(ReasonSealed)+"\n"), true) == nil {
			c.kept, _ = w.stage(c, "truncated: "+ReasonSealed)
			c.late = c.kept == nil // a duplicate, already recorded by stage
			continue
		}
		c.late = true
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
// the destination. It always removes the spool. On error, including a
// footer encoding error or a panic, which it returns as an error, it aborts
// the destination. It has no timer: it writes at most MaxSize of tar content.
func (w *Writer) Finalize(order []string, m Manifest) (err error) {
	// Cleanup and recovery come first, so a panic in Seal is covered too.
	defer func() { err = errors.Join(err, w.sp.Remove()) }()
	defer func() {
		if r := recover(); r != nil {
			err = errors.Join(w.panicErr(r), w.dest.Abort())
		}
	}()
	w.Seal()
	w.mu.Lock()
	defer w.mu.Unlock()
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

// Committed returns the manifest as written to manifest.json, after a
// successful Finalize; before that it is the zero Manifest.
func (w *Writer) Committed() Manifest {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.committed
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
// skipped, built within the cap.
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
		if s := r.item.Status; s != pb.ItemOK && s != pb.ItemSkipped {
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

// degraded returns the collectors with at least one item that is neither ok
// nor skipped; the caller holds w.mu.
func (w *Writer) degraded() map[string]bool {
	out := map[string]bool{}
	for c := range w.failOmit {
		out[c] = true
	}
	for _, r := range w.records {
		if s := r.item.Status; s != pb.ItemOK && s != pb.ItemSkipped {
			out[r.collector] = true
		}
	}
	return out
}

func (w *Writer) panicErr(r any) error {
	return fmt.Errorf("bundle: finalize panicked: %s", redact.ErrorText(fmt.Errorf("%v", r), w.server))
}

// sealOrAbort seals like Seal. A panic while sealing aborts the destination,
// removes the spool and is returned as an error; the writer is then done.
func (w *Writer) sealOrAbort() (err error) {
	defer func() {
		if r := recover(); r != nil {
			w.mu.Lock()
			w.finalized = true
			w.mu.Unlock()
			err = errors.Join(w.panicErr(r), w.dest.Abort(), w.sp.Remove())
		}
	}()
	w.Seal()
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
