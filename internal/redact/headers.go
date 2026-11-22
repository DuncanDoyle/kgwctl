package redact

import (
	"regexp"
	"strings"
)

// debugHeaderRe is one header in Envoy's debug log: 'name', 'value'. The value
// runs to the last quote, so a trailing JSON '"}' stays outside it.
var debugHeaderRe = regexp.MustCompile(`^(\s*'([^']+)', ')(.*)'(.*)$`)

// jsonNewline separates the header lines of a JSON application log entry.
const jsonNewline = `\n`

// debugHeaders applies the debug header-line rule (design §7) to s and to
// each segment between literal \n escapes, as Envoy's JSON log format writes
// them. Values are redacted except for a short allowlist minus --redact-header
// names; the caller's pattern pass covers the kept ones.
func debugHeaders(s string, o Options) (string, int) {
	segs := strings.Split(s, jsonNewline)
	hits := 0
	for i, seg := range segs {
		m := debugHeaderRe.FindStringSubmatch(seg)
		if m == nil || m[3] == Marker || keptDebugHeader(m[2]) && !o.sensitiveHeader(m[2]) {
			continue
		}
		segs[i] = m[1] + Marker + "'" + m[4]
		hits++
	}
	return strings.Join(segs, jsonNewline), hits
}
