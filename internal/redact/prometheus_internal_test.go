package redact

import (
	"errors"
	"testing"
)

// TestPrometheusFailsClosed swaps the rewrite for one that breaks the format
// and expects an error and no output, whatever the rewrite produced.
func TestPrometheusFailsClosed(t *testing.T) {
	orig := promRewrite
	t.Cleanup(func() { promRewrite = orig })
	promRewrite = func([]byte) ([]byte, []Finding) {
		return []byte("m{l=\"unterminated} 1\n"), []Finding{{Rule: RulePattern, Location: "line:1", Count: 1}}
	}
	out, fs, err := Prometheus([]byte("m{l=\"v\"} 1\n"), Options{})
	if !errors.Is(err, ErrNotPrometheus) || out != nil || fs != nil {
		t.Errorf("out %q, findings %+v, err %v", out, fs, err)
	}
}
