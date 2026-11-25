package bundle

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"

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
	ReasonMissingKind   = "missing kind"
	ReasonInvalidJSON   = "invalid JSON"
	ReasonNotPrometheus = "not Prometheus text"
	ReasonSealed        = "sealed"
	ReasonTimeout       = "timeout"
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

	// pods re-reads a Pod after its capture, bounded by podTimeout (design §8.2).
	pods       kubernetes.Interface
	podTimeout time.Duration

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
	omitN       map[string]int             // omission records, per collector
	omitOver    map[string]int             // omissions past maxFailRecords
	committed   Manifest
	placeholder bool // manifest.json is the placeholder
	calls       map[*call]struct{}
	late        map[string]int
	excl        map[Exclusion]int // Count is zero in keys
	granted     map[grantKey]bool // grants denied items need, per namespace
	frozen      []Exclusion
	runLog      []string
	runLogN     int64
	runLogCut   bool

	// errCache holds error text already redacted, as collectors repeat errors.
	errMu    sync.Mutex
	errCache map[[sha256.Size]byte]string // keyed by a hash, so no raw text stays
}

// NewWriter returns a writer that stages into sp and writes a tar.gz with
// members under root to d. ro tunes redaction; server is the API server URL
// that error text hides.
func NewWriter(d Dest, sp *Spool, root string, l Limits, ro redact.Options, server string, now func() time.Time) *Writer {
	return &Writer{
		dest: d, sp: sp, root: root, lim: l, ro: ro, server: server, now: now,
		keys: map[string]map[string]bool{}, calls: map[*call]struct{}{},
		late: map[string]int{}, excl: map[Exclusion]int{}, failN: map[string]int{}, failOmit: map[string]int{},
		omitN: map[string]int{}, omitOver: map[string]int{},
	}
}

// usePods sets the guarded client and request timeout of Pod re-reads.
func (w *Writer) usePods(kube kubernetes.Interface, timeout time.Duration) {
	w.pods, w.podTimeout = kube, timeout
}

// Sink returns the Sink for one collector.
func (w *Writer) Sink(collector string) JSONSink { return &sink{w: w, name: collector} }

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

// Seal freezes staged state at the collection deadline. Every call in flight
// is counted as late for its collector. A text item with staged lines keeps
// them, with a truncation line and its open PEM block's pem-tail finding
// unless it was fully read; a kept Pod item gets
// reread-failed, as its pending re-read is discarded. Any other late result
// is not written. Exclude counts are frozen. Later Sink calls return ErrSealed.
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
		keep, reason := c.text && c.done, c.doneReason
		if !keep && c.text && c.lines > 0 && c.dropErr == nil && w.sp.add(&c.e, []byte(truncation(ReasonSealed)+"\n"), true) == nil {
			keep, reason = true, "truncated: "+ReasonSealed
			// A block still open in the kept lines is redacted to the end.
			if c.tail != nil {
				c.found.add([]redact.Finding{*c.tail})
			}
		}
		if keep {
			if c.src.Pod != "" {
				c.src.Identity = pb.IdentityRereadFailed
			}
			c.kept, _ = w.stage(c, reason)
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

// degraded returns the collectors with at least one item that is neither ok
// nor skipped, omissions aside; the caller holds w.mu.
func (w *Writer) degraded() map[string]bool {
	out := map[string]bool{}
	for c := range w.failOmit {
		out[c] = true
	}
	for _, r := range w.records {
		if s := r.item.Status; !r.omit && s != pb.ItemOK && s != pb.ItemSkipped {
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
