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
	// host matches the API server's host as a whole word; nil when unknown.
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
	// A host part next to a letter, digit, '-' or ':' (or a '.' before it) is another name.
	return Redactor{host: regexp.MustCompile(`(?i)(^|[^A-Za-z0-9.:-])` + regexp.QuoteMeta(u.Hostname()) + `([^A-Za-z0-9:-]|$)`)}
}

// Redact replaces URLs with scheme://<redacted>, the addresses of net and DNS errors
// with <redacted>, and the API server's host with <apiserver>.
func (r Redactor) Redact(s string) string {
	s = urlRe.ReplaceAllString(s, "$1://<redacted>")
	s = netRe.ReplaceAllString(s, "$1 <redacted>$2")
	s = lookupRe.ReplaceAllString(s, "$1 <redacted>$2")
	if r.host != nil {
		// Adjacent matches share a separator, so repeat until none is left.
		for t := ""; t != s; {
			t, s = s, r.host.ReplaceAllString(s, "${1}<apiserver>${2}")
		}
	}
	return s
}

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
