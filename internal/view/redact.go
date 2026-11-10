package view

import (
	"encoding/json"
	"regexp"

	"github.com/DuncanDoyle/kgwctl/internal/load"
)

var (
	// urlRe is a URL up to a space, quote, backslash or angle bracket. The path goes too:
	// through pods/proxy it names the API server's namespace, Pod and port.
	urlRe = regexp.MustCompile(`\b([A-Za-z][A-Za-z0-9+.-]*)://[^\s"'<>\\]+`)
	// netRe is the address part of a Go net error: "dial tcp 10.0.0.1:6443: ...".
	netRe = regexp.MustCompile(`\b((?:dial|read|write) (?:tcp|udp)[46]?) [^\s"\\]+?(:\s|$)`)
)

// Redact replaces URLs with scheme://<redacted> and the addresses of net errors with
// <redacted>, so cluster-derived text never carries a server address (design §3.3 rule 1).
func Redact(s string) string {
	s = urlRe.ReplaceAllString(s, "$1://<redacted>")
	return netRe.ReplaceAllString(s, "$1 <redacted>$2")
}

// RedactInput is in with its reason redacted.
func RedactInput(in load.Input) load.Input {
	in.Reason = Redact(in.Reason)
	return in
}

// redactJSON redacts the strings of b; nil when the result is not valid JSON.
func redactJSON(b []byte) json.RawMessage {
	out := []byte(Redact(string(b)))
	if !json.Valid(out) {
		return nil
	}
	return out
}
