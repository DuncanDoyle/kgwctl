package redact

import "regexp"

// debugHeaderRe is one header in Envoy's debug log: 'name', 'value'.
var debugHeaderRe = regexp.MustCompile(`^(\s*'([^']+)', ')(.*)('\s*)$`)

// debugHeader applies the debug header-line rule (design §7): values are
// redacted except for a short allowlist minus --redact-header names, and kept
// values get the pattern pass. ok is false when s is not a header line.
func debugHeader(s string, o Options) (out string, rule Rule, hits int, ok bool) {
	m := debugHeaderRe.FindStringSubmatch(s)
	if m == nil {
		return s, "", 0, false
	}
	if keptDebugHeader(m[2]) && !o.sensitiveHeader(m[2]) {
		v, n := patternPass(m[3])
		return m[1] + v + m[4], RulePattern, n, true
	}
	if m[3] == Marker {
		return s, "", 0, true
	}
	return m[1] + Marker + m[4], RuleDebugHeader, 1, true
}
