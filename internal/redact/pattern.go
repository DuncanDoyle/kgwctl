package redact

import (
	"regexp"
	"strings"
)

// patternRule rewrites each match of re to submatch keep, the marker, then
// submatch tail (0 means none).
type patternRule struct {
	re         *regexp.Regexp
	keep, tail int
	// skip, when set, leaves a match that is not a credential.
	skip func(sub []string) bool
}

// credAssign is a credential-named key and its separator, up to the value's
// opening quote if any.
const credAssign = `\b[a-z0-9_.-]*(?:password|passwd|secret|token|api[_-]?key|apikey|credentials?|private[_-]?key|access[_-]?key|client[_-]?secret|signature)s?(?:\\?["'])?\s*[:=]\s*`

// emptyOrMarker skips a quoted value that is empty or already the marker.
func emptyOrMarker(sub []string) bool { return sub[2] == "" || sub[2] == Marker }

// patterns is the pattern pass, in order. Unquoted value classes exclude '['
// so a marker from an earlier rule is never matched again.
var patterns = []patternRule{
	// PEM private-key blocks, including one that runs to the end of the string.
	{re: regexp.MustCompile(`(-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----)(?s:.*?)(-----END [A-Z0-9 ]*PRIVATE KEY-----|\z)`), keep: 1, tail: 2},
	// Credential query values: ?access_token=, &X-Amz-Signature=, ?code=.
	{re: regexp.MustCompile(`(?i)([?&](?:[a-z0-9_.-]*(?:token|secret|password|passwd|signature|credential|api[_-]?key|apikey)|code|sig|key)=)[^&\s"'#\[\]]+`), keep: 1},
	// Quoted credential assignments, whole value: password="a b", "apiKey":"a\"b",
	// password='a b' and the escaped JSON form \"access_token\":\"a b\".
	{re: regexp.MustCompile(`(?i)(` + credAssign + `")((?:[^"\\\n]|\\.)*)(")`), keep: 1, tail: 3, skip: emptyOrMarker},
	{re: regexp.MustCompile(`(?i)(` + credAssign + `')([^'\n]*)(')`), keep: 1, tail: 3, skip: emptyOrMarker},
	// In the escaped form a nested quote is \\\" and a nested backslash \\\\;
	// a backslash run is read greedily, so nothing after a nested quote leaks.
	{re: regexp.MustCompile(`(?i)(` + credAssign + `\\")((?:[^"\\\n]|\\\\\\"|\\\\|\\[^"\\\n])*)(\\")`), keep: 1, tail: 3, skip: emptyOrMarker},
	// Unquoted credential assignments in free text: password=..., token: ...
	{re: regexp.MustCompile(`(?i)(` + credAssign + `(?:\\?["'])?)[^\s"'&,;<>{}\[\]\\]+`), keep: 1},
	// Authorization credentials.
	{re: regexp.MustCompile(`(?i)(\bbearer\s+)[A-Za-z0-9._~+/=-]+`), keep: 1},
	// After an authorization header name, the Basic credential is redacted
	// whatever its shape.
	{re: regexp.MustCompile(`(?i)(\bauthorization(?:\\?["'])?\s*[:=]\s*(?:\\?["'])?basic\s+)[^\s"',;\\\[\]]+`), keep: 1},
	// Elsewhere "basic authentication" is prose: an all-lowercase word is skipped.
	{re: regexp.MustCompile(`(?i)(\bbasic\s+)([A-Za-z0-9+/]{8,}={0,2})`), keep: 1,
		skip: func(sub []string) bool {
			return sub[2] == strings.ToLower(sub[2]) && !strings.ContainsAny(sub[2], "0123456789+/=")
		}},
	// JWTs.
	{re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]*`)},
	// AWS access key IDs.
	{re: regexp.MustCompile(`\b(?:AKIA|ASIA|AGPA|AIDA|AROA|ANPA|ANVA|AIPA|ABIA|ACCA)[A-Z0-9]{16}\b`)},
	// Known key prefixes.
	{re: regexp.MustCompile(`\b(sk-(?:ant-|proj-)?|sk_live_|sk_test_|rk_live_|ghp_|gho_|ghu_|ghs_|ghr_|github_pat_|glpat-|xox[abprs]-|AIza)[A-Za-z0-9_-]{16,}`), keep: 1},
}

// patternPass applies the pattern rules to s and returns the number of matches.
func patternPass(s string) (string, int) {
	n := 0
	for _, p := range patterns {
		idx := p.re.FindAllStringSubmatchIndex(s, -1)
		if idx == nil {
			continue
		}
		var b strings.Builder
		last := 0
		for _, m := range idx {
			sub := make([]string, len(m)/2)
			for i := range sub {
				if m[2*i] >= 0 {
					sub[i] = s[m[2*i]:m[2*i+1]]
				}
			}
			if p.skip != nil && p.skip(sub) {
				continue
			}
			n++
			b.WriteString(s[last:m[0]])
			if p.keep > 0 {
				b.WriteString(sub[p.keep])
			}
			b.WriteString(Marker)
			if p.tail > 0 {
				b.WriteString(sub[p.tail])
			}
			last = m[1]
		}
		b.WriteString(s[last:])
		s = b.String()
	}
	return s, n
}
