package redact

import (
	"bytes"
	"strings"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// Prometheus redacts a Prometheus text exposition body: the pattern pass on
// label values and comments; metric names, label names and samples stay. A
// body that does not parse gives ErrNotPrometheus and no output.
func Prometheus(body []byte, o Options) ([]byte, []Finding, error) {
	p := expfmt.NewTextParser(model.UTF8Validation)
	if _, err := p.TextToMetricFamilies(bytes.NewReader(body)); err != nil {
		return nil, nil, ErrNotPrometheus
	}
	rec := &recorder{}
	lines := strings.Split(string(body), "\n")
	for i, l := range lines {
		var hits int
		switch {
		case strings.HasPrefix(l, "# HELP "):
			name, rest := splitName(l[len("# HELP "):])
			rest, hits = patternPass(rest)
			lines[i] = "# HELP " + name + rest
		case strings.HasPrefix(l, "# TYPE "):
		case strings.HasPrefix(l, "#"):
			var c string
			c, hits = patternPass(l[1:])
			lines[i] = "#" + c
		default:
			lines[i], hits = redactLabels(l)
		}
		rec.add(RulePattern, lineLoc(i+1), hits)
	}
	return []byte(strings.Join(lines, "\n")), rec.findings(), nil
}

// splitName splits a HELP line after its metric name, quoted or not.
func splitName(s string) (name, rest string) {
	if strings.HasPrefix(s, `"`) {
		end, _ := scanQuoted(s, 0)
		return s[:end], s[end:]
	}
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i], s[i:]
	}
	return s, ""
}

// redactLabels applies the pattern pass to every label value of a sample line.
func redactLabels(s string) (string, int) {
	open := strings.IndexByte(s, '{')
	if open < 0 {
		return s, 0
	}
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
			if strings.HasSuffix(strings.TrimRight(b.String(), " "), "=") {
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

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func quoteLabel(v string) string { return `"` + labelEscaper.Replace(v) + `"` }
