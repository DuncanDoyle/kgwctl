package view

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

	"github.com/DuncanDoyle/kgwctl/internal/load"
)

var (
	// urlRe is a URL up to a space, quote, backslash or angle bracket. The path goes too:
	// through pods/proxy it names the API server's namespace, Pod and port.
	urlRe = regexp.MustCompile(`\b([A-Za-z][A-Za-z0-9+.-]*)://[^\s"'<>\\]+`)
	// netRe is the address part of a Go net error: "dial tcp 10.0.0.1:6443: ...".
	netRe = regexp.MustCompile(`\b((?:dial|read|write) (?:tcp|udp)[46]?) [^\s"\\]+?(:\s|$)`)
	// lookupRe is the resolver address of a DNS error: "lookup x on 10.96.0.10:53: ...".
	lookupRe = regexp.MustCompile(`\b(lookup [^\s"\\]+ on) [^\s"\\]+?(:\s|$)`)
)

// Redactor redacts cluster-derived text so it never carries a server address (design §3.3 rule 1).
type Redactor struct {
	// host matches the API server's host; nil when unknown. Boundaries are checked in redactHost.
	host *regexp.Regexp
}

// NewRedactor also redacts the host of server, the REST config's Host, as <apiserver>.
func NewRedactor(server string) Redactor {
	if server == "" {
		return Redactor{}
	}
	if !strings.Contains(server, "://") {
		server = "https://" + server
	}
	u, err := url.Parse(server)
	if err != nil || u.Hostname() == "" {
		return Redactor{}
	}
	return Redactor{host: regexp.MustCompile(`(?i)` + regexp.QuoteMeta(u.Hostname()))}
}

// Redact replaces URLs with scheme://<redacted>, the addresses of net and DNS errors
// with <redacted>, and the API server's host with <apiserver>. It is one pass and idempotent.
func (r Redactor) Redact(s string) string {
	s = urlRe.ReplaceAllString(s, "$1://<redacted>")
	s = netRe.ReplaceAllString(s, "$1 <redacted>$2")
	s = lookupRe.ReplaceAllString(s, "$1 <redacted>$2")
	return r.redactHost(s)
}

// redactHost replaces each whole-name match of the host in one pass. A match next to a
// letter, digit, '-', ':', '<' or '>' (or after a '.') is part of another name or a placeholder.
func (r Redactor) redactHost(s string) string {
	if r.host == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range r.host.FindAllStringIndex(s, -1) {
		if m[0] > 0 && strings.ContainsRune(nameChars+".", rune(s[m[0]-1])) ||
			m[1] < len(s) && strings.ContainsRune(nameChars, rune(s[m[1]])) {
			continue
		}
		b.WriteString(s[last:m[0]])
		b.WriteString("<apiserver>")
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// nameChars continue a host name or a placeholder.
const nameChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-:<>"

// Input is in with its reason redacted.
func (r Redactor) Input(in load.Input) load.Input {
	in.Reason = r.Redact(in.Reason)
	return in
}

// JSON redacts the strings of b; nil when the result is not valid JSON.
func (r Redactor) JSON(b []byte) json.RawMessage {
	out := []byte(r.Redact(string(b)))
	if !json.Valid(out) {
		return nil
	}
	return out
}

// Redact is Redactor.Redact without a known server host.
func Redact(s string) string { return Redactor{}.Redact(s) }

// RedactInput is Redactor.Input without a known server host.
func RedactInput(in load.Input) load.Input { return Redactor{}.Input(in) }
