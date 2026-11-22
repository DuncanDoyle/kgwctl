package redact_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	"github.com/DuncanDoyle/kgwctl/internal/redact"
)

const corpusDir = "testdata/canary"

// fixture is one corpus row; the schema is documented in testdata/canary/README.md.
type fixture struct {
	Row      int       `json:"row"`
	Title    string    `json:"title"`
	Cases    []fixCase `json:"cases"`
	Deferred []struct {
		Part string `json:"part"`
		Task string `json:"task"`
	} `json:"deferred"`
}

type fixCase struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Input   string `json:"input"`
	Options struct {
		ExtraHeaders []string `json:"extraHeaders"`
	} `json:"options"`
	Canaries    []canary `json:"canaries"`
	Keep        []string `json:"keep"`
	ExpectError string   `json:"expectError"`
	ReadError   bool     `json:"readError"`
	Server      string   `json:"server"`
	Error       *struct {
		Kind    string `json:"kind"`
		Code    int32  `json:"code"`
		Reason  string `json:"reason"`
		Message string `json:"message"`
	} `json:"error"`
}

type canary struct {
	Value        string `json:"value"`
	Rule         string `json:"rule"`
	Location     string `json:"location"`
	FindingsOnly bool   `json:"findingsOnly"`
}

// result is what a case's redactor produced.
type result struct {
	out      string
	findings []redact.Finding
	err      error
}

func loadCorpus(t *testing.T) map[string]fixture {
	t.Helper()
	dirs, err := os.ReadDir(corpusDir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]fixture{}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(corpusDir, d.Name(), "fixture.yaml"))
		if err != nil {
			t.Fatalf("%s: %v", d.Name(), err)
		}
		var f fixture
		if err := yaml.UnmarshalStrict(b, &f); err != nil {
			t.Fatalf("%s: %v", d.Name(), err)
		}
		out[d.Name()] = f
	}
	return out
}

func TestCorpusCoversEveryRow(t *testing.T) {
	rows := map[int]bool{}
	for name, f := range loadCorpus(t) {
		if !strings.HasPrefix(name, fmt.Sprintf("%02d-", f.Row)) {
			t.Errorf("%s: row %d does not match the directory", name, f.Row)
		}
		if len(f.Cases) == 0 && len(f.Deferred) == 0 {
			t.Errorf("%s: no cases and nothing deferred", name)
		}
		rows[f.Row] = true
	}
	for r := 1; r <= 21; r++ {
		if !rows[r] {
			t.Errorf("row %d has no fixture", r)
		}
	}
}

func TestCorpusNoLeaks(t *testing.T) {
	for dir, f := range loadCorpus(t) {
		for _, c := range f.Cases {
			t.Run(dir+"/"+c.Name, func(t *testing.T) {
				r := runCase(t, filepath.Join(corpusDir, dir), c)
				checkCase(t, c, r)
			})
		}
	}
}

// TestCorpusKeepsUsefulContent requires every successful case to name content
// that must survive, so a redactor that empties its input fails the corpus.
func TestCorpusKeepsUsefulContent(t *testing.T) {
	for dir, f := range loadCorpus(t) {
		for _, c := range f.Cases {
			if c.ExpectError != "" {
				continue
			}
			useful := false
			for _, k := range c.Keep {
				if strings.Trim(strings.ReplaceAll(k, redact.Marker, ""), ` '":,`) != "" {
					useful = true
				}
			}
			if !useful {
				t.Errorf("%s/%s: keep names no content beyond the marker", dir, c.Name)
			}
			if c.Type != "object" {
				continue
			}
			in, err := os.ReadFile(filepath.Join(corpusDir, dir, c.Input))
			if err != nil {
				t.Fatal(err)
			}
			u := objectFromYAML(t, in)
			names := []string{u.GetName()}
			if u.IsList() {
				names = nil
				_ = u.EachListItem(func(o runtime.Object) error {
					names = append(names, o.(*unstructured.Unstructured).GetName())
					return nil
				})
			}
			for _, n := range names {
				if !slices.Contains(c.Keep, n) {
					t.Errorf("%s/%s: keep lacks the object name %q", dir, c.Name, n)
				}
			}
		}
	}
}

func runCase(t *testing.T, dir string, c fixCase) result {
	t.Helper()
	var in []byte
	if c.Input != "" {
		var err error
		if in, err = os.ReadFile(filepath.Join(dir, c.Input)); err != nil {
			t.Fatal(err)
		}
	}
	o := redact.Options{ExtraHeaders: c.Options.ExtraHeaders}
	switch c.Type {
	case "object":
		u := objectFromYAML(t, in)
		fs, err := redact.Object(u, o)
		if err != nil {
			return result{err: err}
		}
		return result{out: objectText(t, u), findings: fs}
	case "config-dump":
		out, fs, err := redact.ConfigDump(in, o)
		return result{out: string(out), findings: fs, err: err}
	case "json":
		out, fs, err := redact.JSON(in, o)
		return result{out: string(out), findings: fs, err: err}
	case "prometheus":
		out, fs, err := redact.Prometheus(in, o)
		return result{out: string(out), findings: fs, err: err}
	case "text":
		var r io.Reader = bytes.NewReader(in)
		if c.ReadError {
			r = io.MultiReader(r, failReader{})
		}
		var w lines
		res, err := redact.Text(r, &w, 1<<20, o)
		if c.ReadError && (err == nil || !res.Truncated) {
			t.Errorf("read error: err %v, truncated %v", err, res.Truncated)
		}
		return result{out: w.String(), findings: res.Findings}
	case "error-text":
		return result{out: redact.ErrorText(errors.New(string(in)), c.Server)}
	case "remote-error":
		return result{out: redact.RemoteError(remoteErr(t, c))}
	}
	t.Fatalf("unknown case type %q", c.Type)
	return result{}
}

func checkCase(t *testing.T, c fixCase, r result) {
	t.Helper()
	switch {
	case c.ExpectError != "":
		want := map[string]error{
			"forbidden-kind": redact.ErrForbiddenKind,
			"invalid-json":   redact.ErrInvalidJSON,
			"not-prometheus": redact.ErrNotPrometheus,
		}[c.ExpectError]
		if !errors.Is(r.err, want) {
			t.Fatalf("err = %v, want %v", r.err, want)
		}
		if r.out != "" {
			t.Errorf("output on error: %q", r.out)
		}
	case r.err != nil:
		t.Fatalf("unexpected error: %v", r.err)
	}

	scan := r.out
	if r.err != nil {
		scan += "\n" + r.err.Error()
	}
	decoded := decodeBase64Runs(r.out)
	if strings.Contains(decoded, "kgwctl-canary") {
		t.Errorf("a base64 run in the output decodes to a canary:\n%s", decoded)
	}
	for _, cn := range c.Canaries {
		for _, f := range r.findings {
			if strings.Contains(f.Location, cn.Value) {
				t.Errorf("finding location %q holds canary %q", f.Location, cn.Value)
			}
		}
		if cn.FindingsOnly {
			scan = strings.ReplaceAll(scan, cn.Value, "")
			continue
		}
		if strings.Contains(r.out, cn.Value) {
			t.Errorf("canary %q leaked", cn.Value)
		}
		if strings.Contains(decoded, cn.Value) {
			t.Errorf("canary %q leaked as base64", cn.Value)
		}
		if cn.Rule != "" && !hasFinding(r.findings, redact.Rule(cn.Rule), cn.Location) {
			t.Errorf("canary %q: no finding {%s %s}; got %+v", cn.Value, cn.Rule, cn.Location, r.findings)
		}
	}
	if strings.Contains(scan, "kgwctl-canary") {
		t.Errorf("output holds a canary:\n%s", scan)
	}
	for _, k := range c.Keep {
		if !strings.Contains(r.out, k) {
			t.Errorf("kept value %q missing from output:\n%s", k, r.out)
		}
	}
	for _, f := range r.findings {
		if f.Count < 1 || f.Location == "" || f.Rule == "" {
			t.Errorf("malformed finding %+v", f)
		}
	}
}

// base64Run is a run that may be base64, in either alphabet, padded or not.
var base64Run = regexp.MustCompile(`[A-Za-z0-9+/_-]{16,}={0,2}`)

// decodeBase64Runs decodes every base64-looking run of s at each of the four
// alignments, in both alphabets, and returns the decoded bytes joined.
func decodeBase64Runs(s string) string {
	var b strings.Builder
	for _, run := range base64Run.FindAllString(s, -1) {
		run = strings.TrimRight(run, "=")
		for off := 0; off < 4 && off < len(run); off++ {
			r := run[off:]
			// Whole quanta always decode; the full run also covers a partial tail.
			for _, in := range []string{r[:len(r)-len(r)%4], r} {
				for _, e := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
					if d, err := e.DecodeString(in); err == nil {
						b.Write(d)
						b.WriteByte('\n')
					}
				}
			}
		}
	}
	return b.String()
}

func hasFinding(fs []redact.Finding, rule redact.Rule, loc string) bool {
	for _, f := range fs {
		if f.Rule == rule && f.Location == loc {
			return true
		}
	}
	return false
}

func objectFromYAML(t *testing.T, b []byte) *unstructured.Unstructured {
	t.Helper()
	j, err := yaml.YAMLToJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{}
	if err := u.UnmarshalJSON(j); err != nil {
		t.Fatal(err)
	}
	return u
}

// objectText is the object as YAML followed by every string leaf, so kept
// values match without YAML quoting or folding getting in the way.
func objectText(t *testing.T, u *unstructured.Unstructured) string {
	t.Helper()
	y, err := yaml.Marshal(u.Object)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.Write(y)
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, e := range v {
				b.WriteString("\n" + k)
				walk(e)
			}
		case []any:
			for _, e := range v {
				walk(e)
			}
		case string:
			b.WriteString("\n" + v)
		}
	}
	walk(u.Object)
	return b.String()
}

func remoteErr(t *testing.T, c fixCase) error {
	t.Helper()
	e := c.Error
	switch e.Kind {
	case "status":
		return &apierrors.StatusError{ErrStatus: metav1.Status{
			Status: metav1.StatusFailure, Code: e.Code, Reason: metav1.StatusReason(e.Reason), Message: e.Message,
		}}
	case "log-stream":
		// client-go's Stream returns this shape for a non-2xx response with an unparsed body.
		return fmt.Errorf("%d while accessing %v: %s", e.Code, "https://10.0.0.1:6443/api/v1/namespaces/team-a/pods/gw/log", e.Message)
	}
	t.Fatalf("unknown error kind %q", e.Kind)
	return nil
}

type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, errors.New("stream reset") }

// lines is a LineWriter that keeps every line.
type lines struct {
	l  []string
	fs [][]redact.Finding // per line
}

func (w *lines) WriteLine(b []byte, fs []redact.Finding) error {
	w.l = append(w.l, string(b))
	w.fs = append(w.fs, fs)
	return nil
}

func (w *lines) String() string { return strings.Join(w.l, "\n") }
