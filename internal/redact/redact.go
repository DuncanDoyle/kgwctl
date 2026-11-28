// Package redact holds the support bundle's redactors, one per content type
// (design §7). The archive writer runs one of them on every member before it
// is staged. The functions are pure: they never log, and a Finding never holds
// a value, a hash, a length or a map key.
package redact

import (
	"errors"
	"strconv"
	"strings"
)

// Marker replaces every redacted value. Values stay strings, so redacted files
// parse with the same keys.
const Marker = "[redacted]"

// envoyMarkerBytes is Envoy's own marker in a bytes field, base64-encoded.
const envoyMarkerBytes = "W3JlZGFjdGVkXQ=="

var (
	// ErrForbiddenKind is returned by Object for Secrets and ConfigMaps.
	ErrForbiddenKind = errors.New("redact: forbidden kind")
	// ErrMissingKind is returned by Object for an object or List item without a kind.
	ErrMissingKind = errors.New("redact: missing kind")
	// ErrInvalidJSON is returned for a body that does not parse as JSON. The
	// error holds no part of the body.
	ErrInvalidJSON = errors.New("redact: invalid JSON")
	// ErrNotPrometheus is returned for a body that is not Prometheus text
	// exposition. The error holds no part of the body.
	ErrNotPrometheus = errors.New("redact: not Prometheus text exposition")
)

// Finding reports that a rule redacted Count values at Location. Location is
// a field path, with array elements as [] and map keys as [key], or line:<n>.
type Finding struct {
	Rule     Rule   `json:"rule"`
	Location string `json:"location"`
	Count    int    `json:"count"`
}

// Options tune the redactors for one run.
type Options struct {
	// ExtraHeaders are header names, from --redact-header, added to the
	// sensitive-header list. Matching ignores case.
	ExtraHeaders []string
	// MaxSize, when positive, makes JSON and ConfigDump output compact when
	// indentation would make it larger.
	MaxSize int64
}

// recorder sums findings per rule and location, in first-seen order.
type recorder struct {
	idx  map[Finding]int
	list []Finding
	// limit, when positive, caps distinct findings; omitted counts the rest.
	limit, omitted int
}

func (r *recorder) add(rule Rule, loc string, n int) {
	if n <= 0 {
		return
	}
	k := Finding{Rule: rule, Location: loc}
	if r.idx == nil {
		r.idx = map[Finding]int{}
	}
	if i, ok := r.idx[k]; ok {
		r.list[i].Count += n
		return
	}
	if r.limit > 0 && len(r.list) >= r.limit {
		r.omitted++
		return
	}
	r.idx[k] = len(r.list)
	k.Count = n
	r.list = append(r.list, k)
}

func (r *recorder) findings() []Finding { return r.list }

// lineLoc is the location of a 1-based text line.
func lineLoc(n int) string { return "line:" + strconv.Itoa(n) }

// joinLoc appends a relative location, such as one from JWKS, to at.
func joinLoc(at, rel string) string {
	switch {
	case rel == "":
		return at
	case at == "" || strings.HasPrefix(rel, "["):
		return at + rel
	}
	return at + "." + rel
}

// maxLoc caps a rendered location; locCut marks the cut middle.
const (
	maxLoc = 512
	locCut = "[...]"
)

// capLoc keeps a location within maxLoc bytes: its head, the marker, then
// the newest segments, so suffix-based rules still match a deep path.
func capLoc(s string) string {
	if len(s) <= maxLoc {
		return s
	}
	head, rest := "", ""
	if i := strings.Index(s, locCut); i >= 0 {
		head, rest = s[:i], s[i+len(locCut):]
	} else {
		j := strings.LastIndexAny(s[:maxLoc/2], ".[")
		if j <= 0 {
			j = maxLoc / 2
		}
		head, rest = s[:j], s[j:]
	}
	if room := maxLoc - len(head) - len(locCut); len(rest) > room {
		rest = rest[len(rest)-room:]
		if k := strings.IndexAny(rest, ".["); k >= 0 {
			rest = rest[k:]
		}
	}
	return head + locCut + rest
}
