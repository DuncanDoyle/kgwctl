package redact

import (
	"bytes"
	"strings"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// Prometheus redacts a Prometheus text exposition body: the pattern pass on
// label values and comments; metric names, label names and samples stay. A
// body that does not parse, or whose redacted form no longer parses, gives
// ErrNotPrometheus and no output.
func Prometheus(body []byte, o Options) ([]byte, []Finding, error) {
	if !parsesAsPrometheus(body) {
		return nil, nil, ErrNotPrometheus
	}
	rec := &recorder{}
	lines := strings.Split(string(body), "\n")
	for i, l := range lines {
		var hits int
		if rest := strings.TrimLeft(l, " \t"); strings.HasPrefix(rest, "#") {
			lines[i], hits = redactComment(l[:len(l)-len(rest)], rest)
		} else {
			lines[i], hits = redactLabels(l)
		}
		rec.add(RulePattern, lineLoc(i+1), hits)
	}
	out := []byte(strings.Join(lines, "\n"))
	if !parsesAsPrometheus(out) {
		return nil, nil, ErrNotPrometheus
	}
	return out, rec.findings(), nil
}

func parsesAsPrometheus(b []byte) bool {
	p := expfmt.NewTextParser(model.UTF8Validation)
	_, err := p.TextToMetricFamilies(bytes.NewReader(b))
	return err == nil
}

// redactComment redacts comment c, whose leading blanks are indent, the way
// expfmt reads it: TYPE stays, HELP keeps its metric name, others are free text.
func redactComment(indent, c string) (string, int) {
	body := c[1:]
	kw, after := cutBlank(strings.TrimLeft(body, " \t"))
	switch kw {
	case "TYPE":
		return indent + c, 0
	case "HELP":
		rest := strings.TrimLeft(after, " \t")
		name, doc := splitName(rest)
		head := c[:len(c)-len(rest)] + name
		v, n := patternPass(helpUnescaper.Replace(doc))
		return indent + head + helpEscaper.Replace(v), n
	}
	s, n := patternPass(body)
	return indent + "#" + s, n
}

// cutBlank splits s at its first blank or tab.
func cutBlank(s string) (tok, rest string) {
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], s[i:]
	}
	return s, ""
}

// splitName splits a HELP line after its metric name, quoted or not.
func splitName(s string) (name, rest string) {
	if strings.HasPrefix(s, `"`) {
		end, _ := scanQuoted(s, 0)
		return s[:end], s[end:]
	}
	return cutBlank(s)
}

// redactLabels applies the pattern pass to every label value of a sample line.
func redactLabels(s string) (string, int) {
	// A quoted metric name before the braces may itself hold '{'.
	i := len(s) - len(strings.TrimLeft(s, " \t"))
	if i < len(s) && s[i] == '"' {
		i, _ = scanQuoted(s, i)
	}
	open := strings.IndexByte(s[i:], '{')
	if open < 0 {
		return s, 0
	}
	open += i
	var b strings.Builder
	b.WriteString(s[:open+1])
	hits := 0
	for i := open + 1; i < len(s); {
		switch s[i] {
		case '}':
			b.WriteString(s[i:])
			return b.String(), hits
		case '"':
			end, val := scanQuoted(s, i)
			// expfmt allows blanks and tabs on both sides of '='.
			if strings.HasSuffix(strings.TrimRight(b.String(), " \t"), "=") {
				v, n := patternPass(val)
				hits += n
				b.WriteString(quoteLabel(v))
			} else {
				b.WriteString(s[i:end])
			}
			i = end
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String(), hits
}

// scanQuoted reads the quoted string at s[i] and returns the index after its
// closing quote and its unescaped value.
func scanQuoted(s string, i int) (int, string) {
	var v strings.Builder
	for j := i + 1; j < len(s); j++ {
		switch c := s[j]; {
		case c == '\\' && j+1 < len(s):
			j++
			if s[j] == 'n' {
				v.WriteByte('\n')
			} else {
				v.WriteByte(s[j])
			}
		case c == '"':
			return j + 1, v.String()
		default:
			v.WriteByte(c)
		}
	}
	return len(s), v.String()
}

var (
	labelEscaper  = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	helpEscaper   = strings.NewReplacer(`\`, `\\`, "\n", `\n`)
	helpUnescaper = strings.NewReplacer(`\\`, `\`, `\n`, "\n")
)

func quoteLabel(v string) string { return `"` + labelEscaper.Replace(v) + `"` }
